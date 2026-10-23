package app

import "testing"

func TestAppBrowser(t *testing.T) {
	b := appBrowser()
	t.Logf("Browser für das Konferenz-Fenster: %q", b)
	args := appWindowArgs("https://beispiel.trycloudflare.com/?room=x")
	if args[0] != "--app=https://beispiel.trycloudflare.com/?room=x" {
		t.Fatalf("falsche Argumente: %v", args)
	}
}
