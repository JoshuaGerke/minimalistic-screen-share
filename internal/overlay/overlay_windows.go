//go:build windows

package overlay

import (
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	kern32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pGetWindowRect       = user32.NewProc("GetWindowRect")
	pSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	pUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	pGetDC               = user32.NewProc("GetDC")
	pReleaseDC           = user32.NewProc("ReleaseDC")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	pSelectObject        = gdi32.NewProc("SelectObject")
	pDeleteObject        = gdi32.NewProc("DeleteObject")
	pDeleteDC            = gdi32.NewProc("DeleteDC")
	pRtlMoveMemory       = kern32.NewProc("RtlMoveMemory")
	pGetModuleHandleW    = kern32.NewProc("GetModuleHandleW")
)

const (
	wmNCHitTest     = 0x0084
	wmMouseActivate = 0x0021
	wmExitSizeMove  = 0x0232
	wmRedraw        = 0x8001 // WM_APP + 1
	wmShow          = 0x8002 // WM_APP + 2
	htCaption       = 2
	htTransparent   = ^uintptr(0) // -1
	maNoActivate    = 3

	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	wsExTransparent = 0x00000020
	gwlExStyle      = ^uintptr(19) // -20

	swHide         = 0
	swShowNoActive = 4
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
	swpNoActivate  = 0x0010
	hwndTopmost    = ^uintptr(0) // -1
	ulwAlpha       = 2
	acSrcAlpha     = 1
	smCXScreen     = 0
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type rect struct{ Left, Top, Right, Bottom int32 }

var (
	mu       sync.Mutex
	state    State
	hwnd     uintptr
	startMu  sync.Once
	ready    = make(chan struct{})
	posLoad  func() (int, int, bool)
	posSave  func(int, int)
	shown    bool
	lastSize struct{ w, h int32 }
)

// SetPositionStore hinterlegt, wo die Position gespeichert wird.
func SetPositionStore(load func() (int, int, bool), save func(int, int)) {
	mu.Lock()
	posLoad, posSave = load, save
	mu.Unlock()
}

// Supported: Unter Windows gibt es das native Overlay.
func Supported() bool { return true }

// Show blendet das Overlay ein oder aus.
func Show(on bool) {
	startMu.Do(func() { go loop() })
	<-ready
	mu.Lock()
	shown = on
	mu.Unlock()
	v := uintptr(0)
	if on {
		v = 1
	}
	pPostMessageW.Call(hwnd, wmShow, v, 0)
}

// Visible meldet, ob das Overlay gerade zu sehen ist.
func Visible() bool {
	mu.Lock()
	defer mu.Unlock()
	return shown
}

// Update übernimmt neue Daten und zeichnet neu.
func Update(st State) {
	mu.Lock()
	state = st.Clean()
	h := hwnd
	mu.Unlock()
	if h != 0 {
		pPostMessageW.Call(h, wmRedraw, 0, 0)
	}
}

func systemFont() []byte {
	dir := os.Getenv("WINDIR")
	if dir == "" {
		dir = `C:\Windows`
	}
	for _, f := range []string{"segoeui.ttf", "arial.ttf"} {
		if b, err := os.ReadFile(filepath.Join(dir, "Fonts", f)); err == nil {
			return b
		}
	}
	return nil
}

// loop besitzt das Fenster: Win32 verlangt, dass ein Thread es erzeugt und
// seine Nachrichten abholt.
func loop() {
	runtime.LockOSThread()
	inst, _, _ := pGetModuleHandleW.Call(0)
	class, _ := windows.UTF16PtrFromString("MssVoiceOverlay")
	wc := wndClassEx{WndProc: syscall.NewCallback(wndProc), Instance: inst, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	title, _ := windows.UTF16PtrFromString("Sprachkanal")

	x, y := -1, -1
	mu.Lock()
	if posLoad != nil {
		if px, py, ok := posLoad(); ok {
			x, y = px, py
		}
	}
	mu.Unlock()
	if x < 0 {
		sw, _, _ := pGetSystemMetrics.Call(smCXScreen)
		x, y = int(sw)-300, 80
	}
	h, _, _ := pCreateWindowExW.Call(wsExLayered|wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), wsPopup,
		uintptr(x), uintptr(y), 10, 10, 0, 0, inst, 0)
	ph, _, _ := pCreateWindowExW.Call(wsExLayered|wsExTopmost|wsExToolWindow|wsExNoActivate|wsExTransparent,
		uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), wsPopup,
		0, 0, 10, 10, 0, 0, inst, 0)
	mu.Lock()
	hwnd = h
	pingHwnd = ph
	mu.Unlock()
	close(ready)
	redraw()

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(h, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmNCHitTest:
		mu.Lock()
		through := state.Settings.ClickThrough || h == pingHwnd
		mu.Unlock()
		if through {
			return htTransparent
		}
		return htCaption // überall anfassbar zum Verschieben
	case wmMouseActivate:
		return maNoActivate // Spiel/Fenster darunter behält den Fokus
	case wmExitSizeMove:
		var r rect
		pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		mu.Lock()
		save := posSave
		mu.Unlock()
		if save != nil {
			save(int(r.Left), int(r.Top))
		}
		return 0
	case wmRedraw:
		redraw()
		return 0
	case wmPing:
		startPing()
		return 0
	case wmTimer:
		if h == pingHwnd {
			pingFrame()
		}
		return 0
	case wmShow:
		if wParam == 1 {
			redraw()
			pShowWindow.Call(h, swShowNoActive)
			pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		} else {
			pShowWindow.Call(h, swHide)
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, message, wParam, lParam)
	return r
}

type bitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	ClrUsed, ClrImportant  uint32
	Colors                 [1]uint32
}

