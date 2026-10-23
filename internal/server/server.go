// Paket server ist der Relay: Er nimmt Hosts und Viewer in Räume auf und
// leitet Bild- und Tondaten stumpf weiter. Kein Transcoding, kaum Zustand.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 75 * time.Second
	pingPeriod     = 25 * time.Second
	joinWait       = 15 * time.Second
	maxMessageSize = 8 << 20
	sendQueue      = 128
	bulkQueue      = 512      // Bild- und Videopakete
	gopLimit       = 16 << 20 // so viel Video merkt sich der Relay für Neue
	maxPending     = 20
	hostGrace      = 20 * time.Second
	viewerTokenTTL = 12 * time.Hour
)

var roomPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

type Config struct {
	MaxViewers int
	HostGrace  time.Duration
	Log        *log.Logger
}

type Server struct {
	cfg      Config
	log      *log.Logger
	upgrader websocket.Upgrader
	mu       sync.Mutex
	rooms    map[string]*room
	// tokens merkt sich freigegebene Zuschauer pro Sitzungs-ID – auch über
	// das Ende eines Streams hinaus. Wer heute schon zugeschaut hat, kommt
	// nach einem Neustart des Streams ohne neue Freigabe wieder rein.
	tokens map[string]map[string]time.Time
}

type room struct {
	code       string
	host       *client
	viewers    map[*client]struct{}
	pending    map[*client]struct{}
	grace      *time.Timer
	autoAccept bool
	nextID     uint64
	// sticky hält die letzte Steuernachricht pro Typ ("meta", "pause"), damit
	// später freigegebene Zuschauer Tonrate und Zustand ebenfalls kennen.
	sticky map[string][]byte
	// gop ist das Video seit dem letzten Keyframe. Neue Zuschauer bekommen
	// es nachgereicht und sehen sofort ein Bild statt bis zu 2 s zu warten.
	gop      [][]byte
	gopBytes int
	// Sprachkanal: Teilnehmer, verbundene Host-Panels und deren Schlüssel.
	voice      map[*client]struct{}
	panels     map[*client]struct{}
	voiceKey   string
	presenters map[*client]*source
	// trusted sind Freigaben, die der Host dauerhaft gespeichert hat.
	trusted map[string]bool
}

// stickyTypes sind Steuernachrichten, die neue Zuschauer nachgereicht bekommen.
var stickyTypes = map[string]bool{"meta": true, "pause": true}

type client struct {
	srv  *Server
	conn *websocket.Conn
	send chan []byte // Steuerung und Ton – geht immer vor
	bulk chan []byte // Bild und Video
	// needKey: Es ist Video verloren gegangen (oder der Zuschauer ist neu) –
	// bis zum nächsten Keyframe nichts schicken, was er nicht dekodieren kann.
	needKey bool
	// Sprachkanal-Zustand (geschützt durch Server.mu)
	voiceMuted, voiceDeaf bool
	voicePkts             int       // seit dem Beitritt weitergeleitete Sprachpakete
	hostView              bool      // der Host selbst schaut sich die Konferenz an
	lastPing              time.Time // Zeige-Klicks drosseln
	role                  string
	name                  string
	room                  *room
	id                    uint64
	token                 string

	mu     sync.Mutex
	closed bool
}

func New(cfg Config) *Server {
	if cfg.MaxViewers <= 0 {
		cfg.MaxViewers = 8
	}
	if cfg.HostGrace <= 0 {
		cfg.HostGrace = hostGrace
	}
	if cfg.Log == nil {
		cfg.Log = log.Default()
	}
	return &Server{
		cfg: cfg,
		log: cfg.Log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 64 << 10,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		rooms:  map[string]*room{},
		tokens: map[string]map[string]time.Time{},
	}
}

