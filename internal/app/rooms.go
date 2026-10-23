package app

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/e2e"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/rendezvous"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/room"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/share"
)

/* Räume: "Wer zuerst da ist, betreibt."
 *
 * Beim Beitreten schlägt die App die Raum-ID nach (ntfy.sh) und prüft, ob
 * der Rechner dahinter den Raum wirklich noch betreibt. Läuft der Raum
 * nirgends, wird dieser PC zum Betreiber: Relay + Tunnel starten, Adresse
 * veröffentlichen. Haben zwei gleichzeitig übernommen, gewinnt der früheste
 * Eintrag – der spätere gibt ab, seine Teilnehmer ziehen um.
 * Fällt der Betreiber weg, sucht jeder nach einer kurzen Zufallspause neu;
 * der Erste übernimmt, die anderen finden ihn. */

type roomConn struct {
	ID      string
	Keys    e2e.Keys
	State   string // suche | betreibe | drin
	Relay   string // WebSocket-Adresse, die dieser PC benutzt
	Hosting bool
	Err     string
	stop    chan struct{}

	autoVoice, autoMuted, autoShare bool // einmalig nach dem Start
}

// RoomView beschreibt den Stand für das Fenster.
type RoomView struct {
	App       string        `json:"app"`
	Version   string        `json:"version"`
	Platform  string        `json:"platform"`
	Room      string        `json:"room,omitempty"`
	State     string        `json:"state,omitempty"`
	Relay     string        `json:"relay,omitempty"`
	Enc       string        `json:"enc,omitempty"`
	Auth      string        `json:"auth,omitempty"`
	Hosting   bool          `json:"hosting"`
	Origin    string        `json:"origin"`
	Error     string        `json:"error,omitempty"`
	Share     *share.Status `json:"share,omitempty"`
	Saved     []string      `json:"saved"`
	AutoVoice bool          `json:"auto_voice,omitempty"`
	AutoMuted bool          `json:"auto_muted,omitempty"`
	Autostart bool          `json:"autostart"`
	Config    config.Config `json:"config"`
	Update    any           `json:"update"`
}

// JoinRoom betritt einen Raum (oder legt ihn an – es gibt keinen
// Unterschied: Wer zuerst da ist, betreibt ihn).
func (a *App) JoinRoom(id, password string, remember bool) error {
	id = e2e.NormalizeRoom(id)
	if !room.ValidID(id) {
		return errors.New("Raum-ID: 2–32 Zeichen, a–z, 0–9 und -")
	}
	if len(password) < 6 {
		return errors.New("Passwort: mindestens 6 Zeichen")
	}
	keys, err := e2e.Derive(id, password)
	if err != nil {
		return err
	}
	if remember {
		c := a.Config()
		saved := config.SavedRoom{ID: id, Enc: hex.EncodeToString(keys.Enc), Auth: hex.EncodeToString(keys.Auth)}
		c.Rooms = append(forgetRoom(c.Rooms, id), saved)
		if err := a.SaveConfig(c); err != nil {
			return err
		}
	}
	a.enterRoom(id, keys, false, false, false)
	return nil
}

// JoinSaved betritt einen gemerkten Raum.
func (a *App) JoinSaved(id string) error {
	r, ok := a.Config().Room(id)
	if !ok {
		return errors.New("Raum ist nicht gemerkt")
	}
	enc, err1 := hex.DecodeString(r.Enc)
	auth, err2 := hex.DecodeString(r.Auth)
	if err1 != nil || err2 != nil || len(enc) != 32 {
		return errors.New("gemerkter Raum ist beschädigt")
	}
	a.enterRoom(r.ID, e2e.Keys{Enc: enc, Auth: auth}, false, false, false)
	return nil
}

// Forget löscht einen gemerkten Raum.
func (a *App) Forget(id string) error {
	c := a.Config()
	c.Rooms = forgetRoom(c.Rooms, id)
	if c.AutoJoin == id {
		c.AutoJoin = ""
	}
	return a.SaveConfig(c)
}

