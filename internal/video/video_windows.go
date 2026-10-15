//go:build windows

package video

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/capture"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/config"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// encoder ist ein Hardware-Encoder samt den Schaltern für niedrige Latenz.
type encoder struct {
	name   string
	codec  string
	vf     string // Filter zwischen Aufnahme und Encoder
	params []string
}

// Reihenfolge = Vorliebe. Alle bekommen Bilder direkt aus dem Grafikspeicher,
// nur die letzten beiden müssen sie erst in den Hauptspeicher holen.
var encoders = []encoder{
	{"NVENC", "h264_nvenc", "", []string{"-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-rc", "cbr", "-forced-idr", "1"}},
	{"AMD AMF", "h264_amf", "", []string{"-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr"}},
	{"Intel QuickSync", "h264_qsv", "hwmap=derive_device=qsv,format=qsv", []string{"-preset", "veryfast", "-look_ahead", "0", "-async_depth", "1"}},
	{"Media Foundation", "h264_mf", "hwdownload,format=bgra,format=nv12", []string{"-hw_encoding", "1", "-scenario", "display_remoting", "-rate_control", "cbr"}},
	{"CPU (x264)", "libx264", "hwdownload,format=bgra,format=yuv420p", []string{"-preset", "ultrafast", "-tune", "zerolatency"}},
}

var (
	probeMu     sync.Mutex
	probed      bool
	probedBin   string
	probedEnc   *encoder
	probedError error
)

// Start beginnt die Aufnahme. ErrUnsupported heißt: JPEG-Weg nehmen.
func Start(o Options) (*Encoder, error) {
	input, err := inputFor(o)
	if err != nil {
		return nil, err
	}
	bin, enc, err := pick(o)
	if err != nil {
		return nil, err
	}
	args := []string{"-hide_banner", "-loglevel", "error"}
	if o.Target.Kind == "test" {
		args = append(args, "-re") // Testbild in Echtzeit, nicht so schnell wie möglich
	}
	args = append(args, "-f", "lavfi", "-i", input)
	args = append(args, encodeArgs(enc, o)...)
	if o.Log != nil {
		o.Log.Printf("Video: %s über %s", o.Target.Label(), enc.name)
	}
	return run(enc.name, exec.Command(bin, args...))
}

func encodeArgs(enc *encoder, o Options) []string {
	fps := o.FPS
	if enc.codec == "libx264" && fps > 30 {
		fps = 30 // auf der CPU wäre mehr zu teuer
	}
	kbps := o.BitrateKbps
	var args []string
	// Das Testbild liegt schon im Hauptspeicher – kein Herunterladen nötig.
	if enc.vf != "" && o.Target.Kind != "test" {
		args = append(args, "-vf", enc.vf)
	}
	args = append(args, "-c:v", enc.codec)
	args = append(args, enc.params...)
	args = append(args,
		"-b:v", strconv.Itoa(kbps)+"k",
		"-maxrate", strconv.Itoa(kbps)+"k",
		"-bufsize", strconv.Itoa(max(kbps*2/max(fps, 1), 200))+"k",
		"-g", strconv.Itoa(fps*2), // alle 2 s ein Keyframe zum Einsteigen
		"-bf", "0",
		"-r", strconv.Itoa(fps),
		"-flush_packets", "1",
		"-f", "h264", "pipe:1",
	)
	return args
}

// inputFor baut die Quelle: Desktop Duplication für Monitore und Bereiche,
// ein Testbild für "test". Fenster und "alle Monitore" bleiben bei JPEG.
func inputFor(o Options) (string, error) {
	t := o.Target
	fps := max(o.FPS, 1)
	mouse := "0"
	if t.Cursor {
		mouse = "1"
	}
	switch t.Kind {
	case "test":
		return fmt.Sprintf("testsrc2=size=1280x720:rate=%d", fps), nil
	case "monitor":
		mons, err := capture.ListMonitors()
		if err != nil {
			return "", err
		}
		for _, m := range mons {
			if m.Index != t.Monitor {
				continue
			}
			idx, err := dxgiOutput(m.X, m.Y)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%s", idx, fps, mouse), nil
		}
		return "", fmt.Errorf("Monitor %d gibt es nicht", t.Monitor+1)
	case "region":
		r := t.Region
		mons, err := capture.ListMonitors()
		if err != nil {
			return "", err
		}
		for _, m := range mons {
			if r.X < m.X || r.Y < m.Y || r.X+r.W > m.X+m.W || r.Y+r.H > m.Y+m.H {
				continue
			}
			idx, err := dxgiOutput(m.X, m.Y)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%s:offset_x=%d:offset_y=%d:video_size=%dx%d",
				idx, fps, mouse, r.X-m.X, r.Y-m.Y, r.W&^1, r.H&^1), nil
		}
	}
	return "", ErrUnsupported
}

