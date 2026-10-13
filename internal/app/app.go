// Paket app verbindet Einstellungen, Stream-Sitzung, Tray und das lokale
// Bedienpanel im Browser.
package app

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/server"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/stream"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/tunnel"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/web"
)

type App struct {
	log *log.Logger

	mu       sync.Mutex
	cfg      config.Config
	sess     *stream.Session
	last     stream.Status
	port     int
	onStatus func(stream.Status)

	relaySrv   *http.Server
	relayPort  int
	tunnel     *tunnel.Tunnel
	tunnelURL  string
	tunnelErr  string
	tunnelBusy bool
}

// StatusView ist die Antwort von /api/status – Panel und Zweitinstanzen
// erkennen daran, dass hier ein mss-Client läuft.
type StatusView struct {
	App           string        `json:"app"`
	Version       string        `json:"version"`
	Streaming     bool          `json:"streaming"`
	Room          string        `json:"room"`
	Server        string        `json:"server"`
	ShareURL      string        `json:"share_url"`
	PanelURL      string        `json:"panel_url"`
	RelayRunning  bool          `json:"relay_running"`
	RelayURL      string        `json:"relay_url,omitempty"`
	TunnelRunning bool          `json:"tunnel_running"`
	TunnelBusy    bool          `json:"tunnel_busy"`
	TunnelURL     string        `json:"tunnel_url,omitempty"`
	TunnelError   string        `json:"tunnel_error,omitempty"`
	Stream        stream.Status `json:"stream"`
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
	oldAccept := a.cfg.AutoAccept
	oldRelay := a.cfg.RunRelay
	sess := a.sess
	a.cfg = c
	a.mu.Unlock()
	if sess != nil && oldAccept != c.AutoAccept {
		sess.SetAutoAccept(c.AutoAccept)
	}
	if oldRelay != c.RunRelay {
		if c.RunRelay {
			if err := a.StartRelay(); err != nil {
				a.log.Printf("Eingebauter Relay: %v", err)
			}
		} else {
			a.StopRelay()
		}
	}
	return nil
}

func (a *App) Status() StatusView {
	a.mu.Lock()
	defer a.mu.Unlock()
	view := StatusView{
		App:           "mss",
		Version:       version.Version,
		Streaming:     a.sess != nil,
		Room:          a.cfg.Room,
		Server:        a.cfg.Server,
		ShareURL:      a.shareURLLocked(),
		PanelURL:      a.panelURL(),
		RelayRunning:  a.relaySrv != nil,
		TunnelRunning: a.tunnel != nil,
		TunnelBusy:    a.tunnelBusy,
		TunnelURL:     a.tunnelURL,
		TunnelError:   a.tunnelErr,
		Stream:        a.last,
	}
	if a.relaySrv != nil {
		view.RelayURL = fmt.Sprintf("http://%s:%d", localIP(), a.relayPort)
	}
	return view
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

// Serve startet das lokale Bedienpanel auf 127.0.0.1 und, wenn gewünscht,
// den eingebauten Relay.
func (a *App) Serve() error {
	ln, port, err := listenLocal()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.port = port
	runRelay := a.cfg.RunRelay
	a.mu.Unlock()
	srv := &http.Server{Handler: a.Handler()}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("Bedienpanel: %v", err)
		}
	}()
	a.log.Printf("Bedienpanel läuft auf %s", a.PanelURL())
	if runRelay {
		if err := a.StartRelay(); err != nil {
			a.log.Printf("Eingebauter Relay: %v", err)
		}
	}
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

// StartRelay bringt den Relay im eigenen Prozess hoch. Damit funktioniert
// Streaming im WLAN ohne zweiten Dienst, und der Tunnel hat etwas zum
// Veröffentlichen.
func (a *App) StartRelay() error {
	a.mu.Lock()
	if a.relaySrv != nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	ln, port, err := listenRelay()
	if err != nil {
		return err
	}
	relay := server.New(server.Config{MaxViewers: 8, Log: a.log})
	srv := &http.Server{Handler: relay.Handler(web.FS()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("Relay: %v", err)
		}
	}()

	a.mu.Lock()
	a.relaySrv = srv
	a.relayPort = port
	// Die eigene Server-Adresse auf den Relay zeigen lassen, damit der
	// Stream sofort läuft – aber nur, wenn bisher nichts Eigenes drinsteht.
	if isLocalServer(a.cfg.Server) {
		a.cfg.Server = fmt.Sprintf("http://127.0.0.1:%d", port)
		a.cfg.Save()
	}
	a.mu.Unlock()

	a.log.Printf("Relay läuft auf http://%s:%d (im WLAN erreichbar)", localIP(), port)
	return nil
}

// StopRelay schaltet den eingebauten Relay wieder ab.
func (a *App) StopRelay() {
	a.mu.Lock()
	srv := a.relaySrv
	a.relaySrv = nil
	a.relayPort = 0
	a.mu.Unlock()
	if srv != nil {
		srv.Close()
		a.log.Println("Relay gestoppt")
	}
}

func listenRelay() (net.Listener, int, error) {
	for port := 8080; port <= 8090; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, errors.New("kein freier Port für den Relay (8080–8090 belegt)")
}

// StartTunnel macht den Relay über cloudflared im Internet erreichbar.
func (a *App) StartTunnel() {
	a.mu.Lock()
	if a.tunnel != nil || a.tunnelBusy {
		a.mu.Unlock()
		return
	}
	port := a.relayPort
	a.tunnelBusy = true
	a.tunnelErr = ""
	a.mu.Unlock()

	if port == 0 {
		a.mu.Lock()
		a.tunnelBusy = false
		a.tunnelErr = "der eingebaute Relay läuft nicht"
		a.mu.Unlock()
		return
	}

	go func() {
		tn, err := tunnel.Start(port, a.log)
		a.mu.Lock()
		a.tunnelBusy = false
		if err != nil {
			a.tunnelErr = err.Error()
			a.mu.Unlock()
			a.log.Printf("Tunnel: %v", err)
			return
		}
		a.tunnel = tn
		a.tunnelURL = tn.URL
		a.mu.Unlock()
	}()
}

// StopTunnel beendet den Tunnel wieder.
func (a *App) StopTunnel() {
	a.mu.Lock()
	tn := a.tunnel
	a.tunnel = nil
	a.tunnelURL = ""
	a.tunnelErr = ""
	a.mu.Unlock()
	if tn != nil {
		tn.Stop()
		a.log.Println("Tunnel gestoppt")
	}
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

// ShareURL ist der Link, den man Freunden schickt. Er nimmt zuerst den
// Tunnel, dann die öffentliche Adresse und sonst die LAN-Adresse des
// eingebauten Relays – damit der Link ohne Konfiguration funktioniert.
func (a *App) ShareURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shareURLLocked()
}

func (a *App) shareURLLocked() string {
	base := a.tunnelURL
	if base == "" {
		base = a.cfg.PublicURL
	}
	if base == "" {
		if isLocalServer(a.cfg.Server) && a.relayPort > 0 {
			base = fmt.Sprintf("http://%s:%d", localIP(), a.relayPort)
		} else {
			base = a.cfg.Server
		}
	}
	return strings.TrimRight(base, "/") + "/?room=" + a.cfg.Room
}

// isLocalServer erkennt Adressen, die auf diesen Rechner zeigen.
func isLocalServer(server string) bool {
	s := strings.ToLower(strings.TrimSpace(server))
	if s == "" {
		return true
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	host := s
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// localIP liefert die LAN-Adresse dieses Rechners.
func localIP() string {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
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
