package app

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/overlay"
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
	mux.HandleFunc("/api/pause", a.localOnly(a.handlePause))
	mux.HandleFunc("/api/target", a.localOnly(a.handleTarget))
	mux.HandleFunc("/api/autostart", a.localOnly(a.handleAutostart))
	mux.HandleFunc("/api/quit", a.localOnly(a.handleQuit))
	mux.HandleFunc("/api/hello", a.handleHello)
	mux.HandleFunc("/api/overlay", a.handleOverlay)
	mux.HandleFunc("/api/overlay/state", a.handleOverlayState)
	mux.HandleFunc("/api/openlog", a.localOnly(a.handleOpenLog))
	mux.HandleFunc("/api/update", a.localOnly(a.handleUpdate))
	mux.HandleFunc("/api/audiosessions", a.localOnly(a.handleAudioSessions))
	mux.HandleFunc("/api/openmixer", a.localOnly(a.handleOpenMixer))
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

// decodeOn liest {"on": bool} aus dem Body.
func decodeOn(w http.ResponseWriter, r *http.Request) (bool, bool) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return false, false
	}
	var body struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "ungültige Anfrage: "+err.Error())
		return false, false
	}
	return body.On, true
}

// handlePause hält den Stream an oder lässt ihn weiterlaufen.
func (a *App) handlePause(w http.ResponseWriter, r *http.Request) {
	on, ok := decodeOn(w, r)
	if !ok {
		return
	}
	if err := a.SetPaused(on); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

// handleTarget wechselt die Quelle – im laufenden Stream ohne Unterbrechung.
func (a *App) handleTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	var target capture.Target
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&target); err != nil {
		writeError(w, http.StatusBadRequest, "ungültiges Ziel: "+err.Error())
		return
	}
	if err := a.SetTarget(target); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

// handleAutostart schaltet den Start beim Anmelden um.
func (a *App) handleAutostart(w http.ResponseWriter, r *http.Request) {
	on, ok := decodeOn(w, r)
	if !ok {
		return
	}
	if err := setAutostart(on); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

// handleHello beantwortet dem Viewer (von jeder Seite aus) nur eine Frage:
// Streamt dieser PC gerade die Sitzung, die du ansehen willst? Dann muss der
// Viewer stumm bleiben – sonst nimmt der Systemton den eigenen Stream wieder
// auf und es koppelt sich ohrenbetäubend laut auf.
func (a *App) handleHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	room := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("room")))
	cfg := a.Config()
	writeJSON(w, http.StatusOK, map[string]any{"self": a.Streaming() && room != "" && room == cfg.Room, "platform": platform()})
}

// openCORS erlaubt Aufrufe von jeder Seite – der Viewer läuft unter der
// Tunnel-Adresse des Streamers, das Overlay aber auf diesem PC.
func openCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return false
	}
	return true
}

// handleOverlay blendet das Sprach-Overlay ein oder aus.
func (a *App) handleOverlay(w http.ResponseWriter, r *http.Request) {
	if !openCORS(w, r) {
		return
	}
	on, ok := decodeOn(w, r)
	if !ok {
		return
	}
	if !overlay.Supported() {
		writeError(w, http.StatusNotImplemented, "Overlay gibt es hier nur im Browser")
		return
	}
	overlay.Show(on)
	writeJSON(w, http.StatusOK, map[string]bool{"on": on})
}

// handleOverlayState nimmt den Zustand des Sprachkanals für das Overlay an.
func (a *App) handleOverlayState(w http.ResponseWriter, r *http.Request) {
	if !openCORS(w, r) {
		return
	}
	var st overlay.State
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&st); err != nil {
		writeError(w, http.StatusBadRequest, "ungültiger Zustand")
		return
	}
	overlay.Update(st)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdate spielt die neueste Version ein (POST).
func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	a.ApplyUpdate()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// handleOpenLog öffnet die Logdatei im Standard-Editor.
func (a *App) handleOpenLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	openURL(filepath.Join(config.Dir(), "share.log"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleQuit beendet die App – das nutzt eine neue Version beim Update.
func (a *App) handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go a.Quit()
}

// handleAudioSessions listet die Tonquellen auf und merkt sich, welche
// Programme nicht mitgestreamt werden sollen.
func (a *App) handleAudioSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions, err := audio.ListSessions()
		if sessions == nil {
			sessions = []audio.Session{}
		}
		a.mu.Lock()
		muted := append([]string(nil), a.cfg.MutedApps...)
		streaming := a.sess != nil
		a.mu.Unlock()
		resp := map[string]any{
			"sessions":  sessions,
			"muted":     muted,
			"streaming": streaming,
			"exclusive": excludeInCapture,
			"note":      audio.ExclusionNote(),
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		var body struct {
			Name  string `json:"name"`
			Muted bool   `json:"muted"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "ungültige Anfrage: "+err.Error())
			return
		}
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if name == "" {
			writeError(w, http.StatusBadRequest, "Programmname fehlt")
			return
		}
		cfg := a.Config()
		if excludeInCapture {
			// Windows kann genau ein Programm aus der Aufnahme nehmen.
			cfg.MutedApps = nil
			if body.Muted {
				cfg.MutedApps = []string{name}
			}
		} else {
			cfg.MutedApps = toggleString(cfg.MutedApps, name, body.Muted)
		}
		if err := a.SaveConfig(cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Nur während eines Streams sofort umsetzen – sonst würde man sich
		// selbst den Ton abdrehen.
		if a.Streaming() && !excludeInCapture {
			if err := audio.SetMuted(name, body.Muted); err != nil {
				writeJSON(w, http.StatusOK, map[string]string{"warning": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "nur GET oder POST")
	}
}

func toggleString(list []string, value string, add bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, v := range list {
		if strings.EqualFold(v, value) {
			continue
		}
		out = append(out, v)
	}
	if add {
		out = append(out, value)
	}
	return out
}

// handleOpenMixer öffnet die Ton-Mischpult-Oberfläche des Systems – dort
// lässt sich ein Programm dauerhaft "Von Aufnahme ausschließen".
func (a *App) handleOpenMixer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return
	}
	openMixer(a.log)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
