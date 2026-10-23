// Paket room ist der Relay eines Raums. Er läuft in der App dessen, der den
// Raum gerade betreibt, und verteilt nur – lesen kann er nichts: Jedes Paket
// ist Ende-zu-Ende verschlüsselt (internal/e2e). Er sieht den Kopf (Art,
// Absender, Nummer) und die Größe, mehr nicht.
//
// Reinkommen darf, wer den Anmeldeschlüssel kennt – also das Passwort. Der
// Relay speichert davon nur den SHA-256-Wert.
package room

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/e2e"
)

const (
	maxMembers   = 16
	maxMessage   = 256 << 10
	chatHistory  = 200
	gopLimit     = 16 << 20
	prioQueue    = 512
	bulkQueue    = 512
	fileQueue    = 2048 // 2048 × 64 KB – so viel Datei darf sich pro Empfänger stauen
	writeWait    = 10 * time.Second
	readWait     = 75 * time.Second
	pingInterval = 25 * time.Second
)

// Ctl ist eine Steuernachricht (lesbar, ohne Inhalte).
type Ctl struct {
	T       string   `json:"t"`
	Room    string   `json:"room,omitempty"`
	Auth    string   `json:"auth,omitempty"`
	Kind    string   `json:"kind,omitempty"` // "person" oder "media"
	ID      uint32   `json:"id,omitempty"`
	Members []Member `json:"members,omitempty"`
	Msg     string   `json:"msg,omitempty"`
	TS      int64    `json:"ts,omitempty"`
	Seq     uint32   `json:"seq,omitempty"`
}

// Member ist ein Teilnehmer, wie ihn der Relay sieht: Kennung, Art, seit wann.
type Member struct {
	ID    uint32 `json:"id"`
	Kind  string `json:"kind"`
	Since int64  `json:"since"`
}

// Server hält die Räume, die dieser PC gerade betreibt.
type Server struct {
	Self     string // Kennung dieses Programmstarts, kommt in /healthz zurück
	log      *log.Logger
	upgrader websocket.Upgrader
	mu       sync.Mutex
	rooms    map[string]*Room
}

// Room ist ein betriebener Raum.
type Room struct {
	id       string
	authHash []byte
	members  map[uint32]*member
	nextID   uint32
	chat     [][]byte
	state    map[uint32][]byte
	gop      map[uint32][][]byte
	gopBytes map[uint32]int
}

type member struct {
	id    uint32
	kind  string
	since time.Time
	conn  *websocket.Conn
	prio  chan qitem
	bulk  chan qitem
	files chan qitem

	mu      sync.Mutex
	closed  bool
	needKey map[uint32]bool // pro Absender: bis zum nächsten Keyframe kein Bild
	dropKey map[uint32]bool // dasselbe beim Senden, nachdem ein altes Stück verworfen wurde
}

// New legt einen leeren Relay an.
func New(logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{
		log:      logger,
		upgrader: websocket.Upgrader{ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10, CheckOrigin: func(*http.Request) bool { return true }},
		rooms:    map[string]*Room{},
	}
}

// Host betreibt einen Raum auf diesem PC. authHash ist e2e.AuthHash(Keys.Auth).
func (s *Server) Host(id, authHash string) {
	id = e2e.NormalizeRoom(id)
	h, _ := hex.DecodeString(authHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if rm := s.rooms[id]; rm != nil {
		rm.authHash = h
		return
	}
	s.rooms[id] = &Room{id: id, authHash: h, members: map[uint32]*member{}, state: map[uint32][]byte{},
		gop: map[uint32][][]byte{}, gopBytes: map[uint32]int{}}
	s.log.Printf("Raum %q wird hier betrieben", id)
}

// Unhost gibt einen Raum ab: Alle Teilnehmer bekommen "moved" und suchen
// den Raum neu (ein anderer PC betreibt ihn jetzt).
func (s *Server) Unhost(id string) {
	id = e2e.NormalizeRoom(id)
	s.mu.Lock()
	rm := s.rooms[id]
	delete(s.rooms, id)
	var all []*member
	if rm != nil {
		for _, m := range rm.members {
			all = append(all, m)
		}
	}
	s.mu.Unlock()
	for _, m := range all {
		m.sendCtl(Ctl{T: "moved"})
		m.close()
	}
	if rm != nil {
		s.log.Printf("Raum %q wird hier nicht mehr betrieben", id)
	}
}

// Hosts sagt, ob dieser PC den Raum betreibt.
func (s *Server) Hosts(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rooms[e2e.NormalizeRoom(id)] != nil
}

// Count nennt, wie viele gerade im Raum sind (für Diagnose und Wahl).
func (s *Server) Count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rm := s.rooms[e2e.NormalizeRoom(id)]; rm != nil {
		return len(rm.members)
	}
	return 0
}

