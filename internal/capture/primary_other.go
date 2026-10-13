//go:build !linux

package capture

// primaryFocused gibt es nur unter Wayland/Hyprland.
func primaryFocused() (Target, bool) { return Target{}, false }
