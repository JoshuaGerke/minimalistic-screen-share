// Paket video erzeugt einen H.264-Strom so, wie es OBS und Discord machen:
// Aufnahme auf der Grafikkarte (Windows: Desktop Duplication, Linux:
// wlroots-Screencopy) und Hardware-Encoder (NVENC, AMF, QuickSync, VAAPI).
// Die eigentliche Arbeit macht ein externes Programm (ffmpeg bzw.
// wf-recorder), hier wird nur gestartet und der Datenstrom in einzelne Bilder
// zerlegt.
package video

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
)

// Options beschreibt, was und wie kodiert wird.
type Options struct {
	Target      capture.Target
	FPS         int
	BitrateKbps int
	Log         *log.Logger
}

// Unit ist ein komplettes Bild (H.264-Zugriffseinheit, Annex B).
type Unit struct {
	Data  []byte
	Key   bool
	Codec string // WebCodecs-Name, sobald ein SPS gesehen wurde
}

// ErrUnsupported heißt: Für dieses Ziel gibt es keinen Hardware-Weg, die
// Sitzung soll beim JPEG-Verfahren bleiben.
var ErrUnsupported = errors.New("H.264 für dieses Ziel nicht verfügbar")

// Encoder ist ein laufender Encoder-Prozess.
type Encoder struct {
	Name  string // z. B. "NVENC"
	cmd   *exec.Cmd
	units chan Unit
	mu    sync.Mutex
	err   error
	tail  *tailBuffer
}

// Units liefert die fertigen Bilder; der Kanal schließt, wenn der Prozess endet.
func (e *Encoder) Units() <-chan Unit { return e.units }

// Err sagt, warum der Encoder aufgehört hat.
func (e *Encoder) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		return nil
	}
	if t := e.tail.String(); t != "" {
		return fmt.Errorf("%v: %s", e.err, t)
	}
	return e.err
}

// Stderr liefert die letzten Meldungen des Encoders (für den Diagnosemodus).
func (e *Encoder) Stderr() string { return e.tail.String() }

// Close beendet den Prozess.
func (e *Encoder) Close() {
	if e.cmd.Process != nil {
		e.cmd.Process.Kill()
	}
}

// run startet cmd und zerlegt seine Standardausgabe in Bilder.
func run(name string, cmd *exec.Cmd) (*Encoder, error) {
	hideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	e := &Encoder{Name: name, cmd: cmd, units: make(chan Unit, 8), tail: tail}

	reads := make(chan []byte, 16)
	go func() {
		defer close(reads)
		for {
			buf := make([]byte, 64<<10)
			n, err := out.Read(buf)
			if n > 0 {
				reads <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(e.units)
		sp := &splitter{emit: func(u Unit) { e.units <- u }}
		// Kommt ein paar Millisekunden nichts nach, ist das Bild komplett –
		// so wartet ein Standbild nicht auf das nächste Bild.
		idle := time.NewTimer(time.Hour)
		for {
			select {
			case p, ok := <-reads:
				if !ok {
					sp.flush()
					err := cmd.Wait()
					if err == nil {
						err = io.EOF
					}
					e.mu.Lock()
					e.err = err
					e.mu.Unlock()
					return
				}
				sp.feed(p)
				idle.Reset(5 * time.Millisecond)
			case <-idle.C:
				sp.flush()
			}
		}
	}()
	return e, nil
}

// splitter zerlegt einen Annex-B-Strom in Zugriffseinheiten.
type splitter struct {
	buf    []byte
	au     []byte
	hasVCL bool
	key    bool
	codec  string
	emit   func(Unit)
}

var startCode = []byte{0, 0, 1}

func (s *splitter) feed(p []byte) {
	s.buf = append(s.buf, p...)
	s.scan(false)
}

// flush behandelt den Rest als vollständig und gibt das Bild aus.
func (s *splitter) flush() {
	s.scan(true)
	s.emitAU()
}

func (s *splitter) scan(final bool) {
	for {
		first := bytes.Index(s.buf, startCode)
		if first < 0 {
			if final {
				s.buf = s.buf[:0]
			}
			return
		}
		next := bytes.Index(s.buf[first+3:], startCode)
		if next < 0 {
			if final && len(s.buf) > first+3 {
				nal := s.buf[first+3:]
				for len(nal) > 1 && nal[len(nal)-1] == 0 {
					nal = nal[:len(nal)-1]
				}
				s.nal(nal)
				s.buf = s.buf[:0]
			} else {
				s.buf = s.buf[first:]
			}
			return
		}
		end := first + 3 + next
		nal := s.buf[first+3 : end]
		// Eine 4-Byte-Startsequenz lässt eine Null am Ende stehen.
		for len(nal) > 0 && nal[len(nal)-1] == 0 {
			nal = nal[:len(nal)-1]
		}
		if len(nal) > 0 {
			s.nal(nal)
		}
		s.buf = s.buf[end:]
	}
}

func (s *splitter) nal(nal []byte) {
	typ := nal[0] & 0x1f
	vcl := typ == 1 || typ == 5
	if s.hasVCL {
		switch {
		case typ == 9 || typ == 7 || typ == 8 || typ == 6:
			s.emitAU()
		case vcl && len(nal) > 1 && nal[1]&0x80 != 0: // first_mb_in_slice == 0
			s.emitAU()
		}
	}
	if typ == 9 {
		return // Trennzeichen brauchen wir nicht
	}
	s.au = append(s.au, 0, 0, 0, 1)
	s.au = append(s.au, nal...)
	if vcl {
		s.hasVCL = true
	}
	if typ == 5 {
		s.key = true
	}
	if typ == 7 && len(nal) >= 4 {
		s.codec = fmt.Sprintf("avc1.%02x%02x%02x", nal[1], nal[2], nal[3])
	}
}

func (s *splitter) emitAU() {
	if !s.hasVCL {
		return
	}
	u := Unit{Data: append([]byte(nil), s.au...), Key: s.key, Codec: s.codec}
	s.au = s.au[:0]
	s.hasVCL, s.key = false, false
	s.emit(u)
}

// tailBuffer behält die letzten Zeilen der Fehlerausgabe.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2000 {
		t.buf = t.buf[len(t.buf)-2000:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}
