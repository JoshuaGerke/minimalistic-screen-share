// Paket config hält die Einstellungen des Clients in einer kleinen JSON-Datei.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
)

type Config struct {
	Server     string         `json:"server"`
	PublicURL  string         `json:"public_url,omitempty"`
	Room       string         `json:"room"`
	Name       string         `json:"name"`
	FPS        int            `json:"fps"`
	Quality    int            `json:"quality"`
	MaxWidth   int            `json:"max_width"`
	Audio      bool           `json:"audio"`
	AutoAccept bool           `json:"auto_accept"`
	RunRelay   bool           `json:"run_relay"`
	Target     capture.Target `json:"target"`
}

var roomPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Default sind die Startwerte beim ersten Programmstart.
func Default() Config {
	host, _ := os.Hostname()
	name := strings.Split(host, ".")[0]
	if name == "" {
		name = "PC"
	}
	return Config{
		Server:   "http://localhost:8080",
		Room:     DefaultSessionID(),
		Name:     name,
		FPS:      15,
		Quality:  70,
		MaxWidth: 1600,
		Audio:    true,
		RunRelay: true,
		Target:   capture.Primary(),
	}
}

// DefaultSessionID baut aus dem Rechnernamen eine stabile Sitzungs-ID mit
// kurzem Anhängsel, damit zwei gleiche Rechnernamen nicht kollidieren.
// 1 PC = 1 ID, änderbar im Panel.
func DefaultSessionID() string {
	host, _ := os.Hostname()
	slug := slugify(strings.Split(host, ".")[0])
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
	}
	if slug == "" {
		slug = "pc"
	}
	return slug + "-" + randSuffix(4)
}

func slugify(s string) string {
	s = strings.ToLower(s)
	replacer := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	s = replacer.Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}

func randSuffix(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// Dir ist das Konfigverzeichnis, wird bei Bedarf angelegt.
func Dir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	dir := filepath.Join(base, "minimalistic-screen-share")
	os.MkdirAll(dir, 0o755)
	return dir
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

// Load liest die Einstellungen und füllt fehlende Werte mit Defaults.
func Load() Config {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}
	// Windows-Editoren (Notepad, PowerShell) hängen gern ein BOM davor.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		return cfg
	}
	if loaded.Server != "" {
		cfg.Server = loaded.Server
	}
	if loaded.Room != "" {
		cfg.Room = loaded.Room
	}
	if loaded.Name != "" {
		cfg.Name = loaded.Name
	}
	if loaded.FPS > 0 {
		cfg.FPS = loaded.FPS
	}
	if loaded.Quality > 0 {
		cfg.Quality = loaded.Quality
	}
	cfg.MaxWidth = loaded.MaxWidth
	cfg.Audio = loaded.Audio
	cfg.AutoAccept = loaded.AutoAccept
	cfg.PublicURL = loaded.PublicURL
	if loaded.Target.Kind != "" {
		cfg.Target = loaded.Target
	}
	// Fehlt der Schlüssel (Config aus einer älteren Version), bleibt der
	// eingebaute Relay angeschaltet.
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) == nil {
		if _, ok := probe["run_relay"]; ok {
			cfg.RunRelay = loaded.RunRelay
		}
	}
	cfg.normalize()
	return cfg
}

func (c Config) Save() error {
	c.normalize()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o644)
}

// Validate prüft die wichtigsten Werte, bevor gespeichert oder gestreamt wird.
func (c *Config) Validate() error {
	c.normalize()
	if strings.TrimSpace(c.Server) == "" {
		return fmt.Errorf("Server-Adresse fehlt")
	}
	if !roomPattern.MatchString(c.Room) {
		return fmt.Errorf("Raumcode muss 2–32 Zeichen haben: a–z, 0–9 und -")
	}
	if c.FPS < 1 || c.FPS > 30 {
		return fmt.Errorf("FPS muss zwischen 1 und 30 liegen")
	}
	if c.Quality < 20 || c.Quality > 95 {
		return fmt.Errorf("Qualität muss zwischen 20 und 95 liegen")
	}
	if c.MaxWidth < 0 || c.MaxWidth > 3840 {
		return fmt.Errorf("maximale Breite muss zwischen 0 und 3840 liegen")
	}
	switch c.Target.Kind {
	case "screen", "monitor", "window", "region", "test":
	default:
		return fmt.Errorf("unbekanntes Ziel: %q", c.Target.Kind)
	}
	return nil
}

func (c *Config) normalize() {
	c.Room = strings.ToLower(strings.TrimSpace(c.Room))
	c.Server = strings.TrimRight(strings.TrimSpace(c.Server), "/")
	if c.Name == "" {
		c.Name = "PC"
	}
}
