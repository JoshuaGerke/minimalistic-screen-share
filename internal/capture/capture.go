// Paket capture holt Bilder vom Bildschirm, von einem Monitor, einem Fenster
// oder einem Bereich. Die Plattform-Implementierungen stecken in den
// screen_*_ Dateien.
package capture

import (
	"fmt"
	"image"

	"golang.org/x/image/draw"
)

// Target beschreibt, was aufgenommen werden soll.
type Target struct {
	Kind    string `json:"kind"` // screen | monitor | window | region | test
	Monitor int    `json:"monitor,omitempty"`
	Window  string `json:"window,omitempty"`
	Region  Rect   `json:"region,omitempty"`
	Cursor  bool   `json:"cursor,omitempty"`
}

type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Source liefert einzelne Bilder.
type Source interface {
	Grab() (*image.RGBA, error)
	Name() string
	Close() error
}

// JPEGSource liefert fertig kodierte Frames. grim auf Wayland kann das,
// dann spart man sich das doppelte Kodieren.
type JPEGSource interface {
	GrabJPEG(quality, maxWidth int) (jpeg []byte, w, h int, err error)
}

// Window ist ein Eintrag für die Fensterauswahl.
type Window struct {
	Title string `json:"title"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

// Monitor ist ein Eintrag für die Monitorauswahl.
type Monitor struct {
	Index int `json:"index"`
	X     int `json:"x"`
	Y     int `json:"y"`
	W     int `json:"w"`
	H     int `json:"h"`
}

// Open öffnet die passende Quelle. "test" liefert ein bewegtes Testbild.
func Open(t Target) (Source, error) {
	if t.Kind == "test" {
		return &testSource{}, nil
	}
	return open(t)
}

// Label ist der Name, der in Menü und Oberfläche auftaucht.
func (t Target) Label() string {
	switch t.Kind {
	case "screen":
		return "Bildschirm"
	case "monitor":
		return fmt.Sprintf("Monitor %d", t.Monitor+1)
	case "window":
		return "Fenster: " + t.Window
	case "region":
		return "Bereich"
	case "test":
		return "Testbild"
	}
	return t.Kind
}

// Primary liefert das Hauptziel: den fokussierten Monitor (Hyprland), sonst
// den Monitor oben links (unter Windows und X11 der primäre).
func Primary() Target {
	if t, ok := primaryFocused(); ok {
		return t
	}
	monitors, err := ListMonitors()
	if err == nil && len(monitors) > 0 {
		for _, m := range monitors {
			if m.X == 0 && m.Y == 0 {
				return Target{Kind: "monitor", Monitor: m.Index, Cursor: true}
			}
		}
		return Target{Kind: "monitor", Monitor: monitors[0].Index, Cursor: true}
	}
	return Target{Kind: "screen", Cursor: true}
}

// Scale verkleinert ein Bild auf höchstens maxWidth Pixel Breite.
// maxWidth <= 0 lässt das Bild unangetastet.
func Scale(src *image.RGBA, maxWidth int) *image.RGBA {
	if maxWidth <= 0 || src.Bounds().Dx() <= maxWidth {
		return src
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw := maxWidth
	dh := sh * maxWidth / sw
	if dh < 2 {
		dh = 2
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// testSource malt ein bewegtes Muster, damit man den Stream ohne
// Bildschirmzugriff prüfen kann.
type testSource struct {
	frame int
}

func (s *testSource) Name() string { return "Testbild" }
func (s *testSource) Close() error { return nil }

func (s *testSource) Grab() (*image.RGBA, error) {
	const w, h = 640, 360
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	s.frame++
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(x/2 + s.frame*3)
			img.Pix[i+1] = uint8(y/2 + s.frame*2)
			img.Pix[i+2] = uint8(90 + s.frame)
			img.Pix[i+3] = 0xFF
		}
	}
	// laufender Block, macht Standbilder sofort erkennbar
	bx := (s.frame * 7) % (w - 80)
	by := (s.frame * 3) % (h - 80)
	for y := by; y < by+80; y++ {
		for x := bx; x < bx+80; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0x58, 0x65, 0xF2
		}
	}
	return img, nil
}
