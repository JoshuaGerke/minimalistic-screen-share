package app

import (
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
)

// Das Konferenz-Fenster ist ein randloses Browserfenster (Edge, Chromium,
// Brave) mit eigenem Profil im Konfig-Ordner. Vorteile gegenüber einem Tab:
//   - Sprachkanal und Ton dürfen ohne Klick starten, das Mikrofon ist
//     freigegeben (eigenes Profil, nur für diese Seiten),
//   - es ist ein Kind der App: Die Windows-Aufnahme kann es samt Stimmen der
//     anderen aus dem eigenen Stream nehmen – kein Echo über den Stream.

var confWindows atomic.Int32 // offene Konferenz-Fenster

func appWindowArgs(url string) []string {
	return []string{
		"--app=" + url,
		"--user-data-dir=" + filepath.Join(config.Dir(), "fenster"),
		"--no-first-run", "--no-default-browser-check",
		"--autoplay-policy=no-user-gesture-required",
		"--use-fake-ui-for-media-stream",
		"--window-size=1280,820",
		"--class=mss-konferenz",
	}
}

// OpenConference öffnet eine Konferenz im eigenen Fenster; ohne passenden
// Browser im normalen Standardbrowser.
func (a *App) OpenConference(url string) {
	bin := appBrowser()
	if bin == "" {
		a.log.Printf("Konferenz-Fenster: kein Edge/Chromium gefunden, nehme den Standardbrowser")
		openURL(url)
		return
	}
	cmd := exec.Command(bin, appWindowArgs(url)...)
	if err := cmd.Start(); err != nil {
		a.log.Printf("Konferenz-Fenster: %v", err)
		openURL(url)
		return
	}
	confWindows.Add(1)
	a.log.Printf("Konferenz-Fenster geöffnet (%s)", filepath.Base(bin))
	go func() {
		cmd.Wait()
		confWindows.Add(-1)
	}()
}

// conferenceOpen sagt, ob gerade ein Konferenz-Fenster läuft.
func conferenceOpen() bool { return confWindows.Load() > 0 }
