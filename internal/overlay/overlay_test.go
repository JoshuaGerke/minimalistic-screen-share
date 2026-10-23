package overlay

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRender malt Beispielzustände; mit MSS_OVERLAY_PNG=<ordner> landen sie
// zum Anschauen als Bild auf der Platte.
func TestRender(t *testing.T) {
	st := State{
		Members: []Member{
			{Name: "Joshua", Speaking: true, Me: true},
			{Name: "Nils", Muted: true},
			{Name: "Kevin mit einem sehr langen Namen", Speaking: true},
			{Name: "Max", Deaf: true},
		},
		Settings: Settings{Opacity: 85, Scale: 100},
	}.Clean()
	img := Render(st)
	if img.Bounds().Dx() < 100 || img.Bounds().Dy() < 4*30 {
		t.Fatalf("Overlay zu klein: %v", img.Bounds())
	}
	empty := Render(State{Settings: Settings{OnlySpeaking: true}}.Clean())
	if empty.Bounds().Dy() == 0 {
		t.Fatal("leeres Overlay hat keine Höhe")
	}
	if dir := os.Getenv("MSS_OVERLAY_PNG"); dir != "" {
		for name, im := range map[string]*image.RGBA{"overlay.png": img, "overlay-leer.png": empty} {
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, im)
			f.Close()
		}
	}
}
