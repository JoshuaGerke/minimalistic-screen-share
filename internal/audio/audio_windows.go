//go:build windows

package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

const (
	waveFormatPCM       = 0x0001
	waveFormatIEEEFloat = 0x0003
	waveFormatExtTag    = 0xFFFE

	streamFlagsEventCallback  = 0x00040000
	streamFlagsAutoConvertPCM = 0x80000000
	streamFlagsSRCDefaultQual = 0x08000000
)

// Open nimmt den Systemton auf – so wie Discord ihn beim Screensharing
// mitschickt. exclude liefert Programme (exe-Namen), die nicht im Stream
// landen sollen, für dich aber hörbar bleiben. Die Liste wird alle zwei
// Sekunden neu gelesen, Änderungen greifen also live.
func Open(exclude func() []string) (Source, error) {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// Läuft schon – kein Problem, COM ist pro Thread initialisiert.
	}
	c, err := openEndpointLoopback()
	if err != nil {
		return nil, err
	}
	s := &switchSource{
		rate:    c.rate,
		exclude: exclude,
		chunks:  make(chan []byte, 50),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	// Ausschluss gleich zu Beginn anwenden, damit nichts kurz durchrutscht.
	s.retarget(&c)
	go s.poll(c)
	return s, nil
}

// capture ist ein laufender WASAPI-Aufnahme-Client.
type capture struct {
	ac      *wca.IAudioClient
	acc     *wca.IAudioCaptureClient
	event   windows.Handle
	rate    int
	ch      int
	bytes   int
	isFloat bool
	target  uint32 // ausgeschlossener Prozess, 0 = ganzer Systemton
}

func (c *capture) close() {
	c.ac.Stop()
	c.acc.Release()
	c.ac.Release()
	if c.event != 0 {
		windows.CloseHandle(c.event)
	}
}

// openEndpointLoopback nimmt die Standard-Ausgabe komplett mit.
func openEndpointLoopback() (*capture, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, fmt.Errorf("WASAPI-Enumerator: %w", err)
	}
	defer enumerator.Release()

	var device *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
		return nil, ErrUnavailable
	}
	defer device.Release()

	var ac *wca.IAudioClient
	if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
		return nil, fmt.Errorf("AudioClient: %w", err)
	}

	var wfx *wca.WAVEFORMATEX
	if err := ac.GetMixFormat(&wfx); err != nil {
		ac.Release()
		return nil, fmt.Errorf("Mix-Format: %w", err)
	}
	defer ole.CoTaskMemFree(uintptr(unsafe.Pointer(wfx)))

	c := &capture{
		ac:      ac,
		rate:    int(wfx.NSamplesPerSec),
		ch:      int(wfx.NChannels),
		bytes:   int(wfx.WBitsPerSample) / 8,
		isFloat: isFloatFormat(wfx),
	}
	if c.bytes != 2 && c.bytes != 4 {
		ac.Release()
		return nil, fmt.Errorf("Sample-Format mit %d Bit wird nicht unterstützt", wfx.WBitsPerSample)
	}
	if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wca.AUDCLNT_STREAMFLAGS_LOOPBACK,
		2000000, 0, wfx, nil); err != nil {
		ac.Release()
		return nil, fmt.Errorf("Loopback init: %w", err)
	}
	return c, c.start()
}

// openProcessLoopback nimmt alles auf außer dem Prozessbaum von pid
// (Windows 10 2004 und neuer). Format wie beim Systemton, damit der Wechsel
// zwischen beiden Arten für die Zuschauer nahtlos ist.
func openProcessLoopback(pid uint32, rate int) (*capture, error) {
	ac, err := activateProcessLoopback(pid)
	if err != nil {
		return nil, err
	}
	wfx := wca.WAVEFORMATEX{
		WFormatTag:      waveFormatPCM,
		NChannels:       2,
		NSamplesPerSec:  uint32(rate),
		NAvgBytesPerSec: uint32(rate * 4),
		NBlockAlign:     4,
		WBitsPerSample:  16,
	}
	flags := uint32(wca.AUDCLNT_STREAMFLAGS_LOOPBACK | streamFlagsEventCallback | streamFlagsAutoConvertPCM | streamFlagsSRCDefaultQual)
	if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, 2000000, 0, &wfx, nil); err != nil {
		ac.Release()
		return nil, fmt.Errorf("Prozess-Loopback init: %w", err)
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		ac.Release()
		return nil, err
	}
	c := &capture{ac: ac, event: ev, rate: rate, ch: 2, bytes: 2, target: pid}
	if err := ac.SetEventHandle(uintptr(ev)); err != nil {
		ac.Release()
		windows.CloseHandle(ev)
		return nil, err
	}
	return c, c.start()
}

