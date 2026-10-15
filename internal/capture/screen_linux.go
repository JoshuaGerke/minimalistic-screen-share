//go:build linux

package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/kbinani/screenshot"
	"golang.org/x/image/draw"
)

func open(t Target) (Source, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return openWayland(t)
	}
	return openX11(t)
}

/* ---------- Wayland (grim, Fensterliste über hyprctl) ---------- */

type grimSource struct {
	mu     sync.Mutex
	target Target
	name   string
	region string
	scaled bool
	scale  float64
}

func openWayland(t Target) (Source, error) {
	if _, err := exec.LookPath("grim"); err != nil {
		return nil, errors.New("grim fehlt – bitte installieren (pacman -S grim)")
	}
	s := &grimSource{target: t}
	switch t.Kind {
	case "screen":
		s.name = "Bildschirm"
	case "region":
		if t.Region.W <= 0 || t.Region.H <= 0 {
			return nil, errors.New("leerer Bildbereich")
		}
		s.region = regionArg(t.Region.X, t.Region.Y, t.Region.W, t.Region.H)
		s.name = "Bereich"
	case "monitor":
		mons, err := hyprMonitors()
		if err != nil || len(mons) == 0 {
			return nil, errors.New("Monitorliste nicht verfügbar (läuft Hyprland?)")
		}
		if t.Monitor < 0 || t.Monitor >= len(mons) {
			return nil, fmt.Errorf("Monitor %d gibt es nicht", t.Monitor+1)
		}
		m := mons[t.Monitor]
		s.region = regionArg(m.X, m.Y, m.W, m.H)
		s.name = "Monitor " + m.Name
	case "window":
		clients, err := hyprClients()
		if err != nil {
			return nil, errors.New("Fensterliste nicht verfügbar (läuft Hyprland?)")
		}
		c, ok := findHyprClient(clients, t.Window)
		if !ok {
			return nil, fmt.Errorf("Fenster %q nicht gefunden", t.Window)
		}
		s.region = regionArg(c.X, c.Y, c.W, c.H)
		s.name = c.Title
	}
	return s, nil
}

func regionArg(x, y, w, h int) string {
	return fmt.Sprintf("%d,%d %dx%d", x, y, w, h)
}

func (s *grimSource) Name() string { return s.name }
func (s *grimSource) Close() error { return nil }

func (s *grimSource) GrabJPEG(quality, maxWidth int) ([]byte, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	args := []string{"-t", "jpeg", "-q", strconv.Itoa(quality)}
	if s.region != "" {
		args = append(args, "-g", s.region)
	}
	if maxWidth > 0 && s.scaled {
		args = append(args, "-s", strconv.FormatFloat(s.scale, 'f', 4, 64))
	}
	args = append(args, "-")

	cmd := exec.Command("grim", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("grim: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("grim-Ausgabe unlesbar: %v", err)
	}
	if maxWidth > 0 && !s.scaled {
		// Beim ersten Frame die Größe messen, danach skaliert grim selbst.
		s.scaled = true
		if cfg.Width > maxWidth {
			s.scale = float64(maxWidth) / float64(cfg.Width)
			img, err := jpeg.Decode(bytes.NewReader(out))
			if err == nil {
				small := Scale(toRGBA(img), maxWidth)
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: quality}); err == nil {
					return buf.Bytes(), small.Bounds().Dx(), small.Bounds().Dy(), nil
				}
			}
		}
	}
	return out, cfg.Width, cfg.Height, nil
}

