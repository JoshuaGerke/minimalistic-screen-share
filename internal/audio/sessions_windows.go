//go:build windows

package audio

import (
	"fmt"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// setSessionMute schaltet eine Session stumm oder laut.
func setSessionMute(ctrl *wca.IAudioSessionControl, muted bool) error {
	var vol *wca.ISimpleAudioVolume
	if err := ctrl.PutQueryInterface(wca.IID_ISimpleAudioVolume, &vol); err != nil {
		return err
	}
	defer vol.Release()
	return vol.SetMute(muted, nil)
}

// listSessions zählt die WASAPI-Sessions der Standard-Ausgabe auf – also
// alle Programme, die gerade Ton auf diesem Gerät ausgeben.
func listSessions() ([]Session, error) {
	type acc struct {
		label string
		muted bool
	}
	found := map[string]*acc{}
	var order []string

	err := eachSession(func(ctrl *wca.IAudioSessionControl) {
		name := sessionName(ctrl)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		entry, ok := found[key]
		if !ok {
			entry = &acc{label: name}
			found[key] = entry
			order = append(order, key)
		}
		if label := displayName(ctrl); label != "" && !strings.HasPrefix(label, "@") {
			entry.label = label
		}
		if muted, err := sessionMuted(ctrl); err == nil && muted {
			entry.muted = true
		}
	})
	if err != nil {
		return nil, err
	}

	out := make([]Session, 0, len(order))
	for _, key := range order {
		e := found[key]
		out = append(out, Session{Name: key, Label: e.label, Muted: e.muted})
	}
	return out, nil
}

// setMutedByName schaltet alle Sessions dieses Programms stumm oder laut.
func setMutedByName(name string, muted bool) error {
	target := strings.ToLower(strings.TrimSpace(name))
	found := false
	err := eachSession(func(ctrl *wca.IAudioSessionControl) {
		if strings.ToLower(sessionName(ctrl)) != target {
			return
		}
		if setSessionMute(ctrl, muted) == nil {
			found = true
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("keine laufende Tonquelle für %q", name)
	}
	return nil
}

// eachSession läuft alle Sessions der Standard-Ausgabe durch.
func eachSession(fn func(ctrl *wca.IAudioSessionControl)) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// läuft schon – egal
	}
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return fmt.Errorf("WASAPI: %w", err)
	}
	defer enumerator.Release()

	var device *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
		return ErrUnavailable
	}
	defer device.Release()

	var mgr *wca.IAudioSessionManager2
	if err := device.Activate(wca.IID_IAudioSessionManager2, wca.CLSCTX_ALL, nil, &mgr); err != nil {
		return fmt.Errorf("Session-Manager: %w", err)
	}
	defer mgr.Release()

	var sessions *wca.IAudioSessionEnumerator
	if err := mgr.GetSessionEnumerator(&sessions); err != nil {
		return err
	}
	defer sessions.Release()

	var count int
	if err := sessions.GetCount(&count); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		var ctrl *wca.IAudioSessionControl
		if err := sessions.GetSession(i, &ctrl); err != nil {
			continue
		}
		fn(ctrl)
		ctrl.Release()
	}
	return nil
}

// sessionName liefert den Programmnamen (z. B. "Discord.exe") zur Session.
func sessionName(ctrl *wca.IAudioSessionControl) string {
	var ctl2 *wca.IAudioSessionControl2
	if err := ctrl.PutQueryInterface(wca.IID_IAudioSessionControl2, &ctl2); err != nil {
		return ""
	}
	defer ctl2.Release()

	var pid uint32
	if err := ctl2.GetProcessId(&pid); err != nil || pid == 0 {
		return "" // Systemklänge o. ä.
	}
	return processName(pid)
}

func displayName(ctrl *wca.IAudioSessionControl) string {
	var name string
	if err := ctrl.GetDisplayName(&name); err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

func sessionMuted(ctrl *wca.IAudioSessionControl) (bool, error) {
	var vol *wca.ISimpleAudioVolume
	if err := ctrl.PutQueryInterface(wca.IID_ISimpleAudioVolume, &vol); err != nil {
		return false, err
	}
	defer vol.Release()
	var muted bool
	if err := vol.GetMute(&muted); err != nil {
		return false, err
	}
	return muted, nil
}

// processName holt den Dateinamen der exe zu einer Prozess-ID.
func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		full = full[i+1:]
	}
	return full
}