func forgetRoom(list []config.SavedRoom, id string) []config.SavedRoom {
	out := list[:0:0]
	for _, r := range list {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) enterRoom(id string, keys e2e.Keys, voice, muted, autoShare bool) {
	a.LeaveRoom()
	rc := &roomConn{ID: id, Keys: keys, State: "suche", stop: make(chan struct{}), autoVoice: voice, autoMuted: muted, autoShare: autoShare}
	a.mu.Lock()
	a.cur = rc
	a.mu.Unlock()
	a.log.Printf("Raum %q: suche…", id)
	go a.roomLoop(rc)
	a.changed()
}

// LeaveRoom verlässt den Raum. Betreibt dieser PC ihn, ziehen die anderen um.
func (a *App) LeaveRoom() {
	a.StopShare()
	a.mu.Lock()
	rc := a.cur
	a.cur = nil
	a.mu.Unlock()
	if rc == nil {
		return
	}
	close(rc.stop)
	if a.relay.Hosts(rc.ID) {
		a.relay.Unhost(rc.ID)
	}
	a.stopTunnel()
	a.log.Printf("Raum %q verlassen", rc.ID)
	a.changed()
}

func (a *App) current() *roomConn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur
}

func (a *App) hosting() bool {
	rc := a.current()
	return rc != nil && a.relay.Hosts(rc.ID)
}

// currentRelay ist die WebSocket-Adresse, über die dieser PC den Raum erreicht.
func (a *App) currentRelay() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur == nil {
		return ""
	}
	return a.cur.Relay
}

func (a *App) setRoom(rc *roomConn, state, relay string, hosting bool, errMsg string) {
	a.mu.Lock()
	changed := rc.State != state || rc.Relay != relay
	rc.State, rc.Relay, rc.Hosting, rc.Err = state, relay, hosting, errMsg
	a.mu.Unlock()
	if changed {
		a.changed()
	}
}

func wsURL(base string) string {
	base = strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + "/ws"
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + "/ws"
	}
	return base + "/ws"
}

var (
	healthClient = http.Client{Timeout: 5 * time.Second}
	lanClient    = http.Client{Timeout: 1500 * time.Millisecond}
)

// alive prüft, ob unter base der Raum wirklich betrieben wird – und zwar
// vom Programmstart host (leer: egal von wem).
func alive(c *http.Client, base, id, host string) bool {
	res, err := c.Get(strings.TrimRight(base, "/") + "/healthz?room=" + id)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 128))
	return res.StatusCode == http.StatusOK && (host == "" || string(body) == host)
}

// findHost sucht den frühesten erreichbaren Betreiber (außer self) und die
// Adresse, über die er zu erreichen ist – im selben Netzwerk direkt.
func findHost(id, self string) (rendezvous.Entry, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := rendezvous.Entries(ctx, id)
	if err != nil {
		return rendezvous.Entry{}, "", false
	}
	for _, e := range entries {
		if self != "" && e.Host == self {
			continue
		}
		if e.LAN != "" && alive(&lanClient, e.LAN, id, e.Host) {
			return e, e.LAN, true
		}
		if e.URL != "" && alive(&healthClient, e.URL, id, e.Host) {
			return e, e.URL, true
		}
	}
	return rendezvous.Entry{}, "", false
}

func (a *App) sleep(rc *roomConn, d time.Duration) bool {
	select {
	case <-rc.stop:
		return false
	case <-time.After(d):
		return true
	}
}

