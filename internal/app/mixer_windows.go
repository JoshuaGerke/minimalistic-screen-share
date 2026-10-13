//go:build windows

package app

import (
	"log"
	"os/exec"
)

// openMixer öffnet den Windows-Lautstärkemixer. Dort gibt es pro Programm
// den Schalter "Von Aufnahme ausschließen" – der einzige offizielle Weg,
// ein Programm im Stream stumm zu haben und trotzdem weiter zu hören.
func openMixer(logg *log.Logger) {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", "ms-settings:apps-volume").Start(); err != nil {
		logg.Printf("Lautstärkemixer öffnen: %v", err)
	}
}