// Handler liefert den HTTP-Handler: /ws für WebSockets, sonst die statische Viewer-UI.
func (s *Server) Handler(static fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	fileServer := http.FileServerFS(static)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	}))
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// Kleiner Sendepuffer: Bei langsamen Zuschauern (z. B. über einen Tunnel)
	// sollen Videoframes früh verworfen werden, statt sich im Kernel
	// aufzustauen – sonst hängen Ton und Bild sekundenlang hinterher.
	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		tcpConn.SetWriteBuffer(512 * 1024)
	}
	c := &client{srv: s, conn: conn, send: make(chan []byte, sendQueue), bulk: make(chan []byte, bulkQueue), needKey: true}

	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(joinWait))
	mt, data, err := conn.ReadMessage()
	if err != nil || mt != websocket.TextMessage {
		conn.Close()
		return
	}
	var join proto.Control
	if err := json.Unmarshal(data, &join); err != nil || join.T != "join" {
		c.fail("Ungültiges Join-Paket")
		return
	}
	code := strings.ToLower(strings.TrimSpace(join.Room))
	if !roomPattern.MatchString(code) {
		c.fail("Ungültiger Raumcode")
		return
	}
	if join.Role != "host" && join.Role != "viewer" && join.Role != "voice" && join.Role != "presenter" {
		c.fail("Unbekannte Rolle")
		return
	}
	c.role = join.Role
	c.name = strings.TrimSpace(join.Name)
	if c.name == "" {
		c.name = "Unbekannt"
	}
	c.token = strings.TrimSpace(join.Token)

	if err := s.join(c, code); err != nil {
		c.fail(err.Error())
		return
	}
	defer s.leave(c)

	go c.writeLoop()
	c.readLoop()
}

// join hängt den Client in den Raum ein und schickt die ersten Steuernachrichten.
// Zuschauer landen erst in der Warteliste und werden vom Host freigegeben.
func (s *Server) join(c *client, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rm := s.rooms[code]

	if c.role == "host" {
		if rm != nil && rm.host != nil {
			return errors.New("Der Raum ist schon belegt")
		}
		if rm == nil {
			rm = &room{
				code:       code,
				viewers:    map[*client]struct{}{},
				pending:    map[*client]struct{}{},
				voice:      map[*client]struct{}{},
				panels:     map[*client]struct{}{},
				presenters: map[*client]*source{},
			}
			s.rooms[code] = rm
		}
		resume := rm.grace != nil
		if rm.grace != nil {
			rm.grace.Stop()
			rm.grace = nil
		}
		rm.host = c
		c.room = rm
		c.enqueueControl(proto.Control{T: "joined", Room: code, N: len(rm.viewers)})
		s.log.Printf("Raum %q: Host %s ist da (%d Zuschauer, %d Anfragen)", code, c.name, len(rm.viewers), len(rm.pending))

		if rm.autoAccept {
			s.acceptAllLocked(rm)
		} else {
			// Wartende wissen lassen, dass der Host wieder da ist, und die
			// offenen Anfragen erneut melden.
			for p := range rm.pending {
				p.enqueueControl(proto.Control{T: "waiting"})
				c.enqueueControl(proto.Control{T: "knock", ID: p.id, Name: p.name, Token: p.token})
			}
		}
		if resume && len(rm.viewers) > 0 {
			s.broadcastLocked(rm, proto.Control{T: "live"})
			s.log.Printf("Raum %q: Host wieder da", code)
		}
		if resume {
			s.sendViewersLocked(rm)
		}
		return nil
	}

	if c.role == "voice" {
		if !s.joinPanelLocked(c, rm) {
			return errors.New("Sprachkanal: kein Zugang")
		}
		return nil
	}
	if c.role == "presenter" {
		if !s.joinPresenterLocked(c, rm) {
			return errors.New("Teilen: nur wer freigegeben ist")
		}
		return nil
	}
	if rm == nil {
		return errors.New("Stream nicht gefunden – läuft er noch?")
	}
	if len(rm.viewers) >= s.cfg.MaxViewers {
		return errors.New("Der Raum ist voll")
	}
	if len(rm.pending) >= maxPending {
		return errors.New("Zu viele offene Anfragen")
	}

	rm.nextID++
	c.id = rm.nextID
	c.room = rm
	c.enqueueControl(proto.Control{T: "joined", Room: code, N: len(rm.viewers)})
	if rm.host == nil {
		c.enqueueControl(proto.Control{T: "reconnecting"})
	}

	// Vertraute Personen ("Immer reinlassen") kommen direkt rein.
	if c.token != "" && rm.trusted[c.token] {
		s.admitLocked(rm, c)
		s.log.Printf("Raum %q: %s (vertraut) dabei (%d)", code, c.name, len(rm.viewers))
		s.sendViewersLocked(rm)
		return nil
	}

	// Der Host selbst (mit dem Schlüssel seiner App) muss nicht anklopfen.
	if rm.voiceKey != "" && subtle.ConstantTimeCompare([]byte(c.token), []byte(rm.voiceKey)) == 1 {
		c.hostView = true
		s.admitLocked(rm, c)
		s.sendViewersLocked(rm)
		return nil
	}

	// Wer vor kurzem schon einmal freigegeben wurde, darf nach einem
	// Verbindungsabbruch direkt wieder rein.
	if c.token != "" {
		if exp, ok := s.tokens[code][c.token]; ok && time.Now().Before(exp) {
			s.tokens[code][c.token] = time.Now().Add(viewerTokenTTL)
			s.admitLocked(rm, c)
			s.log.Printf("Raum %q: Zuschauer %s wieder dabei (%d)", code, c.name, len(rm.viewers))
			s.sendViewersLocked(rm)
			return nil
		}
	}

	if rm.autoAccept && rm.host != nil {
		s.admitLocked(rm, c)
		s.log.Printf("Raum %q: Zuschauer %s dabei (%d)", code, c.name, len(rm.viewers))
		s.sendViewersLocked(rm)
		return nil
	}

	rm.pending[c] = struct{}{}
	if rm.host != nil {
		rm.host.enqueueControl(proto.Control{T: "knock", ID: c.id, Name: c.name, Token: c.token})
	}
	s.log.Printf("Raum %q: %s fragt an (%d offen)", code, c.name, len(rm.pending))
	return nil
}

