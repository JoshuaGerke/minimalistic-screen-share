// Paket app verbindet Einstellungen, Stream-Sitzung, Tray und das lokale
// Bedienpanel im Browser.
package app

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
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
	onNotify func(title, text string)
	onQuit   func()
	// copyPending: Der Stream läuft schon, der Link aber noch nicht (Tunnel
	// baut sich auf). Sobald er steht, landet er in der Zwischenablage.
	copyPending bool

	relaySrv   *http.Server
	relayPort  int
	tunnel     *tunnel.Tunnel
	tunnelURL  string
	tunnelErr  string
	tunnelBusy bool
	tunnelWant bool
}

// StatusView ist die Antwort von /api/status – Panel und Zweitinstanzen
// erkennen daran, dass hier ein mss-Client läuft.
type StatusView struct {
	App         string        `json:"app"`
	Version     string        `json:"version"`
	Platform    string        `json:"platform"`
	Streaming   bool          `json:"streaming"`
	Room        string        `json:"room"`
	Reach       string        `json:"reach"`
	ShareURL    string        `json:"share_url"`
	LinkReady   bool          `json:"link_ready"`
	LinkNote    string        `json:"link_note,omitempty"`
	LinkError   string        `json:"link_error,omitempty"`
	PanelURL    string        `json:"panel_url"`
	Autostart   bool          `json:"autostart"`
	CanAutorun  bool          `json:"can_autostart"`
	Stream      stream.Status `json:"stream"`
	TargetLabel string        `json:"target_label"`
}

func New(log *log.Logger, cfg config.Config) *App {
	return &App{
		log:  log,
		cfg:  cfg,
		last: stream.Status{State: "idle"},
	}
}

func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SaveConfig prüft und speichert neue Einstellungen. Läuft gerade ein Stream,
// greifen Bild-, Ton- und Freigabe-Einstellungen sofort.
func (a *App) SaveConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	a.mu.Lock()
	old := a.cfg
	sess := a.sess
	a.cfg = c
	a.mu.Unlock()

	if sess != nil {
		sess.SetParams(c.FPS, c.Quality, c.MaxWidth)
		sess.SetAudio(c.Audio)
		if old.AutoAccept != c.AutoAccept {
			sess.SetAutoAccept(c.AutoAccept)
		}
	}
	if old.Reach != c.Reach || old.Server != c.Server {
		a.applyReach()
	}
	return nil
}

// applyReach bringt Relay und Tunnel in den Zustand, den der Modus verlangt.
func (a *App) applyReach() {
	reach := a.Config().Reach
	if reach == config.ReachServer {
		a.StopTunnel()
		a.StopRelay()
		return
	}
	if err := a.StartRelay(); err != nil {
		a.log.Printf("Eingebauter Relay: %v", err)
		a.mu.Lock()
		a.tunnelErr = err.Error()
		a.mu.Unlock()
		return
	}
	if reach == config.ReachInternet {
		a.StartTunnel()
	} else {
		a.StopTunnel()
	}
}

func (a *App) Status() StatusView {
	a.mu.Lock()
	defer a.mu.Unlock()
	link, ready, note, linkErr := a.linkLocked()
	view := StatusView{
		App:         "mss",
		Version:     version.Version,
		Platform:    platform(),
		Streaming:   a.sess != nil,
		Room:        a.cfg.Room,
		Reach:       a.cfg.Reach,
		ShareURL:    link,
		LinkReady:   ready,
		LinkNote:    note,
		LinkError:   linkErr,
		PanelURL:    a.panelURL(),
		Autostart:   autostartEnabled(),
		CanAutorun:  true,
		Stream:      a.last,
		TargetLabel: a.cfg.Target.Label(),
	}
	if a.sess != nil {
		view.Stream = a.sess.Status()
	}
	return view
}

// SetOnStatus meldet Zustandsänderungen an das Tray.
func (a *App) SetOnStatus(fn func(stream.Status)) {
	a.mu.Lock()
	a.onStatus = fn
	a.mu.Unlock()
}

// SetOnNotify hinterlegt, wie Hinweise beim Nutzer landen (Tray-Sprechblase).
func (a *App) SetOnNotify(fn func(title, text string)) {
	a.mu.Lock()
	a.onNotify = fn
	a.mu.Unlock()
}

// SetOnQuit hinterlegt, wie sich die App selbst beendet (für Updates).
func (a *App) SetOnQuit(fn func()) {
	a.mu.Lock()
	a.onQuit = fn
	a.mu.Unlock()
}

func (a *App) notify(title, text string) {
	a.mu.Lock()
	fn := a.onNotify
	a.mu.Unlock()
	if fn != nil {
		fn(title, text)
	}
}

// streamServer ist die Adresse, an die der eigene Stream geht.
func (a *App) streamServer() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.Reach == config.ReachServer {
		return a.cfg.Server, nil
	}
	if a.relayPort == 0 {
		return "", errors.New("der eingebaute Relay läuft nicht")
	}
	return fmt.Sprintf("http://127.0.0.1:%d", a.relayPort), nil
}

