//go:build linux

package app

import (
	"log"
	"os/exec"
)

// openMixer öffnet pavucontrol, falls installiert – dort lassen sich
// einzelne Tonströme stummschalten.
func openMixer(logg *log.Logger) {
	if _, err := exec.LookPath("pavucontrol"); err != nil {
		logg.Printf("pavucontrol nicht installiert (pacman -S pavucontrol)")
		return
	}
	if err := exec.Command("pavucontrol").Start(); err != nil {
		logg.Printf("pavucontrol öffnen: %v", err)
	}
}
