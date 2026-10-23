package e2e

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"
)

func TestSealOpen(t *testing.T) {
	k, err := Derive("Test-Raum", "geheim")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k.Enc, k.Auth) {
		t.Fatal("Verschlüsselungs- und Anmeldeschlüssel sind gleich")
	}
	box, _ := NewBox(k.Enc)
	msg := box.Seal(Header{Kind: KindChat, Seq: 7}, []byte("hallo"))

	// Der Relay trägt den Absender ein – das darf die Prüfung nicht brechen.
	SetSender(msg, 42)
	h, plain, err := box.Open(msg)
	if err != nil || string(plain) != "hallo" || h.Sender != 42 || h.Seq != 7 {
		t.Fatalf("Open: %v %q %+v", err, plain, h)
	}

	// Kopf manipuliert (andere Art): muss auffallen.
	bad := append([]byte(nil), msg...)
	bad[0] = KindFile
	if _, _, err := box.Open(bad); err == nil {
		t.Fatal("manipulierter Kopf nicht erkannt")
	}
	// Falsches Passwort: nicht lesbar.
	k2, _ := Derive("Test-Raum", "falsch")
	box2, _ := NewBox(k2.Enc)
	if _, _, err := box2.Open(msg); err == nil {
		t.Fatal("mit falschem Passwort lesbar")
	}
	// Gleiches Passwort, andere Raum-ID: anderer Schlüssel.
	k3, _ := Derive("anderer-raum", "geheim")
	if bytes.Equal(k3.Enc, k.Enc) {
		t.Fatal("Raum-ID fließt nicht in den Schlüssel ein")
	}
}

// TestVector schreibt ein Paket für die Gegenprobe im Browser
// (node internal/web/e2e_check.cjs) – mit MSS_E2E_VECTOR=<datei>.
func TestVector(t *testing.T) {
	path := os.Getenv("MSS_E2E_VECTOR")
	if path == "" {
		t.Skip("nur mit MSS_E2E_VECTOR")
	}
	k, _ := Derive("vektor-raum", "passwort123")
	box, _ := NewBox(k.Enc)
	msg := box.Seal(Header{Kind: KindChat, Seq: 1, Idx: 2, Count: 3}, []byte("Grüße aus Go"))
	SetSender(msg, 9)
	out := "enc=" + hex.EncodeToString(k.Enc) + "\nauth=" + hex.EncodeToString(k.Auth) + "\nmsg=" + hex.EncodeToString(msg) + "\n"
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}