func (s *grimSource) Grab() (*image.RGBA, error) {
	data, _, _, err := s.GrabJPEG(80, 0)
	if err != nil {
		return nil, err
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return toRGBA(img), nil
}

type hyprMonitor struct {
	Name       string
	X, Y, W, H int
	Focused    bool
}

type hyprClient struct {
	Title, Class string
	X, Y, W, H   int
}

func hyprMonitors() ([]hyprMonitor, error) {
	out, err := exec.Command("hyprctl", "monitors", "-j").Output()
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name                string `json:"name"`
		X, Y, Width, Height int
		Focused             bool `json:"focused"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	mons := make([]hyprMonitor, len(raw))
	for i, m := range raw {
		mons[i] = hyprMonitor{Name: m.Name, X: m.X, Y: m.Y, W: m.Width, H: m.Height, Focused: m.Focused}
	}
	return mons, nil
}

func hyprClients() ([]hyprClient, error) {
	out, err := exec.Command("hyprctl", "clients", "-j").Output()
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Title string `json:"title"`
		Class string `json:"class"`
		At    [2]int `json:"at"`
		Size  [2]int `json:"size"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	clients := make([]hyprClient, 0, len(raw))
	for _, c := range raw {
		clients = append(clients, hyprClient{
			Title: c.Title,
			Class: c.Class,
			X:     c.At[0],
			Y:     c.At[1],
			W:     c.Size[0],
			H:     c.Size[1],
		})
	}
	return clients, nil
}

func findHyprClient(clients []hyprClient, title string) (hyprClient, bool) {
	for _, c := range clients {
		if c.Title == title || c.Class == title {
			return c, true
		}
	}
	lower := strings.ToLower(title)
	for _, c := range clients {
		if strings.Contains(strings.ToLower(c.Title), lower) {
			return c, true
		}
	}
	return hyprClient{}, false
}

/* ---------- X11 ---------- */

type x11Source struct {
	target Target
	name   string
	area   image.Rectangle
}

func openX11(t Target) (Source, error) {
	if screenshot.NumActiveDisplays() == 0 {
		return nil, errors.New("kein X-Display gefunden")
	}
	s := &x11Source{target: t}
	switch t.Kind {
	case "screen":
		n := screenshot.NumActiveDisplays()
		u := screenshot.GetDisplayBounds(0)
		for i := 1; i < n; i++ {
			u = u.Union(screenshot.GetDisplayBounds(i))
		}
		s.area = u
		s.name = "Bildschirm"
	case "monitor":
		if t.Monitor < 0 || t.Monitor >= screenshot.NumActiveDisplays() {
			return nil, fmt.Errorf("Monitor %d gibt es nicht", t.Monitor+1)
		}
		s.area = screenshot.GetDisplayBounds(t.Monitor)
		s.name = t.Label()
	case "region":
		if t.Region.W <= 0 || t.Region.H <= 0 {
			return nil, errors.New("leerer Bildbereich")
		}
		s.area = image.Rect(t.Region.X, t.Region.Y, t.Region.X+t.Region.W, t.Region.Y+t.Region.H)
		s.name = "Bereich"
	case "window":
		return nil, errors.New("Fensteraufnahme gibt es nur unter Wayland mit Hyprland")
	}
	return s, nil
}

func (s *x11Source) Name() string { return s.name }
func (s *x11Source) Close() error { return nil }

func (s *x11Source) Grab() (*image.RGBA, error) {
	return screenshot.CaptureRect(s.area)
}

/* ---------- Listen für Menü und Panel ---------- */

func ListMonitors() ([]Monitor, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		mons, err := hyprMonitors()
		if err != nil {
			return nil, nil
		}
		out := make([]Monitor, len(mons))
		for i, m := range mons {
			out[i] = Monitor{Index: i, X: m.X, Y: m.Y, W: m.W, H: m.H}
		}
		return out, nil
	}
	n := screenshot.NumActiveDisplays()
	out := make([]Monitor, 0, n)
	for i := 0; i < n; i++ {
		b := screenshot.GetDisplayBounds(i)
		out = append(out, Monitor{Index: i, X: b.Min.X, Y: b.Min.Y, W: b.Dx(), H: b.Dy()})
	}
	return out, nil
}

func ListWindows() ([]Window, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, nil
	}
	clients, err := hyprClients()
	if err != nil {
		return nil, nil
	}
	out := make([]Window, 0, len(clients))
	for _, c := range clients {
		if c.W < 120 || c.H < 80 {
			continue
		}
		title := c.Title
		if title == "" {
			title = c.Class
		}
		if title == "" {
			continue
		}
		out = append(out, Window{Title: title, W: c.W, H: c.H})
	}
	return out, nil
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// HyprMonitorName liefert den Hyprland-Namen (z. B. "DP-1") zum Monitorindex –
// den braucht wf-recorder für die Video-Aufnahme.
func HyprMonitorName(index int) (string, error) {
	mons, err := hyprMonitors()
	if err != nil {
		return "", err
	}
	if index < 0 || index >= len(mons) {
		return "", fmt.Errorf("Monitor %d gibt es nicht", index+1)
	}
	return mons[index].Name, nil
}