// Handler: /ws für Teilnehmer, /healthz?room=… zum Prüfen, ob der Raum hier
// läuft. Die Antwort ist die Kennung des Programmstarts (Self): Zeigt ein
// alter Eintrag auf eine Adresse, die inzwischen ein anderer Start benutzt,
// fällt das so auf.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if room := r.URL.Query().Get("room"); room != "" && !s.Hosts(room) {
			http.Error(w, "Raum nicht hier", http.StatusNotFound)
			return
		}
		w.Write([]byte(s.Self))
	})
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxMessage)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var join Ctl
	if err := conn.ReadJSON(&join); err != nil || join.T != "join" {
		conn.Close()
		return
	}
	m := &member{conn: conn, kind: join.Kind, since: time.Now(),
		prio: make(chan qitem, prioQueue), bulk: make(chan qitem, bulkQueue), files: make(chan qitem, fileQueue),
		needKey: map[uint32]bool{}, dropKey: map[uint32]bool{}}
	if m.kind != "media" {
		m.kind = "person"
	}
	rm, errMsg := s.join(m, join)
	if rm == nil {
		data, _ := json.Marshal(Ctl{T: "error", Msg: errMsg})
		conn.SetWriteDeadline(time.Now().Add(writeWait))
		conn.WriteMessage(websocket.TextMessage, data)
		conn.Close()
		return
	}
	defer s.leave(rm, m)
	go m.writeLoop()
	s.readLoop(rm, m)
}

// join prüft das Passwort (über den Anmeldeschlüssel) und nimmt auf.
func (s *Server) join(m *member, join Ctl) (*Room, string) {
	auth, err := hex.DecodeString(join.Auth)
	if err != nil {
		return nil, "Falsches Passwort"
	}
	sum := sha256.Sum256(auth)
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := s.rooms[e2e.NormalizeRoom(join.Room)]
	if rm == nil {
		return nil, "Raum nicht hier"
	}
	if subtle.ConstantTimeCompare(sum[:], rm.authHash) != 1 {
		return nil, "Falsches Passwort"
	}
	if len(rm.members) >= maxMembers {
		return nil, "Raum ist voll"
	}
	rm.nextID++
	m.id = rm.nextID
	// Neue bekommen erst Bild ab dem nächsten Keyframe – außer aus dem Cache.
	for id := range rm.members {
		m.needKey[id] = true
	}
	welcome := Ctl{T: "welcome", ID: m.id, Members: rm.roster()}
	m.sendCtl(welcome)
	for _, st := range rm.state {
		m.enqueue(st, m.prio, false)
	}
	for _, c := range rm.chat {
		m.enqueue(c, m.prio, false)
	}
	for sender, gop := range rm.gop {
		if len(gop) > 0 {
			m.needKey[sender] = false
			for _, p := range gop {
				m.enqueue(p, m.bulk, false)
			}
		}
	}
	rm.members[m.id] = m
	for _, o := range rm.members {
		if o != m {
			o.sendCtl(Ctl{T: "join", ID: m.id, Kind: m.kind})
		}
	}
	s.log.Printf("Raum %q: Teilnehmer %d (%s) dabei, %d im Raum", rm.id, m.id, m.kind, len(rm.members))
	return rm, ""
}

