package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/rendezvous"
)

// autoConnect setzt beim Start um, was unter "Beim Start" eingestellt ist:
// eigenen Bildschirm teilen, der Konferenz eines anderen beitreten, dort
// teilen und in den Sprachkanal gehen – alles ohne Klick.
func (a *App) autoConnect() {
	cfg := a.Config()
	if !cfg.AutoShare && cfg.AutoJoin == "" && !cfg.AutoVoice {
		return
	}
	// Kurz warten, bis Relay und Tunnel stehen.
	for i := 0; i < 90; i++ {
		if _, ready, _, _ := a.linkInfo(); ready {
			break
		}
		time.Sleep(time.Second)
	}
	if cfg.AutoShare && !a.Streaming() {
		if err := a.Start(cfg.Target); err != nil {
			a.log.Printf("Automatisch teilen: %v", err)
		}
	}
	if cfg.AutoJoin != "" {
		// Der andere startet vielleicht später – so lange weiter suchen.
		for {
			if err := a.JoinConference(cfg.AutoJoin, cfg.AutoPresent, cfg.AutoVoice, cfg.AutoMuted); err == nil {
				return
			} else {
				a.log.Printf("Konferenz beitreten: %v – neuer Versuch in 30 s", err)
			}
			time.Sleep(30 * time.Second)
		}
	}
	if cfg.AutoVoice && cfg.AutoShare {
		time.Sleep(3 * time.Second)
		a.OpenOwnConference(true, cfg.AutoMuted)
	}
}

// linkInfo liefert den eigenen Link und ob er schon benutzbar ist.
func (a *App) linkInfo() (string, bool, string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.linkLocked()
}

// OpenOwnConference öffnet die eigene Konferenz (alle anderen Bildschirme,
// Sprachkanal) im Konferenz-Fenster – mit dem Schlüssel, ohne Anklopfen.
func (a *App) OpenOwnConference(voice, muted bool) error {
	st := a.Status()
	if !st.Streaming || !st.LinkReady || st.VoiceKey == "" {
		return errors.New("erst Teilen starten")
	}
	q := url.Values{"key": {st.VoiceKey}, "name": {a.Config().Name}}
	if voice {
		q.Set("voice", "1")
	}
	if muted {
		q.Set("muted", "1")
	}
	a.OpenConference(st.ShareURL + "&" + q.Encode())
	return nil
}

// JoinConference öffnet die Konferenz eines anderen. target ist eine
// Sitzungs-ID oder ein Link; eine ID wird über ntfy.sh aufgelöst.
func (a *App) JoinConference(target string, present, voice, muted bool) error {
	base, room, err := resolveTarget2(target)
	if err != nil {
		return err
	}
	q := url.Values{"room": {room}, "name": {a.Config().Name}, "auto": {"1"}}
	if voice {
		q.Set("voice", "1")
	}
	if muted {
		q.Set("muted", "1")
	}
	if present {
		// Einmal-Code: Damit darf genau dieses Fenster ohne Rückfrage teilen.
		b := make([]byte, 12)
		rand.Read(b)
		nonce := hex.EncodeToString(b)
		a.mu.Lock()
		a.presentNonce = nonce
		a.mu.Unlock()
		q.Set("present", nonce)
	}
	a.OpenConference(strings.TrimRight(base, "/") + "/?" + q.Encode())
	return nil
}

// resolveTarget2 macht aus ID oder Link die aktuelle Adresse samt Raum.
func resolveTarget2(target string) (base, room string, err error) {
	t := strings.TrimSpace(target)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		u, err := url.Parse(t)
		if err != nil {
			return "", "", err
		}
		room = strings.ToLower(u.Query().Get("room"))
		if !presentRoom.MatchString(room) {
			return "", "", errors.New("im Link fehlt die Sitzungs-ID")
		}
		// Steht unter der ID inzwischen eine neuere Adresse, die nehmen.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if fresh, err := rendezvous.Resolve(ctx, room); err == nil {
			return fresh, room, nil
		}
		return u.Scheme + "://" + u.Host, room, nil
	}
	room = strings.ToLower(t)
	if !presentRoom.MatchString(room) {
		return "", "", errors.New("das ist keine Sitzungs-ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, err = rendezvous.Resolve(ctx, room)
	return base, room, err
}