func (c *capture) start() error {
	if err := c.ac.GetService(wca.IID_IAudioCaptureClient, &c.acc); err != nil {
		c.ac.Release()
		return fmt.Errorf("CaptureClient: %w", err)
	}
	if err := c.ac.Start(); err != nil {
		c.acc.Release()
		c.ac.Release()
		return fmt.Errorf("Loopback start: %w", err)
	}
	return nil
}

// isFloatFormat erkennt 32-Bit-Float-Samples. Achtung: Go polstert
// WAVEFORMATEX auf 20 Bytes, Windows packt es auf 18 – das SubFormat von
// WAVEFORMATEXTENSIBLE liegt deshalb bei Byte 24 des Rohspeichers, nicht dort,
// wo ein eingebettetes Go-Struct es vermuten würde. Genau das hatte vorher
// Float-Ton als Ganzzahlen gelesen ("Funkgerät-Klang").
func isFloatFormat(wfx *wca.WAVEFORMATEX) bool {
	switch wfx.WFormatTag {
	case waveFormatIEEEFloat:
		return true
	case waveFormatExtTag:
		raw := unsafe.Slice((*byte)(unsafe.Pointer(wfx)), 40)
		return binary.LittleEndian.Uint32(raw[24:28]) == waveFormatIEEEFloat
	}
	return false
}

// switchSource liefert 20-ms-Stereo-S16-Häppchen und tauscht im Hintergrund
// den Aufnahme-Client, wenn sich der Ausschluss ändert.
type switchSource struct {
	rate    int
	exclude func() []string

	pending []byte
	lastFed time.Time
	chunks  chan []byte
	stop    chan struct{}
	done    chan struct{}
	current []byte
}

func (s *switchSource) Rate() int     { return s.rate }
func (s *switchSource) Channels() int { return 2 }

// Read wird von genau einer Goroutine aufgerufen (der Sende-Schleife).
func (s *switchSource) Read(p []byte) (int, error) {
	for len(s.current) == 0 {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				return 0, io.EOF
			}
			s.current = chunk
		case <-s.stop:
			return 0, io.EOF
		}
	}
	n := copy(p, s.current)
	s.current = s.current[n:]
	return n, nil
}

func (s *switchSource) Close() error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	return nil
}

// retarget sorgt dafür, dass genau das gewünschte Programm ausgeschlossen
// ist. Läuft es (noch) nicht, wird der ganze Systemton aufgenommen.
func (s *switchSource) retarget(cp **capture) {
	var names []string
	if s.exclude != nil {
		names = s.exclude()
	}
	want := uint32(0)
	name := ""
	for _, n := range names {
		if pid := rootProcess(n); pid != 0 {
			want, name = pid, n
			break
		}
	}
	cur := *cp
	if want == cur.target {
		return
	}
	var next *capture
	var err error
	if want == 0 {
		next, err = openEndpointLoopback()
		if err == nil && next.rate != s.rate {
			next.close()
			err = fmt.Errorf("Ausgabegerät hat jetzt %d Hz statt %d Hz", next.rate, s.rate)
		}
	} else {
		next, err = openProcessLoopback(want, s.rate)
	}
	if err != nil {
		if want != 0 {
			setExclusionNote(fmt.Sprintf("%s lässt sich nicht ausnehmen (braucht Windows 10 2004 oder neuer): %v", name, err))
		}
		return
	}
	cur.close()
	*cp = next
	if want != 0 {
		setExclusionNote("")
	}
}

