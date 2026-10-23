package rendezvous

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Der Browser rechnet denselben Kanal aus (viewer: topicFor) – beide müssen
// übereinstimmen. Erwarteter Wert: sha256("mss-share:testraum")[:24].
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
	want := "https://beispiel-" + room + ".trycloudflare.com"
	if err := Publish(context.Background(), room, want); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // ntfy speichert leicht verzögert
	got, err := Resolve(context.Background(), room)
	if err != nil || got != want {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if _, err := Resolve(context.Background(), room+"-gibts-nicht"); err == nil {
		t.Fatal("unbekannte ID gefunden")
	}
}
