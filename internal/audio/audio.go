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
