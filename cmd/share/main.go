// mss-share ist die App auf dem PC: ein kleines Fenster für Räume mit
// Sprachkanal, Bildschirm teilen, Chat und Dateien – Ende-zu-Ende
// verschlüsselt. Im Infobereich sitzt ein Symbol.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/app"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/setup"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/single"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/tray"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/update"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

const (
	idOpen = iota + 1
	idStatus
	idShare
	idLeave
	idQuit
)

func main() {
	var (
		showVersion  = flag.Bool("version", false, "Version anzeigen")
		listWindows  = flag.Bool("list-windows", false, "Fenster auflisten")
		listMonitors = flag.Bool("list-monitors", false, "Monitore auflisten")
		debug        = flag.Bool("debug", false, "Logs nur auf der Konsole")
		uninstall    = flag.Bool("uninstall", false, "App entfernen")
		welcome      = flag.Bool("welcome", false, "nach der Installation: Fenster öffnen")
		background   = flag.Bool("background", false, "still starten (Autostart), kein Fenster")
		portable     = flag.Bool("portable", false, "nicht installieren, direkt aus diesem Ordner laufen")
		diagnose     = flag.Bool("diag", false, "Diagnosemodus: Leistungsdaten alle 2 s ins Log")
		doUpdate     = flag.Bool("update", false, "auf die neueste Version aktualisieren")
		fromUpdate   = flag.Bool("from-update", false, "intern: neue Version spielt sich nach dem Download ein")
		_            = flag.Bool("settings", false, "wie ohne Schalter (für alte Desktop-Einträge)")
	)
	flag.Parse()

	capture.MakeDPIAware()
	logger := newLogger(*debug)

	switch {
	case *showVersion:
		fmt.Println("mss-share", version.Version)
		return
	case *listWindows:
		runListWindows()
		return
	case *listMonitors:
		runListMonitors()
		return
	case *uninstall:
		if err := setup.Uninstall(); err != nil {
			setup.Message(err.Error())
		}
		return
	case *doUpdate:
		runUpdate(logger)
		return
	}
	// Doppelklick auf die heruntergeladene exe: erst installieren, dann
	// startet die installierte Kopie. Klappt das nicht, läuft sie portabel.
	if !*portable && setup.Needed() {
		if err := setup.Install(logger, *fromUpdate); err == nil {
			return
		} else {
			logger.Printf("Installation: %v – laufe portabel", err)
		}
	}
	cfg := config.Load()
	if *diagnose {
		cfg.Diagnose = true
	}
	run(logger, cfg, *welcome, *background)
}

/* ---------- Logging ---------- */

func newLogger(debug bool) *log.Logger {
	if debug {
		return log.New(os.Stderr, "", log.LstdFlags)
	}
	// Im Tray-Betrieb sieht man die Konsole nicht – also ins Logfile schreiben.
	f, err := os.OpenFile(filepath.Join(config.Dir(), "share.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return log.New(os.Stderr, "", log.LstdFlags)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 1<<20 {
		f.Truncate(0)
		f.Seek(0, 0)
	}
	return log.New(teeWriter{primary: f, secondary: os.Stderr}, "", log.LstdFlags)
}

// teeWriter schreibt zuerst ins Logfile und danach, wenn möglich, auf die
// Konsole. Kein io.MultiWriter: Im GUI-Build ist die Konsole ungültig, und
// MultiWriter würde dann das Logfile überspringen.
type teeWriter struct {
	primary   io.Writer
	secondary io.Writer
}

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.primary.Write(p)
	if t.secondary != nil {
		t.secondary.Write(p)
	}
	return n, err
}

/* ---------- Kommandozeilenmodi ---------- */

func runListWindows() {
	windows, err := capture.ListWindows()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
	for _, w := range windows {
		fmt.Printf("%dx%d  %s\n", w.W, w.H, w.Title)
	}
}

func runListMonitors() {
	monitors, err := capture.ListMonitors()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
	for _, m := range monitors {
		fmt.Printf("%d: %dx%d bei %d,%d\n", m.Index, m.W, m.H, m.X, m.Y)
	}
}

/* ---------- Update von der Kommandozeile ---------- */

