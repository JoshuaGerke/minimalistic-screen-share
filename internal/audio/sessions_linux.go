//go:build linux

package audio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type pactlEntry struct {
	Index      int  `json:"index"`
	Mute       bool `json:"mute"`
	Properties struct {
		AppName string `json:"application.name"`
		Binary  string `json:"application.process.binary"`
	} `json:"properties"`
}

func (e pactlEntry) key() string {
	name := e.Properties.Binary
	if name == "" {
		name = e.Properties.AppName
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// listSessions liest die Tonquellen über pactl (PulseAudio/PipeWire).
func listSessions() ([]Session, error) {
	entries, err := pactlSinkInputs()
	if err != nil {
		return nil, err
	}
	type acc struct {
		label string
		muted bool
	}
	found := map[string]*acc{}
	var order []string
	for _, e := range entries {
		key := e.key()
		if key == "" {
			continue
		}
		entry, ok := found[key]
		if !ok {
			label := e.Properties.AppName
			if label == "" {
				label = key
			}
			entry = &acc{label: label}
			found[key] = entry
			order = append(order, key)
		}
		if e.Mute {
			entry.muted = true
		}
	}
	out := make([]Session, 0, len(order))
	for _, key := range order {
		e := found[key]
		out = append(out, Session{Name: key, Label: e.label, Muted: e.muted})
	}
	return out, nil
}

// setMutedByName schaltet alle Tonquellen dieses Programms um.
func setMutedByName(name string, muted bool) error {
	target := strings.ToLower(strings.TrimSpace(name))
	entries, err := pactlSinkInputs()
	if err != nil {
		return err
	}
	found := false
	value := "0"
	if muted {
		value = "1"
	}
	for _, e := range entries {
		if e.key() != target {
			continue
		}
		if err := exec.Command("pactl", "set-sink-input-mute", fmt.Sprint(e.Index), value).Run(); err == nil {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("keine laufende Tonquelle für %q", name)
	}
	return nil
}

func pactlSinkInputs() ([]pactlEntry, error) {
	if _, err := exec.LookPath("pactl"); err != nil {
		return nil, errors.New("pactl fehlt – bitte libpulse installieren")
	}
	out, err := exec.Command("pactl", "-f", "json", "list", "sink-inputs").Output()
	if err != nil {
		return nil, fmt.Errorf("pactl: %w", err)
	}
	var entries []pactlEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("pactl-Ausgabe unlesbar: %w", err)
	}
	return entries, nil
}
