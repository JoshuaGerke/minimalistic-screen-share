// Paket stream verbindet Aufnahme und Relay: Es holt Frames und Ton,
// kodiert die Frames als JPEG und schickt alles über den WebSocket.
package stream

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

// Options bündelt alles, was eine Sitzung braucht.
type Options struct {
	Server   string
	Room     string
	Name     string
	FPS      int
	Quality  int
	MaxWidth int
	Audio    bool
	Target   capture.Target
	Log      *log.Logger
}

// Status ist der Zustand, den Tray und Host-Panel anzeigen.
type Status struct {
	State   string    `json:"state"`
	Viewers int       `json:"viewers"`
	FPS     int       `json:"fps"`
	Kbps    int       `json:"kbps"`
	W       int       `json:"w"`
	H       int       `json:"h"`
	RTTms   int       `json:"rtt_ms"`
	Err     string    `json:"error,omitempty"`
	Target  string    `json:"target,omitempty"`
	Started time.Time `json:"started,omitempty"`
}

type Session struct {
	opts Options
	log  *log.Logger

	mu       sync.Mutex
	status   Status
	onChange func(Status)

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	viewers    atomic.Int32
	frameCount atomic.Int64
	byteCount  atomic.Int64
	rtt        atomic.Int64
	width      atomic.Int32
	height     atomic.Int32
}

func New(opts Options) *Session {
	if opts.FPS <= 0 {
		opts.FPS = 15
	}
	if opts.Quality <= 0 {
		opts.Quality = 70
	}
	if opts.Log == nil {
		opts.Log = log.Default()
	}
	return &Session{
		opts:    opts,
		log:     opts.Log,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
		status: Status{
			State:  "connecting",
			Target: opts.Target.Label(),
		},
	}
}

// SetOnChange meldet Zustandsänderungen an Tray oder Panel.
func (s *Session) SetOnChange(fn func(Status)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.RTTms = int(s.rtt.Load())
	return st
}

func (s *Session) Options() Options { return s.opts }

// Stop beendet die Sitzung und wartet, bis die Sende-Schleifen stehen.
func (s *Session) Stop() {
	s.once.Do(func() { close(s.stop) })
	<-s.stopped
}

// Run verbindet und streamt, bis Stop kommt. Nach Verbindungsabbrüchen
// wird mit wachsendem Abstand neu versucht.
func (s *Session) Run() {
	defer close(s.stopped)
	go s.statsLoop()

	backoff := time.Second
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		s.setState("connecting", "")
		started := time.Now()
		err := s.connectAndStream()

		select {
		case <-s.stop:
			return
		default:
		}
		if err != nil {
			s.log.Printf("Stream: %v", err)
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		s.setState("reconnecting", errMsg(err))
		select {
		case <-s.stop:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Session) setState(state, err string) {
	s.mu.Lock()
	s.status.State = state
	s.status.Err = err
	if state == "live" && s.status.Started.IsZero() {
		s.status.Started = time.Now()
	}
	cb := s.onChange
	st := s.status
	s.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// connectAndStream läuft eine Verbindung lang und kehrt bei Fehlern zurück.
func (s *Session) connectAndStream() error {
	target, err := WSURL(s.opts.Server)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  1 << 20,
	}
	conn, _, err := dialer.Dial(target, http.Header{})
	if err != nil {
		return fmt.Errorf("Server nicht erreichbar: %w", err)
	}
	defer conn.Close()

	join := proto.Control{T: "join", Room: s.opts.Room, Role: "host", Name: s.opts.Name, Ver: version.Version}
	if err := conn.WriteJSON(join); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("keine Antwort vom Server: %w", err)
	}
	var ctl proto.Control
	if mt != websocket.TextMessage || json.Unmarshal(data, &ctl) != nil {
		return errors.New("unerwartete Antwort vom Server")
	}
	switch ctl.T {
	case "joined":
		s.viewers.Store(int32(ctl.N))
	case "error":
		return errors.New(ctl.Msg)
	default:
		return fmt.Errorf("unerwartete Antwort: %q", ctl.T)
	}
	conn.SetReadDeadline(time.Time{})
	// Verbunden: Der Statistik-Loop schaltet auf "pausiert", solange
	// niemand zuschaut.
	s.setState("live", "")

	src, err := capture.Open(s.opts.Target)
	if err != nil {
		s.setState("error", err.Error())
		return err
	}
	defer src.Close()
	s.log.Printf("Stream läuft: %s → Raum %q", s.opts.Target.Label(), s.opts.Room)

	var audioSrc audio.Source
	if s.opts.Audio {
		audioSrc, err = audio.Open()
		if err != nil {
			s.log.Printf("Ton nicht verfügbar, streame nur Bild: %v", err)
			audioSrc = nil
		}
	}
	if audioSrc != nil {
		defer audioSrc.Close()
	}

	meta := proto.Control{T: "meta", FPS: s.opts.FPS, Audio: audioSrc != nil, Title: s.opts.Target.Label()}
	if audioSrc != nil {
		meta.Rate = audioSrc.Rate()
		meta.Ch = audioSrc.Channels()
	}
	if err := conn.WriteJSON(meta); err != nil {
		return err
	}

	stop := make(chan struct{})
	defer close(stop)
	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.readLoop(conn, stop); err != nil {
			errCh <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.captureLoop(conn, src, stop); err != nil {
			errCh <- err
		}
	}()
	if audioSrc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioLoop(conn, audioSrc, stop)
		}()
	}

	err = <-errCh
	close(stop)
	wg.Wait()
	if err == nil {
		err = errors.New("Verbindung beendet")
	}
	return err
}

