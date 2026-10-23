// Paket share schickt den eigenen Bildschirm (H.264 über die Grafikkarte)
// und auf Wunsch den Systemton Ende-zu-Ende verschlüsselt in den Raum.
// Es ist ein eigener Teilnehmer der Art "media"; das Raum-Fenster zeigt ihn
// unter dem Namen dessen, der teilt.
package share

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/e2e"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/room"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/video"
)

// Options beschreibt, was wohin geteilt wird.
type Options struct {
	Relay        func() string // aktuelle WebSocket-Adresse des Raums ("" = gerade keine)
	Room         string
	Keys         e2e.Keys
	Owner        string // Name dessen, der teilt
	Target       capture.Target
	FPS          int
	BitrateKbps  int
	Audio        bool
	AudioExclude func() []string
	Origin       string                          // Marke dieses PCs – das eigene Fenster spielt das nie ab
	Point        bool                            // Zeigen per 5-fach-Klick erlaubt
	OnPoint      func(x, y float64, from string) // Zeige-Klick der anderen
	Log          *log.Logger
}

// Status zeigt das Fenster an.
type Status struct {
	Live    bool   `json:"live"`
	ID      uint32 `json:"id,omitempty"` // eigene Kennung im Raum – das Fenster spielt sie nie ab
	Encoder string `json:"encoder,omitempty"`
	FPS     int    `json:"fps"`
	Kbps    int    `json:"kbps"`
	Error   string `json:"error,omitempty"`
	Target  string `json:"target"`
	Paused  bool   `json:"paused"`
	Audio   bool   `json:"audio"`
}

// Share ist ein laufendes Teilen.
type Share struct {
	opts Options
	box  *e2e.Box
	log  *log.Logger

	mu     sync.Mutex
	status Status
	target capture.Target

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	paused    atomic.Bool
	audioOn   atomic.Bool
	targetGen atomic.Int32
	frames    atomic.Int64
	bytes     atomic.Int64
	id        atomic.Uint32
	rate      atomic.Int32
}

// Start beginnt zu teilen und verbindet sich bei Abbrüchen von selbst neu.
func Start(o Options) (*Share, error) {
	box, err := e2e.NewBox(o.Keys.Enc)
	if err != nil {
		return nil, err
	}
	if o.Log == nil {
		o.Log = log.Default()
	}
	if o.FPS <= 0 {
		o.FPS = 30
	}
	if o.BitrateKbps <= 0 {
		o.BitrateKbps = 6000
	}
	s := &Share{opts: o, box: box, log: o.Log, target: o.Target, stop: make(chan struct{}), stopped: make(chan struct{})}
	s.audioOn.Store(o.Audio)
	s.status = Status{Target: o.Target.Label(), Audio: o.Audio}
	go s.run()
	go s.stats()
	return s, nil
}

// Stop beendet das Teilen.
func (s *Share) Stop() {
	s.once.Do(func() { close(s.stop) })
	<-s.stopped
}

// Status liefert den aktuellen Stand.
func (s *Share) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Target ist die aktuelle Quelle.
func (s *Share) Target() capture.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

// SetTarget wechselt die Quelle ohne neu zu verbinden.
func (s *Share) SetTarget(t capture.Target) {
	s.mu.Lock()
	s.target = t
	s.status.Target = t.Label()
	s.mu.Unlock()
	s.targetGen.Add(1)
}

// SetPaused hält das Bild an (die anderen sehen "Pausiert").
func (s *Share) SetPaused(on bool) {
	s.paused.Store(on)
	s.mu.Lock()
	s.status.Paused = on
	s.mu.Unlock()
	s.targetGen.Add(1) // Encoder neu starten → sofort ein Keyframe beim Fortsetzen
}

// SetAudio schaltet den Systemton zu oder ab.
func (s *Share) SetAudio(on bool) {
	s.audioOn.Store(on)
	s.mu.Lock()
	s.status.Audio = on
	s.mu.Unlock()
}

func (s *Share) setErr(e string) {
	s.mu.Lock()
	s.status.Error = e
	s.mu.Unlock()
}

func (s *Share) stats() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var pf, pb int64
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		f, b := s.frames.Load(), s.bytes.Load()
		s.mu.Lock()
		s.status.FPS, s.status.Kbps = int(f-pf), int((b-pb)*8/1000)
		s.mu.Unlock()
		pf, pb = f, b
	}
}

