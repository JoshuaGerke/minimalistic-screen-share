// Paket app verbindet Einstellungen, Stream-Sitzung, Tray und das lokale
// Bedienpanel im Browser.
package app

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/stream"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

type App struct {
	log *log.Logger

	mu       sync.Mutex
	cfg      config.Config
	sess     *stream.Session
	last     stream.Status
	port     int
	onStatus func(stream.Status)
}

// StatusView ist die Antwort von /api/status – Panel und Zweitinstanzen
// erkennen daran, dass hier ein mss-Client läuft.
type StatusView struct {
	App       string        `json:"app"`
	Version   string        `json:"version"`
	Streaming bool          `json:"streaming"`
	Room      string        `json:"room"`
	Server    string        `json:"server"`
	ShareURL  string        `json:"share_url"`
	PanelURL  string        `json:"panel_url"`
	Stream    stream.Status `json:"stream"`
}

func New(log *log.Logger) *App {
	return &App{
		log:  log,
		cfg:  config.Load(),
		last: stream.Status{State: "idle"},
	}
}

func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SaveConfig prüft und speichert neue Einstellungen. Läuft gerade ein Stream,
// wandert die Auto-Freigabe direkt in die laufende Sitzung.
func (a *App) SaveConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	a.mu.Lock()
	old := a.cfg.AutoAccept
	sess := a.sess
	a.cfg = c
	a.mu.Unlock()
	if sess != nil && old != c.AutoAccept {
		sess.SetAutoAccept(c.AutoAccept)
	}
	return nil
}

func (a *App) Status() StatusView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return StatusView{
		App:       "mss",
		Version:   version.Version,
		Streaming: a.sess != nil,
		Room:      a.cfg.Room,
		Server:    a.cfg.Server,
		ShareURL:  a.cfg.Server + "/?room=" + a.cfg.Room,
		PanelURL:  a.panelURL(),
		Stream:    a.last,
	}
}

// SetOnStatus meldet Zustandsänderungen an das Tray.
func (a *App) SetOnStatus(fn func(stream.Status)) {
	a.mu.Lock()
	a.onStatus = fn
	a.mu.Unlock()
}

// Start beginnt zu streamen. Ziel und aktuelle Einstellungen wandern in die
// Konfigdatei, damit der nächste Start gleich passt.
func (a *App) Start(target capture.Target) error {
	a.mu.Lock()
	if a.sess != nil {
		a.mu.Unlock()
		return errors.New("es läuft schon ein Stream")
	}
	cfg := a.cfg
	cfg.Target = target
	if err := cfg.Validate(); err != nil {
		a.mu.Unlock()
		return err
	}
	a.cfg = cfg
	cfg.Save()

	sess := stream.New(stream.Options{
		Server:     cfg.Server,
		Room:       cfg.Room,
		Name:       cfg.Name,
		FPS:        cfg.FPS,
		Quality:    cfg.Quality,
		MaxWidth:   cfg.MaxWidth,
		Audio:      cfg.Audio,
		AutoAccept: cfg.AutoAccept,
		Target:     target,
		Log:        a.log,
	})
	sess.SetOnChange(a.onSessionStatus)
	a.sess = sess
	a.mu.Unlock()

	go func() {
		sess.Run()
		a.mu.Lock()
		if a.sess == sess {
			a.sess = nil
			a.last = stream.Status{State: "idle"}
		}
		cb := a.onStatus
		a.mu.Unlock()
		if cb != nil {
			cb(stream.Status{State: "idle"})
		}
	}()
	return nil
}

func (a *App) Stop() {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
}

// Allow lässt eine wartende Zuschaueranfrage durch.
func (a *App) Allow(id uint64) error {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess == nil {
		return errors.New("kein Stream aktiv")
	}
	return sess.Allow(id)
}

// Deny lehnt eine Zuschaueranfrage ab.
func (a *App) Deny(id uint64) error {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess == nil {
		return errors.New("kein Stream aktiv")
	}
	return sess.Deny(id)
}

func (a *App) Streaming() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess != nil
}

func (a *App) onSessionStatus(st stream.Status) {
	a.mu.Lock()
	a.last = st
	cb := a.onStatus
	a.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// Serve startet das lokale Bedienpanel auf 127.0.0.1.
func (a *App) Serve() error {
	ln, port, err := listenLocal()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.port = port
	a.mu.Unlock()
	srv := &http.Server{Handler: a.Handler()}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("Bedienpanel: %v", err)
		}
	}()
	a.log.Printf("Bedienpanel läuft auf %s", a.PanelURL())
	return nil
}

func listenLocal() (net.Listener, int, error) {
	for port := 7777; port < 7797; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, errors.New("kein freier Port für das Bedienpanel")
}

func (a *App) PanelURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panelURL()
}

func (a *App) panelURL() string {
	if a.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", a.port)
}

// ShareURL ist der Link, den man Freunden schickt.
func (a *App) ShareURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Server + "/?room=" + a.cfg.Room
}

func (a *App) OpenPanel() {
	if url := a.PanelURL(); url != "" {
		openURL(url)
	}
}

func (a *App) OpenViewer() {
	openURL(a.ShareURL())
}

// OpenBrowserURL öffnet eine Adresse im Standardbrowser.
func OpenBrowserURL(url string) bool {
	return openURL(url) == nil
}

// ReportError schiebt eine Fehlermeldung in den Status, damit Tray und
// Panel sie anzeigen (z. B. wenn der Raumcode nicht passt).
func (a *App) ReportError(msg string) {
	a.onSessionStatus(stream.Status{State: "error", Err: msg})
}