// Start beginnt zu streamen. Ziel und aktuelle Einstellungen wandern in die
// Konfigdatei, damit der nächste Start gleich passt. Der Link landet direkt
// in der Zwischenablage – wie bei AnyDesk: klicken, einfügen, fertig.
func (a *App) Start(target capture.Target) error {
	target = resolveTarget(target)
	if a.Config().Reach != config.ReachServer {
		if err := a.StartRelay(); err != nil {
			return err
		}
	}
	srv, err := a.streamServer()
	if err != nil {
		return err
	}

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
		Server:     srv,
		Room:       cfg.Room,
		Name:       cfg.Name,
		FPS:        cfg.FPS,
		Quality:    cfg.Quality,
		MaxWidth:   cfg.MaxWidth,
		Audio:      cfg.Audio,
		AutoAccept: cfg.AutoAccept,
		Target:     target,
		Log:        a.log,
		AudioExclude: func() []string {
			if !excludeInCapture {
				return nil
			}
			return a.Config().MutedApps
		},
	})
	sess.SetOnChange(a.onSessionStatus)
	a.sess = sess
	a.copyPending = true
	a.mu.Unlock()

	go func() {
		sess.Run()
		a.mu.Lock()
		if a.sess == sess {
			a.sess = nil
			a.last = stream.Status{State: "idle"}
			a.copyPending = false
		}
		cb := a.onStatus
		a.mu.Unlock()
		if cb != nil {
			cb(stream.Status{State: "idle"})
		}
	}()

	if !excludeInCapture {
		go a.holdMutes(sess)
	}
	a.copyLinkIfReady()
	return nil
}

// resolveTarget macht aus "primary" (Panel: „Bildschirm“) den echten
// Monitor – unter Hyprland den fokussierten, sonst den Hauptmonitor.
func resolveTarget(t capture.Target) capture.Target {
	if t.Kind != "primary" {
		return t
	}
	p := capture.Primary()
	p.Cursor = t.Cursor
	return p
}

// copyLinkIfReady legt den Link nach dem Start einmal in die Zwischenablage.
func (a *App) copyLinkIfReady() {
	a.mu.Lock()
	link, ready, _, _ := a.linkLocked()
	if !a.copyPending || !ready {
		a.mu.Unlock()
		return
	}
	a.copyPending = false
	a.mu.Unlock()
	if err := copyToClipboard(link); err != nil {
		a.log.Printf("Zwischenablage: %v", err)
		return
	}
	a.notify("Link kopiert", "Einfach im Chat einfügen (Strg+V) – wer ihn öffnet, klopft bei dir an.")
}

// excludeInCapture: Unter Windows nimmt die Aufnahme Programme aus, die du
// selbst weiter hörst. Unter Linux geht das noch nicht – dort werden sie
// während des Streams stummgeschaltet.
const excludeInCapture = runtime.GOOS == "windows"

// holdMutes hält die Stummschaltung der Tonquellen aktuell, solange die
// Sitzung läuft – auch wenn ein Programm erst später Ton spielt. Beim Ende
// wird alles wieder laut gestellt.
func (a *App) holdMutes(sess *stream.Session) {
	apply := func() {
		a.mu.Lock()
		names := append([]string(nil), a.cfg.MutedApps...)
		a.mu.Unlock()
		if len(names) == 0 {
			return
		}
		if err := audio.MuteApps(names); err != nil {
			a.log.Printf("Tonquellen stummschalten: %v", err)
		}
	}
	apply()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			apply()
		case <-sess.Done():
			audio.RestoreAll()
			return
		}
	}
}

func (a *App) Stop() {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
}

// session liefert die laufende Sitzung oder einen Fehler.
func (a *App) session() (*stream.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess == nil {
		return nil, errors.New("kein Stream aktiv")
	}
	return a.sess, nil
}

// Allow lässt eine wartende Zuschaueranfrage durch.
func (a *App) Allow(id uint64) error {
	sess, err := a.session()
	if err != nil {
		return err
	}
	return sess.Allow(id)
}

// Deny lehnt eine Zuschaueranfrage ab.
func (a *App) Deny(id uint64) error {
	sess, err := a.session()
	if err != nil {
		return err
	}
	return sess.Deny(id)
}

// SetPaused hält den laufenden Stream an oder lässt ihn weiterlaufen.
func (a *App) SetPaused(on bool) error {
	sess, err := a.session()
	if err != nil {
		return err
	}
	sess.SetPaused(on)
	return nil
}

// TogglePause ist der Tray-Knopf für die Pause.
func (a *App) TogglePause() {
	if sess, err := a.session(); err == nil {
		sess.SetPaused(!sess.Paused())
	}
}

