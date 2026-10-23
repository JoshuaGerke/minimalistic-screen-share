//go:build windows

package app

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/icon"
)

/* Unter Windows ist das Fenster ein eigenes Programmfenster mit WebView2 –
 * derselben Chromium-Engine wie bei Electron, nur schon in Windows
 * eingebaut (kein zusätzlicher Download). Es läuft im Prozess der App, die
 * Windows-Aufnahme nimmt es samt Stimmen der anderen also aus dem geteilten
 * Ton heraus. */

var native struct {
	sync.Mutex
	w      webview2.WebView
	failed bool
}

var (
	nativeUser32         = windows.NewLazySystemDLL("user32.dll")
	procNativeShowWindow = nativeUser32.NewProc("ShowWindow")
	procNativeForeground = nativeUser32.NewProc("SetForegroundWindow")
	procNativeSendMsg    = nativeUser32.NewProc("SendMessageW")
	procNativeIconFrom   = nativeUser32.NewProc("CreateIconFromResourceEx")
)

// openNative öffnet das Fenster (oder holt das offene nach vorn). false:
// WebView2 fehlt – dann nimmt OpenConference ein Edge-Fenster.
func (a *App) openNative(url string) bool {
	native.Lock()
	if native.failed {
		native.Unlock()
		return false
	}
	if w := native.w; w != nil {
		native.Unlock()
		w.Dispatch(func() {
			hwnd := uintptr(w.Window())
			procNativeShowWindow.Call(hwnd, 9) // SW_RESTORE
			procNativeForeground.Call(hwnd)
		})
		return true
	}
	native.Unlock()

	ok := make(chan bool)
	go func() {
		// Das Fenster gehört zu genau einem Thread samt eigener Nachrichtenschleife.
		runtime.LockOSThread()
		// Ton ohne Klick, Mikrofon ohne Nachfrage – das Fenster zeigt nur die eigene Seite.
		const env = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
		if args := os.Getenv(env); !strings.Contains(args, "--use-fake-ui-for-media-stream") {
			os.Setenv(env, strings.TrimSpace(args+" --autoplay-policy=no-user-gesture-required --use-fake-ui-for-media-stream"))
		}
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      filepath.Join(config.Dir(), "webview"),
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: "Screen Share", Width: 1280, Height: 820, Center: true},
		})
		if w == nil {
			a.log.Printf("WebView2 nicht verfügbar – nehme ein Edge-Fenster")
			native.Lock()
			native.failed = true
			native.Unlock()
			ok <- false
			return
		}
		setWindowIcon(uintptr(w.Window()))
		w.SetSize(1000, 640, webview2.HintMin)
		native.Lock()
		native.w = w
		native.Unlock()
		confWindows.Add(1)
		w.Navigate(url)
		a.log.Printf("Fenster geöffnet (WebView2)")
		ok <- true
		w.Run()
		confWindows.Add(-1)
		native.Lock()
		native.w = nil
		native.Unlock()
	}()
	return <-ok
}

// setWindowIcon gibt dem Fenster das Symbol der App (groß und klein).
func setWindowIcon(hwnd uintptr) {
	ico := icon.ICO("idle")
	for _, s := range []struct{ size, which int }{{32, 1}, {16, 0}} { // ICON_BIG, ICON_SMALL
		count := int(binary.LittleEndian.Uint16(ico[4:]))
		for i := 0; i < count; i++ {
			e := 6 + i*16
			if int(ico[e]) != s.size {
				continue
			}
			size := int(binary.LittleEndian.Uint32(ico[e+8:]))
			off := int(binary.LittleEndian.Uint32(ico[e+12:]))
			h, _, _ := procNativeIconFrom.Call(uintptr(unsafe.Pointer(&ico[off])), uintptr(size), 1, 0x00030000, uintptr(s.size), uintptr(s.size), 0)
			if h != 0 {
				procNativeSendMsg.Call(hwnd, 0x0080, uintptr(s.which), h) // WM_SETICON
			}
		}
	}
}
