//go:build linux

package audio

import (
	"errors"
	"io"
	"os/exec"
)

// Open liest den Monitor der Standard-Senke mit: Das ist der Systemton,
// funktioniert unter PulseAudio genauso wie unter PipeWire.
func Open() (Source, error) {
	if _, err := exec.LookPath("parec"); err != nil {
		return nil, errors.New("parec fehlt – bitte libpulse installieren")
	}
	cmd := exec.Command("parec",
		"--device=@DEFAULT_MONITOR@",
		"--format=s16le",
		"--rate=48000",
		"--channels=2",
		"--latency-msec=20",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &parecSource{cmd: cmd, r: stdout}, nil
}

type parecSource struct {
	cmd *exec.Cmd
	r   io.ReadCloser
}

func (s *parecSource) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *parecSource) Rate() int                  { return 48000 }
func (s *parecSource) Channels() int              { return 2 }

func (s *parecSource) Close() error {
	s.cmd.Process.Kill()
	s.cmd.Wait()
	return nil
}
