package diag

import (
	"strings"
	"testing"
	"time"
)

func TestSample(t *testing.T) {
	var s Sampler
	s.Sample()
	time.Sleep(300 * time.Millisecond)
	line := s.Sample()
	t.Log(line)
	if !strings.Contains(line, "cpu(") {
		t.Fatalf("keine CPU-Werte: %q", line)
	}
}
