// Paket rendezvous macht eine Sitzungs-ID dauerhaft auffindbar – wie die
// Nummer bei AnyDesk. Die Tunnel-Adresse ändert sich bei jedem Neustart der
// App; darum veröffentlicht die App ihre aktuelle Adresse über ntfy.sh (ein
// kostenloser Benachrichtigungsdienst ohne Konto) unter einem Kanal, der sich
// aus der Sitzungs-ID ableitet. Wer die ID kennt, findet so den Link.
//
// Gespeichert wird dort nur die Adresse. Wer die ID kennt, kann damit
// anklopfen – reinlassen muss trotzdem der Streamer.
package rendezvous

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const service = "https://ntfy.sh/"

// Topic leitet den ntfy-Kanal aus der Sitzungs-ID ab (gleich im Browser).
func Topic(room string) string {
	sum := sha256.Sum256([]byte("mss-share:" + strings.ToLower(strings.TrimSpace(room))))
	return "mss-" + hex.EncodeToString(sum[:])[:24]
}

type entry struct {
	URL string `json:"url"`
}

// Publish veröffentlicht die aktuelle Adresse und liefert den Zeitpunkt,
// den ntfy dafür vergeben hat.
func Publish(ctx context.Context, room, url string) (int64, error) {
	body, _ := json.Marshal(entry{URL: url})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, service+Topic(room), strings.NewReader(string(body)))
	req.Header.Set("Cache", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("ntfy antwortet mit %d", resp.StatusCode)
	}
	var ev struct {
		Time int64 `json:"time"`
	}
	json.NewDecoder(resp.Body).Decode(&ev)
	return ev.Time, nil
}

// Resolve sucht die zuletzt veröffentlichte Adresse einer Sitzung.
func Resolve(ctx context.Context, room string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, service+Topic(room)+"/json?poll=1&since=12h", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var last string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev struct {
			Event   string `json:"event"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event != "message" {
			continue
		}
		var e entry
		if json.Unmarshal([]byte(ev.Message), &e) == nil && strings.HasPrefix(e.URL, "https://") {
			last = e.URL
		}
	}
	if last == "" {
		return "", errors.New("Sitzung nicht gefunden – läuft die App beim anderen?")
	}
	return last, nil
}

// Keep veröffentlicht die Adresse jetzt und alle 6 Stunden erneut (ntfy
// hält Nachrichten 12 Stunden). current liefert die aktuelle Adresse oder "".
func Keep(stop <-chan struct{}, room func() string, current func() string, logf func(string, ...any)) {
	last := ""
	lastAt := time.Time{}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		r, u := room(), current()
		if u != "" && r != "" && (r+u != last || time.Since(lastAt) > 6*time.Hour) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, err := Publish(ctx, r, u)
			cancel()
			if err != nil {
				logf("Sitzungs-ID veröffentlichen: %v", err)
			} else {
				last, lastAt = r+u, time.Now()
				logf("Sitzungs-ID %s ist auffindbar", r)
			}
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

// Entry ist eine veröffentlichte Adresse samt Zeitpunkt.
type Entry struct {
	URL  string
	Time int64
}

// Entries liefert alle Adressen der letzten 12 Stunden, älteste zuerst.
// Läuft ein Raum doppelt (zwei haben gleichzeitig übernommen), gewinnt der
// früheste, der noch erreichbar ist.
func Entries(ctx context.Context, room string) ([]Entry, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, service+Topic(room)+"/json?poll=1&since=12h", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Entry
	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev struct {
			Event   string `json:"event"`
			Time    int64  `json:"time"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event != "message" {
			continue
		}
		var e entry
		if json.Unmarshal([]byte(ev.Message), &e) != nil || !strings.HasPrefix(e.URL, "http") || seen[e.URL] {
			continue
		}
		seen[e.URL] = true
		out = append(out, Entry{URL: e.URL, Time: ev.Time})
	}
	return out, nil
}
