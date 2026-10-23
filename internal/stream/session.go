// Paket stream verbindet Aufnahme und Relay: Es holt Frames und Ton,
// kodiert die Frames als JPEG und schickt alles über den WebSocket.
package stream

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net"
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
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/video"
)

// Options bündelt alles, was eine Sitzung braucht.
type Options struct {
	Server   string
	Room     string
	Name     string
	FPS      int
	Quality  int
	MaxWidth int
	// BitrateKbps gilt für H.264, VideoMode "jpeg" erzwingt den alten Weg.
	BitrateKbps int
	VideoMode   string
	Audio       bool
	AutoAccept  bool
	Target      capture.Target
	// AudioExclude nennt Programme, die nicht mitgestreamt werden sollen.
	AudioExclude func() []string
	// Role ist "host" (eigener Raum, Standard) oder "presenter" (Teilen in
	// die Konferenz eines anderen – mit Token aus dessen Freigabe).
	Role  string
	Token string
	// Trusted liefert die dauerhaft freigegebenen Tokens (nur Host).
	Trusted func() []string
	// OnPing meldet Zeige-Klicks der Zuschauer (Position 0..1 im Bild).
	OnPing func(x, y float64, name string)
	// Pointing sagt den Zuschauern, ob Zeige-Klicks erwünscht sind.
	Pointing func() bool
	Log      *log.Logger
}

// Knock ist eine Zuschaueranfrage, die auf Freigabe wartet.
type Knock struct {
	ID    uint64 `json:"id"`
	Name  string `json:"name"`
	Token string `json:"-"` // für "Immer reinlassen"
}

