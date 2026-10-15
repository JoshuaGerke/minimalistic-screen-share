//go:build linux

package video

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
)

func hideWindow(cmd *exec.Cmd) {}

// Start nimmt unter Wayland (Hyprland, Sway …) per wf-recorder auf:
// wlroots-Screencopy plus VAAPI-Encoder der Grafikkarte, notfalls x264.
// Ohne Wayland oder wf-recorder bleibt es beim JPEG-Weg.
func Start(o Options) (*Encoder, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, ErrUnsupported
	}
	bin, err := exec.LookPath("wf-recorder")
	if err != nil {
		return nil, fmt.Errorf("%w: wf-recorder fehlt (pacman -S wf-recorder)", ErrUnsupported)
	}
	var where []string
	switch o.Target.Kind {
	case "monitor":
		name, err := capture.HyprMonitorName(o.Target.Monitor)
		if err != nil {
			return nil, err
		}
		where = []string{"-o", name}
	case "region":
		r := o.Target.Region
		where = []string{"-g", fmt.Sprintf("%d,%d %dx%d", r.X, r.Y, r.W&^1, r.H&^1)}
	default:
		return nil, ErrUnsupported
	}
	fps := max(o.FPS, 1)
	kbps := strconv.Itoa(o.BitrateKbps) + "k"
	common := append(where,
		"-y", "-m", "h264", "-f", "/dev/stdout", "-r", strconv.Itoa(fps),
		"-p", "b="+kbps, "-p", "maxrate="+kbps, "-p", "g="+strconv.Itoa(fps*2), "-p", "bf=0")
	if !o.Target.Cursor {
		common = append(common, "--no-cursor")
	}
	tries := []struct {
		name string
		args []string
	}{
		{"VAAPI", append([]string{"-c", "h264_vaapi", "-d", "/dev/dri/renderD128", "-p", "rc_mode=CBR"}, common...)},
		{"CPU (x264)", append([]string{"-c", "libx264", "-x", "yuv420p", "-p", "preset=ultrafast", "-p", "tune=zerolatency"}, common...)},
	}
	var lastErr error
	for _, t := range tries {
		enc, err := run(t.name, exec.Command(bin, t.args...))
		if err != nil {
			lastErr = err
			continue
		}
		// Bricht wf-recorder gleich wieder ab (kein VAAPI o. ä.), nächster Versuch.
		time.Sleep(1500 * time.Millisecond)
		if err := enc.Err(); err != nil {
			lastErr = err
			continue
		}
		if o.Log != nil {
			o.Log.Printf("Video: %s über wf-recorder/%s", o.Target.Label(), t.name)
		}
		return enc, nil
	}
	if lastErr == nil {
		lastErr = errors.New("wf-recorder startet nicht")
	}
	return nil, lastErr
}

// Prepare ist unter Linux nicht nötig – wf-recorder ist da oder nicht.
func Prepare(logg *log.Logger) {}

// Ready sagt, ob wf-recorder unter Wayland verfügbar ist.
func Ready() bool {
	_, err := exec.LookPath("wf-recorder")
	return err == nil && os.Getenv("WAYLAND_DISPLAY") != ""
}

// Supports nennt die Ziele, die wf-recorder aufnehmen kann.
func Supports(t capture.Target) bool { return t.Kind == "monitor" || t.Kind == "region" }
