//go:build windows

// Paket single sorgt dafür, dass der Client nur einmal läuft.
package single

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	heldHandle       windows.Handle
)

// Acquire liefert true, wenn wir die erste Instanz sind.
func Acquire() (bool, error) {
	// MSS_PROFILE trennt Test-Instanzen (zusammen mit eigenem APPDATA).
	name, err := windows.UTF16PtrFromString(`Local\minimalistic-screen-share` + os.Getenv("MSS_PROFILE"))
	if err != nil {
		return false, err
	}
	r, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		return false, lastErr
	}
	if lastErr == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(windows.Handle(r))
		return false, nil
	}
	heldHandle = windows.Handle(r)
	return true, nil
}