// poll holt alle 10 ms neue Pakete und prüft alle zwei Sekunden den Ausschluss.
//
// Spielt nichts, liefert Loopback gar keine Pakete. Dann füllen wir nach der
// Uhr mit Stille auf: Der Ton bleibt ein durchgehender Strom, und der Viewer
// erkennt echte Aussetzer im Netz sicher als solche.
func (s *switchSource) poll(c *capture) {
	defer close(s.done)
	defer close(s.chunks)
	defer func() { c.close() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	check := time.NewTicker(2 * time.Second)
	defer check.Stop()
	s.lastFed = time.Now()
	for {
		select {
		case <-s.stop:
			return
		case <-check.C:
			s.retarget(&c)
			continue
		case <-ticker.C:
		}
		got, err := s.drain(c)
		if err != nil {
			return
		}
		now := time.Now()
		if got {
			s.lastFed = now
		} else if gap := now.Sub(s.lastFed); gap >= 40*time.Millisecond {
			s.emit(c, nil, uint32(gap.Seconds()*float64(s.rate)), wca.AUDCLNT_BUFFERFLAGS_SILENT)
			s.lastFed = now
		}
	}
}

func (s *switchSource) drain(c *capture) (bool, error) {
	got := false
	for {
		var packetSize uint32
		if err := c.acc.GetNextPacketSize(&packetSize); err != nil {
			return got, err
		}
		if packetSize == 0 {
			return got, nil
		}
		var data *byte
		var frames, flags uint32
		var devPos, qpcPos uint64
		if err := c.acc.GetBuffer(&data, &frames, &flags, &devPos, &qpcPos); err != nil {
			return got, err
		}
		if frames > 0 {
			s.emit(c, data, frames, flags)
			got = true
		}
		if err := c.acc.ReleaseBuffer(frames); err != nil {
			return got, err
		}
	}
}

// emit hängt die Paketdaten als Stereo-S16 an den Ausgabepuffer und schickt
// fertige 20-ms-Häppchen weiter.
func (s *switchSource) emit(c *capture, data *byte, frames, flags uint32) {
	frameCount := int(frames)
	base := len(s.pending)
	s.pending = append(s.pending, make([]byte, frameCount*4)...)
	dst := s.pending[base:]

	if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil {
		raw := unsafe.Slice(data, frameCount*c.ch*c.bytes)
		convertS16(dst, raw, frameCount, c.ch, c.bytes, c.isFloat)
	}

	chunkSize := s.rate / 50 * 4 // 20 ms Stereo-S16
	for len(s.pending) >= chunkSize {
		chunk := make([]byte, chunkSize)
		copy(chunk, s.pending)
		s.pending = s.pending[chunkSize:]
		select {
		case s.chunks <- chunk:
		default:
			// Sendeschleife hängt – lieber ein Knacken als ein Stau.
		}
	}
}

// convertS16 rechnet interleaved Samples nach Stereo-S16 um.
func convertS16(dst, raw []byte, frames, channels, bytesPer int, isFloat bool) {
	for f := 0; f < frames; f++ {
		for c := 0; c < 2; c++ {
			sc := c
			if sc >= channels {
				sc = channels - 1 // Mono auf beide Seiten
			}
			off := (f*channels + sc) * bytesPer
			var v int16
			switch {
			case isFloat:
				fv := math.Float32frombits(binary.LittleEndian.Uint32(raw[off:]))
				if fv > 1 {
					fv = 1
				} else if fv < -1 {
					fv = -1
				}
				v = int16(fv * 32767)
			case bytesPer == 4:
				iv := int32(binary.LittleEndian.Uint32(raw[off:]))
				v = int16(iv >> 16)
			default:
				v = int16(binary.LittleEndian.Uint16(raw[off:]))
			}
			i := (f*2 + c) * 2
			dst[i] = byte(v)
			dst[i+1] = byte(v >> 8)
		}
	}
}

// rootProcess findet den obersten Prozess mit diesem exe-Namen – bei
// Programmen wie Discord spielt ein Kindprozess den Ton, ausgeschlossen
// wird der ganze Baum.
func rootProcess(exe string) uint32 {
	if exe == "" {
		return 0
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	type proc struct {
		parent uint32
		name   string
	}
	procs := map[uint32]proc{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		procs[e.ProcessID] = proc{e.ParentProcessID, lower(windows.UTF16ToString(e.ExeFile[:]))}
	}
	want := lower(exe)
	best := uint32(0)
	for pid, p := range procs {
		if p.name != want || pid == uint32(os.Getpid()) {
			continue
		}
		if parent, ok := procs[p.parent]; ok && parent.name == want {
			continue
		}
		if best == 0 || pid < best {
			best = pid
		}
	}
	return best
}
