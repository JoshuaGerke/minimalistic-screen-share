// Paket overlay zeigt den Sprachkanal als schwebendes, halbtransparentes
// Fenster über allen anderen – wie das Overlay von Discord oder Overwolf.
// Die Daten (wer ist da, wer spricht) liefert das Panel bzw. der Viewer im
// Browser; hier wird nur gezeichnet. Unter Windows nativ, sonst übernimmt
// das Bild-in-Bild-Fenster des Browsers.
package overlay

import (
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Member ist ein Teilnehmer im Sprachkanal.
type Member struct {
	Name     string `json:"name"`
	Speaking bool   `json:"speaking"`
	Muted    bool   `json:"muted"`
	Deaf     bool   `json:"deaf"`
	Me       bool   `json:"me"`
}

// Settings steuern Aussehen und Verhalten.
type Settings struct {
	Opacity      int  `json:"opacity"`      // 20–100 %
	Scale        int  `json:"scale"`        // 60–200 %
	OnlySpeaking bool `json:"onlySpeaking"` // nur wer gerade spricht
	ClickThrough bool `json:"clickThrough"` // Klicks gehen durch, nicht verschiebbar
}

// State ist alles, was das Overlay zeigt.
type State struct {
	Members  []Member `json:"members"`
	Settings Settings `json:"overlay"`
}

// Clean begrenzt, was von außen kommt: höchstens 16 Einträge, kurze Namen.
func (s State) Clean() State {
	if len(s.Members) > 16 {
		s.Members = s.Members[:16]
	}
	for i := range s.Members {
		n := strings.TrimSpace(s.Members[i].Name)
		if r := []rune(n); len(r) > 24 {
			n = string(r[:23]) + "…"
		}
		s.Members[i].Name = n
	}
	s.Settings.Opacity = clamp(s.Settings.Opacity, 20, 100, 85)
	s.Settings.Scale = clamp(s.Settings.Scale, 60, 200, 100)
	return s
}

func clamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	return max(lo, min(hi, v))
}

var (
	faceOnce sync.Once
	faces    = map[int]font.Face{}
	faceMu   sync.Mutex
	fontData *opentype.Font
)

// face liefert die Schrift in passender Größe (Segoe UI, sonst eingebaut).
func face(size int) font.Face {
	faceOnce.Do(func() {
		if data := systemFont(); data != nil {
			fontData, _ = opentype.Parse(data)
		}
	})
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faces[size]; ok {
		return f
	}
	var f font.Face = basicfont.Face7x13
	if fontData != nil {
		if of, err := opentype.NewFace(fontData, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull}); err == nil {
			f = of
		}
	}
	faces[size] = f
	return f
}

var (
	colBG      = color.NRGBA{17, 18, 20, 230}
	colRow     = color.NRGBA{255, 255, 255, 14}
	colAvatar  = color.NRGBA{58, 60, 68, 255}
	colGreen   = color.NRGBA{35, 165, 90, 255}
	colText    = color.NRGBA{242, 243, 245, 255}
	colDim     = color.NRGBA{150, 156, 165, 255}
	colRed     = color.NRGBA{242, 63, 67, 255}
	colGlowRim = color.NRGBA{35, 165, 90, 90}
)

