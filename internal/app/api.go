package app

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/web"
)

// Handler liefert die lokale API plus das Host-Panel.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.localOnly(a.handleStatus))
	mux.HandleFunc("/api/config", a.localOnly(a.handleConfig))
	mux.HandleFunc("/api/targets", a.localOnly(a.handleTargets))
	mux.HandleFunc("/api/start", a.localOnly(a.handleStart))
	mux.HandleFunc("/api/stop", a.localOnly(a.handleStop))
	mux.HandleFunc("/api/allow", a.localOnly(a.handleAllow))
	mux.HandleFunc("/api/deny", a.localOnly(a.handleDeny))
	mux.Handle("/", hostIndex(web.FS()))
	return mux
}

// localOnly lässt nur Anfragen von der eigenen Seite durch. Ohne CORS-Header
// kommt ein fremder Browser mit JSON-Postings ohnehin nicht weit – der
// Origin-Check ist trotzdem billig.
func (a *App) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
				writeError(w, http.StatusForbidden, "fremder Origin")
				return
			}
		}
		next(w, r)
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Status())
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.Config())
	case http.MethodPost:
		var c config.Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
			writeError(w, http.StatusBadRequest, "ungültige Einstellungen: "+err.Error())
			return
		}
		if err := a.SaveConfig(c); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.Config())
	default:
		writeError(w, http.StatusMethodNotAllowed, "nur GET oder POST")
	}
}

func (a *App) handleTargets(w http.ResponseWriter, r *http.Request) {
	monitors, _ := capture.ListMonitors()
	windows, _ := capture.ListWindows()
	if monitors == nil {
		monitors = []capture.Monitor{}
	}
	if windows == nil {
		windows = []capture.Window{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"platform": runtime.GOOS,
		"monitors": monitors,
		"windows":  windows,
	})
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	var target capture.Target
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&target); err != nil {
		writeError(w, http.StatusBadRequest, "ungültiges Ziel: "+err.Error())
		return
	}
	if err := a.Start(target); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	a.Stop()
	writeJSON(w, http.StatusOK, a.Status())
}

// handleAllow/handleDeny entscheiden über eine Zuschaueranfrage.
func (a *App) handleAllow(w http.ResponseWriter, r *http.Request) {
	a.handleKnockDecision(w, r, a.Allow)
}

func (a *App) handleDeny(w http.ResponseWriter, r *http.Request) {
	a.handleKnockDecision(w, r, a.Deny)
}

func (a *App) handleKnockDecision(w http.ResponseWriter, r *http.Request, decide func(uint64) error) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	var body struct {
		ID uint64 `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "ungültige Anfrage: "+err.Error())
		return
	}
	if err := decide(body.ID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

func hostIndex(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/host.html"
			files.ServeHTTP(w, r2)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(msg)})
}
