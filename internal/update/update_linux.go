//go:build linux

package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func assetMatches(name string) bool {
	return strings.HasSuffix(name, "_linux_"+runtime.GOARCH+".tar.gz")
}

// apply tauscht mss-share, mss-relay und mss-viewer an Ort und Stelle aus.
// Unter Linux darf man ein laufendes Programm ersetzen – es läuft mit der
// alten Datei weiter, bis es neu startet.
func apply(url string, logg *log.Logger, quiet bool) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	probe := filepath.Join(dir, ".mss-update-test")
	if f, err := os.Create(probe); err != nil {
		return false, fmt.Errorf("%s ist schreibgeschützt (per pacman installiert?) – bitte darüber aktualisieren", dir)
	} else {
		f.Close()
		os.Remove(probe)
	}

	tmp, err := os.CreateTemp("", "mss-update-*.tar.gz")
	if err != nil {
		return false, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := download(url, tmp.Name()); err != nil {
		return false, err
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, err
	}
	tr := tar.NewReader(gz)
	replaced := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, err
		}
		name := filepath.Base(h.Name)
		if name != "mss-share" && name != "mss-relay" && name != "mss-viewer" {
			continue
		}
		dst := filepath.Join(dir, name)
		if name != filepath.Base(exe) {
			if _, err := os.Stat(dst); err != nil {
				continue // nur vorhandene Programme ersetzen
			}
		}
		if name == "mss-share" {
			dst = exe
		}
		next := dst + ".neu"
		out, err := os.OpenFile(next, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return false, err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(next)
			return false, err
		}
		out.Close()
		if err := os.Rename(next, dst); err != nil {
			os.Remove(next)
			return false, err
		}
		replaced++
	}
	if replaced == 0 {
		return false, fmt.Errorf("im Paket war kein mss-share")
	}
	logg.Printf("Update: %d Programme in %s ersetzt", replaced, dir)
	return true, nil
}

// Restart startet die (neue) App still im Hintergrund, sobald diese beendet ist.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	cmd := exec.Command("sh", "-c", `sleep 1; exec "$0" --background`, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
