package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

func newTestServer(t *testing.T, grace time.Duration, maxViewers int) *httptest.Server {
	t.Helper()
	srv := New(Config{
		MaxViewers: maxViewers,
		HostGrace:  grace,
		Log:        log.New(io.Discard, "", 0),
	})
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	ts := httptest.NewServer(srv.Handler(static))
	t.Cleanup(ts.Close)
	return ts
}

type peer struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, ts *httptest.Server, room, role string) *peer {
	t.Helper()
	return dialToken(t, ts, room, role, "")
}

func dialToken(t *testing.T, ts *httptest.Server, room, role, token string) *peer {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	join := proto.Control{T: "join", Room: room, Role: role, Name: role, Token: token}
	if err := conn.WriteJSON(join); err != nil {
		t.Fatalf("Join senden: %v", err)
	}
	return &peer{t: t, conn: conn}
}

// waitControl liest, bis eine Steuernachricht mit passendem T auftaucht.
func (p *peer) waitControl(want string) proto.Control {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.conn.SetReadDeadline(deadline)
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			p.t.Fatalf("warte auf %q: %v", want, err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var ctl proto.Control
		if err := json.Unmarshal(data, &ctl); err != nil {
			continue
		}
		if ctl.T == want {
			return ctl
		}
	}
}

// waitBinary liest die nächste Binärnachricht.
func (p *peer) waitBinary() []byte {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			p.t.Fatalf("warte auf Binärpaket: %v", err)
		}
		if mt == websocket.BinaryMessage {
			return data
		}
	}
}

func (p *peer) send(ctl proto.Control) {
	p.t.Helper()
	if err := p.conn.WriteJSON(ctl); err != nil {
		p.t.Fatalf("senden: %v", err)
	}
}

// sync schickt einen Ping und wartet auf das Pong: Damit ist sicher, dass
// der Server alle vorherigen Nachrichten verarbeitet hat.
func (p *peer) sync() {
	p.t.Helper()
	p.send(proto.Control{T: "ping", TS: 1})
	p.waitControl("pong")
}

func TestRelayFlowWithApproval(t *testing.T) {
	ts := newTestServer(t, 300*time.Millisecond, 8)

	host := dial(t, ts, "test-raum", "host")
	host.waitControl("joined")

	viewer := dial(t, ts, "test-raum", "viewer")
	viewer.waitControl("joined")

	// Ohne Freigabe kommt nichts durch.
	knock := host.waitControl("knock")
	if knock.Name != "viewer" {
		t.Fatalf("Anfrage von %q statt viewer", knock.Name)
	}
	host.send(proto.Control{T: "allow", ID: knock.ID})
	viewer.waitControl("allowed")

	if ctl := host.waitControl("viewers"); ctl.N != 1 || len(ctl.Names) != 1 || ctl.Names[0] != "viewer" {
		t.Fatalf("Zuschauerstand falsch: %+v", ctl)
	}

	frame := proto.FramePacket(1, 42, 4, 4, []byte{0xFF, 0xD8, 0xFF, 0xD9})
	if err := host.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	if got := viewer.waitBinary(); !bytes.Equal(got, frame) {
		t.Fatalf("Frame nicht durchgereicht")
	}

	host.send(proto.Control{T: "meta", W: 100, H: 50, FPS: 15})
	if ctl := viewer.waitControl("meta"); ctl.W != 100 || ctl.H != 50 {
		t.Fatalf("Meta nicht durchgereicht: %+v", ctl)
	}

	// Host fällt weg: Viewer bekommt Reconnect-Fenster, dann Ende.
	host.conn.Close()
	viewer.waitControl("reconnecting")
	viewer.waitControl("ended")
}

func TestDeny(t *testing.T) {
	ts := newTestServer(t, time.Second, 8)

	host := dial(t, ts, "deny-raum", "host")
	host.waitControl("joined")

	viewer := dial(t, ts, "deny-raum", "viewer")
	viewer.waitControl("joined")

	knock := host.waitControl("knock")
	host.send(proto.Control{T: "deny", ID: knock.ID})
	viewer.waitControl("denied")
}