func (s *Share) run() {
	defer close(s.stopped)
	backoff := time.Second
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		start := time.Now()
		err := s.session()
		select {
		case <-s.stop:
			return
		default:
		}
		s.mu.Lock()
		s.status.Live = false
		s.mu.Unlock()
		if err != nil {
			s.log.Printf("Teilen: %v", err)
			s.setErr(err.Error())
		}
		if time.Since(start) > 20*time.Second {
			backoff = time.Second
		}
		select {
		case <-s.stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 8*time.Second)
	}
}

type out struct {
	prio  chan []byte
	video chan video.Unit
}

// session ist eine Verbindung zum Raum.
func (s *Share) session() error {
	addr := s.opts.Relay()
	if addr == "" {
		return errors.New("Raum gerade nicht erreichbar")
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second, WriteBufferSize: 64 << 10}).Dial(addr, http.Header{})
	if err != nil {
		return fmt.Errorf("Raum nicht erreichbar: %w", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(room.Ctl{T: "join", Room: s.opts.Room, Auth: hex.EncodeToString(s.opts.Keys.Auth), Kind: "media"}); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var w room.Ctl
	if err := conn.ReadJSON(&w); err != nil {
		return err
	}
	if w.T != "welcome" {
		return errors.New(w.Msg)
	}
	conn.SetReadDeadline(time.Time{})
	s.id.Store(w.ID)
	s.setErr("")

	src, err := audio.Open(s.opts.AudioExclude)
	if err != nil {
		s.log.Printf("Systemton nicht verfügbar: %v", err)
		src = nil
	}
	if src != nil {
		defer src.Close()
		s.rate.Store(int32(src.Rate()))
	}

	o := out{prio: make(chan []byte, 256), video: make(chan video.Unit, 120)}
	stop := make(chan struct{})
	var stopOnce sync.Once
	halt := func() { stopOnce.Do(func() { close(stop) }) }
	defer halt()
	errc := make(chan error, 4)
	var wg sync.WaitGroup
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				errc <- err
			}
		}()
	}
	run(func() error { return s.write(conn, o, stop) })
	run(func() error { return s.read(conn, stop) })
	run(func() error { return s.videoLoop(o, stop) })
	if src != nil {
		run(func() error { s.audioLoop(src, o, stop); return nil })
	}
	s.sendState(o)
	s.mu.Lock()
	s.status.Live, s.status.ID = true, w.ID
	s.mu.Unlock()
	s.log.Printf("Teile %s in Raum %q (E2E)", s.Target().Label(), s.opts.Room)

	select {
	case err = <-errc:
	case <-s.stop:
	}
	halt()
	conn.Close()
	wg.Wait()
	return err
}

// sendState beschreibt den Bildschirm für die anderen (verschlüsselt).
func (s *Share) sendState(o out) {
	st := map[string]any{
		"type": "screen", "owner": s.opts.Owner, "title": s.Target().Label(),
		"audio": s.audioOn.Load() && s.rate.Load() > 0, "rate": s.rate.Load(), "paused": s.paused.Load(),
		"point": s.opts.Point, "origin": s.opts.Origin,
	}
	data, _ := json.Marshal(st)
	select {
	case o.prio <- s.box.Seal(e2e.Header{Kind: e2e.KindState}, data):
	default:
	}
}

func (s *Share) write(conn *websocket.Conn, o out, stop <-chan struct{}) error {
	put := func(d []byte) error {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, d); err != nil {
			return err
		}
		s.bytes.Add(int64(len(d)))
		return nil
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	var chunks [][]byte
	var seq uint32
	for {
		select {
		case d := <-o.prio:
			if err := put(d); err != nil {
				return err
			}
			continue
		default:
		}
		if len(chunks) > 0 {
			if err := put(chunks[0]); err != nil {
				return err
			}
			chunks = chunks[1:]
			continue
		}
		select {
		case d := <-o.prio:
			if err := put(d); err != nil {
				return err
			}
		case u := <-o.video:
			seq++
			chunks = s.seal(u, seq)
			s.frames.Add(1)
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(room.Ctl{T: "ping", TS: time.Now().UnixMilli()}); err != nil {
				return err
			}
		case <-stop:
			return nil
		}
	}
}

