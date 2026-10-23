//go:build linux

package diag

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// processes liest utime+stime aus /proc/<pid>/stat (Takt: 100 Hz).
func processes() map[int]procTime {
	out := map[int]procTime{}
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		comm, err := os.ReadFile(d + "/comm")
		if err != nil {
			continue
		}
		name := match(strings.TrimSpace(string(comm)))
		if name == "" {
			continue
		}
		stat, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		// Hinter dem letzten ")" stehen die Felder ab Nummer 3.
		s := string(stat)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 13 {
			continue
		}
		ut, _ := strconv.ParseInt(f[11], 10, 64)
		st, _ := strconv.ParseInt(f[12], 10, 64)
		pid, _ := strconv.Atoi(filepath.Base(d))
		out[pid] = procTime{name, time.Duration(ut+st) * 10 * time.Millisecond}
	}
	return out
}

// gpuBusy liest die Auslastung aller AMD-Grafikkarten.
func gpuBusy() string {
	files, _ := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent")
	var parts []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		card := filepath.Base(filepath.Dir(filepath.Dir(f)))
		parts = append(parts, card+"="+strings.TrimSpace(string(b))+"%")
	}
	return strings.Join(parts, " ")
}

func systemLoad() string {
	var parts []string
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			parts = append(parts, "load "+strings.Join(f[:3], " "))
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "MemAvailable:") {
				f := strings.Fields(line)
				if kb, err := strconv.Atoi(f[1]); err == nil {
					parts = append(parts, "ram frei "+strconv.Itoa(kb/1024)+" MB")
				}
			}
		}
	}
	return strings.Join(parts, " | ")
}

// SystemInfo beschreibt Kernel, Grafikkarten und Monitore – einmal beim
// Einschalten des Diagnosemodus.
func SystemInfo() string {
	var parts []string
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		parts = append(parts, "Kernel "+strings.TrimSpace(string(b)))
	}
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]")
	for _, c := range cards {
		b, err := os.ReadFile(c + "/device/uevent")
		if err != nil {
			continue
		}
		var driver, pci string
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "DRIVER="); ok {
				driver = v
			}
			if v, ok := strings.CutPrefix(l, "PCI_ID="); ok {
				pci = v
			}
		}
		nodes, _ := filepath.Glob(c + "/device/drm/renderD*")
		node := ""
		if len(nodes) > 0 {
			node = filepath.Base(nodes[0])
		}
		var outs []string
		conns, _ := filepath.Glob("/sys/class/drm/" + filepath.Base(c) + "-*/status")
		for _, st := range conns {
			if b, _ := os.ReadFile(st); strings.TrimSpace(string(b)) == "connected" {
				outs = append(outs, strings.TrimPrefix(filepath.Base(filepath.Dir(st)), filepath.Base(c)+"-"))
			}
		}
		parts = append(parts, filepath.Base(c)+" "+driver+" "+pci+" "+node+" Monitore: "+strings.Join(outs, ","))
	}
	if out, err := exec.Command("hyprctl", "monitors").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "Monitor") || strings.Contains(l, "@") {
				parts = append(parts, l)
			}
		}
	}
	return strings.Join(parts, " | ")
}