func TestAutoAccept(t *testing.T) {
	ts := newTestServer(t, time.Second, 8)

	host := dial(t, ts, "auto-raum", "host")
	host.waitControl("joined")
	host.send(proto.Control{T: "auto", On: true})
	host.sync()

	viewer := dial(t, ts, "auto-raum", "viewer")
	viewer.waitControl("joined")
	viewer.waitControl("allowed")

	if ctl := host.waitControl("viewers"); ctl.N != 1 {
		t.Fatalf("Zuschauerstand nach Auto-Freigabe: %+v", ctl)
	}
}

func TestPendingSurvivesHostReconnect(t *testing.T) {
	ts := newTestServer(t, 2*time.Second, 8)

	host := dial(t, ts, "warte-raum", "host")
	host.waitControl("joined")

	viewer := dial(t, ts, "warte-raum", "viewer")
	viewer.waitControl("joined")
	host.waitControl("knock")

	host.conn.Close()
	viewer.waitControl("reconnecting")

	host2 := dial(t, ts, "warte-raum", "host")
	host2.waitControl("joined")
	viewer.waitControl("waiting")

	// Die offene Anfrage wird dem neuen Host erneut gemeldet.
	knock := host2.waitControl("knock")
	host2.send(proto.Control{T: "allow", ID: knock.ID})
	viewer.waitControl("allowed")
}

// waitViewers wartet auf einen Zuschauerstand mit genau dieser Zahl.
func (p *peer) waitViewers(n int) proto.Control {
	p.t.Helper()
	for {
		ctl := p.waitControl("viewers")
		if ctl.N == n {
			return ctl
		}
	}
}

func TestViewerTokenSkipsSecondApproval(t *testing.T) {
	ts := newTestServer(t, time.Second, 8)

	host := dial(t, ts, "token-raum", "host")
	host.waitControl("joined")

	const token = "0123456789abcdef"
	viewer := dialToken(t, ts, "token-raum", "viewer", token)
	viewer.waitControl("joined")
	knock := host.waitControl("knock")
	host.send(proto.Control{T: "allow", ID: knock.ID})
	viewer.waitControl("allowed")

	// Verbindung reißt ab ...
	viewer.conn.Close()
	host.waitViewers(0)

	// ... und darf mit demselben Token ohne neue Freigabe wieder rein.
	viewer2 := dialToken(t, ts, "token-raum", "viewer", token)
	viewer2.waitControl("joined")
	viewer2.waitControl("allowed")
	if ctl := host.waitViewers(1); len(ctl.Names) != 1 || ctl.Names[0] != "viewer" {
		t.Fatalf("Zuschauer nach Token-Reconnect: %+v", ctl)
	}

	// Ein fremdes Token bekommt keine Freigabe.
	stranger := dialToken(t, ts, "token-raum", "viewer", "fremdes-token")
	stranger.waitControl("joined")
	if knock2 := host.waitControl("knock"); knock2.Name != "viewer" {
		t.Fatalf("fremder Zuschauer sollte klopfen: %+v", knock2)
	}
}

func TestJoinErrors(t *testing.T) {
	ts := newTestServer(t, time.Second, 1)

	bad := dial(t, ts, "GROSS BUCHSTABEN", "host")
	if ctl := bad.waitControl("error"); ctl.Msg == "" {
		t.Fatal("erwarte Fehlermeldung für ungültige Sitzungs-ID")
	}

	host := dial(t, ts, "voll", "host")
	host.waitControl("joined")

	second := dial(t, ts, "voll", "host")
	if ctl := second.waitControl("error"); !strings.Contains(ctl.Msg, "belegt") {
		t.Fatalf("erwarte 'belegt', bekam %q", ctl.Msg)
	}

	viewer := dial(t, ts, "voll", "viewer")
	viewer.waitControl("joined")
	knock := host.waitControl("knock")
	host.send(proto.Control{T: "allow", ID: knock.ID})
	viewer.waitControl("allowed")

	full := dial(t, ts, "voll", "viewer")
	if ctl := full.waitControl("error"); !strings.Contains(ctl.Msg, "voll") {
		t.Fatalf("erwarte 'voll', bekam %q", ctl.Msg)
	}

	missing := dial(t, ts, "gibtsnicht", "viewer")
	if ctl := missing.waitControl("error"); !strings.Contains(ctl.Msg, "nicht gefunden") {
		t.Fatalf("erwarte 'nicht gefunden', bekam %q", ctl.Msg)
	}
}
