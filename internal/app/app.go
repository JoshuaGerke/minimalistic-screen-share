// Paket app ist die App auf dem PC: Sie hält das Raum-Fenster bereit,
// betreibt bei Bedarf den Relay eines Raums (samt Tunnel ins Internet) und
// teilt den Bildschirm.
package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/diag"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/overlay"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/rendezvous"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/room"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/share"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/tunnel"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/update"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/video"
)

type App struct {
	log *log.Logger

	mu       sync.Mutex
	cfg      config.Config
	port     int
	onNotify func(title, text string)
	onQuit   func()
	onChange func()

	relay     *room.Server
	relaySrv  *http.Server
	relayPort int

	tunnel     *tunnel.Tunnel
	tunnelURL  string
	tunnelErr  string
	tunnelBusy bool
	tunnelWant bool

	origin string // zufällige Marke dieses Programmstarts (siehe share.Options.Origin)

	cur   *roomConn    // der Raum, in dem dieser PC gerade ist
	share *share.Share // eigener Bildschirm im Raum
}

func New(log *log.Logger, cfg config.Config) *App {
	b := make([]byte, 12)
	rand.Read(b)
	origin := hex.EncodeToString(b)
	relay := room.New(log)
	relay.Self = origin
	return &App{log: log, cfg: cfg, relay: relay, origin: origin}
}

func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SaveConfig prüft und speichert neue Einstellungen.
func (a *App) SaveConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(); err != nil {
		return err
	}
	a.mu.Lock()
	reachChanged := a.cfg.Reach != c.Reach
	a.cfg = c
	a.mu.Unlock()
	if reachChanged && a.hosting() {
		a.ensureReach()
	}
	return nil
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

// SetOnChange meldet Änderungen (Tray-Menü aktualisieren).
func (a *App) SetOnChange(fn func()) {
	a.mu.Lock()
	a.onChange = fn
	a.mu.Unlock()
}

func (a *App) changed() {
	a.mu.Lock()
	fn := a.onChange
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (a *App) notify(title, text string) {
	a.mu.Lock()
	fn := a.onNotify
	a.mu.Unlock()
	if fn != nil {
		fn(title, text)
	}
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

// Shutdown verlässt den Raum und räumt Tunnel und Relay ab.
func (a *App) Shutdown() {
	a.LeaveRoom()
	a.stopTunnel()
	a.mu.Lock()
	srv := a.relaySrv
	a.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// Serve startet das lokale Fenster-Backend auf 127.0.0.1 und die Helfer.
func (a *App) Serve() error {
	ln, port, err := listenLocal()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.port = port
	a.mu.Unlock()
	go func() {
		if err := (&http.Server{Handler: a.Handler()}).Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("Fenster-Backend: %v", err)
		}
	}()
	a.log.Printf("Version %s, Fenster-Backend auf %s", version.Version, a.WindowURL())
	if excludeInCapture {
		// Ältere Versionen haben ausgenommene Programme bei dir stumm
		// geschaltet; wurde die App hart beendet, blieben sie stumm.
		for _, name := range a.Config().MutedApps {
			audio.SetMuted(name, false)
		}
	}
	go video.Prepare(a.log)
	go a.diagLoop()
	go update.Watch(a.log, func(info update.Info) {
		a.notify("Update verfügbar", "Version "+info.Latest+" ist da – im Fenster unter Einstellungen aktualisieren.")
	})
	overlay.SetPositionStore(func() (int, int, bool) {
		c := a.Config()
		return c.OverlayX, c.OverlayY, c.OverlaySet
	}, func(x, y int) {
		a.mu.Lock()
		a.cfg.OverlayX, a.cfg.OverlayY, a.cfg.OverlaySet = x, y, true
		c := a.cfg
		a.mu.Unlock()
		c.Save()
	})
	go a.autoStart()
	return nil
}

func listenLocal() (net.Listener, int, error) {
	for port := 7777; port < 7797; port++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, errors.New("kein freier Port für das Fenster")
}

// WindowURL ist die Adresse des Raum-Fensters (nur auf diesem PC erreichbar).
func (a *App) WindowURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", a.port)
}

// OpenWindow öffnet das Raum-Fenster.
func (a *App) OpenWindow() {
	if u := a.WindowURL(); u != "" {
		a.OpenConference(u)
	}
}

/* ---------- Relay und Tunnel: nur wenn dieser PC einen Raum betreibt ---------- */

// startRelay bringt den Relay im eigenen Prozess hoch. Er lauscht auf allen
// Schnittstellen: Wer im selben Netzwerk sitzt, verbindet sich direkt statt
// über den Tunnel (viel weniger Verzögerung).
func (a *App) startRelay() (int, error) {
	a.mu.Lock()
	if a.relaySrv != nil {
		p := a.relayPort
		a.mu.Unlock()
		return p, nil
	}
	a.mu.Unlock()
	var ln net.Listener
	var port int
	var err error
	for port = 8080; port <= 8090; port++ {
		if ln, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err == nil {
			break
		}
	}
	if ln == nil {
		return 0, errors.New("kein freier Port für den Raum (8080–8090 belegt)")
	}
	srv := &http.Server{Handler: a.relay.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	a.mu.Lock()
	a.relaySrv, a.relayPort = srv, port
	a.mu.Unlock()
	return port, nil
}

// ensureReach startet je nach Einstellung den Tunnel (Internet) oder nicht.
func (a *App) ensureReach() {
	if a.Config().Reach == config.ReachInternet {
		a.startTunnel()
	} else {
		a.stopTunnel()
	}
}

// startTunnel macht den Relay über cloudflared im Internet erreichbar und
// baut ihn neu auf, falls er wegbricht.
func (a *App) startTunnel() {
	a.mu.Lock()
	a.tunnelWant = true
	if a.tunnel != nil || a.tunnelBusy || a.relayPort == 0 {
		a.mu.Unlock()
		return
	}
	port := a.relayPort
	a.tunnelBusy, a.tunnelErr = true, ""
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
				time.Sleep(backoff)
				backoff = min(backoff*2, time.Minute)
				continue
			}
			a.tunnel, a.tunnelURL, a.tunnelErr, a.tunnelBusy = tn, tn.URL, "", false
			a.mu.Unlock()
			a.changed()
			<-tn.Done()
			a.mu.Lock()
			if !a.tunnelWant || a.tunnel != tn {
				a.mu.Unlock()
				return
			}
			a.tunnel, a.tunnelURL, a.tunnelBusy = nil, "", true
			a.mu.Unlock()
			a.log.Println("Tunnel weggebrochen, baue neu auf")
			time.Sleep(time.Second)
		}
	}()
}

