package audio

import (
	"strings"
	"sync"
)

// Session ist eine Tonquelle – eine App, die gerade Ton spielt.
type Session struct {
	Name  string `json:"name"`  // stabiler Schlüssel (Programmname, klein geschrieben)
	Label string `json:"label"` // Anzeigename
	Muted bool   `json:"muted"` // gerade stumm
}

// muMuted schützt die Liste der Programme, die wir selbst stumm geschaltet
// haben – damit wir beim Beenden genau die wieder laut machen.
var (
	muMuted   sync.Mutex
	mutedByUs = map[string]bool{}
)

// ListSessions zählt die aktuellen Tonquellen auf.
func ListSessions() ([]Session, error) { return listSessions() }

// SetMuted schaltet ein einzelnes Programm sofort stumm oder laut.
func SetMuted(name string, muted bool) error {
	return setMutedByName(strings.ToLower(strings.TrimSpace(name)), muted)
}

// MuteApps schaltet genau die genannten Programme stumm und macht alles
// wieder laut, was wir vorher stumm gemacht haben, aber nicht mehr in der
// Liste steht. Programme, die der Nutzer selbst stumm gestellt hat, bleiben
// unangetastet.
func MuteApps(names []string) error {
	want := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" {
			want[n] = true
		}
	}

	muMuted.Lock()
	var toUnmute []string
	for n := range mutedByUs {
		if !want[n] {
			toUnmute = append(toUnmute, n)
		}
	}
	var toMute []string
	for n := range want {
		if !mutedByUs[n] {
			toMute = append(toMute, n)
		}
	}
	muMuted.Unlock()

	var err error
	for _, n := range toUnmute {
		if e := setMutedByName(n, false); e == nil {
			muMuted.Lock()
			delete(mutedByUs, n)
			muMuted.Unlock()
		}
	}
	for _, n := range toMute {
		if e := setMutedByName(n, true); e == nil {
			muMuted.Lock()
			mutedByUs[n] = true
			muMuted.Unlock()
		} else {
			err = e
		}
	}
	return err
}

// RestoreAll macht alle von uns stumm geschalteten Programme wieder laut.
func RestoreAll() {
	muMuted.Lock()
	names := make([]string, 0, len(mutedByUs))
	for n := range mutedByUs {
		names = append(names, n)
	}
	mutedByUs = map[string]bool{}
	muMuted.Unlock()
	for _, n := range names {
		setMutedByName(n, false)
	}
}
