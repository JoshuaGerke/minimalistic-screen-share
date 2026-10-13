//go:build linux

// Paket single sorgt dafür, dass der Client nur einmal läuft.
package single

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
)

var heldFile *os.File

// Acquire liefert true, wenn wir die erste Instanz sind.
func Acquire() (bool, error) {
	path := filepath.Join(config.Dir(), "share.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false, nil
	}
	heldFile = f
	return true, nil
}
