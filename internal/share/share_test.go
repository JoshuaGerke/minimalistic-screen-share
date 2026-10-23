package share

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/e2e"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/room"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/video"
)

func TestShareEndToEnd(t *testing.T) {
	keys, _ := e2e.Derive("share-test", "pw")
	relay := room.New(log.New(io.Discard, "", 0))
	relay.Host("share-test", e2e.AuthHash(keys.Auth))
	ts := httptest.NewServer(relay.Handler())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// Ohne Encoder (Linux ohne Wayland o. ä.) gibt es nichts zu prüfen.
	if enc, err := video.Start(video.Options{Target: capture.Target{Kind: "test"}, FPS: 30, BitrateKbps: 2000}); err != nil {
		t.Skipf("kein H.264 hier: %v", err)
	} else {
		enc.Close()
	}

	// Ein Teilnehmer schaut zu.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.WriteJSON(room.Ctl{T: "join", Room: "share-test", Auth: hex.EncodeToString(keys.Auth), Kind: "person"})
	box, _ := e2e.NewBox(keys.Enc)

	pointed := make(chan string, 1)
	s, err := Start(Options{
		Relay: func() string { return wsURL }, Room: "share-test", Keys: keys, Owner: "Anna",
		Target: capture.Target{Kind: "test"}, FPS: 30, BitrateKbps: 2000, Log: log.New(io.Discard, "", 0),
		OnPoint: func(x, y float64, from string) { pointed <- from },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	var shareID uint32
	gotState, keyChunks := false, 0
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	for !gotState || keyChunks == 0 {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("lesen: %v (Zustand %v, Keyframe-Stücke %d)", err, gotState, keyChunks)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		h, plain, err := box.Open(data)
		if err != nil {
			t.Fatalf("Paket vom Sender nicht entschlüsselbar: %v", err)
		}
		switch h.Kind {
		case e2e.KindState:
			var st map[string]any
			json.Unmarshal(plain, &st)
			if st["owner"] != "Anna" || st["type"] != "screen" {
				t.Fatalf("Zustand falsch: %v", st)
			}
			gotState, shareID = true, h.Sender
		case e2e.KindVideo:
			if h.Flags&e2e.FlagKey != 0 && len(plain) > 0 {
				keyChunks++
			}
		}
	}

	// Zeige-Klick an den Bildschirm zurück.
	ev, _ := json.Marshal(map[string]any{"type": "point", "target": shareID, "x": 0.5, "y": 0.5, "from": "Ben"})
	conn.WriteMessage(websocket.BinaryMessage, box.Seal(e2e.Header{Kind: e2e.KindEvent}, ev))
	select {
	case from := <-pointed:
		if from != "Ben" {
			t.Fatalf("Zeige-Klick von %q", from)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Zeige-Klick kam nicht an")
	}
	if st := s.Status(); !st.Live || st.Encoder == "" {
		t.Fatalf("Status falsch: %+v", st)
	}
}