// Status ist der Zustand, den Tray und Host-Panel anzeigen.
type Status struct {
	State   string    `json:"state"`
	Viewers int       `json:"viewers"`
	Names   []string  `json:"names,omitempty"`
	Pending []Knock   `json:"pending,omitempty"`
	FPS     int       `json:"fps"`
	Kbps    int       `json:"kbps"`
	W       int       `json:"w"`
	H       int       `json:"h"`
	RTTms   int       `json:"rtt_ms"`
	Err     string    `json:"error,omitempty"`
	Target  string    `json:"target,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Paused  bool      `json:"paused"`
	Audio   bool      `json:"audio"`
	Video   string    `json:"video,omitempty"` // z. B. "H.264 · NVENC" oder "JPEG"
}

type Session struct {
	opts Options
	log  *log.Logger

	mu       sync.Mutex
	status   Status
	pending  []Knock
	names    []string
	onChange func(Status)
	target   capture.Target
	// voiceKey lässt das eigene Bedienpanel ohne Anklopfen in den Sprachkanal.
	voiceKey string

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	outMu sync.Mutex
	out   *outbox

	// Live-Einstellungen: lassen sich während des Streams ändern.
	fps        atomic.Int32
	bitrate    atomic.Int32
	quality    atomic.Int32
	maxWidth   atomic.Int32
	audioOn    atomic.Bool
	paused     atomic.Bool
	autoAccept atomic.Bool
	targetGen  atomic.Int32
	forceFrame atomic.Bool
	paramGen   atomic.Int32
	// h264Off merkt sich die Quellen-Generation (+1), für die H.264 nicht
	// ging – dann bleibt es für diese Quelle beim JPEG-Weg.
	h264Off   atomic.Int32
	codec     atomic.Value // string
	videoMode atomic.Value // string

	audioAvail atomic.Bool
	audioRate  atomic.Int32
	audioCh    atomic.Int32

	// Diagnose: Sprünge zum Keyframe, längster Schreibvorgang, laufender Encoder.
	skips    atomic.Int64
	writeMax atomic.Int64 // Mikrosekunden, wird beim Auslesen zurückgesetzt
	encoder  atomic.Pointer[video.Encoder]

	viewers    atomic.Int32
	frameCount atomic.Int64
	byteCount  atomic.Int64
	rtt        atomic.Int64
	width      atomic.Int32
	height     atomic.Int32
}

// outbox ist die Sendeschlange einer Verbindung. Steuernachrichten und Ton
// gehen immer vor, vom Bild zählt nur das neueste – ein langsamer Upload
// verliert so Bilder, aber nie Ton.
type outbox struct {
	prio  chan outMsg
	frame chan frameJob
	video chan video.Unit
	done  chan struct{}
}

type outMsg struct {
	typ  int
	data []byte
}

type frameJob struct {
	seq, ts uint32
	w, h    int
	jpeg    []byte
}

// chunkSize begrenzt, wie lange ein Tonpaket hinter einem Bildstück warten
// muss: 32 KB sind selbst bei 5 Mbit/s nur rund 50 ms.
const chunkSize = 32 << 10

func newOutbox() *outbox {
	return &outbox{
		prio:  make(chan outMsg, 256),
		frame: make(chan frameJob, 1),
		video: make(chan video.Unit, 120),
		done:  make(chan struct{}),
	}
}

// putFrame ersetzt ein noch nicht gesendetes Bild durch das neue.
func (ob *outbox) putFrame(f frameJob) {
	select {
	case ob.frame <- f:
		return
	default:
	}
	select {
	case <-ob.frame:
	default:
	}
	select {
	case ob.frame <- f:
	default:
	}
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
	s := &Session{
		opts:     opts,
		log:      opts.Log,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
		target:   opts.Target,
		voiceKey: randomKey(),
		status: Status{
			State:  "connecting",
			Target: opts.Target.Label(),
			Audio:  opts.Audio,
		},
	}
	s.fps.Store(int32(opts.FPS))
	s.bitrate.Store(int32(max(opts.BitrateKbps, 500)))
	s.codec.Store("")
	s.videoMode.Store(opts.VideoMode)
	s.quality.Store(int32(opts.Quality))
	s.maxWidth.Store(int32(opts.MaxWidth))
	s.audioOn.Store(opts.Audio)
	s.autoAccept.Store(opts.AutoAccept)
	return s
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
	st.Pending = append([]Knock(nil), s.pending...)
	st.Names = append([]string(nil), s.names...)
	return st
}

// Allow lässt einen wartenden Zuschauer durch.
func (s *Session) Allow(id uint64) error {
	if err := s.sendJSON(proto.Control{T: "allow", ID: id}); err != nil {
		return err
	}
	s.removePending(id)
	return nil
}

// Deny lehnt eine Anfrage ab.
func (s *Session) Deny(id uint64) error {
	if err := s.sendJSON(proto.Control{T: "deny", ID: id}); err != nil {
		return err
	}
	s.removePending(id)
	return nil
}

// SetAutoAccept schaltet die automatische Freigabe im laufenden Stream um.
func (s *Session) SetAutoAccept(on bool) error {
	s.autoAccept.Store(on)
	return s.sendJSON(proto.Control{T: "auto", On: on})
}

// SetParams ändert Bildrate, Qualität, Breite und Bitrate sofort.
func (s *Session) SetParams(fps, quality, maxWidth, bitrateKbps int) {
	if s.fps.Load() != int32(fps) || s.bitrate.Load() != int32(bitrateKbps) {
		s.paramGen.Add(1) // H.264-Encoder mit neuen Werten neu starten
	}
	if fps > 0 {
		s.fps.Store(int32(fps))
	}
	if bitrateKbps > 0 {
		s.bitrate.Store(int32(bitrateKbps))
	}
	if quality > 0 {
		s.quality.Store(int32(quality))
	}
	s.maxWidth.Store(int32(maxWidth))
	s.forceFrame.Store(true)
}

// SetVideoMode wechselt zwischen H.264 ("auto") und JPEG – sofort.
func (s *Session) SetVideoMode(mode string) {
	if s.videoMode.Swap(mode) != mode {
		s.targetGen.Add(1)
	}
}

// SetAudio schaltet den Ton im laufenden Stream an oder aus.
func (s *Session) SetAudio(on bool) {
	if s.audioOn.Swap(on) == on {
		return
	}
	s.mu.Lock()
	s.status.Audio = on
	s.mu.Unlock()
	s.sendMeta()
	s.notifyStatus()
}

// SetPaused hält den Stream an (Privatsphäre-Knopf). Zuschauer sehen
// „Pausiert“, Bild und Ton gehen nicht mehr raus.
func (s *Session) SetPaused(on bool) {
	if s.paused.Swap(on) == on {
		return
	}
	if !on {
		s.forceFrame.Store(true)
	}
	s.mu.Lock()
	s.status.Paused = on
	s.mu.Unlock()
	s.sendJSON(proto.Control{T: "pause", On: on})
	s.notifyStatus()
}

// Paused meldet, ob der Stream gerade angehalten ist.
func (s *Session) Paused() bool { return s.paused.Load() }

// SetTarget wechselt die Quelle, ohne den Stream zu unterbrechen.
func (s *Session) SetTarget(t capture.Target) {
	s.mu.Lock()
	s.target = t
	s.status.Target = t.Label()
	s.mu.Unlock()
	s.targetGen.Add(1)
	s.notifyStatus()
}

// Target liefert die aktuelle Quelle.
func (s *Session) Target() capture.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

func (s *Session) outboxNow() *outbox {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return s.out
}

func (s *Session) setOutbox(ob *outbox) {
	s.outMu.Lock()
	s.out = ob
	s.outMu.Unlock()
}

// sendJSON legt eine Steuernachricht in die Vorrang-Schlange der Verbindung.
func (s *Session) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ob := s.outboxNow()
	if ob == nil {
		return errors.New("nicht verbunden")
	}
	select {
	case ob.prio <- outMsg{websocket.TextMessage, data}:
		return nil
	case <-ob.done:
		return errors.New("nicht verbunden")
	default:
		return errors.New("Sendepuffer voll")
	}
}

// metaMsg beschreibt den Stream für die Zuschauer: Tonrate, Titel, FPS.
func (s *Session) metaMsg() proto.Control {
	m := proto.Control{
		T:     "meta",
		Name:  s.opts.Name,
		Point: s.opts.Pointing != nil && s.opts.Pointing(),
		FPS:   int(s.fps.Load()),
		Audio: s.audioAvail.Load() && s.audioOn.Load(),
		Title: s.Target().Label(),
	}
	if c := s.codec.Load().(string); c != "" {
		m.Video, m.Codec = "h264", c
	} else {
		m.Video = "jpeg"
	}
	if s.audioAvail.Load() {
		m.Rate = int(s.audioRate.Load())
		m.Ch = int(s.audioCh.Load())
	}
	return m
}

func (s *Session) sendMeta() { s.sendJSON(s.metaMsg()) }

func (s *Session) addPending(k Knock) {
	s.mu.Lock()
	for _, p := range s.pending {
		if p.ID == k.ID {
			s.mu.Unlock()
			return
		}
	}
	s.pending = append(s.pending, k)
	s.mu.Unlock()
	s.notifyStatus()
}

func (s *Session) removePending(id uint64) {
	s.mu.Lock()
	out := s.pending[:0]
	changed := false
	for _, p := range s.pending {
		if p.ID == id {
			changed = true
			continue
		}
		out = append(out, p)
	}
	s.pending = out
	s.mu.Unlock()
	if changed {
		s.notifyStatus()
	}
}

func (s *Session) clearPending() {
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
}

// notifyStatus schiebt den aktuellen Zustand sofort an den Empfänger
// (z. B. damit das Tray bei einer Anfrage direkt Bescheid weiß).
func (s *Session) notifyStatus() {
	st := s.Status()
	s.mu.Lock()
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

func (s *Session) Options() Options { return s.opts }

// Done ist geschlossen, sobald die Sitzung beendet ist.
func (s *Session) Done() <-chan struct{} { return s.stopped }

// Stop beendet die Sitzung und wartet, bis die Sende-Schleifen stehen.
func (s *Session) Stop() {
	s.once.Do(func() {
		// Dem Relay Bescheid geben: Wir hören absichtlich auf. Dann beenden
		// die Zuschauer sofort statt das Reconnect-Fenster abzuwarten. Die
		// Schreibschleife leert die Vorrang-Schlange vor dem Aussteigen.
		s.sendJSON(proto.Control{T: "bye"})
		close(s.stop)
	})
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
	s.mu.Unlock()
	s.notifyStatus()
}

// shrinkSendBuffer hält den Kernel-Puffer klein. Sonst stauen sich bei
// langsamem Upload Sekunden an Bilddaten an, und der Ton steht dahinter.
func shrinkSendBuffer(conn *websocket.Conn) {
	nc := conn.UnderlyingConn()
	if tc, ok := nc.(*tls.Conn); ok {
		nc = tc.NetConn()
	}
	if tcp, ok := nc.(*net.TCPConn); ok {
		tcp.SetWriteBuffer(256 << 10)
		tcp.SetNoDelay(true)
	}
}

// connectAndStream läuft eine Verbindung lang und kehrt bei Fehlern zurück.
func (s *Session) connectAndStream() error {
	target, err := WSURL(s.opts.Server)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  64 << 10,
	}
	conn, _, err := dialer.Dial(target, http.Header{})
	if err != nil {
		return fmt.Errorf("Server nicht erreichbar: %w", err)
	}
	defer conn.Close()
	shrinkSendBuffer(conn)

	role := s.opts.Role
	if role == "" {
		role = "host"
	}
	join := proto.Control{T: "join", Room: s.opts.Room, Role: role, Name: s.opts.Name, Ver: version.Version, Token: s.opts.Token}
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
	defer s.clearPending()

	s.log.Printf("Stream läuft: %s → Raum %q", s.Target().Label(), s.opts.Room)

	// Ton immer öffnen, wenn es geht – dann lässt er sich live zuschalten.
	audioSrc, err := audio.Open(s.opts.AudioExclude)
	if err != nil {
		s.log.Printf("Ton nicht verfügbar, streame nur Bild: %v", err)
		audioSrc = nil
	}
	s.audioAvail.Store(audioSrc != nil)
	if audioSrc != nil {
		defer audioSrc.Close()
		s.audioRate.Store(int32(audioSrc.Rate()))
		s.audioCh.Store(int32(audioSrc.Channels()))
	}

	// Freigabemodus, Stream-Beschreibung und Pausenzustand gehen vor allem
	// anderen raus (gilt auch nach Reconnects).
	first := []proto.Control{s.metaMsg(), {T: "pause", On: s.paused.Load()}}
	if role == "host" {
		first = append([]proto.Control{{T: "auto", On: s.autoAccept.Load()}, {T: "voicekey", Token: s.voiceKey}, s.trustedMsg()}, first...)
	}
	for _, c := range first {
		if err := conn.WriteJSON(c); err != nil {
			return err
		}
	}

	ob := newOutbox()
	s.setOutbox(ob)
	defer func() {
		s.setOutbox(nil)
		close(ob.done)
	}()
	s.setState("live", "")

	stop := make(chan struct{})
	var stopOnce sync.Once
	doStop := func() { stopOnce.Do(func() { close(stop) }) }
	defer doStop()

	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errCh <- err
			}
		}()
	}
	run(func() error { return s.writeLoop(conn, ob, stop) })
	run(func() error { return s.readLoop(conn, stop) })
	run(func() error { return s.videoLoop(ob, stop) })
	if audioSrc != nil {
		run(func() error { s.audioLoop(audioSrc, ob, stop); return nil })
	}

	select {
	case err = <-errCh:
	case <-s.stop:
		doStop()
		wg.Wait()
		return nil
	}
	doStop()
	wg.Wait()
	if err == nil {
		err = errors.New("Verbindung beendet")
	}
	return err
}

// writeLoop ist der einzige Schreiber auf der Verbindung. Vor jedem Bildstück
// kommt erst alles aus der Vorrang-Schlange dran.
func (s *Session) writeLoop(conn *websocket.Conn, ob *outbox, stop <-chan struct{}) error {
	write := func(typ int, data []byte) error {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		start := time.Now()
		if err := conn.WriteMessage(typ, data); err != nil {
			return err
		}
		if d := time.Since(start).Microseconds(); d > s.writeMax.Load() {
			s.writeMax.Store(d)
		}
		s.byteCount.Add(int64(len(data)))
		return nil
	}
	var chunks [][]byte
	var videoSeq uint32
	for {
		select {
		case m := <-ob.prio:
			if err := write(m.typ, m.data); err != nil {
				return err
			}
			continue
		default:
		}
		if len(chunks) > 0 {
			if err := write(websocket.BinaryMessage, chunks[0]); err != nil {
				return err
			}
			isJPEG := chunks[0][0] == proto.MsgFrameChunk
			chunks = chunks[1:]
			if len(chunks) == 0 && isJPEG {
				s.frameCount.Add(1)
			}
			continue
		}
		select {
		case m := <-ob.prio:
			if err := write(m.typ, m.data); err != nil {
				return err
			}
		case f := <-ob.frame:
			chunks = proto.FrameChunks(f.seq, f.ts, f.w, f.h, f.jpeg, chunkSize)
		case u := <-ob.video:
			chunks = proto.VideoChunks(videoSeq, nowMS(), u.Key, u.Data, chunkSize)
			videoSeq++
		case <-stop:
			// Restliche Steuernachrichten (z. B. "bye") noch rausschicken.
			for {
				select {
				case m := <-ob.prio:
					if write(m.typ, m.data) != nil {
						return nil
					}
				default:
					return nil
				}
			}
		}
	}
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
				s.mu.Lock()
				s.names = append([]string(nil), ctl.Names...)
				s.mu.Unlock()
				s.notifyStatus()
			case "knock":
				s.addPending(Knock{ID: ctl.ID, Name: ctl.Name, Token: ctl.Token})
			case "knock-gone":
				s.removePending(ctl.ID)
			case "ping":
				if s.opts.OnPing != nil {
					go s.opts.OnPing(ctl.X, ctl.Y, ctl.Name)
				}
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
			// WriteControl darf parallel zur Schreibschleife laufen.
			binary.LittleEndian.PutUint64(ts[:], uint64(time.Now().UnixMilli()))
			conn.WriteControl(websocket.PingMessage, ts[:], time.Now().Add(5*time.Second))
		}
	}
}

// errSwitch: Quelle oder Verfahren hat gewechselt – videoLoop entscheidet neu.
var errSwitch = errors.New("Quelle gewechselt")

// useH264 sagt, ob für die aktuelle Quelle der Hardware-Weg genommen wird.
func (s *Session) useH264() bool {
	if s.videoMode.Load() == "jpeg" || s.h264Off.Load() == s.targetGen.Load()+1 {
		return false
	}
	return video.Ready() && video.Supports(s.Target())
}

func (s *Session) setVideoLabel(label string) {
	s.mu.Lock()
	changed := s.status.Video != label
	s.status.Video = label
	s.mu.Unlock()
	if changed {
		s.notifyStatus()
	}
}

// videoLoop wählt pro Quelle das Verfahren: H.264 über die Grafikkarte,
// wo es geht, sonst JPEG.
func (s *Session) videoLoop(ob *outbox, stop <-chan struct{}) error {
	for {
		gen := s.targetGen.Load()
		if s.useH264() {
			err := s.h264Loop(ob, stop, gen)
			if err == nil {
				return nil
			}
			if err == errSwitch {
				continue
			}
			s.log.Printf("H.264 geht nicht, nehme JPEG: %v", err)
			s.h264Off.Store(gen + 1)
			s.codec.Store("")
			s.sendMeta()
		}
		err := s.jpegLoop(ob, stop, gen)
		if err == errSwitch {
			continue
		}
		return err
	}
}

// h264Loop lässt den Encoder laufen, solange jemand zuschaut. Ohne Zuschauer
// oder bei Pause steht er – das kostet dann gar nichts.
func (s *Session) h264Loop(ob *outbox, stop <-chan struct{}, gen int32) error {
	var enc *video.Encoder
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()
	check := time.NewTicker(200 * time.Millisecond)
	defer check.Stop()
	params := s.paramGen.Load()
	s.setVideoLabel("H.264 (startet mit dem ersten Zuschauer)")
	failures := 0
	skipToKey := false
	var lastWatched time.Time

	for {
		// Nach dem letzten Zuschauer läuft der Encoder noch kurz weiter: Ein
		// Viewer, der sich neu verbindet, soll keinen Neustart auslösen.
		if s.viewers.Load() > 0 {
			lastWatched = time.Now()
		}
		want := !s.paused.Load() && time.Since(lastWatched) < 15*time.Second
		if params != s.paramGen.Load() && enc != nil {
			enc.Close() // neue Bitrate/FPS: Encoder neu starten
			enc = nil
		}
		params = s.paramGen.Load()
		if want && enc == nil {
			var err error
			enc, err = video.Start(video.Options{
				Target:      s.Target(),
				FPS:         int(s.fps.Load()),
				BitrateKbps: int(s.bitrate.Load()),
				Log:         s.log,
			})
			if err != nil {
				return err
			}
			s.setVideoLabel("H.264 · " + enc.Name)
			s.encoder.Store(enc)
			skipToKey = true
		}
		if !want && enc != nil {
			enc.Close()
			enc = nil
		}
		var units <-chan video.Unit
		if enc != nil {
			units = enc.Units()
		}
		select {
		case <-stop:
			return nil
		case <-check.C:
			if s.targetGen.Load() != gen {
				return errSwitch
			}
		case u, ok := <-units:
			if !ok {
				err := enc.Err()
				enc = nil
				if failures++; failures > 3 {
					return err
				}
				s.log.Printf("Encoder beendet, starte neu: %v", err)
				continue
			}
			failures = 0
			if u.Codec != "" && u.Codec != s.codec.Load().(string) {
				s.codec.Store(u.Codec)
				s.sendMeta()
			}
			// Staut sich die Leitung, lieber bis zum nächsten Keyframe springen
			// als Sekunden hinterherzuhinken.
			if len(ob.video) > max(int(s.fps.Load())/2, 10) && !skipToKey {
				skipToKey = true
				s.skips.Add(1)
			}
			if skipToKey {
				if !u.Key {
					continue
				}
				skipToKey = false
			}
			select {
			case ob.video <- u:
			default:
				skipToKey = true
			}
			s.frameCount.Add(1)
		}
	}
}

// jpegLoop holt Frames im eingestellten Takt. Ohne Zuschauer wird pausiert –
// das spart Rechenzeit und Traffic, genau wie bei Discord. Unveränderte Bilder
// werden übersprungen, neue Zuschauer bekommen trotzdem sofort ein Bild.
func (s *Session) jpegLoop(ob *outbox, stop <-chan struct{}, gen int32) error {
	src, err := capture.Open(s.Target())
	if err != nil {
		s.setState("error", err.Error())
		return err
	}
	defer src.Close()
	s.setVideoLabel("JPEG")
	if s.codec.Load().(string) != "" {
		s.codec.Store("")
		s.sendMeta()
	}

	fps := s.fps.Load()
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	var buf bytes.Buffer
	var seq uint32
	var failures int
	var prevPix, prevJPEG []byte
	var lastViewers int32 = -1
	var retryAt time.Time
	lastCheck := time.Now()

	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}
		if f := s.fps.Load(); f != fps {
			fps = f
			ticker.Reset(time.Second / time.Duration(fps))
		}
		if s.targetGen.Load() != gen {
			return errSwitch
		}
		// Ist H.264 inzwischen bereit (z. B. ffmpeg fertig geladen), umsteigen.
		if time.Since(lastCheck) > 2*time.Second {
			lastCheck = time.Now()
			if s.useH264() {
				return errSwitch
			}
		}

		viewers := s.viewers.Load()
		if viewers == 0 || s.paused.Load() || time.Now().Before(retryAt) {
			continue
		}
		force := viewers != lastViewers || s.forceFrame.Swap(false)
		quality := int(s.quality.Load())
		maxWidth := int(s.maxWidth.Load())

		var data []byte
		var w, h int
		if jpegSrc, ok := src.(capture.JPEGSource); ok {
			jd, jw, jh, err := jpegSrc.GrabJPEG(quality, maxWidth)
			if err != nil {
				s.captureFailed(&failures, &retryAt, err)
				continue
			}
			s.captureOK(&failures)
			if !force && bytes.Equal(jd, prevJPEG) {
				continue
			}
			prevJPEG = append(prevJPEG[:0], jd...)
			data, w, h = append([]byte(nil), jd...), jw, jh
		} else {
			img, err := src.Grab()
			if err != nil {
				s.captureFailed(&failures, &retryAt, err)
				continue
			}
			s.captureOK(&failures)
			img = capture.Scale(img, maxWidth)
			if !force && bytes.Equal(img.Pix, prevPix) {
				continue
			}
			prevPix = append(prevPix[:0], img.Pix...)
			w, h = img.Bounds().Dx(), img.Bounds().Dy()
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				continue
			}
			data = append([]byte(nil), buf.Bytes()...)
		}
		lastViewers = viewers
		ob.putFrame(frameJob{seq: seq, ts: nowMS(), w: w, h: h, jpeg: data})
		seq++
		s.width.Store(int32(w))
		s.height.Store(int32(h))
	}
}

func (s *Session) trustedMsg() proto.Control {
	var list []string
	if s.opts.Trusted != nil {
		list = s.opts.Trusted()
	}
	return proto.Control{T: "trusted", Names: list}
}

// SendTrusted schickt die Liste vertrauter Personen erneut an den Relay.
func (s *Session) SendTrusted() error { return s.sendJSON(s.trustedMsg()) }

// KnockToken liefert den Token einer wartenden Anfrage.
func (s *Session) KnockToken(id uint64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.pending {
		if k.ID == id {
			return k.Token
		}
	}
	return ""
}

// SetPointing hinterlegt, was bei Zeige-Klicks passiert und ob sie erlaubt sind.
func (s *Session) SetPointing(onPing func(x, y float64, name string), allowed func() bool) {
	s.opts.OnPing = onPing
	s.opts.Pointing = allowed
}

// VoiceKey ist der Schlüssel, mit dem das Panel dem Sprachkanal beitritt.
func (s *Session) VoiceKey() string { return s.voiceKey }

func randomKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// DiagLine fasst den Zustand des Streams für den Diagnosemodus zusammen.
func (s *Session) DiagLine() string {
	st := s.Status()
	line := fmt.Sprintf("stream %s %s · %d fps · %d kbit/s · %d Zuschauer · Ping %d ms · Keyframe-Sprünge %d · längstes Senden %d ms",
		st.State, st.Video, st.FPS, st.Kbps, st.Viewers, st.RTTms, s.skips.Load(), s.writeMax.Swap(0)/1000)
	if ob := s.outboxNow(); ob != nil {
		line += fmt.Sprintf(" · Warteschlange Video %d/%d Ton %d/%d", len(ob.video), cap(ob.video), len(ob.prio), cap(ob.prio))
	}
	if st.Paused {
		line += " · pausiert"
	}
	if st.Err != "" {
		line += " · Fehler: " + st.Err
	}
	return line
}

// EncoderStderr liefert die letzten Meldungen des laufenden Encoders.
func (s *Session) EncoderStderr() string {
	if e := s.encoder.Load(); e != nil {
		return e.Stderr()
	}
	return ""
}

// captureFailed: Klappt die Aufnahme mehrmals hintereinander nicht, steht der
// Fehler im Panel und es wird alle 2 s neu versucht. Die Verbindung bleibt –
// früher flogen dadurch alle Zuschauer im Sekundentakt raus.
func (s *Session) captureFailed(failures *int, retryAt *time.Time, err error) {
	if *failures++; *failures < 6 {
		return
	}
	if *failures == 6 {
		s.log.Printf("Aufnahme klappt nicht: %v", err)
	}
	s.mu.Lock()
	s.status.Err = "Aufnahme: " + err.Error()
	s.mu.Unlock()
	*retryAt = time.Now().Add(2 * time.Second)
}

func (s *Session) captureOK(failures *int) {
	if *failures >= 6 {
		s.log.Println("Aufnahme läuft wieder")
		s.mu.Lock()
		s.status.Err = ""
		s.mu.Unlock()
	}
	*failures = 0
}

// audioLoop liest den Systemton und schickt ihn in 20-ms-Häppchen.
// Stille wird als Marker übertragen, das spart Bandbreite.
func (s *Session) audioLoop(src audio.Source, ob *outbox, stop <-chan struct{}) {
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
			s.log.Printf("Tonquelle beendet: %v", err)
			return
		}
		if s.viewers.Load() == 0 || s.paused.Load() || !s.audioOn.Load() {
			continue
		}
		var packet []byte
		if isSilent(buf) {
			packet = proto.SilencePacket(seq, nowMS())
		} else {
			packet = proto.AudioPacket(seq, nowMS(), buf)
		}
		seq++
		select {
		case ob.prio <- outMsg{websocket.BinaryMessage, packet}:
		case <-stop:
			return
		}
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
		s.mu.Unlock()
		prevFrames, prevBytes = frames, bytesSent
		s.notifyStatus()
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
