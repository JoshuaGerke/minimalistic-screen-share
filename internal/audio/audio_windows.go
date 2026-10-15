//go:build windows

package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

const (
	waveFormatPCM       = 0x0001
	waveFormatIEEEFloat = 0x0003
	waveFormatExtTag    = 0xFFFE
)

// waveFormatExt ist WAVEFORMATEXTENSIBLE – die Erweiterung hinter
// WAVEFORMATEX, nötig um Float- von PCM-Samples zu unterscheiden.
type waveFormatExt struct {
	wca.WAVEFORMATEX
	Samples     uint16
	ChannelMask uint32
	SubFormat   [16]byte
}

// Open öffnet die Standard-Ausgabe als Loopback: Das ist der Systemton,
// so wie Discord ihn beim Screensharing mitschickt.
func Open() (Source, error) {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// Läuft schon – kein Problem, COM ist pro Thread initialisiert.
	}

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

	// 200 ms Puffer reichen locker, gelesen wird alle 10 ms.
	if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wca.AUDCLNT_STREAMFLAGS_LOOPBACK,
		2000000, 0, wfx, nil); err != nil {
		ac.Release()
		return nil, fmt.Errorf("Loopback init: %w", err)
	}

	var acc *wca.IAudioCaptureClient
	if err := ac.GetService(wca.IID_IAudioCaptureClient, &acc); err != nil {
		ac.Release()
		return nil, fmt.Errorf("CaptureClient: %w", err)
	}

	rate := int(wfx.NSamplesPerSec)
	channels := int(wfx.NChannels)
	bits := int(wfx.WBitsPerSample)
	isFloat := false
	switch wfx.WFormatTag {
	case waveFormatIEEEFloat:
		isFloat = true
	case waveFormatExtTag:
		ext := (*waveFormatExt)(unsafe.Pointer(wfx))
		sub := binary.LittleEndian.Uint32(ext.SubFormat[:4])
		isFloat = sub == 3
	case waveFormatPCM:
		// bleibt int
	}
	bytesPer := bits / 8
	if bytesPer != 2 && bytesPer != 4 {
		ac.Release()
		return nil, fmt.Errorf("Sample-Format mit %d Bit wird nicht unterstützt", bits)
	}

	if err := ac.Start(); err != nil {
		ac.Release()
		return nil, fmt.Errorf("Loopback start: %w", err)
	}

	s := &wasapiSource{
		ac:       ac,
		acc:      acc,
		rate:     rate,
		srcCh:    channels,
		srcBytes: bytesPer,
		isFloat:  isFloat,
		chunks:   make(chan []byte, 50),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.poll()
	return s, nil
}

type wasapiSource struct {
	ac       *wca.IAudioClient
	acc      *wca.IAudioCaptureClient
	rate     int
	srcCh    int
	srcBytes int
	isFloat  bool

	pending []byte
	chunks  chan []byte
	stop    chan struct{}
	done    chan struct{}

	current []byte
}

func (s *wasapiSource) Rate() int     { return s.rate }
func (s *wasapiSource) Channels() int { return 2 }

// Read wird von genau einer Goroutine aufgerufen (der Sende-Schleife).
func (s *wasapiSource) Read(p []byte) (int, error) {
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

func (s *wasapiSource) Close() error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	s.ac.Stop()
	s.acc.Release()
	s.ac.Release()
	return nil
}

// poll holt alle 10 ms neue Pakete aus dem Loopback-Puffer.
//
// Spielt nichts, liefert WASAPI-Loopback gar keine Pakete. Dann füllen wir
// nach der Uhr mit Stille auf: Der Ton bleibt ein durchgehender Strom, und
// der Viewer erkennt echte Aussetzer im Netz sicher als solche.
func (s *wasapiSource) poll() {
	defer close(s.done)
	defer close(s.chunks)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	lastFed := time.Now()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		got := false
		for {
			var packetSize uint32
			if err := s.acc.GetNextPacketSize(&packetSize); err != nil {
				return
			}
			if packetSize == 0 {
				break
			}
			var data *byte
			var frames, flags uint32
			var devPos, qpcPos uint64
			if err := s.acc.GetBuffer(&data, &frames, &flags, &devPos, &qpcPos); err != nil {
				return
			}
			if frames > 0 {
				s.emit(data, frames, flags)
				got = true
			}
			if err := s.acc.ReleaseBuffer(frames); err != nil {
				return
			}
		}
		now := time.Now()
		if got {
			lastFed = now
		} else if gap := now.Sub(lastFed); gap >= 40*time.Millisecond {
			s.emit(nil, uint32(gap.Seconds()*float64(s.rate)), wca.AUDCLNT_BUFFERFLAGS_SILENT)
			lastFed = now
		}
	}
}

// emit hängt die Paketdaten als Stereo-S16 an den Ausgabepuffer und schickt
// fertige 20-ms-Häppchen weiter.
func (s *wasapiSource) emit(data *byte, frames, flags uint32) {
	frameCount := int(frames)
	need := frameCount * 4
	base := len(s.pending)
	s.pending = append(s.pending, make([]byte, need)...)
	dst := s.pending[base:]

	if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil {
		raw := unsafe.Slice(data, frameCount*s.srcCh*s.srcBytes)
		convertS16(dst, raw, frameCount, s.srcCh, s.srcBytes, s.isFloat)
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