// leave räumt den Client aus dem Raum. Der Host bekommt ein Reconnect-Fenster,
// damit ein kurzer Netzausfall den Stream nicht sofort beendet.
func (s *Server) leave(c *client) {
	s.mu.Lock()
	rm := c.room
	if rm == nil {
		s.mu.Unlock()
		c.close()
		return
	}
	s.leaveVoiceLocked(rm, c)
	s.leavePresenterLocked(rm, c)
	if c.role == "host" && rm.host == c {
		rm.host = nil
		s.notifyLocked(rm, proto.Control{T: "reconnecting"})
		code := rm.code
		rm.grace = time.AfterFunc(s.cfg.HostGrace, func() { s.hostTimeout(code, rm) })
		s.log.Printf("Raum %q: Host weg, warte %s auf Reconnect", code, s.cfg.HostGrace)
	} else if c.role == "viewer" {
		if _, ok := rm.pending[c]; ok {
			delete(rm.pending, c)
			if rm.host != nil {
				rm.host.enqueueControl(proto.Control{T: "knock-gone", ID: c.id})
			}
			s.log.Printf("Raum %q: Anfrage von %s zurückgezogen", rm.code, c.name)
		} else if _, ok := rm.viewers[c]; ok {
			delete(rm.viewers, c)
			s.log.Printf("Raum %q: Zuschauer %s raus (%d)", rm.code, c.name, len(rm.viewers))
			s.sendViewersLocked(rm)
		}
	}
	s.mu.Unlock()
	c.close()
}

func (s *Server) hostTimeout(code string, rm *room) {
	s.mu.Lock()
	stale := s.rooms[code] != rm || rm.host != nil
	s.mu.Unlock()
	if stale {
		return
	}
	s.closeRoom(rm)
}

