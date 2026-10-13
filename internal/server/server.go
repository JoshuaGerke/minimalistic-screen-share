// Paket server ist der Relay: Er nimmt Hosts und Viewer in Räume auf und
// leitet Bild- und Tondaten stumpf weiter. Kein Transcoding, kaum Zustand.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"
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
	hostGrace      = 20 * time.Second
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
}

type room struct {
	code    string
	host    *client
	viewers map[*client]struct{}
	grace   *time.Timer
}

type client struct {
	srv  *Server
	conn *websocket.Conn
	send chan []byte
	role string
	name string
	room *room

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
		rooms: map[string]*room{},
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
	c := &client{srv: s, conn: conn, send: make(chan []byte, sendQueue)}

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
	if join.Role != "host" && join.Role != "viewer" {
		c.fail("Unbekannte Rolle")
		return
	}
	c.role = join.Role
	c.name = strings.TrimSpace(join.Name)
	if c.name == "" {
		c.name = "Unbekannt"
	}

	if err := s.join(c, code); err != nil {
		c.fail(err.Error())
		return
	}
	defer s.leave(c)

	go c.writeLoop()
	c.readLoop()
}

// join hängt den Client in den Raum ein und schickt die ersten Steuernachrichten.
func (s *Server) join(c *client, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rm := s.rooms[code]

	if c.role == "host" {
		if rm != nil && rm.host != nil {
			return errors.New("Der Raum ist schon belegt")
		}
		if rm == nil {
			rm = &room{code: code, viewers: map[*client]struct{}{}}
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
		s.log.Printf("Raum %q: Host %s ist da (%d Zuschauer)", code, c.name, len(rm.viewers))
		if resume {
			if len(rm.viewers) > 0 {
				s.broadcastLocked(rm, proto.Control{T: "live"})
				s.log.Printf("Raum %q: Host wieder da", code)
			}
			s.sendViewersLocked(rm)
		}
		return nil
	}

	if rm == nil {
		return errors.New("Stream nicht gefunden – läuft er noch?")
	}
	if len(rm.viewers) >= s.cfg.MaxViewers {
		return errors.New("Der Raum ist voll")
	}
	rm.viewers[c] = struct{}{}
	c.room = rm
	c.enqueueControl(proto.Control{T: "joined", Room: code, N: len(rm.viewers)})
	if rm.host == nil {
		c.enqueueControl(proto.Control{T: "reconnecting"})
	}
	s.log.Printf("Raum %q: Zuschauer %s dabei (%d)", code, c.name, len(rm.viewers))
	s.sendViewersLocked(rm)
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
	if c.role == "host" && rm.host == c {
		rm.host = nil
		s.broadcastLocked(rm, proto.Control{T: "reconnecting"})
		code := rm.code
		rm.grace = time.AfterFunc(s.cfg.HostGrace, func() { s.hostTimeout(code, rm) })
		s.log.Printf("Raum %q: Host weg, warte %s auf Reconnect", code, s.cfg.HostGrace)
	} else if c.role == "viewer" {
		delete(rm.viewers, c)
		s.log.Printf("Raum %q: Zuschauer %s raus (%d)", rm.code, c.name, len(rm.viewers))
		s.sendViewersLocked(rm)
	}
	s.mu.Unlock()
	c.close()
}

func (s *Server) hostTimeout(code string, rm *room) {
	s.mu.Lock()
	if s.rooms[code] != rm || rm.host != nil {
		s.mu.Unlock()
		return
	}
	delete(s.rooms, code)
	viewers := make([]*client, 0, len(rm.viewers))
	for v := range rm.viewers {
		viewers = append(viewers, v)
	}
	rm.viewers = map[*client]struct{}{}
	s.mu.Unlock()

	for _, v := range viewers {
		v.enqueueControl(proto.Control{T: "ended"})
		v.close()
	}
	s.log.Printf("Raum %q: geschlossen", code)
}

// relayBinary schickt ein Binärpaket vom Host an alle Zuschauer.
func (s *Server) relayBinary(from *client, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if from.room == nil || from.room.host != from {
		return
	}
	for v := range from.room.viewers {
		v.enqueue(msg, false)
	}
}

// relayControl schickt eine Steuernachricht vom Host an alle Zuschauer.
func (s *Server) relayControl(from *client, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if from.room == nil || from.room.host != from {
		return
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

// sendViewersLocked verteilt den aktuellen Zuschauerstand an alle im Raum.
func (s *Server) sendViewersLocked(rm *room) {
	data, _ := json.Marshal(proto.Control{T: "viewers", N: len(rm.viewers)})
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
			if c.role == "host" {
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
		default:
			if c.role == "host" {
				c.srv.relayControl(c, data)
			}
		}
	}
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.Close()
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			typ := websocket.BinaryMessage
			if len(msg) > 0 && msg[0] == '{' {
				typ = websocket.TextMessage
			}
			if err := c.conn.WriteMessage(typ, msg); err != nil {
				c.conn.Close()
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
// Frames verworfen (priority false) oder die älteste Steuernachricht ersetzt
// (priority true), damit ein hängender Viewer niemanden blockiert.
func (c *client) enqueue(msg []byte, priority bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
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
