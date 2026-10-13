//go:build linux

package capture

import "os"

// primaryFocused liefert unter Hyprland den gerade fokussierten Monitor –
// das ist das, was Nutzer unter "mein Hauptbildschirm" verstehen.
func primaryFocused() (Target, bool) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return Target{}, false
	}
	monitors, err := hyprMonitors()
	if err != nil {
		return Target{}, false
	}
	for i, m := range monitors {
		if m.Focused {
			return Target{Kind: "monitor", Monitor: i, Cursor: true}, true
		}
	}
	return Target{}, false
}
