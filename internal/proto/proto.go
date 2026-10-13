// Paket proto beschreibt das Drahtformat zwischen Client, Relay und Viewer.
// Steuerbefehle laufen als JSON, Bild und Ton als Binärpakete mit kurzem Kopf.
package proto

import "encoding/binary"

const (
	MsgFrame   byte = 0x01
	MsgAudio   byte = 0x02
	MsgSilence byte = 0x03
)

const (
	FrameHeaderLen = 13 // Typ + Seq + Zeit + Breite + Höhe
	AudioHeaderLen = 9  // Typ + Seq + Zeit
)

// Control ist eine Steuernachricht. Welche Felder gesetzt sind, hängt von T ab.
type Control struct {
	T     string   `json:"t"`
	Room  string   `json:"room,omitempty"`
	Role  string   `json:"role,omitempty"`
	Name  string   `json:"name,omitempty"`
	Ver   string   `json:"ver,omitempty"`
	TS    int64    `json:"ts,omitempty"`
	N     int      `json:"n,omitempty"`
	W     int      `json:"w,omitempty"`
	H     int      `json:"h,omitempty"`
	FPS   int      `json:"fps,omitempty"`
	Rate  int      `json:"rate,omitempty"`
	Ch    int      `json:"ch,omitempty"`
	Audio bool     `json:"audio,omitempty"`
	Title string   `json:"title,omitempty"`
	Msg   string   `json:"msg,omitempty"`
	Fatal bool     `json:"fatal,omitempty"`
	ID    uint64   `json:"id,omitempty"`
	Names []string `json:"names,omitempty"`
	On    bool     `json:"on,omitempty"`
}

// FramePacket baut ein Binärpaket mit einem JPEG-Frame.
func FramePacket(seq uint32, ts uint32, w, h int, jpeg []byte) []byte {
	p := make([]byte, FrameHeaderLen+len(jpeg))
	p[0] = MsgFrame
	binary.LittleEndian.PutUint32(p[1:], seq)
	binary.LittleEndian.PutUint32(p[5:], ts)
	binary.LittleEndian.PutUint16(p[9:], uint16(w))
	binary.LittleEndian.PutUint16(p[11:], uint16(h))
	copy(p[FrameHeaderLen:], jpeg)
	return p
}

// AudioPacket baut ein Binärpaket mit rohen PCM-Samples (S16LE, interleaved).
func AudioPacket(seq uint32, ts uint32, pcm []byte) []byte {
	p := make([]byte, AudioHeaderLen+len(pcm))
	p[0] = MsgAudio
	binary.LittleEndian.PutUint32(p[1:], seq)
	binary.LittleEndian.PutUint32(p[5:], ts)
	copy(p[AudioHeaderLen:], pcm)
	return p
}

// SilencePacket markiert Stille, damit der Viewer die Zeit weiterzählt.
func SilencePacket(seq uint32, ts uint32) []byte {
	p := make([]byte, AudioHeaderLen)
	p[0] = MsgSilence
	binary.LittleEndian.PutUint32(p[1:], seq)
	binary.LittleEndian.PutUint32(p[5:], ts)
	return p
}

// Header liest den Binärkopf. Für Audio- und Silence-Pakete sind w und h 0.
func Header(b []byte) (typ byte, seq, ts uint32, w, h int, ok bool) {
	if len(b) < AudioHeaderLen {
		return 0, 0, 0, 0, 0, false
	}
	typ = b[0]
	seq = binary.LittleEndian.Uint32(b[1:])
	ts = binary.LittleEndian.Uint32(b[5:])
	if typ == MsgFrame {
		if len(b) < FrameHeaderLen {
			return 0, 0, 0, 0, 0, false
		}
		w = int(binary.LittleEndian.Uint16(b[9:]))
		h = int(binary.LittleEndian.Uint16(b[11:]))
	}
	return typ, seq, ts, w, h, true
}
