//go:build windows

package video

import (
	"testing"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
)

func encodeFor(t *testing.T, target capture.Target, d time.Duration) (units, keys int, bytes int, codec string) {
	t.Helper()
	enc, err := Start(Options{Target: target, FPS: 60, BitrateKbps: 6000})
	if err != nil {
		t.Skipf("kein H.264 möglich: %v", err)
	}
	defer enc.Close()
	deadline := time.After(d)
	for {
		select {
		case u, ok := <-enc.Units():
			if !ok {
				t.Fatalf("Encoder beendet: %v", enc.Err())
			}
			if units == 0 && !u.Key {
				t.Fatal("erstes Bild ist kein Keyframe")
			}
			units++
			bytes += len(u.Data)
			if u.Key {
				keys++
				codec = u.Codec
			}
		case <-deadline:
			return
		}
	}
}

func TestEncodeTestPattern(t *testing.T) {
	units, keys, size, codec := encodeFor(t, capture.Target{Kind: "test"}, 3*time.Second)
	t.Logf("Testbild: %d Bilder, %d Keyframes, %d KB, %s, Encoder %s", units, keys, size/1024, codec, probedEnc.name)
	if units < 100 || keys < 1 || codec == "" {
		t.Fatalf("zu wenig: %d Bilder, %d Keyframes, Codec %q", units, keys, codec)
	}
}

func TestEncodeMonitor(t *testing.T) {
	units, keys, size, codec := encodeFor(t, capture.Target{Kind: "monitor", Monitor: 0, Cursor: true}, 3*time.Second)
	t.Logf("Monitor 1: %d Bilder in 3 s, %d Keyframes, %d KB, %s", units, keys, size/1024, codec)
	if units < 30 {
		t.Fatalf("nur %d Bilder in 3 s", units)
	}
}
