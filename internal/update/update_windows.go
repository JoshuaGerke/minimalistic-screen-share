//go:build windows

package update

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func assetMatches(name string) bool { return strings.EqualFold(name, "MinimalisticScreenShare.exe") }

// apply startet die neue exe mit --from-update: Sie beendet diese App über
// ihre lokale API, installiert sich an denselben Platz und startet still neu.
func apply(url string, logg *log.Logger, quiet bool) (bool, error) {
	dir := filepath.Join(os.TempDir(), "mss-update")
	os.MkdirAll(dir, 0o755)
	exe := filepath.Join(dir, "MinimalisticScreenShare.exe")
	if err := download(url, exe); err != nil {
		return false, err
	}
	logg.Println("Update: starte die neue Version")
	if quiet {
		return false, exec.Command(exe, "--from-update").Start()
	}
	return false, exec.Command(exe).Start()
}

// Restart ist unter Windows nicht nötig – die neue exe startet sich selbst.
func Restart() error { return nil }
