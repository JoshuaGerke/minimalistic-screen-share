package app

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/audio"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/overlay"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/update"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/web"
)

func versionString() string { return version.Version }
func updateStatus() any     { return update.Status() }

// Handler liefert das Raum-Fenster und seine lokale API. Alles hier ist nur
// für dieses Fenster gedacht – der Status enthält die Raum-Schlüssel.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	api := map[string]http.HandlerFunc{
		"/api/status":        a.handleStatus,
		"/api/config":        a.handleConfig,
		"/api/targets":       a.handleTargets,
		"/api/join":          a.handleJoin,
		"/api/join-saved":    a.handleJoinSaved,
		"/api/forget":        a.handleForget,
		"/api/leave":         post(func() { a.LeaveRoom() }),
		"/api/share/start":   a.handleShareStart,
		"/api/share/stop":    post(a.StopShare),
		"/api/share/pause":   onOff(a.SharePaused),
		"/api/share/audio":   onOff(a.ShareAudio),
		"/api/voice-joined":  post(a.ConsumeAutoVoice),
		"/api/autostart":     a.handleAutostart,
		"/api/overlay":       onOff(func(on bool) { overlay.Show(on && overlay.Supported()) }),
		"/api/overlay/state": a.handleOverlayState,
		"/api/update":        post(a.ApplyUpdate),
		"/api/openlog":       post(func() { openURL(filepath.Join(config.Dir(), "share.log")) }),
		"/api/audiosessions": a.handleAudioSessions,
		"/api/openmixer":     post(func() { openMixer(a.log) }),
		"/api/show":          post(a.OpenWindow),
		"/api/quit":          post(func() { go a.Quit() }),
	}
	for path, h := range api {
		mux.HandleFunc(path, localOnly(h))
	}
	files := http.FileServerFS(web.FS())
	mux.Handle("/", localOnly(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			r = r.Clone(r.Context())
			r.URL.Path = "/room.html"
		}
		files.ServeHTTP(w, r)
	}))
	return mux
}

// localOnly lässt nur das eigene Fenster durch: Host muss 127.0.0.1 sein
// (gegen DNS-Rebinding), ein Origin ebenfalls.
func localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			writeError(w, http.StatusForbidden, "nur lokal")
			return
		}
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

// post macht aus einer Aktion ohne Eingabe einen POST-Handler.
func post(fn func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "nur POST")
			return
		}
		fn()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// onOff macht aus einem Schalter einen POST-Handler für {"on": bool}.
func onOff(fn func(bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if on, ok := decodeOn(w, r); ok {
			fn(on)
			writeJSON(w, http.StatusOK, map[string]bool{"on": on})
		}
	}
}

// decode liest einen POST-Body als JSON.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "nur POST")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "ungültige Anfrage: "+err.Error())
		return false
	}
	return true
}

func decodeOn(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body struct {
		On bool `json:"on"`
	}
	ok := decode(w, r, &body)
	return body.On, ok
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.RoomStatus())
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, a.Config())
		return
	}
	var c config.Config
	if !decode(w, r, &c) {
		return
	}
	// Gemerkte Räume ändert nur Beitreten/Vergessen.
	c.Rooms = a.Config().Rooms
	if err := a.SaveConfig(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Config())
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
	writeJSON(w, http.StatusOK, map[string]any{"platform": runtime.GOOS, "monitors": monitors, "windows": windows})
}

func (a *App) handleJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Room     string `json:"room"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.JoinRoom(body.Room, body.Password, body.Remember); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.RoomStatus())
}

func (a *App) handleJoinSaved(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Room string `json:"room"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.JoinSaved(body.Room); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.RoomStatus())
}

func (a *App) handleForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Room string `json:"room"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.Forget(body.Room); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.RoomStatus())
}

func (a *App) handleShareStart(w http.ResponseWriter, r *http.Request) {
	var target capture.Target
	if !decode(w, r, &target) {
		return
	}
	if err := a.StartShare(target); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.RoomStatus())
}

func (a *App) handleAutostart(w http.ResponseWriter, r *http.Request) {
	on, ok := decodeOn(w, r)
	if !ok {
		return
	}
	if err := setAutostart(on); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": on})
}

// handleOverlayState nimmt den Zustand des Sprachkanals für das Overlay an.
func (a *App) handleOverlayState(w http.ResponseWriter, r *http.Request) {
	var st overlay.State
	if !decode(w, r, &st) {
		return
	}
	overlay.Update(st)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAudioSessions listet die Tonquellen auf und merkt sich, welches
// Programm nicht mitgeteilt werden soll.
func (a *App) handleAudioSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sessions, err := audio.ListSessions()
		if sessions == nil {
			sessions = []audio.Session{}
		}
		resp := map[string]any{
			"sessions":  sessions,
			"muted":     a.Config().MutedApps,
			"exclusive": excludeInCapture,
			"note":      audio.ExclusionNote(),
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Muted bool   `json:"muted"`
	}
	if !decode(w, r, &body) {
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
	// Linux: nur während des Teilens stumm schalten – sonst dreht man sich
	// selbst den Ton ab.
	if a.Sharing() && !excludeInCapture {
		if err := audio.SetMuted(name, body.Muted); err != nil {
			writeJSON(w, http.StatusOK, map[string]string{"warning": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func toggleString(list []string, value string, add bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, v := range list {
		if !strings.EqualFold(v, value) {
			out = append(out, v)
		}
	}
	if add {
		out = append(out, value)
	}
	return out
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
