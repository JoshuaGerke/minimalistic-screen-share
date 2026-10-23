//go:build windows

package capture

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procEnumWindows                   = user32.NewProc("EnumWindows")
	procIsWindow                      = user32.NewProc("IsWindow")
	procIsWindowVisible               = user32.NewProc("IsWindowVisible")
	procIsIconic                      = user32.NewProc("IsIconic")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	procPrintWindow                   = user32.NewProc("PrintWindow")
	procGetCursorInfo                 = user32.NewProc("GetCursorInfo")
	procGetIconInfo                   = user32.NewProc("GetIconInfo")
	procDrawIconEx                    = user32.NewProc("DrawIconEx")
	procSetProcessDPIAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")

	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	srcCopy      = 0x00CC0020
	captureBLT   = 0x40000000
	biRGB        = 0
	dibRGBColors = 0
	diNormal     = 0x0003
	cursorShown  = 0x0001

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14
	pwRenderFullContent      = 0x00000002
)

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Pos    point
}

type iconInfo struct {
	Icon     int32
	HotspotX uint32
	HotspotY uint32
	Mask     uintptr
	Color    uintptr
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type winSource struct {
	mu     sync.Mutex
	target Target
	hwnd   uintptr
	name   string
}

func open(t Target) (Source, error) {
	switch t.Kind {
	case "screen":
		return &winSource{target: t, name: "Bildschirm"}, nil
	case "region":
		return &winSource{target: t, name: "Bereich"}, nil
	case "monitor":
		area, err := monitorBounds(t.Monitor)
		if err != nil {
			return nil, err
		}
		nt := t
		nt.Kind = "region"
		nt.Region = Rect{X: int(area.Left), Y: int(area.Top), W: int(area.Right - area.Left), H: int(area.Bottom - area.Top)}
		return &winSource{target: nt, name: t.Label()}, nil
	case "window":
		hwnd, title, err := findWindow(t.Window)
		if err != nil {
			return nil, err
		}
		return &winSource{target: t, hwnd: hwnd, name: title}, nil
	}
	return nil, fmt.Errorf("unbekanntes Ziel: %q", t.Kind)
}

func (s *winSource) Name() string { return s.name }
func (s *winSource) Close() error { return nil }

func (s *winSource) Grab() (*image.RGBA, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.target.Kind == "window" {
		if ok, _, _ := procIsWindow.Call(s.hwnd); ok == 0 {
			return nil, errors.New("Fenster existiert nicht mehr")
		}
		return grabWindow(s.hwnd, s.target.Cursor)
	}
	area, err := s.area()
	if err != nil {
		return nil, err
	}
	return grabScreen(area, s.target.Cursor)
}

func (s *winSource) area() (rect, error) {
	if s.target.Kind == "region" {
		r := s.target.Region
		if r.W <= 0 || r.H <= 0 {
			return rect{}, errors.New("leerer Bildbereich")
		}
		return rect{int32(r.X), int32(r.Y), int32(r.X + r.W), int32(r.Y + r.H)}, nil
	}
	x := getSystemMetrics(smXVirtualScreen)
	y := getSystemMetrics(smYVirtualScreen)
	w := getSystemMetrics(smCXVirtualScreen)
	h := getSystemMetrics(smCYVirtualScreen)
	if w <= 0 || h <= 0 {
		return rect{}, errors.New("keine Bildschirmgröße ermittelbar")
	}
	return rect{int32(x), int32(y), int32(x + w), int32(y + h)}, nil
}

func getSystemMetrics(index int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(v))
}

func grabScreen(area rect, withCursor bool) (*image.RGBA, error) {
	w := int(area.Right - area.Left)
	h := int(area.Bottom - area.Top)
	if w <= 0 || h <= 0 {
		return nil, errors.New("leerer Bildbereich")
	}
	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, errors.New("GetDC fehlgeschlagen")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, errors.New("CreateCompatibleDC fehlgeschlagen")
	}
	defer procDeleteDC.Call(hdcMem)

	hbm, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(w), uintptr(h))
	if hbm == 0 {
		return nil, errors.New("CreateCompatibleBitmap fehlgeschlagen")
	}
	defer procDeleteObject.Call(hbm)

	old, _, _ := procSelectObject.Call(hdcMem, hbm)
	defer procSelectObject.Call(hdcMem, old)

	ok, _, err := procBitBlt.Call(hdcMem, 0, 0, uintptr(w), uintptr(h), hdcScreen,
		uintptr(area.Left), uintptr(area.Top), srcCopy|captureBLT)
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt: %v", err)
	}
	if withCursor {
		drawCursor(hdcMem, area)
	}
	return readBitmap(hdcMem, hbm, w, h)
}

