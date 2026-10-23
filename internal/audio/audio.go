// Paket audio liefert den Systemton als PCM-Stream (S16LE, interleaved, stereo).
package audio

import (
	"errors"
	"io"
)

// ErrUnavailable heißt: Auf diesem System gibt es gerade keine Loopback-Quelle.
var ErrUnavailable = errors.New("keine Systemtonquelle verfügbar")

// Source ist ein PCM-Stream fester Rate. Read blockiert, bis Daten da sind.
type Source interface {
	io.Reader
	Rate() int
	Channels() int
	Close() error
}

// SelfTree als Ausschluss nimmt den Prozessbaum der App selbst aus der
// Aufnahme – dazu gehört das Konferenz-Fenster mit Sprachkanal und den
// Bildschirmen der anderen. Sonst hörten die anderen sich über den eigenen
// Stream selbst wieder.
const SelfTree = "<self>"
