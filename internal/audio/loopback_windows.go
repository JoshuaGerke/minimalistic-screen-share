//go:build windows

package audio

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// Prozess-Loopback wird asynchron über ActivateAudioInterfaceAsync
// aktiviert. Dafür braucht es ein kleines COM-Objekt, das Windows nach
// getaner Arbeit zurückruft – das bauen wir hier von Hand nach.

var (
	mmdevapi                        = windows.NewLazySystemDLL("mmdevapi.dll")
	procActivateAudioInterfaceAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")
	procRtlMoveMemory               = windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory")

	iidCompletionHandler = ole.NewGUID("{41D949AB-9862-444A-80F6-C261334DA5EB}")
	iidAgileObject       = ole.NewGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")
)

const (
	activationTypeProcessLoopback = 1
	loopbackModeExclude           = 1
	vtBlob                        = 65
	eNoInterface                  = 0x80004002
)

type handlerVtbl struct {
	queryInterface, addRef, release, activateCompleted uintptr
}

type asyncOpVtbl struct {
	queryInterface, addRef, release, getActivateResult uintptr
}

var (
	handlerOnce sync.Once
	handlerMu   sync.Mutex // eine Aktivierung zur Zeit
	handlerTbl  handlerVtbl
	handlerObj  struct{ vtbl *handlerVtbl }
	handlerDone = make(chan struct{}, 1)
)

func initHandler() {
	handlerTbl = handlerVtbl{
		queryInterface: syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			var g ole.GUID
			procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&g)), riid, unsafe.Sizeof(g))
			out := uintptr(0)
			hr := uintptr(eNoInterface)
			// Agil sein ist Pflicht: Windows ruft aus einem fremden Thread zurück.
			if ole.IsEqualGUID(&g, ole.IID_IUnknown) || ole.IsEqualGUID(&g, iidCompletionHandler) || ole.IsEqualGUID(&g, iidAgileObject) {
				out, hr = this, 0
			}
			procRtlMoveMemory.Call(ppv, uintptr(unsafe.Pointer(&out)), unsafe.Sizeof(out))
			return hr
		}),
		// Das Objekt lebt global für immer – Referenzen zählen ist unnötig.
		addRef:  syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		release: syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		activateCompleted: syscall.NewCallback(func(this, op uintptr) uintptr {
			select {
			case handlerDone <- struct{}{}:
			default:
			}
			return 0
		}),
	}
	handlerObj.vtbl = &handlerTbl
}

// activateProcessLoopback liefert einen AudioClient, der allen Ton außer dem
// des Prozessbaums pid aufnimmt.
func activateProcessLoopback(pid uint32) (*wca.IAudioClient, error) {
	if err := procActivateAudioInterfaceAsync.Find(); err != nil {
		return nil, errors.New("Windows zu alt für Prozess-Loopback")
	}
	handlerOnce.Do(initHandler)
	handlerMu.Lock()
	defer handlerMu.Unlock()
	select {
	case <-handlerDone:
	default:
	}

	params := struct{ activationType, pid, mode uint32 }{activationTypeProcessLoopback, pid, loopbackModeExclude}
	pv := struct {
		vt         uint16
		r1, r2, r3 uint16
		size       uint32
		_          uint32
		data       *byte
	}{vt: vtBlob, size: uint32(unsafe.Sizeof(params)), data: (*byte)(unsafe.Pointer(&params))}
	path, _ := windows.UTF16PtrFromString(`VAD\Process_Loopback`)

	var op *ole.IUnknown
	hr, _, _ := procActivateAudioInterfaceAsync.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(wca.IID_IAudioClient)),
		uintptr(unsafe.Pointer(&pv)),
		uintptr(unsafe.Pointer(&handlerObj)),
		uintptr(unsafe.Pointer(&op)),
	)
	runtime.KeepAlive(&params)
	if int32(hr) < 0 || op == nil {
		return nil, fmt.Errorf("Aktivierung fehlgeschlagen (0x%08x)", uint32(hr))
	}
	defer op.Release()

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		return nil, errors.New("Aktivierung antwortet nicht")
	}

	var result int32
	var unk *ole.IUnknown
	vt := (*asyncOpVtbl)(unsafe.Pointer(op.RawVTable))
	r, _, _ := syscall.SyscallN(vt.getActivateResult,
		uintptr(unsafe.Pointer(op)), uintptr(unsafe.Pointer(&result)), uintptr(unsafe.Pointer(&unk)))
	if int32(r) < 0 || result < 0 || unk == nil {
		return nil, fmt.Errorf("Aktivierung abgelehnt (0x%08x)", uint32(result))
	}
	return (*wca.IAudioClient)(unsafe.Pointer(unk)), nil
}

var (
	noteMu        sync.Mutex
	exclusionNote string
)

func setExclusionNote(s string) {
	noteMu.Lock()
	exclusionNote = s
	noteMu.Unlock()
}

// ExclusionNote erklärt, warum ein Programm gerade nicht ausgenommen werden
// kann (leer = alles in Ordnung).
func ExclusionNote() string {
	noteMu.Lock()
	defer noteMu.Unlock()
	return exclusionNote
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
