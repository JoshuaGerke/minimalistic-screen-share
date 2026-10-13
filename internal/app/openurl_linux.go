//go:build linux

package app

import "os/exec"

func openURL(url string) error {
	return exec.Command("xdg-open", url).Start()
}
