//go:build windows

package setup

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

const (
	appName      = "Minimalistic Screen Share"
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\minimalistic-screen-share`
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue     = "minimalistic-screen-share"
)

// InstallDir ist der feste Platz der App, ohne Admin-Rechte beschreibbar.
func InstallDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "minimalistic-screen-share")
}

func installedExe() string { return filepath.Join(InstallDir(), "mss-share.exe") }

func updateShortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, appName+" aktualisieren.lnk")
}

func shortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, appName+".lnk")
}

// Needed sagt, ob diese exe sich erst noch installieren sollte.
func Needed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if devBuild(exe) {
		return false
	}
	return !strings.EqualFold(filepath.Clean(exe), filepath.Clean(installedExe()))
}

// Install kopiert die laufende exe an ihren Platz, legt Startmenü-Eintrag
// und den Eintrag unter "Apps" an und startet die installierte Kopie. Eine
// laufende ältere Version wird vorher beendet – so ist ein Update einfach
// "neue exe doppelklicken".
func Install(logg *log.Logger, quiet bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	StopRunning()
	dst := installedExe()
	if err := copyFile(exe, dst); err != nil {
		return fmt.Errorf("kopieren nach %s: %w", dst, err)
	}
	if err := createShortcut(updateShortcutPath(), dst, "--update"); err != nil {
		logg.Printf("Startmenü-Eintrag zum Aktualisieren: %v", err)
	}
	if err := createShortcut(shortcutPath(), dst, ""); err != nil {
		logg.Printf("Startmenü-Eintrag: %v", err)
	}
	if err := registerUninstall(dst); err != nil {
		logg.Printf("Eintrag unter Apps: %v", err)
	}
	// Autostart zeigte vielleicht noch auf einen alten Ort – nachziehen.
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if _, _, err := k.GetStringValue(runValue); err == nil {
			k.SetStringValue(runValue, `"`+dst+`" --background`)
		}
		k.Close()
	}
	logg.Printf("Installiert nach %s (Version %s)", dst, version.Version)
	// Nach einem Update per Knopf still weiterlaufen, sonst Fenster zeigen.
	if quiet {
		return exec.Command(dst, "--background").Start()
	}
	return exec.Command(dst, "--welcome").Start()
}

// createShortcut legt die .lnk über die Windows-Shell an – PowerShell ist
// auf jedem Windows da und spart eine COM-Anbindung.
func createShortcut(lnk, target, args string) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := fmt.Sprintf("$s=(New-Object -ComObject WScript.Shell).CreateShortcut(%s);$s.TargetPath=%s;$s.WorkingDirectory=%s;$s.Arguments=%s;$s.Description='Bildschirm und Ton teilen';$s.Save()",
		q(lnk), q(target), q(filepath.Dir(target)), q(args))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}

func registerUninstall(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.SetStringValue("DisplayName", appName)
	k.SetStringValue("DisplayVersion", version.Version)
	k.SetStringValue("Publisher", "Joshua Gerke")
	k.SetStringValue("DisplayIcon", exe)
	k.SetStringValue("InstallLocation", filepath.Dir(exe))
	k.SetStringValue("UninstallString", `"`+exe+`" --uninstall`)
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	if fi, err := os.Stat(exe); err == nil {
		k.SetDWordValue("EstimatedSize", uint32(fi.Size()/1024))
	}
	return nil
}

// Uninstall räumt alles wieder ab. Die Einstellungen bleiben liegen, damit
// eine Neuinstallation die gleiche Sitzungs-ID behält.
func Uninstall() error {
	StopRunning()
	os.Remove(shortcutPath())
	os.Remove(updateShortcutPath())
	os.Remove(filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`, appName+".lnk"))
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(runValue)
		k.Close()
	}
	registry.DeleteKey(registry.CURRENT_USER, uninstallKey)

	// Die laufende exe kann sich nicht selbst löschen: Das erledigt ein
	// kurzer cmd-Aufruf, nachdem wir uns beendet haben.
	dir := InstallDir()
	cmd := exec.Command("cmd", "/c", "ping 127.0.0.1 -n 3 >nul & rmdir /s /q \""+dir+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Start()
	Message("Minimalistic Screen Share wurde entfernt.\n\nDie Einstellungen liegen weiter unter %APPDATA%\\minimalistic-screen-share – einfach löschen, wenn du sie nicht mehr brauchst.")
	return nil
}

// Message zeigt ein einfaches Hinweisfenster.
func Message(text string) {
	title, _ := windows.UTF16PtrFromString(appName)
	body, _ := windows.UTF16PtrFromString(text)
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x40)
}
