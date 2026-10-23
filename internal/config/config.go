// Paket config hält die Einstellungen der App in einer kleinen JSON-Datei.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
)

// Erreichbarkeit eines Raums, den dieser PC betreibt.
const (
	ReachInternet = "internet" // eingebauter Relay + Tunnel, erreichbar von überall
	ReachLAN      = "lan"      // nur im selben Netzwerk
)

// SavedRoom ist ein gemerkter Raum. Gespeichert werden die abgeleiteten
// Schlüssel, nicht das Passwort.
type SavedRoom struct {
	ID   string `json:"id"`
	Enc  string `json:"enc"`
	Auth string `json:"auth"`
}

type Config struct {
	Name  string `json:"name"`
	Reach string `json:"reach"`

	// Bildschirm teilen
	FPS       int            `json:"fps"`
	Bitrate   int            `json:"bitrate"` // kbit/s
	Audio     bool           `json:"audio"`   // Systemton mitschicken
	Target    capture.Target `json:"target"`
	MutedApps []string       `json:"muted_apps,omitempty"` // nicht mitstreamen
	Pointing  bool           `json:"pointing"`             // Zeigen per 5-fach-Klick erlauben

	Rooms []SavedRoom `json:"rooms,omitempty"`

	// Beim Start
	AutoJoin  string `json:"auto_join,omitempty"` // ID eines gemerkten Raums
	AutoShare bool   `json:"auto_share,omitempty"`
	AutoVoice bool   `json:"auto_voice,omitempty"`
	AutoMuted bool   `json:"auto_muted,omitempty"`

	Diagnose   bool `json:"diagnose,omitempty"`
	OverlayX   int  `json:"overlay_x,omitempty"`
	OverlayY   int  `json:"overlay_y,omitempty"`
	OverlaySet bool `json:"overlay_set,omitempty"`
}

// Default sind die Startwerte beim ersten Programmstart.
func Default() Config {
	host, _ := os.Hostname()
	name := strings.Split(host, ".")[0]
	if name == "" {
		name = "PC"
	}
	return Config{
		Name:     name,
		Reach:    ReachInternet,
		FPS:      60,
		Bitrate:  6000,
		Audio:    true,
		Pointing: true,
		Target:   capture.Primary(),
	}
}

// Dir ist das Konfigverzeichnis, wird bei Bedarf angelegt.
func Dir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	dir := filepath.Join(base, "minimalistic-screen-share")
	os.MkdirAll(dir, 0o700)
	return dir
}

func Path() string { return filepath.Join(Dir(), "config.json") }

// Load liest die Einstellungen; fehlende Werte kommen aus Default.
func Load() Config {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var loaded Config
	if json.Unmarshal(data, &loaded) != nil {
		return cfg
	}
	// Felder aus älteren Versionen (Zuschauer-Links) werden einfach ignoriert.
	if loaded.Name != "" {
		cfg.Name = loaded.Name
	}
	if loaded.Reach == ReachLAN {
		cfg.Reach = ReachLAN
	}
	if loaded.FPS > 0 {
		cfg.FPS = loaded.FPS
	}
	if loaded.Bitrate > 0 {
		cfg.Bitrate = loaded.Bitrate
	}
	if loaded.Target.Kind != "" {
		cfg.Target = loaded.Target
	}
	var probe map[string]json.RawMessage
	json.Unmarshal(data, &probe)
	if _, ok := probe["audio"]; ok {
		cfg.Audio = loaded.Audio
	}
	if _, ok := probe["pointing"]; ok {
		cfg.Pointing = loaded.Pointing
	}
	cfg.MutedApps = loaded.MutedApps
	cfg.Rooms = loaded.Rooms
	cfg.AutoJoin, cfg.AutoShare, cfg.AutoVoice, cfg.AutoMuted = loaded.AutoJoin, loaded.AutoShare, loaded.AutoVoice, loaded.AutoMuted
	cfg.Diagnose = loaded.Diagnose
	cfg.OverlayX, cfg.OverlayY, cfg.OverlaySet = loaded.OverlayX, loaded.OverlayY, loaded.OverlaySet
	return cfg
}

// Save schreibt die Einstellungen – nur für den eigenen Benutzer lesbar,
// denn darin stehen die Schlüssel gemerkter Räume.
func (c Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o600)
}

// Validate prüft die Werte, bevor gespeichert wird.
func (c *Config) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("Name fehlt")
	}
	if r := []rune(c.Name); len(r) > 32 {
		c.Name = string(r[:32])
	}
	if c.Reach != ReachLAN {
		c.Reach = ReachInternet
	}
	if c.FPS < 5 || c.FPS > 120 {
		return fmt.Errorf("Bilder pro Sekunde: 5 bis 120")
	}
	if c.Bitrate < 500 || c.Bitrate > 50000 {
		return fmt.Errorf("Datenrate: 500 bis 50000 kbit/s")
	}
	return nil
}

// Room sucht einen gemerkten Raum.
func (c Config) Room(id string) (SavedRoom, bool) {
	for _, r := range c.Rooms {
		if r.ID == id {
			return r, true
		}
	}
	return SavedRoom{}, false
}
