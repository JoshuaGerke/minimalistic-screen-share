package video

import (
	"bytes"
	"testing"
)

// TestSplitter: Ein Strom aus SPS, PPS, IDR und zwei P-Bildern ergibt drei
// Bilder, das erste ist ein Keyframe samt Codec-Name.
func TestSplitter(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x2a, 0xac}
	pps := []byte{0, 0, 0, 1, 0x68, 0xee}
	idr := []byte{0, 0, 1, 0x65, 0x88, 1, 2}
	p1 := []byte{0, 0, 0, 1, 0x41, 0x9a, 3}
	p2 := []byte{0, 0, 0, 1, 0x41, 0x9a, 4, 0}
	stream := bytes.Join([][]byte{sps, pps, idr, p1, p2}, nil)

	var got []Unit
	sp := &splitter{emit: func(u Unit) { got = append(got, u) }}
	// In ungünstigen Häppchen füttern, wie es aus der Pipe kommt.
	for i := 0; i < len(stream); i += 3 {
		sp.feed(stream[i:min(i+3, len(stream))])
	}
	sp.flush()

	if len(got) != 3 {
		t.Fatalf("%d Bilder statt 3", len(got))
	}
	if !got[0].Key || got[0].Codec != "avc1.64002a" {
		t.Fatalf("erstes Bild falsch: key=%v codec=%q", got[0].Key, got[0].Codec)
	}
	if got[1].Key || !bytes.Equal(got[1].Data, []byte{0, 0, 0, 1, 0x41, 0x9a, 3}) {
		t.Fatalf("zweites Bild falsch: % x", got[1].Data)
	}
	if !bytes.Equal(got[2].Data, []byte{0, 0, 0, 1, 0x41, 0x9a, 4}) {
		t.Fatalf("drittes Bild falsch: % x", got[2].Data)
	}
}
