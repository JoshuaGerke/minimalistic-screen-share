package room

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/e2e"
)

type peer struct {
	t    *testing.T
	conn *websocket.Conn
	box  *e2e.Box
	id   uint32
}

func setup(t *testing.T) (*Server, *httptest.Server, e2e.Keys) {
	t.Helper()
	s := New(log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	keys, _ := e2e.Derive("testraum", "passwort")
	s.Host("testraum", e2e.AuthHash(keys.Auth))
	return s, ts, keys
}

func connect(t *testing.T, ts *httptest.Server, room string, auth []byte, enc []byte) (*peer, Ctl) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.WriteJSON(Ctl{T: "join", Room: room, Auth: hex.EncodeToString(auth), Kind: "person"})
	p := &peer{t: t, conn: conn}
	p.box, _ = e2e.NewBox(enc)
	first := p.ctl("")
	p.id = first.ID
	return p, first
}

// ctl liest bis zur nächsten Steuernachricht (want leer = irgendeine).
func (p *peer) ctl(want string) Ctl {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			p.t.Fatalf("warte auf %q: %v", want, err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var c Ctl
		json.Unmarshal(data, &c)
		if want == "" || c.T == want {
			return c
		}
	}
}

// packet liest das nächste verschlüsselte Paket und öffnet es.
func (p *peer) packet() (e2e.Header, string) {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			p.t.Fatalf("warte auf Paket: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		h, plain, err := p.box.Open(data)
		if err != nil {
			p.t.Fatalf("Paket nicht lesbar: %v", err)
		}
		return h, string(plain)
	}
}

func (p *peer) send(h e2e.Header, text string) {
	p.conn.WriteMessage(websocket.BinaryMessage, p.box.Seal(h, []byte(text)))
}

func TestRoom(t *testing.T) {
	_, ts, keys := setup(t)

	// Falsches Passwort bleibt draußen.
	wrong, _ := e2e.Derive("testraum", "falsch")
	if _, c := connect(t, ts, "testraum", wrong.Auth, wrong.Enc); c.T != "error" || c.Msg != "Falsches Passwort" {
		t.Fatalf("falsches Passwort: %+v", c)
	}
	// Raum, den dieser PC nicht betreibt.
	if _, c := connect(t, ts, "anderer", keys.Auth, keys.Enc); c.T != "error" || c.Msg != "Raum nicht hier" {
		t.Fatalf("fremder Raum: %+v", c)
	}

	a, wa := connect(t, ts, "TestRaum", keys.Auth, keys.Enc)
	if wa.T != "welcome" || a.id == 0 {
		t.Fatalf("kein welcome: %+v", wa)
	}
	b, wb := connect(t, ts, "testraum", keys.Auth, keys.Enc)
	if len(wb.Members) != 1 || wb.Members[0].ID != a.id {
		t.Fatalf("Teilnehmerliste falsch: %+v", wb)
	}
	if j := a.ctl("join"); j.ID != b.id {
		t.Fatalf("join falsch: %+v", j)
	}

	// Chat: Absender stempelt der Relay, Inhalt bleibt lesbar nur mit Schlüssel.
	a.send(e2e.Header{Kind: e2e.KindChat, Seq: 1}, `{"text":"hallo"}`)
	a.send(e2e.Header{Kind: e2e.KindState}, `{"name":"Anna"}`)
	if h, txt := b.packet(); h.Sender != a.id || txt != `{"text":"hallo"}` {
		t.Fatalf("Chat falsch: %+v %q", h, txt)
	}
	b.packet()

	// Bild: Delta vor dem ersten Keyframe wird nicht gecacht.
	a.send(e2e.Header{Kind: e2e.KindVideo, Seq: 1}, "delta-alt")
	a.send(e2e.Header{Kind: e2e.KindVideo, Flags: e2e.FlagKey, Seq: 2, Count: 1}, "key")
	a.send(e2e.Header{Kind: e2e.KindVideo, Seq: 3, Count: 1}, "delta")
	time.Sleep(100 * time.Millisecond)

	// Nachzügler: bekommt Zustand, Chatverlauf und Bild ab dem Keyframe.
	c, _ := connect(t, ts, "testraum", keys.Auth, keys.Enc)
	got := map[string]bool{}
	var order []string
	for i := 0; i < 4; i++ {
		_, txt := c.packet()
		got[txt] = true
		order = append(order, txt)
	}
	for _, want := range []string{`{"name":"Anna"}`, `{"text":"hallo"}`, "key", "delta"} {
		if !got[want] {
			t.Fatalf("Nachzügler: %q fehlt (bekommen: %v)", want, order)
		}
	}
	if got["delta-alt"] {
		t.Fatal("Bild vor dem Keyframe nachgereicht")
	}

	// Gehen: die anderen erfahren es.
	a.conn.Close()
	if l := b.ctl("leave"); l.ID != a.id {
		t.Fatalf("leave falsch: %+v", l)
	}
}

func TestHealthzAndMoved(t *testing.T) {
	s, ts, keys := setup(t)
	check := func(room string) int {
		res, err := http.Get(ts.URL + "/healthz?room=" + room)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if check("testraum") != 200 || check("gibtsnicht") != 404 {
		t.Fatal("healthz falsch")
	}
	p, _ := connect(t, ts, "testraum", keys.Auth, keys.Enc)
	s.Unhost("testraum")
	if c := p.ctl("moved"); c.T != "moved" {
		t.Fatal("kein moved")
	}
	if check("testraum") != 404 {
		t.Fatal("Raum nach Unhost noch da")
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{"fuchs-7k2m": true, "ab": true, "a": false, "-ab": false, "Ab C": false, "ümlaut": false} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
}
