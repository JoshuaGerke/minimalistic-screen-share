// Paket setup macht aus der heruntergeladenen exe eine installierte App:
// Doppelklick genügt, kein Installer, kein Admin.
package setup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StopRunning beendet eine laufende Instanz über deren lokale API. Liefert
// true, wenn eine lief.
func StopRunning() bool {
	client := http.Client{Timeout: 2 * time.Second}
	for port := 7777; port < 7797; port++ {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
		if err != nil {
			continue
		}
		var st struct {
			App string `json:"app"`
		}
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st.App != "mss" {
			continue
		}
		if r, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/quit", port), "application/json", strings.NewReader("{}")); err == nil {
			r.Body.Close()
		}
		return true
	}
	return false
}

// devBuild erkennt "go run" und Builds aus dem Repo – die sollen sich nicht
// selbst installieren.
func devBuild(exe string) bool {
	if strings.Contains(strings.ToLower(exe), "go-build") {
		return true
	}
	dir := filepath.Dir(exe)
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return true
		}
	}
	return os.Getenv("MSS_PORTABLE") != ""
}

// copyFile kopiert mit ein paar Versuchen – die alte exe ist kurz nach dem
// Beenden manchmal noch gesperrt.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".neu"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	for i := 0; ; i++ {
		os.Remove(dst)
		err = os.Rename(tmp, dst)
		if err == nil || i >= 20 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
