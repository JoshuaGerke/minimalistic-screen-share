// Der Viewer-CLI hängt sich als Zuschauer in einen Raum, zählt Frames und
// Tonpakete und kann das jeweils letzte Bild auf die Platte schreiben.
// Gedacht zum Prüfen der Verbindung ohne Browser.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/stream"
	"github.com/JoshuaGerke/minimalistic-screen-share/internal/version"
)

// int16Peak liefert die größte Amplitude im PCM-Paket – damit sieht man,
// ob echter Ton ankommt oder nur Stille.
func int16Peak(pcm []byte) int32 {
	var max int32
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		if v > max {
			max = v
		}
	}
	return max
}

func main() {
	server := flag.String("server", "http://localhost:8080", "Relay-Adresse")
	room := flag.String("room", "", "Sitzungs-ID")
	name := flag.String("name", "cli", "Name, den der Streamer bei der Anfrage sieht")
	save := flag.String("save", "", "letztes Frame fortlaufend in diese JPEG-Datei schreiben")
	flag.Parse()

	if *room == "" {
		fmt.Fprintln(os.Stderr, "bitte -room angeben")
		os.Exit(2)
	}
	url, err := stream.WSURL(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Verbindung:", err)
		os.Exit(1)
	}
	defer conn.Close()

	join := proto.Control{T: "join", Room: *room, Role: "viewer", Name: *name, Ver: version.Version}
	if err := conn.WriteJSON(join); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("mss-viewer %s – Sitzung %q auf %s\n", version.Version, *room, *server)

	var frames, audio, bytesIn atomic.Int64
	var peak atomic.Int64
	var sizeW, sizeH atomic.Int64
	var mu sync.Mutex
	var lastFrame, assembling []byte

	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				fmt.Println("\nVerbindung beendet:", err)
				os.Exit(0)
			}
			if mt == websocket.TextMessage {
				var ctl proto.Control
				if json.Unmarshal(data, &ctl) != nil {
					continue
				}
				switch ctl.T {
				case "meta":
					fmt.Printf("Stream: %s (%d fps, Ton: %v)\n", ctl.Title, ctl.FPS, ctl.Audio)
				case "joined":
					fmt.Println("Anfrage gesendet – warte auf Freigabe durch den Streamer…")
				case "allowed":
					fmt.Println("Freigegeben.")
				case "denied":
					fmt.Println("Der Streamer hat die Anfrage abgelehnt.")
					os.Exit(1)
				case "viewers":
					// egal, wir zählen selbst
				case "ended":
					fmt.Println("Stream beendet")
					os.Exit(0)
				case "error":
					fmt.Println("Fehler:", ctl.Msg)
					os.Exit(1)
				}
				continue
			}
			bytesIn.Add(int64(len(data)))
			typ, _, _, w, h, ok := proto.Header(data)
			if !ok {
				continue
			}
			switch typ {
			case proto.MsgFrameChunk:
				idx, count, _ := proto.ChunkIndex(data)
				if idx == 0 {
					assembling = assembling[:0]
				}
				assembling = append(assembling, data[proto.ChunkHeaderLen:]...)
				if idx == count-1 {
					frames.Add(1)
					sizeW.Store(int64(w))
					sizeH.Store(int64(h))
					if *save != "" {
						mu.Lock()
						lastFrame = append(lastFrame[:0], assembling...)
						mu.Unlock()
					}
				}
			case proto.MsgFrame:
				frames.Add(1)
				sizeW.Store(int64(w))
				sizeH.Store(int64(h))
				if *save != "" {
					mu.Lock()
					lastFrame = append(lastFrame[:0], data[proto.FrameHeaderLen:]...)
					mu.Unlock()
				}
			case proto.MsgAudio:
				audio.Add(1)
				if p := int16Peak(data[proto.AudioHeaderLen:]); int64(p) > peak.Load() {
					peak.Store(int64(p))
				}
			case proto.MsgSilence:
				audio.Add(1)
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var prevFrames, prevBytes int64
	for {
		select {
		case <-sig:
			return
		case <-ticker.C:
			f := frames.Load()
			b := bytesIn.Load()
			peakPct := int(peak.Swap(0)) * 100 / 32767
			line := fmt.Sprintf("%d fps · %d kbit/s · %d×%d · %d Tonpakete · Tonpegel %d%%",
				f-prevFrames, (b-prevBytes)*8/1000, sizeW.Load(), sizeH.Load(), audio.Load(), peakPct)
			if *save != "" {
				mu.Lock()
				if len(lastFrame) > 0 {
					os.WriteFile(*save, lastFrame, 0o644)
					line += " · " + *save + " aktualisiert"
				}
				mu.Unlock()
			}
			fmt.Println(line)
			prevFrames, prevBytes = f, b
		}
	}
}