// SetTarget wechselt die Quelle. Läuft ein Stream, passiert das ohne
// Unterbrechung für die Zuschauer.
func (a *App) SetTarget(target capture.Target) error {
	target = resolveTarget(target)
	a.mu.Lock()
	cfg := a.cfg
	cfg.Target = target
	if err := cfg.Validate(); err != nil {
		a.mu.Unlock()
		return err
	}
	a.cfg = cfg
	sess := a.sess
	a.mu.Unlock()
	cfg.Save()
	if sess != nil {
		sess.SetTarget(target)
	}
	return nil
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

// Serve startet das lokale Bedienpanel auf 127.0.0.1 und je nach Modus
// Relay und Tunnel – damit der Link schon steht, bevor man auf Start drückt.
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
	if excludeInCapture {
		// Ältere Versionen haben ausgenommene Programme bei dir stumm
		// geschaltet; wurde die App hart beendet, blieben sie stumm.
		for _, name := range a.Config().MutedApps {
			audio.SetMuted(name, false)
		}
	}
	a.applyReach()
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
	relay := server.New(server.Config{MaxViewers: 16, Log: a.log})
	srv := &http.Server{Handler: relay.Handler(web.FS()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("Relay: %v", err)
		}
	}()

	a.mu.Lock()
	a.relaySrv = srv
	a.relayPort = port
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

// StartTunnel macht den Relay über cloudflared im Internet erreichbar und
// baut ihn neu auf, falls er wegbricht.
func (a *App) StartTunnel() {
	a.mu.Lock()
	a.tunnelWant = true
	if a.tunnel != nil || a.tunnelBusy {
		a.mu.Unlock()
		return
	}
	port := a.relayPort
	a.tunnelBusy = true
	a.tunnelErr = ""
	a.mu.Unlock()

	go func() {
		backoff := 3 * time.Second
		for {
			tn, err := tunnel.Start(port, a.log)
			a.mu.Lock()
			if !a.tunnelWant {
				a.tunnelBusy = false
				a.mu.Unlock()
				if tn != nil {
					tn.Stop()
				}
				return
			}
			if err != nil {
				a.tunnelErr = err.Error()
				a.mu.Unlock()
				a.log.Printf("Tunnel: %v – neuer Versuch in %s", err, backoff)
				time.Sleep(backoff)
				if backoff < time.Minute {
					backoff *= 2
				}
				continue
			}
			backoff = 3 * time.Second
			changed := a.tunnelURL != "" && a.tunnelURL != tn.URL
			a.tunnel = tn
			a.tunnelURL = tn.URL
			a.tunnelErr = ""
			a.tunnelBusy = false
			streaming := a.sess != nil
			if changed && streaming {
				a.copyPending = true
			}
			a.mu.Unlock()
			if changed && streaming {
				a.notify("Neuer Link", "Die Internet-Verbindung wurde neu aufgebaut. Der neue Link ist in der Zwischenablage.")
			}
			a.copyLinkIfReady()

			<-tn.Done()
			a.mu.Lock()
			if !a.tunnelWant || a.tunnel != tn {
				a.mu.Unlock()
				return
			}
			a.tunnel = nil
			a.tunnelBusy = true
			a.mu.Unlock()
			a.log.Println("Tunnel weggebrochen, baue neu auf")
			time.Sleep(time.Second)
		}
	}()
}

// StopTunnel beendet den Tunnel wieder.
func (a *App) StopTunnel() {
	a.mu.Lock()
	tn := a.tunnel
	a.tunnel = nil
	a.tunnelWant = false
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

// ShareURL ist der Link, den man Freunden schickt.
func (a *App) ShareURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	link, _, _, _ := a.linkLocked()
	return link
}

// linkLocked baut den Link je nach Modus und sagt, ob er schon benutzbar ist.
func (a *App) linkLocked() (link string, ready bool, note, linkErr string) {
	var base string
	switch a.cfg.Reach {
	case config.ReachServer:
		base = a.cfg.Server
		note = "Über deinen eigenen Server"
	case config.ReachLAN:
		if a.relayPort > 0 {
			base = fmt.Sprintf("http://%s:%d", localIP(), a.relayPort)
		}
		note = "Nur im selben Netzwerk (WLAN/LAN) erreichbar"
	default:
		base = a.tunnelURL
		switch {
		case base != "":
			note = "Funktioniert überall – der Link gilt, solange die App läuft"
		case a.tunnelErr != "":
			linkErr = a.tunnelErr
			note = "Internet-Verbindung klappt gerade nicht, neuer Versuch läuft…"
		default:
			note = "Link wird vorbereitet… (beim ersten Start einmalig ca. 40 MB)"
		}
	}
	if base == "" {
		return "", false, note, linkErr
	}
	return strings.TrimRight(base, "/") + "/?room=" + a.cfg.Room, true, note, linkErr
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
	// self=1: Der Viewer bleibt stumm, sonst koppelt der eigene Ton auf.
	if url := a.ShareURL(); url != "" {
		openURL(url + "&self=1")
	}
}

// CopyLink legt den Link in die Zwischenablage (Tray-Menü).
func (a *App) CopyLink() {
	link := a.ShareURL()
	if link == "" {
		a.notify("Noch kein Link", "Die Verbindung wird gerade aufgebaut – gleich noch mal versuchen.")
		return
	}
	if err := copyToClipboard(link); err != nil {
		a.notify("Kopieren fehlgeschlagen", err.Error())
		return
	}
	a.notify("Link kopiert", link)
}

// Quit beendet die App (z. B. wenn eine neue Version sich installiert).
func (a *App) Quit() {
	a.mu.Lock()
	fn := a.onQuit
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
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
