// Paket diag sammelt Leistungsdaten für den Diagnosemodus: CPU-Last der
// beteiligten Prozesse, Auslastung der Grafikkarten und Systemlast. Damit
// lässt sich im Log nachvollziehen, wann und warum ein PC ins Stocken kommt.
package diag

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// watched sind die Prozesse, die beim Streamen eine Rolle spielen – inklusive
// Compositor und Browser, weil die oft die eigentlichen Bremsen sind.
var watched = []string{
	"mss-share", "ffmpeg", "wf-recorder", "parec", "cloudflared", "grim",
	"hyprland", "dwm", "brave", "chrome", "chromium", "msedge", "firefox",
}

// Sampler misst die CPU-Zeit zwischen zwei Aufrufen.
type Sampler struct {
	prev   map[int]procTime
	prevAt time.Time
}

type procTime struct {
	name string
	cpu  time.Duration
}

// match ordnet einen Prozessnamen einem beobachteten Namen zu.
func match(name string) string {
	n := strings.ToLower(strings.TrimSuffix(strings.ToLower(name), ".exe"))
	for _, w := range watched {
		if strings.HasPrefix(n, w) {
			return w
		}
	}
	return ""
}

// Sample liefert eine Zeile wie "cpu mss-share=3% wf-recorder=12% … | gpu …".
func (s *Sampler) Sample() string {
	now := time.Now()
	cur := processes()
	var parts []string
	if s.prev != nil {
		dt := now.Sub(s.prevAt).Seconds()
		sum := map[string]float64{}
		for pid, p := range cur {
			if old, ok := s.prev[pid]; ok && old.name == p.name {
				sum[p.name] += (p.cpu - old.cpu).Seconds() / dt * 100
			}
		}
		names := make([]string, 0, len(sum))
		for n := range sum {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return sum[names[i]] > sum[names[j]] })
		var cpu []string
		for _, n := range names {
			cpu = append(cpu, fmt.Sprintf("%s=%.0f%%", n, sum[n]))
		}
		parts = append(parts, "cpu(% eines Kerns) "+strings.Join(cpu, " "))
	}
	s.prev, s.prevAt = cur, now
	if g := gpuBusy(); g != "" {
		parts = append(parts, "gpu "+g)
	}
	if l := systemLoad(); l != "" {
		parts = append(parts, l)
	}
	return strings.Join(parts, " | ")
}
