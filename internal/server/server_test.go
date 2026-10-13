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
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(proto.Control{T: "join", Room: room, Role: role, Name: role}); err != nil {
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

func TestRelayFlow(t *testing.T) {
	ts := newTestServer(t, 300*time.Millisecond, 8)

	host := dial(t, ts, "test-raum", "host")
	host.waitControl("joined")

	viewer := dial(t, ts, "test-raum", "viewer")
	viewer.waitControl("joined")

	if ctl := host.waitControl("viewers"); ctl.N != 1 {
		t.Fatalf("Zuschauerstand: %d, erwartet 1", ctl.N)
	}

	frame := proto.FramePacket(1, 42, 4, 4, []byte{0xFF, 0xD8, 0xFF, 0xD9})
	if err := host.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	if got := viewer.waitBinary(); !bytes.Equal(got, frame) {
		t.Fatalf("Frame nicht durchgereicht")
	}

	if err := host.conn.WriteJSON(proto.Control{T: "meta", W: 100, H: 50, FPS: 15}); err != nil {
		t.Fatal(err)
	}
	if ctl := viewer.waitControl("meta"); ctl.W != 100 || ctl.H != 50 {
		t.Fatalf("Meta nicht durchgereicht: %+v", ctl)
	}

	// Host fällt weg: Viewer bekommt Reconnect-Fenster, dann Ende.
	host.conn.Close()
	viewer.waitControl("reconnecting")
	viewer.waitControl("ended")
}

func TestHostResume(t *testing.T) {
	ts := newTestServer(t, 2*time.Second, 8)

	host := dial(t, ts, "resume", "host")
	host.waitControl("joined")
	viewer := dial(t, ts, "resume", "viewer")
	viewer.waitControl("joined")

	host.conn.Close()
	viewer.waitControl("reconnecting")

	host2 := dial(t, ts, "resume", "host")
	host2.waitControl("joined")
	viewer.waitControl("live")

	if ctl := host2.waitControl("viewers"); ctl.N != 1 {
		t.Fatalf("Zuschauerstand nach Reconnect: %d", ctl.N)
	}
}

func TestJoinErrors(t *testing.T) {
	ts := newTestServer(t, time.Second, 1)

	bad := dial(t, ts, "GROSS BUCHSTABEN", "host")
	if ctl := bad.waitControl("error"); ctl.Msg == "" {
		t.Fatal("erwarte Fehlermeldung für ungültigen Raumcode")
	}

	host := dial(t, ts, "voll", "host")
	host.waitControl("joined")

	second := dial(t, ts, "voll", "host")
	if ctl := second.waitControl("error"); !strings.Contains(ctl.Msg, "belegt") {
		t.Fatalf("erwarte 'belegt', bekam %q", ctl.Msg)
	}

	viewer := dial(t, ts, "voll", "viewer")
	viewer.waitControl("joined")

	full := dial(t, ts, "voll", "viewer")
	if ctl := full.waitControl("error"); !strings.Contains(ctl.Msg, "voll") {
		t.Fatalf("erwarte 'voll', bekam %q", ctl.Msg)
	}

	missing := dial(t, ts, "gibtsnicht", "viewer")
	if ctl := missing.waitControl("error"); !strings.Contains(ctl.Msg, "nicht gefunden") {
		t.Fatalf("erwarte 'nicht gefunden', bekam %q", ctl.Msg)
	}
}
