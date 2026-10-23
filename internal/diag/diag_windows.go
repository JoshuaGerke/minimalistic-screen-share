//go:build windows

package diag

import (
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processes liest die CPU-Zeit (Kernel + Benutzer) der beobachteten Prozesse.
func processes() map[int]procTime {
	out := map[int]procTime{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := match(windows.UTF16ToString(e.ExeFile[:]))
		if name == "" {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if err != nil {
			continue
		}
		var c, x, k, u windows.Filetime
		if windows.GetProcessTimes(h, &c, &x, &k, &u) == nil {
			ticks := int64(k.HighDateTime)<<32 | int64(k.LowDateTime) + (int64(u.HighDateTime)<<32 | int64(u.LowDateTime))
			out[int(e.ProcessID)] = procTime{name, time.Duration(ticks * 100)}
		}
		windows.CloseHandle(h)
	}
	return out
}

// gpuBusy gibt es unter Windows ohne Treiber-API nicht – der Task-Manager
// zeigt die Grafikkarten-Last.
func gpuBusy() string { return "" }

var (
	procGetSystemTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemTimes")
	lastIdle, lastBusy int64
)

// systemLoad nennt die Gesamt-CPU-Auslastung seit dem letzten Aufruf.
func systemLoad() string {
	var idle, kernel, user windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r == 0 {
		return ""
	}
	ft := func(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	i, busy := ft(idle), ft(kernel)+ft(user)-ft(idle)
	defer func() { lastIdle, lastBusy = i, busy }()
	if lastBusy == 0 {
		return ""
	}
	di, db := i-lastIdle, busy-lastBusy
	if di+db <= 0 {
		return ""
	}
	return "cpu gesamt " + strconv.Itoa(int(db*100/(di+db))) + "%"
}

// SystemInfo nennt die Windows-Version (Grafikkarten zeigt der Task-Manager).
func SystemInfo() string {
	v := windows.RtlGetVersion()
	return "Windows " + strconv.Itoa(int(v.MajorVersion)) + "." + strconv.Itoa(int(v.MinorVersion)) + " Build " + strconv.Itoa(int(v.BuildNumber))
}