// runUpdate holt die neueste Version (auch für den Desktop-Eintrag
// "Screen Share aktualisieren").
func runUpdate(logger *log.Logger) {
	var last string
	say := func(f string, a ...any) {
		last = fmt.Sprintf(f, a...)
		fmt.Println(last)
		logger.Println(last)
	}
	done := false
	defer func() {
		if runtime.GOOS == "windows" {
			// Die exe hat unter Windows keine Konsole. Nach erfolgreichem
			// Download sofort raus – sonst ist die Datei gesperrt und die neue
			// Version kann sie nicht ersetzen. Sie öffnet dann ihr Fenster.
			if !done {
				setup.Message(last)
			}
		} else {
			time.Sleep(3 * time.Second) // Terminalfenster nicht sofort schließen
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	info, err := update.Check(ctx)
	cancel()
	if err != nil {
		say("Update-Prüfung fehlgeschlagen: %v", err)
		return
	}
	if !info.Available {
		say("Schon aktuell (Version %s).", info.Current)
		return
	}
	say("Aktualisiere %s → %s …", info.Current, info.Latest)
	restart, err := update.Apply(logger, false)
	if err != nil {
		say("Update fehlgeschlagen: %v", err)
		return
	}
	if restart {
		// Linux: laufende App beenden, neue Version still starten.
		if setup.StopRunning() {
			update.Restart()
		}
	}
	done = runtime.GOOS == "windows"
	say("Fertig – Version %s.", info.Latest)
}

/* ---------- Betrieb ---------- */

func run(logger *log.Logger, cfg config.Config, welcome, background bool) {
	first, err := single.Acquire()
	// Nach einem Update startet die neue Version, während die alte noch
	// herunterfährt – kurz warten statt aufzugeben.
	for i := 0; background && err == nil && !first && i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		first, err = single.Acquire()
	}
	if err != nil {
		logger.Printf("Einzelinstanz-Prüfung: %v", err)
	} else if !first {
		if !background && !showRunning() {
			logger.Println("App läuft schon, Fenster war nicht erreichbar")
		}
		return
	}

	a := app.New(logger, cfg)
	if err := a.Serve(); err != nil {
		logger.Fatalf("Fenster-Backend: %v", err)
	}
	// Mit "Beim Start Raum betreten" öffnet autoStart das Fenster selbst.
	if !background && cfg.AutoJoin == "" {
		a.OpenWindow()
	}

	var cur tray.Tray
	items := []*tray.Item{
		{ID: idOpen, Title: "Öffnen", OnClick: a.OpenWindow},
		{ID: idStatus, Title: "In keinem Raum", Disabled: true},
		{ID: idShare, Title: "Bildschirm teilen", Disabled: true, OnClick: func() {
			if a.Sharing() {
				a.StopShare()
			} else if err := a.StartShare(a.Config().Target); err != nil {
				cur.Notify("Teilen geht nicht", err.Error())
			}
		}},
		{ID: idLeave, Title: "Raum verlassen", Disabled: true, OnClick: a.LeaveRoom},
		{Separator: true},
		{ID: idQuit, Title: "Beenden", OnClick: func() { cur.Quit() }},
	}
	err = tray.Run(items, func(t tray.Tray) {
		cur = t
		t.SetActivate(a.OpenWindow)
		a.SetOnNotify(t.Notify)
		a.SetOnQuit(t.Quit)
		refresh := func() {
			st := a.RoomStatus()
			text, icon := "In keinem Raum", "idle"
			if st.Room != "" {
				text, icon = "Raum "+st.Room+" – "+map[string]string{"suche": "suche…", "betreibe": "verbunden", "drin": "verbunden"}[st.State], "paused"
				if st.State != "suche" {
					icon = "idle"
				}
			}
			if st.Share != nil {
				icon = "live"
				t.SetItemTitle(idShare, "Teilen beenden")
			} else {
				t.SetItemTitle(idShare, "Bildschirm teilen")
			}
			t.SetItemTitle(idStatus, text)
			t.SetTooltip("Screen Share – " + text)
			t.SetIconState(icon)
			t.SetItemEnabled(idShare, st.Room != "")
			t.SetItemEnabled(idLeave, st.Room != "")
		}
		a.SetOnChange(refresh)
		refresh()
		if welcome {
			t.Notify("Installiert", "Das Symbol sitzt hier im Infobereich. Klick öffnet das Fenster.")
		}
	})
	if err != nil {
		logger.Printf("Kein Tray verfügbar (%v) – Fenster: %s", err, a.WindowURL())
		quit := make(chan struct{})
		var once sync.Once
		a.SetOnQuit(func() { once.Do(func() { close(quit) }) })
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		select {
		case <-sig:
		case <-quit:
		}
	}
	a.Shutdown()
}

// showRunning bittet eine laufende Instanz, ihr Fenster zu öffnen.
func showRunning() bool {
	for port := 7777; port < 7797; port++ {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
		if err != nil {
			continue
		}
		var st struct {
			App string `json:"app"`
		}
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.App == "mss" {
			r, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/show", port), "application/json", nil)
			if err == nil {
				r.Body.Close()
				return true
			}
		}
	}
	return false
}
