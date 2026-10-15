//go:build windows

package audio

import (
	"encoding/binary"
	"io"
	"testing"
	"time"
	"unsafe"

	"github.com/moutend/go-wca/pkg/wca"
)

func TestLoopbackSmoke(t *testing.T) {
	src, err := Open(nil)
	if err != nil {
		t.Skipf("kein Loopback verfügbar: %v", err)
	}
	defer src.Close()
	t.Logf("Format: %d Hz, %d Kanäle", src.Rate(), src.Channels())

	buf := make([]byte, 3840)
	deadline := time.Now().Add(2 * time.Second)
	chunks := 0
	for time.Now().Before(deadline) {
		n, err := src.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if n > 0 {
			chunks++
		}
	}
	if chunks == 0 {
		t.Fatal("keine Audiodaten in 2 Sekunden")
	}
	t.Logf("%d Chunks in 2 s", chunks)
}

// TestFloatFormatDetected: Windows liefert fast immer WAVEFORMATEXTENSIBLE mit
// Float-Samples. Vorher wurde das SubFormat an der falschen Stelle gelesen,
// Float als Ganzzahl gewandelt – beim Zuschauer klang das wie ein Funkgerät.
func TestFloatFormatDetected(t *testing.T) {
	var raw [10]uint32 // 40 Bytes, ausgerichtet
	b := unsafe.Slice((*byte)(unsafe.Pointer(&raw[0])), 40)
	binary.LittleEndian.PutUint16(b[0:], waveFormatExtTag)
	binary.LittleEndian.PutUint32(b[24:], waveFormatIEEEFloat)
	if !isFloatFormat((*wca.WAVEFORMATEX)(unsafe.Pointer(&raw[0]))) {
		t.Fatal("Float-Format nicht erkannt")
	}
}

// TestProcessLoopbackActivates prüft, dass sich ein Programm aus der
// Aufnahme nehmen lässt (nur Aufnahme, es wird nichts abgespielt).
func TestProcessLoopbackActivates(t *testing.T) {
	pid := rootProcess("explorer.exe")
	if pid == 0 {
		t.Skip("explorer.exe läuft nicht")
	}
	c, err := openProcessLoopback(pid, 48000)
	if err != nil {
		t.Fatalf("Prozess-Loopback: %v", err)
	}
	c.close()

	src, err := Open(func() []string { return []string{"explorer.exe"} })
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	buf := make([]byte, src.Rate()/50*4)
	if _, err := io.ReadFull(src, buf); err != nil {
		t.Fatalf("lesen: %v", err)
	}
	if note := ExclusionNote(); note != "" {
		t.Fatalf("Ausschluss klappt nicht: %s", note)
	}
}
