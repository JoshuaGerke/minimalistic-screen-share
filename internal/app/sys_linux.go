//go:build linux

package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func platform() string { return "linux" }

const isWindows = false

func autostartFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "autostart", "minimalistic-screen-share.desktop")
}

func autostartEnabled() bool {
	_, err := os.Stat(autostartFile())
	return err == nil
}

// setAutostart legt einen XDG-Autostart-Eintrag an. Hyprland ohne uwsm liest
// den nicht – dort hilft "exec-once = mss-share --background".
func setAutostart(on bool) error {
	path := autostartFile()
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Minimalistic Screen Share\nExec=%s --background\nX-GNOME-Autostart-enabled=true\n", exe)
	return os.WriteFile(path, []byte(entry), 0o644)
}

// copyToClipboard nimmt wl-copy (Wayland) oder xclip/xsel (X11).
func copyToClipboard(text string) error {
	for _, c := range [][]string{
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errors.New("wl-copy oder xclip fehlt (pacman -S wl-clipboard)")
}

// showPointer: Hyprland erlaubt keine fremden Overlay-Fenster – der
// Mauszeiger springt an die Stelle, dazu eine kurze Meldung.
func showPointer(x, y int, name string, logg *log.Logger) {
	if _, err := exec.LookPath("hyprctl"); err != nil {
		logg.Printf("Zeige-Klick: hyprctl fehlt")
		return
	}
	exec.Command("hyprctl", "dispatch", "movecursor", fmt.Sprint(x), fmt.Sprint(y)).Run()
	exec.Command("hyprctl", "notify", "1", "2500", "rgb(5865f2)", name+" zeigt hierhin").Run()
}
