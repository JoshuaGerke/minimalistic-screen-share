// Paket icon malt das Tray-Symbol zur Laufzeit – so muss keine Bilddatei
// ins Repo und der Zustand (live, pausiert, Fehler) lässt sich direkt einfärben.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
)

type point struct{ x, y float64 }

var (
	colBG  = color.RGBA{0x2b, 0x2d, 0x31, 0xFF}
	colFG  = color.RGBA{0xf2, 0xf3, 0xf5, 0xFF}
	colDot = map[string]color.RGBA{
		"idle":   {0x94, 0x9b, 0xa4, 0xFF},
		"live":   {0x23, 0xa5, 0x5a, 0xFF},
		"paused": {0xf0, 0xb2, 0x32, 0xFF},
		"error":  {0xf2, 0x3f, 0x43, 0xFF},
	}
)

// ICO liefert ein Windows-Icon in den üblichen Größen.
func ICO(state string) []byte {
	return encodeICO([]int{16, 32, 48}, state)
}

// Pixmap liefert RGBA-Pixel für StatusNotifier-Einträge (Linux).
func Pixmap(state string, size int) *image.RGBA {
	return render(size, state)
}

func render(size int, state string) *image.RGBA {
	const ss = 4
	s := size * ss
	f := float64(s)
	img := image.NewRGBA(image.Rect(0, 0, s, s))

	fillRoundRect(img, 0.03*f, 0.03*f, 0.97*f, 0.97*f, 0.24*f, colBG)
	strokeRoundRect(img, 0.22*f, 0.24*f, 0.78*f, 0.62*f, 0.06*f, 0.07*f, colFG)
	fillTriangle(img,
		point{0.42 * f, 0.32 * f},
		point{0.42 * f, 0.54 * f},
		point{0.62 * f, 0.43 * f},
		colFG)
	fillRect(img, 0.44*f, 0.62*f, 0.56*f, 0.69*f, colFG)

	dot := colDot[state]
	if dot.A == 0 {
		dot = colDot["idle"]
	}
	fillCircle(img, 0.74*f, 0.74*f, 0.21*f, colBG)
	fillCircle(img, 0.74*f, 0.74*f, 0.155*f, dot)

	return downsample(img, size)
}

func insideRoundRect(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx := math.Min(math.Max(px, x0+r), x1-r)
	cy := math.Min(math.Max(py, y0+r), y1-r)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

func fillRoundRect(img *image.RGBA, x0, y0, x1, y1, r float64, c color.RGBA) {
	for y := int(y0); y <= int(y1); y++ {
		if y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		for x := int(x0); x <= int(x1); x++ {
			if x < 0 || x >= img.Bounds().Dx() {
				continue
			}
			if insideRoundRect(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, r) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func strokeRoundRect(img *image.RGBA, x0, y0, x1, y1, r, w float64, c color.RGBA) {
	for y := int(y0); y <= int(y1); y++ {
		for x := int(x0); x <= int(x1); x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			outer := insideRoundRect(px, py, x0, y0, x1, y1, r)
			inner := insideRoundRect(px, py, x0+w, y0+w, x1-w, y1-w, math.Max(0.5, r-w))
			if outer && !inner {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 float64, c color.RGBA) {
	fillRoundRect(img, x0, y0, x1, y1, 0, c)
}

func fillCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	for y := int(cy - r); y <= int(cy+r); y++ {
		for x := int(cx - r); x <= int(cx+r); x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func fillTriangle(img *image.RGBA, a, b, c point, col color.RGBA) {
	minX := int(math.Min(a.x, math.Min(b.x, c.x)))
	maxX := int(math.Max(a.x, math.Max(b.x, c.x)))
	minY := int(math.Min(a.y, math.Min(b.y, c.y)))
	maxY := int(math.Max(a.y, math.Max(b.y, c.y)))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			d1 := edge(px, py, a, b)
			d2 := edge(px, py, b, c)
			d3 := edge(px, py, c, a)
			neg := d1 < 0 || d2 < 0 || d3 < 0
			pos := d1 > 0 || d2 > 0 || d3 > 0
			if !(neg && pos) {
				img.SetRGBA(x, y, col)
			}
		}
	}
}

func edge(px, py float64, a, b point) float64 {
	return (px-b.x)*(a.y-b.y) - (a.x-b.x)*(py-b.y)
}

// downsample mittelt die Supersampling-Blöcke und beachtet dabei Alpha,
// damit die Ränder sauber auslaufen.
func downsample(src *image.RGBA, size int) *image.RGBA {
	const ss = 4
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sr, sg, sb, sa uint64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := src.RGBAAt(x*ss+dx, y*ss+dy)
					a := uint64(c.A)
					sa += a
					sr += uint64(c.R) * a
					sg += uint64(c.G) * a
					sb += uint64(c.B) * a
				}
			}
			if sa == 0 {
				dst.SetRGBA(x, y, color.RGBA{})
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(sr / sa),
				G: uint8(sg / sa),
				B: uint8(sb / sa),
				A: uint8(sa / (ss * ss)),
			})
		}
	}
	return dst
}

/* ---------- ICO-Kodierung ---------- */

func encodeICO(sizes []int, state string) []byte {
	blobs := make([][]byte, len(sizes))
	offset := uint32(6 + 16*len(sizes))
	type entry struct {
		w, h        byte
		planes, bpp uint16
		size, off   uint32
	}
	entries := make([]entry, len(sizes))
	for i, size := range sizes {
		blob := encodeBMP(render(size, state))
		blobs[i] = blob
		w := byte(size)
		if size >= 256 {
			w = 0
		}
		entries[i] = entry{w: w, h: w, planes: 1, bpp: 32, size: uint32(len(blob)), off: offset}
		offset += uint32(len(blob))
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(sizes)))
	for _, e := range entries {
		out.Write([]byte{e.w, e.h, 0, 0})
		binary.Write(&out, binary.LittleEndian, e.planes)
		binary.Write(&out, binary.LittleEndian, e.bpp)
		binary.Write(&out, binary.LittleEndian, e.size)
		binary.Write(&out, binary.LittleEndian, e.off)
	}
	for _, b := range blobs {
		out.Write(b)
	}
	return out.Bytes()
}

// encodeBMP liefert den DIB-Teil eines ICO-Eintrags: 32 Bit BGRA von unten
// nach oben plus AND-Maske.
func encodeBMP(img *image.RGBA) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(40))
	binary.Write(&buf, binary.LittleEndian, int32(w))
	binary.Write(&buf, binary.LittleEndian, int32(h*2))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(32))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(w*h*4))
	binary.Write(&buf, binary.LittleEndian, int32(0))
	binary.Write(&buf, binary.LittleEndian, int32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	row := ((w + 31) / 32) * 4
	buf.Write(make([]byte, row*h))
	return buf.Bytes()
}