// grabWindow malt das Fenster über PrintWindow. Klappt nicht bei jedem
// (Hardware-Overlays, manche Spiele) – dann wird der Bildschirmbereich genommen.
func grabWindow(hwnd uintptr, withCursor bool) (*image.RGBA, error) {
	area, err := windowBounds(hwnd)
	if err != nil {
		return nil, err
	}
	w := int(area.Right - area.Left)
	h := int(area.Bottom - area.Top)

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, errors.New("GetDC fehlgeschlagen")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, errors.New("CreateCompatibleDC fehlgeschlagen")
	}
	defer procDeleteDC.Call(hdcMem)

	hbm, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(w), uintptr(h))
	if hbm == 0 {
		return nil, errors.New("CreateCompatibleBitmap fehlgeschlagen")
	}
	defer procDeleteObject.Call(hbm)

	old, _, _ := procSelectObject.Call(hdcMem, hbm)
	defer procSelectObject.Call(hdcMem, old)

	if ok, _, _ := procPrintWindow.Call(hwnd, hdcMem, pwRenderFullContent); ok != 0 {
		if img, err := readBitmap(hdcMem, hbm, w, h); err == nil && !mostlyBlack(img) {
			return img, nil
		}
	}
	return grabScreen(area, withCursor)
}

func readBitmap(hdcMem, hbm uintptr, w, h int) (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // von oben nach unten
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	got, _, err := procGetDIBits.Call(hdcMem, hbm, 0, uintptr(h),
		uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if got == 0 {
		return nil, fmt.Errorf("GetDIBits: %v", err)
	}
	pix := img.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i], pix[i+2] = pix[i+2], pix[i]
		pix[i+3] = 0xFF
	}
	return img, nil
}

func drawCursor(hdcMem uintptr, area rect) {
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci)))
	if ok == 0 || ci.Flags != cursorShown || ci.Cursor == 0 {
		return
	}
	var ii iconInfo
	if ok, _, _ := procGetIconInfo.Call(ci.Cursor, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return
	}
	defer func() {
		if ii.Mask != 0 {
			procDeleteObject.Call(ii.Mask)
		}
		if ii.Color != 0 {
			procDeleteObject.Call(ii.Color)
		}
	}()
	x := ci.Pos.X - int32(ii.HotspotX) - area.Left
	y := ci.Pos.Y - int32(ii.HotspotY) - area.Top
	procDrawIconEx.Call(hdcMem, uintptr(x), uintptr(y), ci.Cursor, 0, 0, 0, 0, diNormal)
}

// mostlyBlack erkennt PrintWindow-Ausgaben, die nur schwarz sind, weil die
// Anwendung sich nicht malen lässt.
func mostlyBlack(img *image.RGBA) bool {
	pix := img.Pix
	black, total := 0, 0
	for i := 0; i+3 < len(pix); i += 4 * 97 {
		total++
		if pix[i]|pix[i+1]|pix[i+2] < 12 {
			black++
		}
	}
	return total > 0 && black*100/total > 98
}

func windowBounds(hwnd uintptr) (rect, error) {
	var r rect
	if ok, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds,
		uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); ok != 0 {
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	}
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return rect{}, errors.New("Fenster hat keine Größe")
	}
	return r, nil
}