// redraw malt den aktuellen Zustand und gibt ihn mit Transparenz an Windows.
func redraw() {
	mu.Lock()
	st := state
	h := hwnd
	mu.Unlock()
	if h == 0 {
		return
	}
	if st.Settings.Opacity == 0 {
		st = st.Clean()
	}
	img := Render(st)
	w, ht := int32(img.Bounds().Dx()), int32(img.Bounds().Dy())

	ex := uintptr(wsExLayered | wsExTopmost | wsExToolWindow | wsExNoActivate)
	if st.Settings.ClickThrough {
		ex |= wsExTransparent
	}
	pSetWindowLongPtrW.Call(h, gwlExStyle, ex)

	present(h, img, byte(st.Settings.Opacity*255/100), nil)
	lastSize.w, lastSize.h = w, ht
}

// present gibt ein Bild mit Transparenz an ein Layered-Fenster, auf Wunsch
// gleich an eine neue Position.
func present(h uintptr, img *image.RGBA, alpha byte, pos *struct{ X, Y int32 }) {
	w, ht := int32(img.Bounds().Dx()), int32(img.Bounds().Dy())
	// Windows will BGRA mit vorab multipliziertem Alpha – image.RGBA hat das
	// Alpha schon drin, nur Rot und Blau tauschen.
	pix := make([]byte, len(img.Pix))
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]
	}

	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bi := bitmapInfo{Width: w, Height: -ht, Planes: 1, BitCount: 32}
	bi.Size = 40
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		return
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	defer pSelectObject.Call(mem, old)
	pRtlMoveMemory.Call(bits, uintptr(unsafe.Pointer(&pix[0])), uintptr(len(pix)))

	size := struct{ CX, CY int32 }{w, ht}
	src := struct{ X, Y int32 }{0, 0}
	blend := [4]byte{0, 0, alpha, acSrcAlpha}
	pUpdateLayeredWindow.Call(h, screen, uintptr(unsafe.Pointer(pos)), uintptr(unsafe.Pointer(&size)), mem,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), ulwAlpha)
}