// endRoomNow beendet den Raum sofort – der Host hat absichtlich gestoppt.
func (s *Server) endRoomNow(host *client) {
	s.mu.Lock()
	rm := host.room
	if rm == nil || rm.host != host {
		s.mu.Unlock()
		return
	}
	if rm.grace != nil {
		rm.grace.Stop()
		rm.grace = nil
	}
	s.mu.Unlock()
	s.closeRoom(rm)
}

// closeRoom wirft den Raum weg und schickt allen Beteiligten "ended".
func (s *Server) closeRoom(rm *room) {
	s.mu.Lock()
	if existing, ok := s.rooms[rm.code]; !ok || existing != rm {
		s.mu.Unlock()
		return
	}
	delete(s.rooms, rm.code)
	all := make([]*client, 0, len(rm.viewers)+len(rm.pending))
	for v := range rm.viewers {
		all = append(all, v)
	}
	for p := range rm.pending {
		all = append(all, p)
	}
	for p := range rm.panels {
		all = append(all, p)
	}
	for p := range rm.presenters {
		all = append(all, p)
	}
	rm.presenters = map[*client]*source{}
	rm.voice = map[*client]struct{}{}
	rm.panels = map[*client]struct{}{}
	s.log.Printf("Raum %q: %d Teilnehmer werden informiert", rm.code, len(all))
	rm.viewers = map[*client]struct{}{}
	rm.pending = map[*client]struct{}{}
	rm.host = nil
	s.mu.Unlock()

	for _, v := range all {
		v.enqueueControl(proto.Control{T: "ended"})
		v.close()
	}
	s.log.Printf("Raum %q: geschlossen", rm.code)
}