// readLoop verarbeitet Steuernachrichten und misst per WebSocket-Ping die Laufzeit.
func (s *Session) readLoop(conn *websocket.Conn, stop <-chan struct{}) error {
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(payload string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if len(payload) == 8 {
			ts := int64(binary.LittleEndian.Uint64([]byte(payload)))
			s.rtt.Store(int64(time.Since(time.UnixMilli(ts)) / time.Millisecond))
		}
		return nil
	})

	readErr := make(chan error, 1)
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			if mt != websocket.TextMessage {
				continue
			}
			var ctl proto.Control
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.T {
			case "viewers":
				s.viewers.Store(int32(ctl.N))
			case "error":
				if ctl.Fatal {
					readErr <- errors.New(ctl.Msg)
					return
				}
			}
		}
	}()

	ping := time.NewTicker(3 * time.Second)
	defer ping.Stop()
	var ts [8]byte
	for {
		select {
		case <-stop:
			return nil
		case err := <-readErr:
			return err
		case <-ping.C:
			binary.LittleEndian.PutUint64(ts[:], uint64(time.Now().UnixMilli()))
			conn.WriteControl(websocket.PingMessage, ts[:], time.Now().Add(5*time.Second))
		}
	}
}

// captureLoop holt Frames im eingestellten Takt. Ohne Zuschauer wird pausiert –
// das spart Rechenzeit und Traffic, genau wie bei Discord. Unveränderte Bilder
// werden übersprungen, neue Zuschauer bekommen trotzdem sofort ein Bild.
func (s *Session) captureLoop(conn *websocket.Conn, src capture.Source, stop <-chan struct{}) error {
	interval := time.Second / time.Duration(s.opts.FPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	jpegSrc, isJPEG := src.(capture.JPEGSource)
	var buf bytes.Buffer
	var seq uint32
	var failures int
	var prevPix, prevJPEG []byte
	var lastViewers int32 = -1

	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}

		viewers := s.viewers.Load()
		if viewers == 0 {
			continue
		}
		force := viewers != lastViewers

		var packet []byte
		var w, h int
		if isJPEG {
			data, jw, jh, err := jpegSrc.GrabJPEG(s.opts.Quality, s.opts.MaxWidth)
			if err != nil {
				if failures++; failures > 5 {
					return fmt.Errorf("Aufnahme: %w", err)
				}
				continue
			}
			failures = 0
			if !force && bytes.Equal(data, prevJPEG) {
				continue
			}
			prevJPEG = append(prevJPEG[:0], data...)
			w, h = jw, jh
			packet = proto.FramePacket(seq, nowMS(), w, h, data)
		} else {
			img, err := src.Grab()
			if err != nil {
				if failures++; failures > 5 {
					return fmt.Errorf("Aufnahme: %w", err)
				}
				continue
			}
			failures = 0
			img = capture.Scale(img, s.opts.MaxWidth)
			if !force && bytes.Equal(img.Pix, prevPix) {
				continue
			}
			prevPix = append(prevPix[:0], img.Pix...)
			w, h = img.Bounds().Dx(), img.Bounds().Dy()
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: s.opts.Quality}); err != nil {
				continue
			}
			packet = proto.FramePacket(seq, nowMS(), w, h, buf.Bytes())
		}
		seq++
		lastViewers = viewers

		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
			return err
		}
		s.frameCount.Add(1)
		s.byteCount.Add(int64(len(packet)))
		s.width.Store(int32(w))
		s.height.Store(int32(h))
	}
}

// audioLoop liest den Systemton und schickt ihn in 20-ms-Häppchen.
// Stille wird als Marker übertragen, das spart Bandbreite.
func (s *Session) audioLoop(conn *websocket.Conn, src audio.Source, stop <-chan struct{}) {
	chunkSize := src.Rate() / 50 * 2 * src.Channels()
	buf := make([]byte, chunkSize)
	var seq uint32
	for {
		select {
		case <-stop:
			return
		default:
		}
		if _, err := io.ReadFull(src, buf); err != nil {
			s.log.Printf("Tonquelle beendet: %v", err)
			return
		}
		if s.viewers.Load() == 0 {
			continue
		}
		var packet []byte
		if isSilent(buf) {
			packet = proto.SilencePacket(seq, nowMS())
		} else {
			packet = proto.AudioPacket(seq, nowMS(), buf)
		}
		seq++
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
			return
		}
		s.byteCount.Add(int64(len(packet)))
	}
}

func isSilent(pcm []byte) bool {
	for _, b := range pcm {
		if b != 0 {
			return false
		}
	}
	return true
}

// statsLoop rechnet FPS und Bitrate aus und pflegt den Pausenzustand.
func (s *Session) statsLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var prevFrames, prevBytes int64
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		frames := s.frameCount.Load()
		bytesSent := s.byteCount.Load()
		s.mu.Lock()
		s.status.FPS = int(frames - prevFrames)
		s.status.Kbps = int((bytesSent - prevBytes) * 8 / 1000)
		s.status.W = int(s.width.Load())
		s.status.H = int(s.height.Load())
		s.status.Viewers = int(s.viewers.Load())
		if s.status.State == "live" || s.status.State == "paused" {
			if s.viewers.Load() == 0 {
				s.status.State = "paused"
			} else {
				s.status.State = "live"
			}
		}
		cb := s.onChange
		st := s.status
		s.mu.Unlock()
		prevFrames, prevBytes = frames, bytesSent
		if cb != nil {
			cb(st)
		}
	}
}

func nowMS() uint32 {
	return uint32(time.Now().UnixMilli())
}

// WSURL macht aus der Server-Adresse den WebSocket-Endpunkt.
func WSURL(server string) (string, error) {
	s := strings.TrimSpace(server)
	if s == "" {
		s = "http://localhost:8080"
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("ungültige Server-Adresse: %q", server)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("ungültiges Schema: %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
