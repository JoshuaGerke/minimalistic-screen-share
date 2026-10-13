// mss-share sitzt im Tray und streamt auf Knopfdruck Bildschirm oder Fenster
// samt Systemton zum Relay.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/app"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/single"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/stream"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/tray"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

const (
	idStartScreen   = 1
	idStop          = 2
	idStatus        = 3
	idWatch         = 4
	idPanel         = 5
	idQuit          = 6
	idSessionID     = 7
	idWindowMenu    = 10
	idWindowBase    = 100
	windowSlotCount = 20
	idWindowRefresh = 199
	idMonitorMenu   = 20
	idMonitorAll    = 21
	idMonitorBase   = 30
)

var (
	currentTray tray.Tray
	refreshFn   func()
)

func main() {
	var (
		showVersion  = flag.Bool("version", false, "Version anzeigen")
		listWindows  = flag.Bool("list-windows", false, "Fenster auflisten")
		listMonitors = flag.Bool("list-monitors", false, "Monitore auflisten")
		snapshot     = flag.String("snapshot", "", "einmal aufnehmen und als JPEG speichern")
		window       = flag.String("window", "", "Fenstertitel für -snapshot")
		monitor      = flag.Int("monitor", -1, "Monitorindex für -snapshot")
		region       = flag.String("region", "", "Bereich x,y,w,h für -snapshot")
		pattern      = flag.Bool("test-pattern", false, "Testbild statt echter Aufnahme")
		streamNow    = flag.Bool("stream", false, "ohne Tray direkt streamen")
		settings     = flag.Bool("settings", false, "nur das Bedienpanel öffnen")
		server       = flag.String("server", "", "Server-Adresse überschreiben")
		room         = flag.String("room", "", "Raumcode überschreiben")
		fps          = flag.Int("fps", 0, "FPS überschreiben")
		quality      = flag.Int("quality", 0, "JPEG-Qualität überschreiben")
		width        = flag.Int("width", -1, "maximale Breite überschreiben (0 = Original)")
		noAudio      = flag.Bool("no-audio", false, "ohne Ton streamen")
		debug        = flag.Bool("debug", false, "Logs nur auf der Konsole")
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
	case *snapshot != "":
		target, err := snapshotTarget(*window, *monitor, *region, *pattern)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Fehler:", err)
			os.Exit(1)
		}
		q := 80
		if *quality > 0 {
			q = *quality
		}
		maxW := 0
		if *width >= 0 {
			maxW = *width
		}
		if err := runSnapshot(*snapshot, target, q, maxW); err != nil {
			fmt.Fprintln(os.Stderr, "Fehler:", err)
			os.Exit(1)
		}
		return
	}

	cfg := config.Load()
	if *server != "" {
		cfg.Server = *server
	}
	if *room != "" {
		cfg.Room = *room
	}
	if *fps > 0 {
		cfg.FPS = *fps
	}
	if *quality > 0 {
		cfg.Quality = *quality
	}
	if *width >= 0 {
		cfg.MaxWidth = *width
	}
	if *noAudio {
		cfg.Audio = false
	}
	if *pattern {
		cfg.Target = capture.Target{Kind: "test"}
	}

	if *streamNow {
		if err := runHeadless(logger, cfg); err != nil {
			logger.Fatal(err)
		}
		return
	}
	runTray(logger, cfg, *settings)
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

func snapshotTarget(window string, monitor int, region string, pattern bool) (capture.Target, error) {
	switch {
	case pattern:
		return capture.Target{Kind: "test"}, nil
	case window != "":
		return capture.Target{Kind: "window", Window: window, Cursor: true}, nil
	case monitor >= 0:
		return capture.Target{Kind: "monitor", Monitor: monitor, Cursor: true}, nil
	case region != "":
		r, err := parseRegion(region)
		if err != nil {
			return capture.Target{}, err
		}
		return capture.Target{Kind: "region", Region: r, Cursor: true}, nil
	}
	return capture.Target{Kind: "screen", Cursor: true}, nil
}

func parseRegion(s string) (capture.Rect, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return capture.Rect{}, fmt.Errorf("Bereich muss als x,y,w,h angegeben werden")
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return capture.Rect{}, fmt.Errorf("Bereich %q: %v", s, err)
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return capture.Rect{}, fmt.Errorf("Bereich hat keine Größe")
	}
	return capture.Rect{X: v[0], Y: v[1], W: v[2], H: v[3]}, nil
}