// Render malt das Overlay. Ist niemand zu zeigen, kommt ein kleiner Hinweis.
func Render(st State) *image.RGBA {
	sc := float64(st.Settings.Scale) / 100
	px := func(v float64) int { return int(math.Round(v * sc)) }
	members := st.Members
	if st.Settings.OnlySpeaking {
		var only []Member
		for _, m := range members {
			if m.Speaking {
				only = append(only, m)
			}
		}
		members = only
	}
	rowH, pad, w := px(36), px(8), px(230)
	rows := len(members)
	if rows == 0 {
		rows = 1
	}
	h := pad*2 + rows*rowH + (rows-1)*px(4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	roundRect(img, 0, 0, float64(w), float64(h), float64(px(12)), colBG)

	nameFace := face(px(15))
	smallFace := face(px(11))
	if len(members) == 0 {
		text := "Sprachkanal leer"
		if st.Settings.OnlySpeaking {
			text = "Gerade spricht niemand"
		}
		drawText(img, smallFace, text, pad+px(6), pad+rowH/2+px(4), colDim)
		return img
	}
	for i, m := range members {
		y := pad + i*(rowH+px(4))
		roundRect(img, float64(pad), float64(y), float64(w-pad), float64(y+rowH), float64(px(8)), colRow)
		cx, cy, r := float64(pad+px(6)+px(12)), float64(y+rowH/2), float64(px(12))
		if m.Speaking {
			circle(img, cx, cy, r+float64(px(5)), colGlowRim)
			circle(img, cx, cy, r+float64(px(3)), colGreen)
		}
		circle(img, cx, cy, r, colAvatar)
		initial := strings.ToUpper(string([]rune(m.Name + "?")[:1]))
		iw := font.MeasureString(smallFace, initial).Ceil()
		drawText(img, smallFace, initial, int(cx)-iw/2, int(cy)+px(4), colText)

		x := pad + px(6) + px(24) + px(10)
		nameCol := colText
		if m.Me {
			nameCol = colDim
		}
		flag := ""
		switch {
		case m.Deaf:
			flag = "taub"
		case m.Muted:
			flag = "stumm"
		}
		maxW := w - pad - px(8) - x
		if flag != "" {
			maxW -= font.MeasureString(smallFace, flag).Ceil() + px(8)
			fw := font.MeasureString(smallFace, flag).Ceil()
			drawText(img, smallFace, flag, w-pad-px(8)-fw, int(cy)+px(4), colRed)
		}
		drawText(img, nameFace, fit(nameFace, m.Name, maxW), x, int(cy)+px(5), nameCol)
	}
	return img
}

// fit kürzt einen Namen mit "…", bis er in maxW passt.
func fit(f font.Face, s string, maxW int) string {
	if font.MeasureString(f, s).Ceil() <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if font.MeasureString(f, string(r)+"…").Ceil() <= maxW {
			return string(r) + "…"
		}
	}
	return s
}

func drawText(img *image.RGBA, f font.Face, s string, x, y int, c color.Color) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// fill malt eine Form mit weichen Kanten (Alpha-Überblendung).
func fill(img *image.RGBA, z *vector.Rasterizer, c color.NRGBA) {
	z.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{})
}

func circle(img *image.RGBA, cx, cy, r float64, c color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	z.DrawOp = draw.Over
	const k = 0.5522847498
	z.MoveTo(float32(cx+r), float32(cy))
	z.CubeTo(float32(cx+r), float32(cy+k*r), float32(cx+k*r), float32(cy+r), float32(cx), float32(cy+r))
	z.CubeTo(float32(cx-k*r), float32(cy+r), float32(cx-r), float32(cy+k*r), float32(cx-r), float32(cy))
	z.CubeTo(float32(cx-r), float32(cy-k*r), float32(cx-k*r), float32(cy-r), float32(cx), float32(cy-r))
	z.CubeTo(float32(cx+k*r), float32(cy-r), float32(cx+r), float32(cy-k*r), float32(cx+r), float32(cy))
	z.ClosePath()
	fill(img, z, c)
}

func roundRect(img *image.RGBA, x0, y0, x1, y1, r float64, c color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	z.DrawOp = draw.Over
	const k = 0.5522847498
	z.MoveTo(float32(x0+r), float32(y0))
	z.LineTo(float32(x1-r), float32(y0))
	z.CubeTo(float32(x1-r+k*r), float32(y0), float32(x1), float32(y0+r-k*r), float32(x1), float32(y0+r))
	z.LineTo(float32(x1), float32(y1-r))
	z.CubeTo(float32(x1), float32(y1-r+k*r), float32(x1-r+k*r), float32(y1), float32(x1-r), float32(y1))
	z.LineTo(float32(x0+r), float32(y1))
	z.CubeTo(float32(x0+r-k*r), float32(y1), float32(x0), float32(y1-r+k*r), float32(x0), float32(y1-r))
	z.LineTo(float32(x0), float32(y0+r))
	z.CubeTo(float32(x0), float32(y0+r-k*r), float32(x0+r-k*r), float32(y0), float32(x0+r), float32(y0))
	z.ClosePath()
	fill(img, z, c)
}
