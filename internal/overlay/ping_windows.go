//go:build windows

package overlay

import (
	"time"
)

// Zeige-Klick: Ein Zuschauer hat fünfmal auf eine Stelle geklickt – dort
// pulsiert kurz ein Ring mit seinem Namen. Das Fenster lässt alle Klicks
// durch und nimmt keinen Fokus.

const (
	wmPing    = 0x8003 // WM_APP + 3
	wmTimer   = 0x0113
	pingSize  = 220
	pingTotal = 1600 * time.Millisecond
)

var (
	pSetTimer  = user32.NewProc("SetTimer")
	pKillTimer = user32.NewProc("KillTimer")

	pingHwnd  uintptr
	pingX     int
	pingY     int
	pingName  string
	pingStart time.Time
)

// Ping zeigt den Ring an Bildschirmposition x, y.
func Ping(x, y int, name string) {
	startMu.Do(func() { go loop() })
	<-ready
	mu.Lock()
	pingX, pingY, pingName = x, y, name
	h := hwnd
	mu.Unlock()
	pPostMessageW.Call(h, wmPing, 0, 0)
}

func startPing() {
	mu.Lock()
	pingStart = time.Now()
	h := pingHwnd
	mu.Unlock()
	pingFrame()
	pShowWindow.Call(h, swShowNoActive)
	pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	pSetTimer.Call(h, 1, 30, 0)
}

func pingFrame() {
	mu.Lock()
	h, x, y, name, start := pingHwnd, pingX, pingY, pingName, pingStart
	mu.Unlock()
	p := float64(time.Since(start)) / float64(pingTotal)
	if p >= 1 {
		pKillTimer.Call(h, 1)
		pShowWindow.Call(h, swHide)
		return
	}
	img := RenderPing(p, name, pingSize)
	pos := struct{ X, Y int32 }{int32(x - pingSize/2), int32(y - pingSize/2)}
	present(h, img, 255, &pos)
}
