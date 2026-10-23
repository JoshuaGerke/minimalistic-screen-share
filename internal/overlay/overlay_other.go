//go:build !windows

package overlay

// Außerhalb von Windows zeigt der Browser das Overlay (Bild-in-Bild).

func SetPositionStore(load func() (int, int, bool), save func(int, int)) {}
func Supported() bool                                                    { return false }
func Show(on bool)                                                       {}
func Visible() bool                                                      { return false }
func Update(st State)                                                    {}
func systemFont() []byte                                                 { return nil }
