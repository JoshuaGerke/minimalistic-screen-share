// Paket e2e verschlüsselt alles im Raum Ende-zu-Ende.
//
// Aus Raum-ID und Passwort entstehen zwei getrennte Schlüssel:
//   - Enc verschlüsselt jedes Paket (AES-256-GCM). Ihn kennen nur die
//     Teilnehmer – nicht der Relay, nicht Cloudflare (Tunnel), nicht ntfy.
//   - Auth weist beim Beitreten nach, dass man das Passwort kennt. Der Relay
//     speichert nur seinen SHA-256-Wert und kann damit nichts entschlüsseln.
//
// Ein Paket ist: Kopf (16 Byte, lesbar) + Nonce (12) + Geheimtext. Der Kopf
// sagt dem Relay nur, was er zum Verteilen braucht (Art, Absender, Nummer,
// Stück i von n). Er ist als Zusatzdaten mitgeschützt – bis auf die
// Absender-Kennung, die der Relay selbst einträgt.
package e2e

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

// Iterations macht Passwort-Raten teuer (OWASP-Empfehlung für PBKDF2-SHA256).
const Iterations = 310000

// HeaderLen, NonceLen: Aufbau eines Pakets.
const (
	HeaderLen = 16
	NonceLen  = 12
	Overhead  = HeaderLen + NonceLen + 16
)

// Arten von Paketen.
const (
	KindVideo  byte = 1 // H.264-Stück; Flag 1 = Keyframe-Beginn
	KindSystem byte = 2 // Systemton (PCM); Flag 1 = Stille
	KindVoice  byte = 3 // Opus; Flag 1 = Stereo
	KindFile   byte = 4 // Datei-Stück: Seq = Datei, Idx/Count
	KindChat   byte = 5 // Chatnachricht (JSON)
	KindState  byte = 6 // Zustand eines Teilnehmers (JSON), wird nachgereicht
	KindEvent  byte = 7 // einmalige Ereignisse (JSON), z. B. Zeige-Klicks
)

// FlagKey markiert den Beginn eines Keyframes (KindVideo).
const FlagKey byte = 1

// Keys sind die aus dem Passwort abgeleiteten Schlüssel.
type Keys struct {
	Enc  []byte
	Auth []byte
}

// NormalizeRoom macht die Raum-ID vergleichbar.
func NormalizeRoom(room string) string { return strings.ToLower(strings.TrimSpace(room)) }

// Derive leitet die Schlüssel ab. Dauert absichtlich ein paar hundert
// Millisekunden.
func Derive(room, password string) (Keys, error) {
	master, err := pbkdf2.Key(sha256.New, password, []byte("mss-room:"+NormalizeRoom(room)), Iterations, 32)
	if err != nil {
		return Keys{}, err
	}
	enc, err := hkdf.Key(sha256.New, master, nil, "mss-enc", 32)
	if err != nil {
		return Keys{}, err
	}
	auth, err := hkdf.Key(sha256.New, master, nil, "mss-auth", 32)
	if err != nil {
		return Keys{}, err
	}
	return Keys{Enc: enc, Auth: auth}, nil
}

// AuthHash ist, was der Relay speichert und vergleicht.
func AuthHash(auth []byte) string {
	sum := sha256.Sum256(auth)
	return hex.EncodeToString(sum[:])
}

// Header ist der lesbare Kopf eines Pakets.
type Header struct {
	Kind   byte
	Flags  byte
	Sender uint32 // trägt der Relay ein
	Seq    uint32
	Idx    uint16
	Count  uint16
}

// Put schreibt den Kopf in b (mindestens HeaderLen Byte).
func (h Header) Put(b []byte) {
	b[0], b[1], b[2], b[3] = h.Kind, h.Flags, 0, 0
	binary.LittleEndian.PutUint32(b[4:], h.Sender)
	binary.LittleEndian.PutUint32(b[8:], h.Seq)
	binary.LittleEndian.PutUint16(b[12:], h.Idx)
	binary.LittleEndian.PutUint16(b[14:], h.Count)
}

// ParseHeader liest den Kopf (ohne zu entschlüsseln) – das darf der Relay.
func ParseHeader(b []byte) (Header, bool) {
	if len(b) < Overhead {
		return Header{}, false
	}
	return Header{
		Kind:   b[0],
		Flags:  b[1],
		Sender: binary.LittleEndian.Uint32(b[4:]),
		Seq:    binary.LittleEndian.Uint32(b[8:]),
		Idx:    binary.LittleEndian.Uint16(b[12:]),
		Count:  binary.LittleEndian.Uint16(b[14:]),
	}, true
}

// SetSender trägt die Absender-Kennung ein (Relay).
func SetSender(b []byte, id uint32) { binary.LittleEndian.PutUint32(b[4:], id) }

// aad ist der Kopf ohne Absender – der Relay darf nur den ändern.
func aad(b []byte) []byte {
	a := make([]byte, HeaderLen)
	copy(a, b[:HeaderLen])
	a[4], a[5], a[6], a[7] = 0, 0, 0, 0
	return a
}

// Box verschlüsselt und entschlüsselt Pakete eines Raums.
type Box struct{ aead cipher.AEAD }

// NewBox bereitet AES-256-GCM mit dem Raumschlüssel vor.
func NewBox(enc []byte) (*Box, error) {
	block, err := aes.NewCipher(enc)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal baut ein Paket: Kopf + zufällige Nonce + Geheimtext.
func (x *Box) Seal(h Header, plain []byte) []byte {
	out := make([]byte, HeaderLen+NonceLen, HeaderLen+NonceLen+len(plain)+16)
	h.Put(out)
	rand.Read(out[HeaderLen : HeaderLen+NonceLen])
	return x.aead.Seal(out, out[HeaderLen:HeaderLen+NonceLen], plain, aad(out))
}

// ErrAuth: Paket kaputt, manipuliert oder mit anderem Passwort.
var ErrAuth = errors.New("Paket lässt sich nicht entschlüsseln")

// Open prüft und entschlüsselt ein Paket.
func (x *Box) Open(msg []byte) (Header, []byte, error) {
	h, ok := ParseHeader(msg)
	if !ok {
		return Header{}, nil, ErrAuth
	}
	plain, err := x.aead.Open(nil, msg[HeaderLen:HeaderLen+NonceLen], msg[HeaderLen+NonceLen:], aad(msg))
	if err != nil {
		return h, nil, ErrAuth
	}
	return h, plain, nil
}
