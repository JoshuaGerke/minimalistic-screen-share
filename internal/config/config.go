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

// Erreichbarkeit: Wie Zuschauer zum Stream kommen.
const (
	ReachInternet = "internet" // eingebauter Relay + Tunnel, Link funktioniert überall
	ReachLAN      = "lan"      // eingebauter Relay, nur im selben Netz
	ReachServer   = "server"   // eigener Relay, z. B. auf einem VPS
)

type Config struct {
	Reach      string         `json:"reach"`
	Server     string         `json:"server"`
	Room       string         `json:"room"`
	Name       string         `json:"name"`
	FPS        int            `json:"fps"`
	Quality    int            `json:"quality"`
	MaxWidth   int            `json:"max_width"`
	Audio      bool           `json:"audio"`
	AutoAccept bool           `json:"auto_accept"`
	MutedApps  []string       `json:"muted_apps,omitempty"`
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
		Reach:    ReachInternet,
		Server:   "",
		Room:     DefaultSessionID(),
		Name:     name,
		FPS:      15,
		Quality:  70,
		MaxWidth: 1600,
		Audio:    true,
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
	cfg.MutedApps = loaded.MutedApps
	if loaded.Target.Kind != "" {
		cfg.Target = loaded.Target
	}
	cfg.Reach = loaded.Reach
	if cfg.Reach == "" {
		// Config aus einer Version vor den Modi: Zeigte der Server woanders
		// hin, war das ein eigener Relay – sonst gleich Internet-Modus.
		cfg.Reach = ReachInternet
		if !IsLocalServer(cfg.Server) {
			cfg.Reach = ReachServer
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
	switch c.Reach {
	case ReachInternet, ReachLAN:
	case ReachServer:
		if c.Server == "" || IsLocalServer(c.Server) {
			return fmt.Errorf("Adresse des eigenen Servers fehlt")
		}
	default:
		return fmt.Errorf("unbekannte Erreichbarkeit: %q", c.Reach)
	}
	if !roomPattern.MatchString(c.Room) {
		return fmt.Errorf("Raumcode muss 2–32 Zeichen haben: a–z, 0–9 und -")
	}
	if c.FPS < 1 || c.FPS > 60 {
		return fmt.Errorf("FPS muss zwischen 1 und 60 liegen")
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

// IsLocalServer erkennt Adressen, die auf diesen Rechner zeigen.
func IsLocalServer(server string) bool {
	s := strings.ToLower(strings.TrimSpace(server))
	if s == "" {
		return true
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	host := s
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" || host == ""
}

func (c *Config) normalize() {
	c.Room = strings.ToLower(strings.TrimSpace(c.Room))
	c.Server = strings.TrimRight(strings.TrimSpace(c.Server), "/")
	if c.Name == "" {
		c.Name = "PC"
	}
}