// allow entscheidet über eine offene Anfrage.
func (s *Server) allow(host *client, id uint64, accept bool) {
	s.mu.Lock()
	rm := host.room
	if rm == nil || rm.host != host {
		s.mu.Unlock()
		return
	}
	var target *client
	for p := range rm.pending {
		if p.id == id {
			target = p
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return
	}
	delete(rm.pending, target)
	if accept {
		s.admitLocked(rm, target)
		s.log.Printf("Raum %q: %s darf zuschauen (%d)", rm.code, target.name, len(rm.viewers))
		s.sendViewersLocked(rm)
	} else {
		target.enqueueControl(proto.Control{T: "denied"})
		s.log.Printf("Raum %q: %s abgelehnt", rm.code, target.name)
	}
	s.mu.Unlock()

	if !accept {
		target.close()
	}
}

// admitLocked nimmt einen Zuschauer in den Raum auf, merkt sich die Freigabe
// (Reconnect ohne neues Anklopfen) und reicht ihm die gemerkten
// Steuernachrichten nach. Der Aufrufer muss s.mu halten.
func (s *Server) admitLocked(rm *room, c *client) {
	rm.viewers[c] = struct{}{}
	if c.token != "" {
		s.rememberLocked(rm.code, c.token)
	}
	c.enqueueControl(proto.Control{T: "allowed"})
	if !c.hostView {
		for _, msg := range rm.sticky {
			c.enqueue(msg, true)
		}
		for _, msg := range rm.gop {
			c.enqueue(msg, false)
		}
	}
	s.sendSourceStateLocked(rm, c)
	if len(rm.voice) > 0 {
		c.enqueueControl(rm.voiceRoster())
	}
}

// rememberLocked merkt sich eine Freigabe und räumt abgelaufene weg. Der
// Aufrufer muss s.mu halten.
func (s *Server) rememberLocked(code, token string) {
	now := time.Now()
	for c, m := range s.tokens {
		for t, exp := range m {
			if now.After(exp) {
				delete(m, t)
			}
		}
		if len(m) == 0 {
			delete(s.tokens, c)
		}
	}
	if s.tokens[code] == nil {
		s.tokens[code] = map[string]time.Time{}
	}
	s.tokens[code][token] = now.Add(viewerTokenTTL)
}

// setAutoAccept schaltet die automatische Freigabe um und lässt offene
// Anfragen sofort durch, wenn eingeschaltet wird.
func (s *Server) setAutoAccept(host *client, on bool) {
	s.mu.Lock()
	rm := host.room
	if rm == nil || rm.host != host {
		s.mu.Unlock()
		return
	}
	rm.autoAccept = on
	if on {
		s.acceptAllLocked(rm)
	}
	s.mu.Unlock()
	s.log.Printf("Raum %q: automatische Freigabe %v", rm.code, on)
}

// acceptAllLocked lässt alle wartenden Anfragen durch. Der Aufrufer muss
// s.mu halten.
func (s *Server) acceptAllLocked(rm *room) {
	for p := range rm.pending {
		s.admitLocked(rm, p)
	}
	if len(rm.pending) > 0 {
		s.log.Printf("Raum %q: %d offene Anfragen automatisch durchgelassen", rm.code, len(rm.pending))
	}
	rm.pending = map[*client]struct{}{}
	s.sendViewersLocked(rm)
}

// relayBinary schickt ein Binärpaket vom Host an alle Zuschauer. Tondaten
// haben Vorrang: Sie werden nie verworfen, Videoframes schon – sonst knackt
// der Ton, sobald die Leitung nicht hinterherkommt.
func (s *Server) relayBinary(from *client, msg []byte) {
	priority := len(msg) > 0 && (msg[0] == proto.MsgAudio || msg[0] == proto.MsgSilence)
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := from.room
	if rm == nil || rm.host != from {
		return
	}
	if len(msg) > 0 && msg[0] == proto.MsgVideoChunk {
		if proto.VideoKeyStart(msg) {
			rm.gop, rm.gopBytes = rm.gop[:0], 0
		}
		if rm.gopBytes+len(msg) <= gopLimit {
			rm.gop = append(rm.gop, msg)
			rm.gopBytes += len(msg)
		}
	}
	for v := range rm.viewers {
		if !v.hostView {
			v.enqueue(msg, priority)
		}
	}
}

// relayControl schickt eine Steuernachricht vom Host an alle Zuschauer.
func (s *Server) relayControl(from *client, typ string, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if from.room == nil || from.room.host != from {
		return
	}
	if stickyTypes[typ] {
		if from.room.sticky == nil {
			from.room.sticky = map[string][]byte{}
		}
		from.room.sticky[typ] = msg
	}
	for v := range from.room.viewers {
		v.enqueue(msg, true)
	}
}

// broadcastLocked schickt eine Steuernachricht an alle Zuschauer. Der Aufrufer
// muss s.mu halten.
func (s *Server) broadcastLocked(rm *room, ctl proto.Control) {
	data, _ := json.Marshal(ctl)
	for v := range rm.viewers {
		v.enqueue(data, true)
	}
}

// notifyLocked erreicht Zuschauer und wartende Anfragen – für Zustände wie
// "Host ist weg" oder "Host ist wieder da", die alle angehen.
func (s *Server) notifyLocked(rm *room, ctl proto.Control) {
	data, _ := json.Marshal(ctl)
	for v := range rm.viewers {
		v.enqueue(data, true)
	}
	for p := range rm.pending {
		p.enqueue(data, true)
	}
}

// sendViewersLocked verteilt den aktuellen Zuschauerstand an alle im Raum.
func (s *Server) sendViewersLocked(rm *room) {
	names := make([]string, 0, len(rm.viewers))
	for v := range rm.viewers {
		if !v.hostView {
			names = append(names, v.name)
		}
	}
	sort.Strings(names)
	s.sendPresenterAudienceLocked(rm)
	data, _ := json.Marshal(proto.Control{T: "viewers", N: len(names), Names: names})
	if rm.host != nil {
		rm.host.enqueue(data, true)
	}
	for v := range rm.viewers {
		v.enqueue(data, true)
	}
}

func (c *client) readLoop() {
	defer c.close()
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		mt, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		if mt == websocket.BinaryMessage {
			if len(data) > 0 && data[0] == proto.MsgVoice {
				c.srv.relayVoice(c, data)
			} else if c.role == "presenter" {
				c.srv.relaySourced(c, data)
			} else if c.role == "host" {
				c.srv.relayBinary(c, data)
			}
			continue
		}
		var ctl proto.Control
		if err := json.Unmarshal(data, &ctl); err != nil {
			continue
		}
		switch ctl.T {
		case "ping":
			c.enqueueControl(proto.Control{T: "pong", TS: ctl.TS})
		case "join":
			// ist schon durch
		case "allow":
			if c.role == "host" {
				c.srv.allow(c, ctl.ID, true)
			}
		case "deny":
			if c.role == "host" {
				c.srv.allow(c, ctl.ID, false)
			}
		case "auto":
			if c.role == "host" {
				c.srv.setAutoAccept(c, ctl.On)
			}
		case "bye":
			if c.role == "host" {
				c.srv.endRoomNow(c)
			}
		case "trusted":
			if c.role == "host" {
				c.srv.setTrusted(c, ctl.Names)
			}
		case "voicekey":
			if c.role == "host" {
				c.srv.setVoiceKey(c, ctl.Token)
			}
		case "point":
			c.srv.ping(c, ctl)
		case "voice-join":
			c.srv.voiceJoin(c, ctl.On)
		case "voice-state":
			c.srv.voiceState(c, ctl.On, ctl.Deaf)
		default:
			if c.role == "host" {
				c.srv.relayControl(c, ctl.T, data)
			} else if c.role == "presenter" {
				if ctl.T == "bye" {
					return
				}
				c.srv.relayPresenterControl(c, ctl)
			}
		}
	}
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	write := func(msg []byte) bool {
		c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		typ := websocket.BinaryMessage
		if len(msg) > 0 && msg[0] == '{' {
			typ = websocket.TextMessage
		}
		if err := c.conn.WriteMessage(typ, msg); err != nil {
			c.conn.Close()
			return false
		}
		return true
	}
	for {
		// Steuerung und Ton zuerst, dann Bild.
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.Close()
				return
			}
			if !write(msg) {
				return
			}
			continue
		default:
		}
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.Close()
				return
			}
			if !write(msg) {
				return
			}
		case msg := <-c.bulk:
			if !write(msg) {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.conn.Close()
				return
			}
		}
	}
}