func runSnapshot(path string, target capture.Target, quality, maxWidth int) error {
	src, err := capture.Open(target)
	if err != nil {
		return err
	}
	defer src.Close()
	img, err := src.Grab()
	if err != nil {
		return err
	}
	img = capture.Scale(img, maxWidth)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		return err
	}
	fmt.Printf("%s gespeichert (%dx%d)\n", path, img.Bounds().Dx(), img.Bounds().Dy())
	return nil
}

// runHeadless streamt ohne Tray – praktisch zum Testen und für Autostart.
func runHeadless(logger *log.Logger, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sess := stream.New(stream.Options{
		Server:   cfg.Server,
		Room:     cfg.Room,
		Name:     cfg.Name,
		FPS:      cfg.FPS,
		Quality:  cfg.Quality,
		MaxWidth: cfg.MaxWidth,
		Audio:    cfg.Audio,
		Target:   cfg.Target,
		Log:      logger,
	})
	last := ""
	sess.SetOnChange(func(st stream.Status) {
		line := statusText(st)
		if line != last {
			logger.Println(line)
			last = line
		}
	})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Println("beende Stream")
		sess.Stop()
	}()
	sess.Run()
	return nil
}

/* ---------- Tray-Betrieb ---------- */

func runTray(logger *log.Logger, cfg config.Config, settingsOnly bool) {
	firstRun := false
	if _, err := os.Stat(config.Path()); os.IsNotExist(err) {
		firstRun = true
	}

	a := app.New(logger)

	if settingsOnly {
		if openExistingPanel() {
			return
		}
	} else {
		first, err := single.Acquire()
		if err != nil {
			logger.Printf("Einzelinstanz-Prüfung: %v", err)
		} else if !first {
			if !openExistingPanel() {
				logger.Println("Client läuft schon, Panel war nicht erreichbar")
			}
			return
		}
	}

	if err := a.Serve(); err != nil {
		logger.Fatalf("Bedienpanel: %v", err)
	}
	if settingsOnly || firstRun {
		a.OpenPanel()
	}

	items, slots := buildMenu(a)
	err := tray.Run(items, func(t tray.Tray) {
		wireTray(a, t, slots)
	})
	if err != nil {
		logger.Printf("Kein Tray verfügbar (%v)", err)
		logger.Printf("Steuerung über %s", a.PanelURL())
		waitSignal(a)
	}
	a.Stop()
}

func waitSignal(a *app.App) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	a.Stop()
}

// openExistingPanel sucht eine laufende Instanz und öffnet deren Panel.
func openExistingPanel() bool {
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
			return app.OpenBrowserURL(fmt.Sprintf("http://127.0.0.1:%d/", port))
		}
	}
	return false
}

type windowSlots struct {
	mu     sync.Mutex
	titles []string
}

func (s *windowSlots) get(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.titles[i]
}

func (s *windowSlots) set(i int, title string) {
	s.mu.Lock()
	s.titles[i] = title
	s.mu.Unlock()
}

