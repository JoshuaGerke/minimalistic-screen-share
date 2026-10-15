package stream

import (
	"bytes"
	"image/jpeg"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/server"
)

// TestStopWhileConnected sichert den wichtigsten Weg ab: Während der Stream
// läuft, muss Stop() sofort greifen. Vorher hing das Beenden, weil die
// Sendeschleifen nur auf Verbindungsfehler reagiert haben.
func TestStopWhileConnected(t *testing.T) {
	relay := server.New(server.Config{Log: log.New(io.Discard, "", 0)})
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	ts := httptest.NewServer(relay.Handler(static))
	defer ts.Close()

	sess := New(Options{
		Server:   ts.URL,
		Room:     "stopp-test",
		Name:     "tester",
		FPS:      10,
		Quality:  50,
		MaxWidth: 320,
		Target:   capture.Target{Kind: "test"},
		Log:      log.New(io.Discard, "", 0),
	})

	done := make(chan struct{})
	go func() {
		sess.Run()
		close(done)
	}()

	// Warten, bis die Sitzung wirklich verbunden ist.
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := sess.Status().State
		if state == "live" || state == "paused" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sitzung wurde nicht verbunden: %+v", sess.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}

	stopDone := make(chan struct{})
	go func() {
		sess.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hängt bei laufender Verbindung")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() kehrt nach Stop() nicht zurück")
	}
}

// TestChunkedFramesAndPause: Ein Zuschauer bekommt das Bild in Stücken, die
// zusammen ein gültiges JPEG ergeben, und erfährt von der Pause.
func TestChunkedFramesAndPause(t *testing.T) {
	relay := server.New(server.Config{Log: log.New(io.Discard, "", 0)})
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	ts := httptest.NewServer(relay.Handler(static))
	defer ts.Close()

	sess := New(Options{
		Server: ts.URL, Room: "stueck-test", Name: "host", FPS: 20, Quality: 90,
		AutoAccept: true, Target: capture.Target{Kind: "test"}, Log: log.New(io.Discard, "", 0),
	})
	go sess.Run()
	defer sess.Stop()
	for i := 0; sess.Status().State != "live" && sess.Status().State != "paused"; i++ {
		if i > 100 {
			t.Fatal("Sitzung verbindet nicht")
		}
		time.Sleep(50 * time.Millisecond)
	}

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.WriteJSON(proto.Control{T: "join", Room: "stueck-test", Role: "viewer", Name: "v"})

	var frame []byte
	gotMeta, gotFrame := false, false
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !gotFrame || !gotMeta {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("lesen: %v", err)
		}
		if mt == websocket.TextMessage {
			gotMeta = gotMeta || strings.Contains(string(data), `"t":"meta"`)
			continue
		}
		idx, count, ok := proto.ChunkIndex(data)
		if !ok {
			continue
		}
		if idx == 0 {
			frame = frame[:0]
		}
		frame = append(frame, data[proto.ChunkHeaderLen:]...)
		if idx != count-1 {
			continue
		}
		if _, err := jpeg.Decode(bytes.NewReader(frame)); err != nil {
			t.Fatalf("zusammengesetztes Bild kaputt: %v", err)
		}
		gotFrame = true
	}

	sess.SetPaused(true)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Pause kam nicht an: %v", err)
		}
		if mt == websocket.TextMessage && strings.Contains(string(data), `"t":"pause","on":true`) {
			break
		}
	}
}