// enqueue hängt eine Nachricht an die Sendeschlange. Bei vollen Queues werden
// Bilder verworfen (priority false) oder die älteste Steuernachricht ersetzt
// (priority true), damit ein hängender Viewer niemanden blockiert. Fehlt einem
// Zuschauer ein Stück Video, bekommt er erst ab dem nächsten Keyframe wieder
// welches – kaputte Bilder gibt es so nicht.
func (c *client) enqueue(msg []byte, priority bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if !priority {
		isVideo := len(msg) > 0 && msg[0] == proto.MsgVideoChunk
		if isVideo && c.needKey {
			if !proto.VideoKeyStart(msg) {
				return
			}
			c.needKey = false
		}
		select {
		case c.bulk <- msg:
		default:
			if isVideo {
				c.needKey = true
			}
		}
		return
	}
	if priority {
		select {
		case c.send <- msg:
			return
		default:
		}
		select {
		case <-c.send:
		default:
		}
	}
	select {
	case c.send <- msg:
	default:
	}
}

func (c *client) enqueueControl(ctl proto.Control) {
	data, _ := json.Marshal(ctl)
	c.enqueue(data, true)
}

func (c *client) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.send)
	c.mu.Unlock()
}

// fail meldet einen Fehler und macht die Verbindung zu.
func (c *client) fail(msg string) {
	data, _ := json.Marshal(proto.Control{T: "error", Msg: msg, Fatal: true})
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(websocket.TextMessage, data)
	c.conn.Close()
	c.close()
}
