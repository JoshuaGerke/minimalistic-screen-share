//go:build windows

package audio

import (
	"testing"
	"time"
)

func TestLoopbackSmoke(t *testing.T) {
	src, err := Open()
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
