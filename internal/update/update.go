// Paket update sucht auf GitHub nach neuen Versionen und spielt sie ein –
// ein Klick im Fenster genügt.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

const repo = "JoshuaGerke/minimalistic-screen-share"

// Info beschreibt den Stand der Updates.
type Info struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Notes     string `json:"notes_url,omitempty"`
	Applying  bool   `json:"applying,omitempty"`
	Error     string `json:"error,omitempty"`
	asset     string
}

var (
	mu   sync.Mutex
	last = Info{Current: version.Version}
)

// Status liefert den zuletzt bekannten Stand.
func Status() Info {
	mu.Lock()
	defer mu.Unlock()
	return last
}

// Check fragt GitHub nach der neuesten Version.
func Check(ctx context.Context) (Info, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mss-share/"+version.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Status(), err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status(), fmt.Errorf("GitHub antwortet mit %d", resp.StatusCode)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Status(), err
	}
	info := Info{Current: version.Version, Latest: strings.TrimPrefix(rel.Tag, "v"), Notes: rel.URL}
	for _, a := range rel.Assets {
		if assetMatches(a.Name) {
			info.asset = a.URL
		}
	}
	info.Available = info.asset != "" && Newer(info.Latest, info.Current)
	mu.Lock()
	info.Applying, info.Error = last.Applying, last.Error
	last = info
	mu.Unlock()
	return info, nil
}

// Watch prüft jetzt und danach alle 6 Stunden. found wird einmal pro neuer
// Version aufgerufen.
func Watch(logg *log.Logger, found func(Info)) {
	announced := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		info, err := Check(ctx)
		cancel()
		if err != nil {
			logg.Printf("Update-Prüfung: %v", err)
		} else if info.Available && info.Latest != announced {
			announced = info.Latest
			logg.Printf("Update verfügbar: %s → %s", info.Current, info.Latest)
			if found != nil {
				found(info)
			}
		}
		time.Sleep(6 * time.Hour)
	}
}

// Newer sagt, ob Version a neuer ist als b ("0.7.1" > "0.7.0").
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		n, _ := strconv.Atoi(strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}

// Apply lädt die neue Version und spielt sie ein. Unter Windows übernimmt
// die heruntergeladene exe den Rest (beendet die laufende App, ersetzt sie,
// startet neu); unter Linux werden die Programme an Ort und Stelle getauscht
// und restart muss die App neu starten.
//
// quiet: Die neue Version startet still im Hintergrund (Knopf im Fenster).
// Sonst öffnet sie ihr Fenster – das ist die Rückmeldung fürs Startmenü.
func Apply(logg *log.Logger, quiet bool) (restart bool, err error) {
	mu.Lock()
	if last.Applying {
		mu.Unlock()
		return false, errors.New("Update läuft schon")
	}
	last.Applying, last.Error = true, ""
	info := last
	mu.Unlock()
	defer func() {
		mu.Lock()
		last.Applying = false
		if err != nil {
			last.Error = err.Error()
		}
		mu.Unlock()
	}()

	if info.asset == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		info, err = Check(ctx)
		cancel()
		if err != nil {
			return false, err
		}
	}
	if !info.Available {
		return false, errors.New("es gibt keine neuere Version")
	}
	logg.Printf("Update: lade %s", info.asset)
	return apply(info.asset, logg, quiet)
}

// download holt eine Datei nach dst.
func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Download: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(dst)
		return err
	}
	return f.Close()
}
