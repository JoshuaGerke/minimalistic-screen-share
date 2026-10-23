package rendezvous

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Groß-/Kleinschreibung und Leerzeichen dürfen den Kanal nicht ändern.
func TestTopic(t *testing.T) {
	if got := Topic(" TestRaum "); got != Topic("testraum") || len(got) != 4+24 {
		t.Fatalf("Topic unerwartet: %q", got)
	}
	t.Log(Topic("testraum"))
}

// TestRoundTrip spricht wirklich mit ntfy.sh – nur mit MSS_NET_TEST=1.
func TestRoundTrip(t *testing.T) {
	if os.Getenv("MSS_NET_TEST") != "1" {
		t.Skip("nur mit MSS_NET_TEST=1")
	}
	room := fmt.Sprintf("test-%d", time.Now().UnixNano())
	want := Entry{URL: "https://beispiel-" + room + ".trycloudflare.com", LAN: "http://192.168.1.2:8080", Host: "abc"}
	if _, err := Publish(context.Background(), room, want); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // ntfy speichert leicht verzögert
	got, err := Entries(context.Background(), room)
	if err != nil || len(got) != 1 || got[0].URL != want.URL || got[0].LAN != want.LAN || got[0].Host != want.Host || got[0].Time == 0 {
		t.Fatalf("Entries = %+v, %v", got, err)
	}
}
