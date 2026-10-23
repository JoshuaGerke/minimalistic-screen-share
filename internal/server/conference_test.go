package server

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

// noBinary prüft, dass bis zum Pong kein Bild/Ton ankommt.
func (p *peer) noBinary(what string) {
	p.t.Helper()
	p.send(proto.Control{T: "ping", TS: 99})
	p.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			p.t.Fatal(err)
		}
		if mt == websocket.BinaryMessage {
			p.t.Fatalf("%s: bekommt trotzdem Bild/Ton (Typ %d)", what, data[0])
		}
		if string(data) == `{"t":"pong","ts":99}` {
			return
		}
	}
}

func TestConference(t *testing.T) {
	ts := newTestServer(t, time.Second, 8)
	host := dial(t, ts, "konferenz", "host")
	host.waitControl("joined")
	host.send(proto.Control{T: "auto", On: true})
	host.send(proto.Control{T: "voicekey", Token: "host-schluessel-123456"})
	host.sync()

	anna := dialToken(t, ts, "konferenz", "viewer", "token-anna")
	anna.waitControl("allowed")
	ben := dialToken(t, ts, "konferenz", "viewer", "token-ben")
	ben.waitControl("allowed")
	hostView := dialToken(t, ts, "konferenz", "viewer", "host-schluessel-123456")
	hostView.waitControl("allowed")

	// Ohne Freigabe darf niemand teilen.
	fremd := dialToken(t, ts, "konferenz", "presenter", "unbekannt")
	fremd.waitControl("error")

	// Anna teilt über ihre App.
	pres := dialToken(t, ts, "konferenz", "presenter", "token-anna")
	presID := pres.waitControl("joined").ID
	if ctl := ben.waitControl("sources"); len(ctl.Members) != 1 || ctl.Members[0].ID != presID {
		t.Fatalf("Ben sieht die Quelle nicht: %+v", ctl)
	}
	pres.send(proto.Control{T: "meta", FPS: 30})
	if ctl := ben.waitControl("meta"); ctl.Src != presID {
		t.Fatalf("meta ohne Quelle: %+v", ctl)
	}
	for _, p := range proto.VideoChunks(1, 0, true, []byte{0, 0, 0, 1, 0x65, 1}, 1024) {
		pres.conn.WriteMessage(websocket.BinaryMessage, p)
	}
	for _, v := range []*peer{ben, hostView} {
		got := v.waitBinary()
		if got[0] != proto.MsgSourced || uint64(binary.LittleEndian.Uint32(got[1:])) != presID || got[5] != proto.MsgVideoChunk {
			t.Fatalf("Bild falsch verpackt: % x", got[:8])
		}
	}
	anna.noBinary("Anna (sieht ihr eigenes Bild)")

	// Das Bild des Hosts geht nicht an die Host-Ansicht.
	host.conn.WriteMessage(websocket.BinaryMessage, proto.FramePacket(1, 0, 2, 2, []byte{1}))
	ben.waitBinary()
	hostView.noBinary("Host-Ansicht (eigenes Bild)")

	// Zeige-Klicks: an den Teilenden bzw. den Host, gedrosselt.
	ben.send(proto.Control{T: "point", Src: presID, X: 0.5, Y: 0.25})
	if ctl := pres.waitControl("ping"); ctl.X != 0.5 || ctl.Y != 0.25 || ctl.Name != "viewer" {
		t.Fatalf("Zeige-Klick falsch: %+v", ctl)
	}
	anna.send(proto.Control{T: "point", X: 0.1, Y: 0.9})
	anna.send(proto.Control{T: "point", X: 0.2, Y: 0.8}) // zu schnell – fällt weg
	if ctl := host.waitControl("ping"); ctl.X != 0.1 {
		t.Fatalf("Zeige-Klick an Host falsch: %+v", ctl)
	}
	host.send(proto.Control{T: "ping", TS: 5})
	host.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var ctl proto.Control
		if err := host.conn.ReadJSON(&ctl); err != nil {
			t.Fatal(err)
		}
		if ctl.T == "ping" {
			t.Fatal("Drosselung greift nicht")
		}
		if ctl.T == "pong" {
			break
		}
	}

	// Anna hört auf: Ben erfährt es.
	pres.conn.Close()
	if ctl := ben.waitControl("source-gone"); ctl.Src != presID {
		t.Fatalf("source-gone falsch: %+v", ctl)
	}
}