// Bounds liefert, wo das Ziel auf dem Bildschirm liegt (für Zeige-Klicks).
func Bounds(t Target) (Rect, error) {
	var r rect
	var err error
	switch t.Kind {
	case "monitor":
		r, err = monitorBounds(t.Monitor)
	case "region":
		return t.Region, nil
	case "window":
		h, _, e := findWindow(t.Window)
		if e != nil {
			return Rect{}, e
		}
		r, err = windowBounds(h)
	default:
		x, y := getSystemMetrics(smXVirtualScreen), getSystemMetrics(smYVirtualScreen)
		r = rect{int32(x), int32(y), int32(x + getSystemMetrics(smCXVirtualScreen)), int32(y + getSystemMetrics(smCYVirtualScreen))}
	}
	return Rect{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}, err
}

// ListMonitors liefert alle Monitore in Enum-Reihenfolge.
func ListMonitors() ([]Monitor, error) {
	var out []Monitor
	cb := windows.NewCallback(func(hMonitor, hdc, lprc, data uintptr) uintptr {
		var mi monitorInfo
		mi.Size = uint32(unsafe.Sizeof(mi))
		if ok, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); ok != 0 {
			out = append(out, Monitor{
				Index: len(out),
				X:     int(mi.Monitor.Left),
				Y:     int(mi.Monitor.Top),
				W:     int(mi.Monitor.Right - mi.Monitor.Left),
				H:     int(mi.Monitor.Bottom - mi.Monitor.Top),
			})
		}
		return 1
	})
	if ok, _, err := procEnumDisplayMonitors.Call(0, 0, cb, 0); ok == 0 {
		return nil, err
	}
	return out, nil
}

func monitorBounds(index int) (rect, error) {
	monitors, err := ListMonitors()
	if err != nil {
		return rect{}, err
	}
	for _, m := range monitors {
		if m.Index == index {
			return rect{int32(m.X), int32(m.Y), int32(m.X + m.W), int32(m.Y + m.H)}, nil
		}
	}
	return rect{}, fmt.Errorf("Monitor %d gibt es nicht", index+1)
}

// ListWindows liefert sichtbare Fenster in Z-Reihenfolge (oben zuerst).
func ListWindows() ([]Window, error) {
	ownPID := windows.GetCurrentProcessId()
	var out []Window
	cb := windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
		if ok, _, _ := procIsWindowVisible.Call(hwnd); ok == 0 {
			return 1
		}
		if ok, _, _ := procIsIconic.Call(hwnd); ok != 0 {
			return 1
		}
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == ownPID {
			return 1
		}
		var cloaked uint32
		if ok, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked,
			uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); ok == 0 && cloaked != 0 {
			return 1
		}
		n, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if n == 0 {
			return 1
		}
		buf := make([]uint16, n+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
		title := strings.TrimSpace(windows.UTF16ToString(buf))
		if title == "" {
			return 1
		}
		var r rect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		if r.Right-r.Left < 120 || r.Bottom-r.Top < 80 {
			return 1
		}
		out = append(out, Window{Title: title, W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)})
		return 1
	})
	if ok, _, err := procEnumWindows.Call(cb, 0); ok == 0 {
		return nil, err
	}
	return out, nil
}

func findWindow(title string) (uintptr, string, error) {
	windows, err := listWindowHandles()
	if err != nil {
		return 0, "", err
	}
	for _, w := range windows {
		if w.title == title {
			return w.hwnd, w.title, nil
		}
	}
	lower := strings.ToLower(title)
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.title), lower) {
			return w.hwnd, w.title, nil
		}
	}
	return 0, "", fmt.Errorf("Fenster %q nicht gefunden", title)
}

type windowHandle struct {
	hwnd  uintptr
	title string
}

func listWindowHandles() ([]windowHandle, error) {
	ownPID := windows.GetCurrentProcessId()
	var out []windowHandle
	cb := windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
		if ok, _, _ := procIsWindowVisible.Call(hwnd); ok == 0 {
			return 1
		}
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == ownPID {
			return 1
		}
		n, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if n == 0 {
			return 1
		}
		buf := make([]uint16, n+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
		title := strings.TrimSpace(windows.UTF16ToString(buf))
		if title != "" {
			out = append(out, windowHandle{hwnd: hwnd, title: title})
		}
		return 1
	})
	if ok, _, err := procEnumWindows.Call(cb, 0); ok == 0 {
		return nil, err
	}
	return out, nil
}
