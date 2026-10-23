//go:build linux

package app

import "os/exec"

// appBrowser sucht einen Chromium-Abkömmling (Omarchy bringt Chromium mit).
func appBrowser() string {
	for _, b := range []string{"chromium", "brave", "brave-browser", "google-chrome-stable", "google-chrome", "chromium-browser", "microsoft-edge-stable"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}
