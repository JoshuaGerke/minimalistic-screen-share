// Paket rendezvous macht einen Raum auffindbar. Wer ihn betreibt, hat eine
// wechselnde Tunnel-Adresse; darum veröffentlicht die App ihre Adressen über
// ntfy.sh (ein kostenloser Benachrichtigungsdienst ohne Konto) unter einem
// Kanal, der sich aus der Raum-ID ableitet.
//
// Dort stehen nur Adressen. Hinein kommt nur, wer das Passwort kennt, und
// lesen kann den Raum ohnehin nur, wer es kennt (Ende-zu-Ende).
package rendezvous

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const service = "https://ntfy.sh/"

// Topic leitet den ntfy-Kanal aus der Raum-ID ab.
func Topic(room string) string {
	sum := sha256.Sum256([]byte("mss-share:" + strings.ToLower(strings.TrimSpace(room))))
	return "mss-" + hex.EncodeToString(sum[:])[:24]
}

// Entry ist ein veröffentlichter Betreiber samt Zeitpunkt.
type Entry struct {
	URL  string `json:"url,omitempty"`  // über das Internet (Tunnel)
	LAN  string `json:"lan,omitempty"`  // direkt im selben Netzwerk
	Host string `json:"host,omitempty"` // Kennung des Programmstarts, der betreibt
	Time int64  `json:"-"`
}

// Publish veröffentlicht die Adressen und liefert den Zeitpunkt, den ntfy
// dafür vergeben hat.
func Publish(ctx context.Context, room string, e Entry) (int64, error) {
	body, _ := json.Marshal(e)
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

// Entries liefert alle Einträge der letzten 12 Stunden, älteste zuerst.
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
		var e Entry
		if json.Unmarshal([]byte(ev.Message), &e) != nil {
			continue
		}
		key := e.URL + "|" + e.LAN + "|" + e.Host
		if seen[key] || (!strings.HasPrefix(e.URL, "http") && !strings.HasPrefix(e.LAN, "http")) {
			continue
		}
		seen[key] = true
		e.Time = ev.Time
		out = append(out, e)
	}
	return out, nil
}
