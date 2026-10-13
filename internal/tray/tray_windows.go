//go:build windows

package tray

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/icon"
)

var (
	trayUser32   = windows.NewLazySystemDLL("user32.dll")
	trayShell32  = windows.NewLazySystemDLL("shell32.dll")
	trayKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW         = trayUser32.NewProc("RegisterClassExW")
	procCreateWindowExW          = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProcW           = trayUser32.NewProc("DefWindowProcW")
	procGetMessageW              = trayUser32.NewProc("GetMessageW")
	procTranslateMessage         = trayUser32.NewProc("TranslateMessage")
	procDispatchMessageW         = trayUser32.NewProc("DispatchMessageW")
	procPostQuitMessage          = trayUser32.NewProc("PostQuitMessage")
	procPostMessageW             = trayUser32.NewProc("PostMessageW")
	procDestroyWindow            = trayUser32.NewProc("DestroyWindow")
	procCreatePopupMenu          = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenuW              = trayUser32.NewProc("AppendMenuW")
	procDestroyMenu              = trayUser32.NewProc("DestroyMenu")
	procTrackPopupMenu           = trayUser32.NewProc("TrackPopupMenu")
	procGetCursorPos             = trayUser32.NewProc("GetCursorPos")
	procSetForegroundWindow      = trayUser32.NewProc("SetForegroundWindow")
	procCreateIconFromResourceEx = trayUser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = trayUser32.NewProc("DestroyIcon")
	procRegisterWindowMessageW   = trayUser32.NewProc("RegisterWindowMessageW")

	procShellNotifyIconW = trayShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = trayKernel32.NewProc("GetModuleHandleW")
)

const (
	wmApp        = 0x8000
	wmTray       = wmApp + 1
	wmTrayUpdate = wmApp + 2
	wmTrayQuit   = wmApp + 3

	wmLButtonUp         = 0x0202
	wmRButtonUp         = 0x0205
	wmDestroy           = 0x0002
	ninBalloonUserClick = 0x0405

	nimAdd     = 0x00000000
	nimModify  = 0x00000001
	nimDelete  = 0x00000002
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	mfString         = 0x00000000
	mfDisabled       = 0x00000002
	mfChecked        = 0x00000008
	mfPopup          = 0x00000010
	mfSeparator      = 0x00000800
	tpmRightButton   = 0x0002
	tpmReturnCommand = 0x0100

	hwndMessage = ^uintptr(2) // HWND_MESSAGE = -3
)

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type winTray struct {
	mu       sync.Mutex
	items    []*Item
	byID     map[int]*Item
	icons    map[string][]byte
	tooltip  string
	hIcon    uintptr
	hwnd     uintptr
	taskbar  uint32
	activate func()
	quitOnce sync.Once
}

func Run(items []*Item, onReady func(Tray)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	t := &winTray{
		items: items,
		byID:  map[int]*Item{},
		icons: map[string][]byte{},
	}
	indexItems(items, t.byID)
	for _, state := range []string{"idle", "live", "paused", "error"} {
		t.icons[state] = icon.ICO(state)
	}
	if err := t.createWindow(); err != nil {
		return err
	}
	t.SetIconState("idle")
	t.SetTooltip("minimalistic screen share – bereit")
	if onReady != nil {
		onReady(t)
	}
	t.loop()
	return nil
}

func (t *winTray) createWindow() error {
	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("MssTrayWindowClass")
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   windows.NewCallback(t.wndProc),
		HInstance:     hInst,
		LpszClassName: className,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW: %v", err)
	}
	hwnd, _, err := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0,
		hwndMessage, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", err)
	}
	t.hwnd = hwnd

	msgName, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(msgName)))
	t.taskbar = uint32(r)

	nid := t.iconData()
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	return nil
}

func (t *winTray) iconData() notifyIconData {
	t.mu.Lock()
	defer t.mu.Unlock()
	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             t.hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTray,
		HIcon:            t.hIcon,
	}
	copyUTF16(nid.SzTip[:], t.tooltip)
	return nid
}

func copyUTF16(dst []uint16, s string) {
	u := windows.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// refresh stößt ein Neuzeichnen des Symbols an (threadsicher).
func (t *winTray) refresh() {
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, wmTrayUpdate, 0, 0)
	}
}