// pick findet ffmpeg und den ersten Encoder, der auf diesem PC wirklich
// läuft. Das Ergebnis gilt, bis die App neu startet.
func pick(o Options) (string, *encoder, error) {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probed {
		return probedBin, probedEnc, probedError
	}
	probed = true
	bin, err := findFFmpeg(o)
	if err != nil {
		probedError = err
		return "", nil, err
	}
	probedBin = bin
	for i := range encoders {
		enc := &encoders[i]
		// Kurzer Probelauf mit echter Bildschirmaufnahme: Die Encoder melden
		// sich sonst gern als vorhanden, obwohl die Grafikkarte fehlt.
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "ddagrab=output_idx=0:framerate=10"}
		args = append(args, encodeArgs(enc, Options{FPS: 10, BitrateKbps: 1000})...)
		args = append(args[:len(args)-3], "-frames:v", "3", "-f", "null", "-")
		cmd := exec.Command(bin, args...)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			probedEnc = enc
			if o.Log != nil {
				o.Log.Printf("Video-Encoder: %s", enc.name)
			}
			return bin, enc, nil
		} else if o.Log != nil {
			o.Log.Printf("Encoder %s geht nicht: %s", enc.name, firstLine(string(out)))
		}
	}
	probedError = errors.New("kein H.264-Encoder lauffähig")
	return "", nil, probedError
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// findFFmpeg nimmt ein vorhandenes ffmpeg (ab Version 6, wegen ddagrab) oder
// lädt einmalig einen schlanken Build ins Konfigverzeichnis.
func findFFmpeg(o Options) (string, error) {
	own := filepath.Join(config.Dir(), "ffmpeg.exe")
	candidates := []string{own}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		if hasDDAGrab(c) {
			return c, nil
		}
	}
	if o.Log != nil {
		o.Log.Println("Lade ffmpeg herunter (einmalig, ca. 60 MB)…")
	}
	if err := downloadFFmpeg(own); err != nil {
		return "", fmt.Errorf("ffmpeg herunterladen: %w", err)
	}
	if !hasDDAGrab(own) {
		return "", errors.New("ffmpeg ohne ddagrab")
	}
	return own, nil
}

func hasDDAGrab(bin string) bool {
	if _, err := os.Stat(bin); err != nil {
		return false
	}
	cmd := exec.Command(bin, "-hide_banner", "-h", "filter=ddagrab")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(string(out), "Desktop Duplication")
}

// downloadFFmpeg holt den LGPL-Build von BtbN (enthält NVENC, AMF, QSV und
// Media Foundation) und packt nur ffmpeg.exe aus.
func downloadFFmpeg(dst string) error {
	const url = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-lgpl.zip"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmpZip := dst + ".zip.teil"
	f, err := os.Create(tmpZip)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	defer os.Remove(tmpZip)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(tmpZip)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if !strings.HasSuffix(zf.Name, "/bin/ffmpeg.exe") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		tmp := dst + ".teil"
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, dst)
	}
	return errors.New("ffmpeg.exe nicht im Archiv")
}

/* ---------- DXGI: welcher ffmpeg-Ausgang ist welcher Monitor? ---------- */

var procCreateDXGIFactory1 = windows.NewLazySystemDLL("dxgi.dll").NewProc("CreateDXGIFactory1")

var iidDXGIFactory1 = ole.NewGUID("{770aae78-f26f-4dba-a829-253c83d1b387}")

type dxgiOutputDesc struct {
	DeviceName [32]uint16
	Left       int32
	Top        int32
	Right      int32
	Bottom     int32
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

// comCall ruft Methode Nummer index eines COM-Objekts auf.
func comCall(obj *ole.IUnknown, index int, args ...uintptr) uintptr {
	vtbl := (*[16]uintptr)(unsafe.Pointer(obj.RawVTable))
	r, _, _ := syscall.SyscallN(vtbl[index], append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)...)
	return r
}

// dxgiOutput sucht den Ausgang der ersten Grafikkarte mit dieser Position –
// genau so zählt ddagrab seine output_idx.
func dxgiOutput(x, y int) (int, error) {
	var factory *ole.IUnknown
	if hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(iidDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); int32(hr) < 0 || factory == nil {
		return 0, fmt.Errorf("DXGI nicht verfügbar (0x%08x)", uint32(hr))
	}
	defer factory.Release()
	var adapter *ole.IUnknown
	if hr := comCall(factory, 7, 0, uintptr(unsafe.Pointer(&adapter))); int32(hr) < 0 || adapter == nil { // EnumAdapters
		return 0, errors.New("keine Grafikkarte gefunden")
	}
	defer adapter.Release()
	for i := 0; i < 16; i++ {
		var output *ole.IUnknown
		if hr := comCall(adapter, 7, uintptr(i), uintptr(unsafe.Pointer(&output))); int32(hr) < 0 || output == nil { // EnumOutputs
			break
		}
		var desc dxgiOutputDesc
		comCall(output, 7, uintptr(unsafe.Pointer(&desc))) // GetDesc
		output.Release()
		if int(desc.Left) == x && int(desc.Top) == y {
			return i, nil
		}
	}
	// Monitor hängt an einer anderen Grafikkarte – das kann ddagrab nicht.
	return 0, ErrUnsupported
}

// Prepare sucht bzw. lädt ffmpeg und probiert die Encoder aus. Läuft beim
// App-Start im Hintergrund, damit der erste Stream nicht darauf wartet.
func Prepare(logg *log.Logger) { pick(Options{Log: logg}) }

// Ready sagt, ob H.264 sofort startklar ist (blockiert nie).
func Ready() bool {
	if !probeMu.TryLock() {
		return false // wird gerade vorbereitet
	}
	defer probeMu.Unlock()
	return probed && probedError == nil
}

// Supports nennt die Ziele, die über Desktop Duplication laufen.
func Supports(t capture.Target) bool {
	return t.Kind == "monitor" || t.Kind == "region" || t.Kind == "test"
}
