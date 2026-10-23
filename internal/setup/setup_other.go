//go:build !windows

package setup

import (
	"errors"
	"log"
	"os"
)

// Unter Linux übernimmt install.sh bzw. das PKGBUILD das Installieren.
func Needed() bool                               { return false }
func Install(logg *log.Logger, quiet bool) error { return errors.New("nur unter Windows") }

func Uninstall() error {
	StopRunning()
	return errors.New("unter Linux bitte uninstall.sh benutzen")
}

func Message(text string) { os.Stderr.WriteString(text + "\n") }