func (t *winTray) loop() {
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (t *winTray) wndProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	switch message {
	case wmTray:
		switch lparam & 0xFFFF {
		case wmLButtonUp, wmRButtonUp:
			t.showMenu()
		case ninBalloonUserClick:
			t.mu.Lock()
			fn := t.activate
			t.mu.Unlock()
			if fn != nil {
				go fn()
			}
		}
		return 0
	case wmTrayUpdate:
		nid := t.iconData()
		procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
		return 0
	case wmTrayQuit:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		nid := t.iconData()
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		procPostQuitMessage.Call(0)
		return 0
	}
	if t.taskbar != 0 && message == t.taskbar {
		// Explorer neu gestartet – Symbol wieder anmelden.
		nid := t.iconData()
		procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return r
}

func (t *winTray) showMenu() {
	hmenu, _, _ := procCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	t.mu.Lock()
	items := t.items
	t.mu.Unlock()
	t.appendItems(hmenu, items)
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(hmenu,
		tpmRightButton|tpmReturnCommand, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procDestroyMenu.Call(hmenu)
	if cmd != 0 {
		t.mu.Lock()
		it := t.byID[int(cmd)]
		t.mu.Unlock()
		if it != nil && it.OnClick != nil {
			go it.OnClick()
		}
	}
	// Ohne das bleibt das Menü beim nächsten Klick hängen.
	procPostMessageW.Call(t.hwnd, 0, 0, 0)
}

func (t *winTray) appendItems(hmenu uintptr, items []*Item) {
	for _, it := range items {
		if it.Separator {
			procAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
			continue
		}
		title := windows.StringToUTF16(it.Title)
		if len(it.Items) > 0 {
			sub, _, _ := procCreatePopupMenu.Call()
			t.appendItems(sub, it.Items)
			procAppendMenuW.Call(hmenu, mfPopup, sub, uintptr(unsafe.Pointer(&title[0])))
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled {
			flags |= mfDisabled
		}
		if it.Checked {
			flags |= mfChecked
		}
		procAppendMenuW.Call(hmenu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(&title[0])))
	}
}

func (t *winTray) SetTooltip(s string) {
	t.mu.Lock()
	t.tooltip = s
	t.mu.Unlock()
	t.refresh()
}

func (t *winTray) SetItemTitle(id int, title string) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Title = title
	}
	t.mu.Unlock()
}

func (t *winTray) SetItemEnabled(id int, enabled bool) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Disabled = !enabled
	}
	t.mu.Unlock()
}

func (t *winTray) SetItemChecked(id int, checked bool) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Checked = checked
	}
	t.mu.Unlock()
}

func (t *winTray) SetIconState(state string) {
	ico, ok := t.icons[state]
	if !ok {
		return
	}
	h, err := iconFromICO(ico)
	if err != nil || h == 0 {
		return
	}
	t.mu.Lock()
	old := t.hIcon
	t.hIcon = h
	t.mu.Unlock()
	if old != 0 {
		procDestroyIcon.Call(old)
	}
	t.refresh()
}

func (t *winTray) Quit() {
	t.quitOnce.Do(func() {
		procPostMessageW.Call(t.hwnd, wmTrayQuit, 0, 0)
	})
}

func (t *winTray) SetActivate(fn func()) {
	t.mu.Lock()
	t.activate = fn
	t.mu.Unlock()
}

// Notify zeigt eine Sprechblase am Symbol (Windows-Balloon). Ein Klick
// darauf öffnet über SetActivate das Panel.
func (t *winTray) Notify(title, text string) {
	nid := t.iconData()
	nid.UFlags |= nifInfo
	// iconData sichert nur den Tooltip; Titel und Text hier nachtragen.
	t.mu.Lock()
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], text)
	t.mu.Unlock()
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// iconFromICO holt das 32×32-Bild aus den rohen ICO-Daten.
func iconFromICO(ico []byte) (uintptr, error) {
	if len(ico) < 6 {
		return 0, errors.New("ICO zu kurz")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	for i := 0; i < count; i++ {
		e := 6 + i*16
		if e+16 > len(ico) {
			break
		}
		w := int(ico[e])
		if w == 0 {
			w = 256
		}
		if w != 32 {
			continue
		}
		size := int(binary.LittleEndian.Uint32(ico[e+8:]))
		offset := int(binary.LittleEndian.Uint32(ico[e+12:]))
		if offset+size > len(ico) {
			continue
		}
		h, _, _ := procCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&ico[offset])), uintptr(size), 1, 0x00030000,
			uintptr(w), uintptr(w), 0)
		if h != 0 {
			return h, nil
		}
	}
	return 0, errors.New("kein 32×32-Bild im ICO")
}