// roomLoop hält die Verbindung zum Raum – egal, wer ihn gerade betreibt.
func (a *App) roomLoop(rc *roomConn) {
	for {
		select {
		case <-rc.stop:
			return
		default:
		}
		if e, base, ok := findHost(rc.ID, a.origin); ok {
			a.setRoom(rc, "drin", wsURL(base), false, "")
			how := "über das Internet"
			if base == e.LAN {
				how = "direkt im Netzwerk"
			}
			a.log.Printf("Raum %q: betrieben von %s (%s)", rc.ID, base, how)
			a.afterConnect(rc)
			// Läuft der Betreiber noch? Zweimal hintereinander weg = neu suchen.
			misses := 0
			for misses < 2 {
				if !a.sleep(rc, 3*time.Second) {
					return
				}
				if alive(&healthClient, base, rc.ID, e.Host) {
					misses = 0
				} else {
					misses++
				}
			}
			a.log.Printf("Raum %q: Betreiber ist weg, suche neu", rc.ID)
			a.setRoom(rc, "suche", "", false, "")
			// Zufallspause: Nicht alle sollen gleichzeitig übernehmen.
			if !a.sleep(rc, time.Duration(500+rand.Intn(2500))*time.Millisecond) {
				return
			}
			continue
		}
		if err := a.hostRoom(rc); err != nil {
			a.setRoom(rc, "suche", "", false, err.Error())
			a.log.Printf("Raum %q betreiben: %v", rc.ID, err)
			if !a.sleep(rc, 5*time.Second) {
				return
			}
		}
	}
}

// hostRoom betreibt den Raum auf diesem PC, bis jemand Früheres auftaucht
// oder der Raum verlassen wird.
func (a *App) hostRoom(rc *roomConn) error {
	port, err := a.startRelay()
	if err != nil {
		return err
	}
	a.relay.Host(rc.ID, e2e.AuthHash(rc.Keys.Auth))
	a.ensureReach()
	local := wsURL("http://127.0.0.1:" + itoa(port))
	a.setRoom(rc, "betreibe", local, true, "")
	a.afterConnect(rc)
	defer func() {
		if a.relay.Hosts(rc.ID) {
			a.relay.Unhost(rc.ID)
		}
	}()

	var published rendezvous.Entry
	ourTime := int64(0)
	for {
		// Adressen (neu) veröffentlichen, sobald sie stehen oder sich ändern.
		if e := a.published(); (e.URL != "" || e.LAN != "") && e != published {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			t, err := rendezvous.Publish(ctx, rc.ID, e)
			cancel()
			if err == nil {
				published = e
				if ourTime == 0 {
					ourTime = t
				}
				a.log.Printf("Raum %q: auffindbar unter %s %s", rc.ID, e.URL, e.LAN)
			}
		}
		if !a.sleep(rc, 4*time.Second) {
			return nil
		}
		// Hat jemand früher übernommen? Dann abgeben und dort mitmachen.
		if ourTime != 0 {
			if e, base, ok := findHost(rc.ID, a.origin); ok && e.Time < ourTime {
				a.log.Printf("Raum %q: %s war früher da – gebe ab", rc.ID, base)
				return nil
			}
		}
	}
}

// afterConnect setzt einmalig die Wünsche vom Start um (Bildschirm teilen).
func (a *App) afterConnect(rc *roomConn) {
	a.mu.Lock()
	doShare := rc.autoShare
	rc.autoShare = false
	a.mu.Unlock()
	if doShare {
		go func() {
			time.Sleep(2 * time.Second)
			if err := a.StartShare(a.Config().Target); err != nil {
				a.log.Printf("Automatisch teilen: %v", err)
			}
		}()
	}
}

// RoomStatus beschreibt den Stand für das Fenster (enthält die Schlüssel –
// geht nur an das lokale Fenster).
func (a *App) RoomStatus() RoomView {
	a.mu.Lock()
	cfg := a.cfg
	rc := a.cur
	sh := a.share
	v := RoomView{Origin: a.origin, App: "mss", Version: versionString(), Platform: platform(), Config: cfg, Autostart: autostartEnabled()}
	for _, r := range cfg.Rooms {
		v.Saved = append(v.Saved, r.ID)
	}
	if rc != nil {
		v.Room, v.State, v.Relay, v.Hosting, v.Error = rc.ID, rc.State, rc.Relay, rc.Hosting, rc.Err
		v.Enc, v.Auth = hex.EncodeToString(rc.Keys.Enc), hex.EncodeToString(rc.Keys.Auth)
		v.AutoVoice, v.AutoMuted = rc.autoVoice, rc.autoMuted
	}
	a.mu.Unlock()
	if sh != nil {
		st := sh.Status()
		v.Share = &st
	}
	v.Update = updateStatus()
	return v
}

