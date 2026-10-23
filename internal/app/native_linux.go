//go:build linux

package app

// openNative: Unter Linux öffnet OpenConference ein Chromium-App-Fenster.
func (a *App) openNative(url string) bool { return false }