func (a *App) stopTunnel() {
	a.mu.Lock()
	tn := a.tunnel
	a.tunnel, a.tunnelWant, a.tunnelURL, a.tunnelErr = nil, false, "", ""
	a.mu.Unlock()
	if tn != nil {
		tn.Stop()
	}
}

// published sind die Adressen, unter denen andere den Raum auf diesem PC
// finden: der Tunnel (nur bei "Überall") und das eigene Netzwerk.
func (a *App) published() rendezvous.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.relayPort == 0 {
		return rendezvous.Entry{}
	}
	e := rendezvous.Entry{Host: a.origin}
	if a.cfg.Reach == config.ReachInternet {
		e.URL = a.tunnelURL
	}
	if ip := localIP(); ip != "127.0.0.1" {
		e.LAN = fmt.Sprintf("http://%s:%d", ip, a.relayPort)
	}
	return e
}

// localIP liefert die LAN-Adresse dieses Rechners.
func localIP() string {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}
	return "127.0.0.1"
}

// ApplyUpdate spielt die neueste Version ein und startet neu.
func (a *App) ApplyUpdate() {
	go func() {
		restart, err := update.Apply(a.log, true)
		if err != nil {
			a.log.Printf("Update: %v", err)
			return
		}
		if restart && update.Restart() == nil {
			a.Quit()
		}
	}()
}

// diagLoop schreibt im Diagnosemodus alle 2 s Leistungsdaten ins Log.
func (a *App) diagLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var sampler diag.Sampler
	active := false
	for range ticker.C {
		if !a.Config().Diagnose {
			active = false
			continue
		}
		if !active {
			active = true
			sampler = diag.Sampler{}
			sampler.Sample()
			a.log.Printf("Diagnose: an – Version %s, %s", version.Version, diag.SystemInfo())
			continue
		}
		line := "Diagnose: " + sampler.Sample()
		st := a.RoomStatus()
		if st.Room != "" {
			line += fmt.Sprintf(" | Raum %s %s, Betreiber: %v, im Raum: %d", st.Room, st.State, st.Hosting, a.relay.Count(st.Room))
		}
		if st.Share != nil {
			line += fmt.Sprintf(" | teile %s · %d fps · %d kbit/s %s", st.Share.Encoder, st.Share.FPS, st.Share.Kbps, st.Share.Error)
		}
		a.log.Println(line)
	}
}

// audioExclude bestimmt, was nicht in den geteilten Systemton soll: das vom
// Nutzer gewählte Programm – sonst die App selbst samt Raum-Fenster
// (Stimmen und Ton der anderen kämen sonst als Echo zurück).
func (a *App) audioExclude() []string {
	if !excludeInCapture {
		return nil
	}
	if apps := a.Config().MutedApps; len(apps) > 0 {
		return apps
	}
	return []string{audio.SelfTree}
}

// excludeInCapture: Unter Windows nimmt die Aufnahme Programme aus, die du
// selbst weiter hörst. Unter Linux geht das (noch) nicht.
const excludeInCapture = isWindows
