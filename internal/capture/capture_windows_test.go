//go:build windows

package capture

import "testing"

func TestScreenCaptureSmoke(t *testing.T) {
	src, err := Open(Target{Kind: "screen"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := src.Grab()
	if err != nil {
		t.Skipf("kein Bildschirm verfügbar: %v", err)
	}
	if img.Bounds().Dx() < 100 || img.Bounds().Dy() < 100 {
		t.Fatalf("verdächtig kleines Bild: %v", img.Bounds())
	}
}

func TestLists(t *testing.T) {
	monitors, err := ListMonitors()
	if err != nil {
		t.Fatalf("Monitore: %v", err)
	}
	if len(monitors) == 0 {
		t.Skip("keine Monitore gefunden")
	}
	t.Logf("%d Monitore, erster: %+v", len(monitors), monitors[0])

	windows, err := ListWindows()
	if err != nil {
		t.Fatalf("Fenster: %v", err)
	}
	t.Logf("%d Fenster", len(windows))
	for i, w := range windows {
		if i >= 5 {
			break
		}
		t.Logf("  %q (%dx%d)", w.Title, w.W, w.H)
	}
}