func (rm *Room) roster() []Member {
	out := make([]Member, 0, len(rm.members))
	for _, m := range rm.members {
		out = append(out, Member{ID: m.id, Kind: m.kind, Since: m.since.Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Server) leave(rm *Room, m *member) {
	s.mu.Lock()
	if rm.members[m.id] == m {
		delete(rm.members, m.id)
		delete(rm.state, m.id)
		delete(rm.gop, m.id)
		delete(rm.gopBytes, m.id)
		for _, o := range rm.members {
			o.sendCtl(Ctl{T: "leave", ID: m.id})
		}
		s.log.Printf("Raum %q: Teilnehmer %d raus, %d im Raum", rm.id, m.id, len(rm.members))
	}
	s.mu.Unlock()
	m.close()
}

func (s *Server) readLoop(rm *Room, m *member) {
	m.conn.SetReadDeadline(time.Now().Add(readWait))
	m.conn.SetPongHandler(func(string) error { m.conn.SetReadDeadline(time.Now().Add(readWait)); return nil })
	for {
		mt, data, err := m.conn.ReadMessage()
		if err != nil {
			return
		}
		m.conn.SetReadDeadline(time.Now().Add(readWait))
		if mt == websocket.TextMessage {
			var c Ctl
			if json.Unmarshal(data, &c) == nil && c.T == "ping" {
				m.sendCtl(Ctl{T: "pong", TS: c.TS})
			}
			continue
		}
		s.relay(rm, m, data)
	}
}

// relay verteilt ein verschlüsseltes Paket an alle anderen im Raum.
func (s *Server) relay(rm *Room, from *member, data []byte) {
	h, ok := e2e.ParseHeader(data)
	if !ok {
		return
	}
	e2e.SetSender(data, from.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if rm.members[from.id] != from {
		return
	}
	switch h.Kind {
	case e2e.KindState:
		rm.state[from.id] = data
	case e2e.KindChat:
		rm.chat = append(rm.chat, data)
		if len(rm.chat) > chatHistory {
			rm.chat = rm.chat[len(rm.chat)-chatHistory:]
		}
	case e2e.KindVideo:
		if h.Flags&e2e.FlagKey != 0 && h.Idx == 0 {
			rm.gop[from.id], rm.gopBytes[from.id] = rm.gop[from.id][:0], 0
		}
		if rm.gopBytes[from.id]+len(data) <= gopLimit {
			rm.gop[from.id] = append(rm.gop[from.id], data)
			rm.gopBytes[from.id] += len(data)
		}
	}
	for _, m := range rm.members {
		if m == from {
			continue
		}
		switch h.Kind {
		case e2e.KindVideo:
			m.enqueueVideo(data, from.id, h)
		case e2e.KindFile:
			if !m.enqueue(data, m.files, false) {
				m.sendCtl(Ctl{T: "file-lost", ID: from.id, Seq: h.Seq})
			}
		default:
			m.enqueue(data, m.prio, true)
		}
	}
}

// enqueue legt ab. evict: bei voller Schlange das Älteste verwerfen statt
// das Neue (für Ton – lieber ein Knacken als Verzögerung).
// qitem ist ein Paket in der Warteschlange samt Zeitpunkt – was zu alt ist,
// fliegt beim Senden raus (siehe stale), damit sich keine Verzögerung aufbaut.
type qitem struct {
	data []byte
	at   time.Time
}

// Höchstalter in der Warteschlange: Lieber ein Bild oder 20 ms Ton
// auslassen als dauerhaft hinterherhängen.
const (
	maxAgeVideo = 400 * time.Millisecond
	maxAgeAudio = 250 * time.Millisecond
)

func (m *member) enqueue(data []byte, q chan qitem, evict bool) bool {
	it := qitem{data, time.Now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	select {
	case q <- it:
		return true
	default:
	}
	if evict {
		select {
		case <-q:
		default:
		}
		select {
		case q <- it:
			return true
		default:
		}
	}
	return false
}

// stale: Ist ein Bild- oder Tonpaket zu lange liegen geblieben, wird es
// nicht mehr gesendet. Fehlt dadurch ein Bildstück, geht es beim nächsten
// Keyframe weiter.
func (m *member) stale(it qitem) bool {
	if len(it.data) < e2e.HeaderLen || it.data[0] == '{' {
		return false
	}
	h, ok := e2e.ParseHeader(it.data)
	if !ok {
		return false
	}
	age := time.Since(it.at)
	switch h.Kind {
	case e2e.KindVideo:
		// Nach einem verworfenen Stück erst ab dem nächsten Keyframe weiter.
		m.mu.Lock()
		defer m.mu.Unlock()
		if age > maxAgeVideo {
			m.dropKey[h.Sender] = true
			return true
		}
		if m.dropKey[h.Sender] {
			if h.Flags&e2e.FlagKey != 0 && h.Idx == 0 {
				delete(m.dropKey, h.Sender)
				return false
			}
			return true
		}
		return false
	case e2e.KindSystem, e2e.KindVoice:
		return age > maxAgeAudio
	}
	return false
}

// enqueueVideo: Fehlt einem Empfänger ein Stück, bekommt er von diesem
// Absender erst ab dem nächsten Keyframe wieder Bild – kaputte Bilder gibt
// es so nicht.
func (m *member) enqueueVideo(data []byte, sender uint32, h e2e.Header) {
	m.mu.Lock()
	need := m.needKey[sender]
	if need {
		if h.Flags&e2e.FlagKey == 0 || h.Idx != 0 {
			m.mu.Unlock()
			return
		}
		m.needKey[sender] = false
	}
	m.mu.Unlock()
	if !m.enqueue(data, m.bulk, false) {
		m.mu.Lock()
		m.needKey[sender] = true
		m.mu.Unlock()
	}
}

func (m *member) sendCtl(c Ctl) {
	data, _ := json.Marshal(c)
	m.enqueue(data, m.prio, true)
}

func (m *member) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.prio)
	}
}

// writeLoop: Ton, Stimme und Chat zuerst, dann Bild, dann Dateien.
func (m *member) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	defer m.conn.Close()
	write := func(it qitem) bool {
		if m.stale(it) {
			return true
		}
		data := it.data
		typ := websocket.BinaryMessage
		if len(data) > 0 && data[0] == '{' {
			typ = websocket.TextMessage
		}
		m.conn.SetWriteDeadline(time.Now().Add(writeWait))
		return m.conn.WriteMessage(typ, data) == nil
	}
	for {
		select {
		case d, ok := <-m.prio:
			if !ok || !write(d) {
				return
			}
			continue
		default:
		}
		select {
		case d, ok := <-m.prio:
			if !ok || !write(d) {
				return
			}
			continue
		case d := <-m.bulk:
			if !write(d) {
				return
			}
			continue
		default:
		}
		select {
		case d, ok := <-m.prio:
			if !ok || !write(d) {
				return
			}
		case d := <-m.bulk:
			if !write(d) {
				return
			}
		case d := <-m.files:
			if !write(d) {
				return
			}
		case <-ping.C:
			m.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if m.conn.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}

// ValidID prüft eine Raum-ID (2–32 Zeichen: a–z, 0–9, -).
func ValidID(id string) bool {
	id = e2e.NormalizeRoom(id)
	if len(id) < 2 || len(id) > 32 || strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