// chunkSize: Ton muss nie lange hinter einem Bildstück warten.
const chunkSize = 32 << 10

func (s *Share) seal(u video.Unit, seq uint32) [][]byte {
	count := (len(u.Data) + chunkSize - 1) / chunkSize
	var flags byte
	if u.Key {
		flags = e2e.FlagKey
	}
	res := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		part := u.Data[i*chunkSize : min((i+1)*chunkSize, len(u.Data))]
		res = append(res, s.box.Seal(e2e.Header{Kind: e2e.KindVideo, Flags: flags, Seq: seq, Idx: uint16(i), Count: uint16(count)}, part))
	}
	return res
}

// read wertet Ereignisse der anderen aus (Zeige-Klicks).
func (s *Share) read(conn *websocket.Conn, stop <-chan struct{}) error {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
			}
			return err
		}
		if mt == websocket.TextMessage {
			var c room.Ctl
			if json.Unmarshal(data, &c) == nil && c.T == "moved" {
				return errors.New("Raum ist umgezogen")
			}
			continue
		}
		h, ok := e2e.ParseHeader(data)
		if !ok || h.Kind != e2e.KindEvent {
			continue
		}
		_, plain, err := s.box.Open(data)
		if err != nil {
			continue
		}
		var ev struct {
			Type   string  `json:"type"`
			Target uint32  `json:"target"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			From   string  `json:"from"`
		}
		if json.Unmarshal(plain, &ev) != nil {
			continue
		}
		if ev.Type == "point" && ev.Target == s.id.Load() && s.opts.OnPoint != nil && ev.X >= 0 && ev.X <= 1 && ev.Y >= 0 && ev.Y <= 1 {
			go s.opts.OnPoint(ev.X, ev.Y, ev.From)
		}
	}
}

// videoLoop lässt den Hardware-Encoder laufen; bei Stau geht es zum
// nächsten Keyframe statt hinterherzuhinken.
func (s *Share) videoLoop(o out, stop <-chan struct{}) error {
	for {
		gen := s.targetGen.Load()
		if s.paused.Load() {
			s.sendState(o)
			for s.paused.Load() && s.targetGen.Load() == gen {
				select {
				case <-stop:
					return nil
				case <-time.After(200 * time.Millisecond):
				}
			}
			continue
		}
		enc, err := video.Start(video.Options{Target: s.Target(), FPS: s.opts.FPS, BitrateKbps: s.opts.BitrateKbps, Log: s.log})
		if err != nil {
			s.setErr("Bildschirm: " + err.Error())
			return err
		}
		s.mu.Lock()
		s.status.Encoder = enc.Name
		s.mu.Unlock()
		s.sendState(o)
		skip := true
		check := time.NewTicker(200 * time.Millisecond)
	inner:
		for {
			select {
			case <-stop:
				check.Stop()
				enc.Close()
				return nil
			case <-check.C:
				if s.targetGen.Load() != gen {
					break inner
				}
			case u, ok := <-enc.Units():
				if !ok {
					check.Stop()
					return fmt.Errorf("Encoder beendet: %v", enc.Err())
				}
				if len(o.video) > max(s.opts.FPS/2, 10) {
					skip = true
				}
				if skip {
					if !u.Key {
						continue
					}
					skip = false
				}
				select {
				case o.video <- u:
				default:
					skip = true
				}
			}
		}
		check.Stop()
		enc.Close()
	}
}

// audioLoop schickt den Systemton in 20-ms-Stücken; Stille nur als Marke.
func (s *Share) audioLoop(src audio.Source, o out, stop <-chan struct{}) {
	size := src.Rate() / 50 * 2 * src.Channels()
	var seq uint32
	for {
		select {
		case <-stop:
			return
		default:
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(src, buf); err != nil {
			return
		}
		if s.paused.Load() || !s.audioOn.Load() {
			continue
		}
		seq++
		h := e2e.Header{Kind: e2e.KindSystem, Seq: seq}
		payload := buf
		if silent(buf) {
			h.Flags, payload = 1, nil
		}
		select {
		case o.prio <- s.box.Seal(h, payload):
		case <-stop:
			return
		}
	}
}

func silent(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