func buildMenu(a *app.App) ([]*tray.Item, *windowSlots) {
	slots := &windowSlots{titles: make([]string, windowSlotCount)}

	windowItems := make([]*tray.Item, 0, windowSlotCount+2)
	for i := 0; i < windowSlotCount; i++ {
		i := i
		windowItems = append(windowItems, &tray.Item{
			ID:       idWindowBase + i,
			Title:    "–",
			Disabled: true,
			OnClick: func() {
				if title := slots.get(i); title != "" {
					startTarget(a, capture.Target{Kind: "window", Window: title, Cursor: true})
				}
			},
		})
	}
	windowItems = append(windowItems,
		&tray.Item{Separator: true},
		&tray.Item{ID: idWindowRefresh, Title: "Liste aktualisieren", OnClick: func() {
			if refreshFn != nil {
				refreshFn()
			}
		}},
	)

	monitors, _ := capture.ListMonitors()
	monitorItems := []*tray.Item{{
		ID:    idMonitorAll,
		Title: "Alle Monitore",
		OnClick: func() {
			startTarget(a, capture.Target{Kind: "screen", Cursor: true})
		},
	}}
	for _, m := range monitors {
		m := m
		monitorItems = append(monitorItems, &tray.Item{
			ID:    idMonitorBase + m.Index,
			Title: fmt.Sprintf("Monitor %d (%d×%d)", m.Index+1, m.W, m.H),
			OnClick: func() {
				startTarget(a, capture.Target{Kind: "monitor", Monitor: m.Index, Cursor: true})
			},
		})
	}

	items := []*tray.Item{
		{ID: idStartScreen, Title: "Bildschirm streamen", OnClick: func() {
			startTarget(a, capture.Primary())
		}},
		{ID: idWindowMenu, Title: "Fenster streamen", Items: windowItems},
		{ID: idMonitorMenu, Title: "Monitor streamen", Items: monitorItems},
		{ID: idStop, Title: "Stream stoppen", Disabled: true, OnClick: a.Stop},
		{Separator: true},
		{ID: idStatus, Title: "Status: Bereit", Disabled: true},
		{ID: idSessionID, Title: "Sitzungs-ID: –", OnClick: a.OpenPanel},
		{ID: idWatch, Title: "Im Browser ansehen", Disabled: true, OnClick: a.OpenViewer},
		{Separator: true},
		{ID: idPanel, Title: "Einstellungen…", OnClick: a.OpenPanel},
		{ID: idQuit, Title: "Beenden", OnClick: func() {
			if currentTray != nil {
				currentTray.Quit()
			}
		}},
	}
	return items, slots
}

func wireTray(a *app.App, t tray.Tray, slots *windowSlots) {
	currentTray = t
	t.SetActivate(a.OpenPanel)
	refreshFn = func() { refreshWindows(t, slots) }
	refreshWindows(t, slots)

	lastRoom := ""
	lastPending := 0
	t.SetItemTitle(idSessionID, "Sitzungs-ID: "+a.Config().Room)

	a.SetOnStatus(func(st stream.Status) {
		iconState, text := describe(st)
		t.SetItemTitle(idStatus, text)
		t.SetTooltip("minimalistic screen share – " + text)
		t.SetIconState(iconState)
		live := a.Streaming()
		t.SetItemEnabled(idStop, live)
		t.SetItemEnabled(idWatch, live)

		if room := a.Config().Room; room != lastRoom {
			lastRoom = room
			t.SetItemTitle(idSessionID, "Sitzungs-ID: "+room)
		}

		if n := len(st.Pending); n > 0 {
			if n > lastPending {
				name := st.Pending[n-1].Name
				t.Notify("Anfrage von "+name, name+" möchte zuschauen. Klick öffnet das Panel zum Annehmen oder Ablehnen.")
			}
		}
		lastPending = len(st.Pending)
	})
}

func refreshWindows(t tray.Tray, slots *windowSlots) {
	windows, err := capture.ListWindows()
	if err != nil {
		return
	}
	for i := 0; i < windowSlotCount; i++ {
		if i < len(windows) {
			title := windows[i].Title
			slots.set(i, title)
			t.SetItemTitle(idWindowBase+i, truncate(title, 48))
			t.SetItemEnabled(idWindowBase+i, true)
		} else {
			slots.set(i, "")
			t.SetItemTitle(idWindowBase+i, "–")
			t.SetItemEnabled(idWindowBase+i, false)
		}
	}
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func startTarget(a *app.App, target capture.Target) {
	if err := a.Start(target); err != nil {
		a.ReportError(err.Error())
	}
}

func describe(st stream.Status) (iconState, text string) {
	if n := len(st.Pending); n > 0 {
		if n == 1 {
			return "paused", "Status: Anfrage von " + st.Pending[0].Name
		}
		return "paused", fmt.Sprintf("Status: %d offene Anfragen", n)
	}
	switch st.State {
	case "connecting":
		return "idle", "Status: Verbinde…"
	case "live":
		return "live", fmt.Sprintf("Status: Live – %d Zuschauer", st.Viewers)
	case "paused":
		return "paused", "Status: Pausiert – niemand schaut zu"
	case "reconnecting":
		return "paused", "Status: Neu verbinden…"
	case "error":
		return "error", "Status: Fehler – " + st.Err
	default:
		return "idle", "Status: Bereit"
	}
}

func statusText(st stream.Status) string {
	_, text := describe(st)
	return text
}
