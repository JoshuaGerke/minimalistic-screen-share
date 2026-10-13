//go:build !windows

package capture

// MakeDPIAware ist nur unter Windows nötig.
func MakeDPIAware() {}
