package stream

import (
	"io"
	"log"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
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