// ConsumeAutoVoice: Das Fenster ist dem Sprachkanal beigetreten.
func (a *App) ConsumeAutoVoice() {
	a.mu.Lock()
	if a.cur != nil {
		a.cur.autoVoice = false
	}
	a.mu.Unlock()
}

/* ---------- Bildschirm teilen ---------- */

// StartShare teilt den Bildschirm im aktuellen Raum – oder wechselt die Quelle.
func (a *App) StartShare(target capture.Target) error {
	rc := a.current()
	if rc == nil {
		return errors.New("du bist in keinem Raum")
	}
	target = resolveTarget(target)
	a.mu.Lock()
	sh := a.share
	a.cfg.Target = target
	cfg := a.cfg
	a.mu.Unlock()
	cfg.Save()
	if sh != nil {
		sh.SetTarget(target)
		return nil
	}
	var s *share.Share
	s, err := share.Start(share.Options{
		Relay: a.currentRelay, Room: rc.ID, Keys: rc.Keys, Owner: cfg.Name, Target: target,
		FPS: cfg.FPS, BitrateKbps: cfg.Bitrate, Audio: cfg.Audio, AudioExclude: a.audioExclude, Log: a.log, Point: cfg.Pointing, Origin: a.origin,
		OnPoint: func(x, y float64, from string) { a.pointAt(s.Target(), x, y, from) },
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.share = s
	a.mu.Unlock()
	a.changed()
	return nil
}

// StopShare beendet das Teilen.
func (a *App) StopShare() {
	a.mu.Lock()
	s := a.share
	a.share = nil
	a.mu.Unlock()
	if s != nil {
		s.Stop()
		a.changed()
	}
}

// SharePaused hält das geteilte Bild an oder lässt es weiterlaufen.
func (a *App) SharePaused(on bool) {
	a.mu.Lock()
	s := a.share
	a.mu.Unlock()
	if s != nil {
		s.SetPaused(on)
	}
}

// ShareAudio schaltet den Systemton beim Teilen.
func (a *App) ShareAudio(on bool) {
	c := a.Config()
	c.Audio = on
	a.SaveConfig(c)
	a.mu.Lock()
	s := a.share
	a.mu.Unlock()
	if s != nil {
		s.SetAudio(on)
	}
}

// Sharing sagt, ob gerade geteilt wird.
func (a *App) Sharing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.share != nil
}

// resolveTarget macht aus "primary" den echten Monitor (Hyprland: den
// fokussierten, sonst den Hauptmonitor).
func resolveTarget(t capture.Target) capture.Target {
	if t.Kind != "primary" && t.Kind != "" {
		return t
	}
	p := capture.Primary()
	p.Cursor = t.Cursor
	return p
}

// pointAt zeigt einen Zeige-Klick an der passenden Stelle des Bildschirms.
func (a *App) pointAt(target capture.Target, x, y float64, name string) {
	if !a.Config().Pointing {
		return
	}
	r, err := capture.Bounds(target)
	if err != nil || r.W <= 0 || r.H <= 0 {
		return
	}
	showPointer(r.X+int(x*float64(r.W)), r.Y+int(y*float64(r.H)), name, a.log)
}

// autoStart setzt "Beim Start" um: gemerkten Raum betreten, Fenster öffnen,
// teilen, Sprachkanal.
func (a *App) autoStart() {
	cfg := a.Config()
	if cfg.AutoJoin == "" {
		return
	}
	r, ok := cfg.Room(cfg.AutoJoin)
	if !ok {
		return
	}
	enc, _ := hex.DecodeString(r.Enc)
	auth, _ := hex.DecodeString(r.Auth)
	a.enterRoom(r.ID, e2e.Keys{Enc: enc, Auth: auth}, cfg.AutoVoice, cfg.AutoMuted, cfg.AutoShare)
	if cfg.AutoVoice {
		a.OpenWindow() // der Sprachkanal läuft im Fenster
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
