// Paket tunnel holt sich cloudflared und stellt den Relay damit kostenlos
// ins Internet – ohne Konto, ohne Portfreigabe am Router.
package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
)

var urlPattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Tunnel ist ein laufender cloudflared-Prozess mit öffentlicher Adresse.
type Tunnel struct {
	URL  string
	cmd  *exec.Cmd
	mu   sync.Mutex
	done chan struct{}
}

// Done ist geschlossen, sobald cloudflared nicht mehr läuft.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Start lädt cloudflared bei Bedarf herunter und veröffentlicht den
// lokalen Port. Die zurückgegebene Adresse zeigt auf diesen Rechner.
func Start(port int, logg *log.Logger) (*Tunnel, error) {
	bin, err := ensureBinary(logg)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, "tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", port), "--no-autoupdate")
	hideWindow(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	urlCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		lastLines := make([]string, 0, 8)
		for scanner.Scan() {
			line := scanner.Text()
			if m := urlPattern.FindString(line); m != "" {
				select {
				case urlCh <- m:
				default:
				}
			}
			lastLines = append(lastLines, line)
			if len(lastLines) > 8 {
				lastLines = lastLines[1:]
			}
		}
		errCh <- strings.Join(lastLines, " | ")
	}()

	select {
	case url := <-urlCh:
		logg.Printf("Tunnel steht bereit: %s", url)
		t := &Tunnel{URL: url, cmd: cmd, done: make(chan struct{})}
		go func() {
			cmd.Wait()
			close(t.done)
		}()
		return t, nil
	case tail := <-errCh:
		cmd.Process.Kill()
		return nil, fmt.Errorf("cloudflared beendet sich: %s", shortTail(tail))
	case <-time.After(45 * time.Second):
		cmd.Process.Kill()
		return nil, errors.New("cloudflared hat in 45 Sekunden keine Adresse geliefert")
	}
}

func shortTail(s string) string {
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}

// Stop beendet den Tunnel.
func (t *Tunnel) Stop() {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cmd.Process.Kill()
}

// ensureBinary lädt cloudflared einmalig ins Konfigverzeichnis.
func ensureBinary(logg *log.Logger) (string, error) {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(config.Dir(), name)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 1<<20 {
		return path, nil
	}

	url := downloadURL()
	logg.Printf("Lade cloudflared herunter (%s)…", url)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("cloudflared herunterladen: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cloudflared herunterladen: HTTP %d", resp.StatusCode)
	}

	tmp := path + ".teil"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	os.Chmod(path, 0o755)
	logg.Printf("cloudflared bereit: %s", path)
	return path, nil
}

func downloadURL() string {
	const base = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-"
	switch runtime.GOOS {
	case "windows":
		return base + "windows-amd64.exe"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return base + "linux-arm64"
		}
		return base + "linux-amd64"
	}
	return base + runtime.GOOS + "-" + runtime.GOARCH
}
