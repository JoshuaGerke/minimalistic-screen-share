//go:build windows

package capture

// MakeDPIAware schaltet die volle Auflösung frei, sonst liefert Windows
// auf skalierten Displays verwaschene Koordinaten.
func MakeDPIAware() {
	if r, _, _ := procSetProcessDPIAwarenessContext.Call(^uintptr(3)); r == 0 { // PER_MONITOR_AWARE_V2
		procSetProcessDPIAware.Call()
	}
}
