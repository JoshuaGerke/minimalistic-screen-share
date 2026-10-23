package app

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/stream"
)

// Teilen in die Konferenz eines anderen: Der Viewer im Browser fragt die
// App auf diesem PC, ob sie den Bildschirm in diesen Raum schicken soll.
// Gestreamt wird erst nach einem Klick im eigenen Fenster der App – so kann
// keine fremde Webseite heimlich den Bildschirm verschicken.

// PresentRequest ist eine offene Bitte, in einen Raum zu teilen.
type PresentRequest struct {
	Server string    `json:"server"`
	Room   string    `json:"room"`
	Host   string    `json:"host"`
	Token  string    `json:"-"`
	Nonce  string    `json:"-"`
	At     time.Time `json:"at"`
}

// PresentView zeigt, ob und wohin gerade geteilt wird.
type PresentView struct {
	Host    string `json:"host"`
	Room    string `json:"room"`
	State   string `json:"state"`
	Viewers int    `json:"viewers"`
	Paused  bool   `json:"paused"`
}

var presentRoom = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// RequestPresent nimmt die Bitte entgegen und öffnet das Fenster zum Bestätigen.
func (a *App) RequestPresent(req PresentRequest) error {
	u, err := url.Parse(req.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("ungültige Server-Adresse")
	}
	req.Room = strings.ToLower(strings.TrimSpace(req.Room))
	if !presentRoom.MatchString(req.Room) || len(req.Token) < 8 {
		return errors.New("ungültiger Raum")
	}
	if r := []rune(strings.TrimSpace(req.Host)); len(r) > 32 {
		req.Host = string(r[:32])
	}
	req.At = time.Now()
	a.mu.Lock()
	// Der Einmal-Code bleibt gültig, solange die App läuft – lädt das
	// Konferenz-Fenster neu, startet das Teilen ohne Rückfrage wieder.
	auto := req.Nonce != "" && a.presentNonce != "" && req.Nonce == a.presentNonce
	if !auto {
		a.presentReq = &req
	}
	a.mu.Unlock()
	if auto {
		// Eingestellt unter "Beim Start": ohne Rückfrage teilen.
		return a.StartPresent(req, a.Config().Target)
	}
	a.notify("Bildschirm teilen?", "In der Konferenz von "+req.Host+" – im Fenster bestätigen.")
	a.OpenPanel()
	return nil
}

// AcceptPresent startet das Teilen in den angefragten Raum.
func (a *App) AcceptPresent(target capture.Target) error {
	a.mu.Lock()
	req := a.presentReq
	a.presentReq = nil
	a.mu.Unlock()
	if req == nil || time.Since(req.At) > 10*time.Minute {
		return errors.New("keine offene Anfrage")
	}
	return a.StartPresent(*req, target)
}

// DeclinePresent verwirft die Anfrage.
func (a *App) DeclinePresent() {
	a.mu.Lock()
	a.presentReq = nil
	a.mu.Unlock()
}

// StartPresent teilt den Bildschirm in die Konferenz eines anderen.
func (a *App) StartPresent(req PresentRequest, target capture.Target) error {
	target = resolveTarget(target)
	a.StopPresent()
	cfg := a.Config()
	sess := stream.New(stream.Options{
		Server:       req.Server,
		Room:         req.Room,
		Name:         cfg.Name,
		Role:         "presenter",
		Token:        req.Token,
		FPS:          cfg.FPS,
		Quality:      cfg.Quality,
		MaxWidth:     cfg.MaxWidth,
		BitrateKbps:  cfg.Bitrate,
		VideoMode:    cfg.VideoMode,
		Audio:        cfg.Audio,
		Target:       target,
		Log:          a.log,
		AudioExclude: a.audioExclude,
	})
	sess.SetPointing(func(x, y float64, name string) { a.pointAt(sess.Target(), x, y, name) }, func() bool { return a.Config().Pointing })
	a.mu.Lock()
	a.present = sess
	a.presentInfo = req
	a.mu.Unlock()
	a.log.Printf("Teile in die Konferenz von %s (Raum %q)", req.Host, req.Room)
	go func() {
		sess.Run()
		a.mu.Lock()
		if a.present == sess {
			a.present = nil
		}
		a.mu.Unlock()
	}()
	return nil
}

// StopPresent beendet das Teilen in eine fremde Konferenz.
func (a *App) StopPresent() {
	a.mu.Lock()
	sess := a.present
	a.present = nil
	a.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
}

// presentViewLocked beschreibt das laufende Teilen für das Panel.
func (a *App) presentViewLocked() *PresentView {
	if a.present == nil {
		return nil
	}
	st := a.present.Status()
	return &PresentView{Host: a.presentInfo.Host, Room: a.presentInfo.Room, State: st.State, Viewers: st.Viewers, Paused: st.Paused}
}

// pointAt zeigt einen Zeige-Klick an der passenden Stelle des Bildschirms.
func (a *App) pointAt(target capture.Target, x, y float64, name string) {
	if !a.Config().Pointing {
		return
	}
	r, err := capture.Bounds(target)
	if err != nil || r.W <= 0 || r.H <= 0 {
		a.log.Printf("Zeige-Klick: %v", err)
		return
	}
	px, py := r.X+int(x*float64(r.W)), r.Y+int(y*float64(r.H))
	a.log.Printf("Zeige-Klick von %s bei %d,%d", name, px, py)
	showPointer(px, py, name, a.log)
}
