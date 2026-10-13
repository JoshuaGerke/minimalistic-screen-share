//go:build windows

package tunnel

import (
	"os/exec"
	"syscall"
)

// hideWindow verhindert, dass beim Start von cloudflared ein Konsolenfenster
// aufblitzt.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
