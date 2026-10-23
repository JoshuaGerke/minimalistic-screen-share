package server

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

func TestVoiceChannel(t *testing.T) {
	ts := newTestServer(t, time.Second, 8)
	host := dial(t, ts, "voice-raum", "host")
	host.waitControl("joined")
	host.send(proto.Control{T: "auto", On: true})
	host.send(proto.Control{T: "voicekey", Token: "geheimer-schluessel-1234"})
	host.sync()

	// Falscher Schlüssel: kein Zugang.
	bad := dialToken(t, ts, "voice-raum", "voice", "falsch-falsch-falsch")
	if ctl := bad.waitControl("error"); ctl.Msg == "" {
		t.Fatal("Panel mit falschem Schlüssel wurde reingelassen")
	}

	panel := dialToken(t, ts, "voice-raum", "voice", "geheimer-schluessel-1234")
	panelID := panel.waitControl("joined").ID
	viewer := dial(t, ts, "voice-raum", "viewer")
	viewer.waitControl("allowed")

	panel.send(proto.Control{T: "voice-join", On: true})
	panel.waitControl("voice-you")
	viewer.send(proto.Control{T: "voice-join", On: true})
	viewer.waitControl("voice-you")
	for {
		if ctl := viewer.waitControl("voice"); len(ctl.Members) == 2 {
			break
		}
	}

	// Das Panel spricht, der Zuschauer hört es mit der ID des Panels.
	pkt := make([]byte, proto.VoiceHeaderLen+3)
	pkt[0] = proto.MsgVoice
	binary.LittleEndian.PutUint32(pkt[1:], 999) // wird vom Relay überschrieben
	copy(pkt[proto.VoiceHeaderLen:], []byte{1, 2, 3})
	if err := panel.conn.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
		t.Fatal(err)
	}
	got := viewer.waitBinary()
	if got[0] != proto.MsgVoice || uint64(binary.LittleEndian.Uint32(got[1:])) != panelID {
		t.Fatalf("Sprachpaket falsch: % x (Panel-ID %d)", got[:5], panelID)
	}

	// Taub: nichts mehr bekommen. Danach kommt nur noch das Pong an.
	viewer.send(proto.Control{T: "voice-state", Deaf: true})
	viewer.sync()
	panel.conn.WriteMessage(websocket.BinaryMessage, pkt)
	panel.sync()
	viewer.send(proto.Control{T: "ping", TS: 7})
	viewer.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		mt, data, err := viewer.conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if mt == websocket.BinaryMessage {
			t.Fatal("tauber Zuschauer bekommt trotzdem Sprache")
		}
		if string(data) == `{"t":"pong","ts":7}` {
			break
		}
	}
}
