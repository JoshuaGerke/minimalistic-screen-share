package server

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

// Konferenz: Neben dem Host können freigegebene Teilnehmer ihren Bildschirm
// in den Raum teilen. Ihre App verbindet sich dafür als "presenter" – mit
// dem Freigabe-Token ihres Viewers (oder dem Schlüssel des Hosts). Alles,
// was ein Teilender schickt, bekommt seine Kennung vorangestellt
// (MsgSourced), damit die Viewer die Bildschirme auseinanderhalten.

// source ist ein zusätzlicher Bildschirm in der Konferenz.
type source struct {
	name     string
	token    string // gleicher Token = gleiche Person, bekommt ihr Bild nicht zurück
	sticky   map[string][]byte
	gop      [][]byte
	gopBytes int
}

// validTokenLocked prüft, ob jemand in diesem Raum freigegeben ist.
func (s *Server) validTokenLocked(rm *room, token string) bool {
	if token == "" {
		return false
	}
	if rm.voiceKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(rm.voiceKey)) == 1 {
		return true
	}
	exp, ok := s.tokens[rm.code][token]
	return ok && time.Now().Before(exp)
}

// joinPresenterLocked nimmt einen Teilenden auf. Der Aufrufer hält s.mu.
func (s *Server) joinPresenterLocked(c *client, rm *room) bool {
	if rm == nil || !s.validTokenLocked(rm, c.token) {
		return false
	}
	rm.nextID++
	c.id = rm.nextID
	c.room = rm
	rm.presenters[c] = &source{name: c.name, token: c.token, sticky: map[string][]byte{}}
	c.enqueueControl(proto.Control{T: "joined", Room: rm.code, ID: c.id, N: rm.audienceFor(c.token)})
	s.log.Printf("Raum %q: %s teilt seinen Bildschirm", rm.code, c.name)
	s.sendSourcesLocked(rm)
	return true
}

// audienceFor zählt, wer ein Bild zu sehen bekommt – ohne die Person selbst
// und ohne den Host, der sich die Konferenz ansieht.
func (rm *room) audienceFor(token string) int {
	n := 0
	for v := range rm.viewers {
		if token == "" && v.hostView {
			continue
		}
		if token != "" && v.token == token {
			continue
		}
		n++
	}
	return n
}

// sendsTo sagt, ob ein Bild der Quelle an den Zuschauer v gehen soll.
func (src *source) sendsTo(v *client) bool { return v.token != src.token }

// relaySourced verteilt Bild und Ton eines Teilenden mit seiner Kennung.
func (s *Server) relaySourced(c *client, msg []byte) {
	if len(msg) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil {
		return
	}
	src, ok := rm.presenters[c]
	if !ok {
		return
	}
	out := make([]byte, 5+len(msg))
	out[0] = proto.MsgSourced
	binary.LittleEndian.PutUint32(out[1:], uint32(c.id))
	copy(out[5:], msg)
	if msg[0] == proto.MsgVideoChunk {
		if proto.VideoKeyStart(msg) {
			src.gop, src.gopBytes = src.gop[:0], 0
		}
		if src.gopBytes+len(out) <= gopLimit {
			src.gop = append(src.gop, out)
			src.gopBytes += len(out)
		}
	}
	priority := msg[0] == proto.MsgAudio || msg[0] == proto.MsgSilence
	for v := range rm.viewers {
		if src.sendsTo(v) {
			v.enqueue(out, priority)
		}
	}
}

// relayPresenterControl reicht meta und pause eines Teilenden weiter.
func (s *Server) relayPresenterControl(c *client, ctl proto.Control) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil {
		return
	}
	src, ok := rm.presenters[c]
	if !ok {
		return
	}
	ctl.Src = c.id
	data, _ := json.Marshal(ctl)
	if ctl.T == "meta" || ctl.T == "pause" {
		src.sticky[ctl.T] = data
	}
	for v := range rm.viewers {
		if src.sendsTo(v) {
			v.enqueue(data, true)
		}
	}
}

// leavePresenterLocked räumt einen Teilenden ab. Der Aufrufer hält s.mu.
func (s *Server) leavePresenterLocked(rm *room, c *client) {
	if _, ok := rm.presenters[c]; !ok {
		return
	}
	delete(rm.presenters, c)
	s.log.Printf("Raum %q: %s teilt nicht mehr", rm.code, c.name)
	gone, _ := json.Marshal(proto.Control{T: "source-gone", Src: c.id})
	for v := range rm.viewers {
		v.enqueue(gone, true)
	}
	s.sendSourcesLocked(rm)
}

// sendSourcesLocked nennt allen Zuschauern die zusätzlichen Bildschirme.
func (s *Server) sendSourcesLocked(rm *room) {
	for v := range rm.viewers {
		v.enqueueControl(rm.sourcesFor(v))
	}
}

func (rm *room) sourcesFor(v *client) proto.Control {
	list := make([]proto.VoiceMember, 0, len(rm.presenters))
	for p, src := range rm.presenters {
		if v == nil || src.sendsTo(v) {
			list = append(list, proto.VoiceMember{ID: p.id, Name: src.name})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return proto.Control{T: "sources", Members: list}
}

// sendSourceStateLocked reicht einem neuen Zuschauer alle Teilenden samt
// Stream-Beschreibung und Bild ab dem letzten Keyframe nach.
func (s *Server) sendSourceStateLocked(rm *room, v *client) {
	if len(rm.presenters) == 0 {
		return
	}
	v.enqueueControl(rm.sourcesFor(v))
	for _, src := range rm.presenters {
		if !src.sendsTo(v) {
			continue
		}
		for _, msg := range src.sticky {
			v.enqueue(msg, true)
		}
		for _, msg := range src.gop {
			v.enqueue(msg, false)
		}
	}
}

// sendPresenterAudienceLocked sagt jedem Teilenden, ob jemand zuschaut –
// ohne Zuschauer pausiert seine Aufnahme wie beim Host.
func (s *Server) sendPresenterAudienceLocked(rm *room) {
	for p, src := range rm.presenters {
		p.enqueueControl(proto.Control{T: "viewers", N: rm.audienceFor(src.token)})
	}
}

// ping leitet einen Zeige-Klick an den weiter, dessen Bildschirm gemeint
// ist. Höchstens einer pro Sekunde und Person – damit es nicht nervt.
func (s *Server) ping(c *client, ctl proto.Control) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil {
		return
	}
	if _, ok := rm.viewers[c]; !ok || time.Since(c.lastPing) < time.Second {
		return
	}
	if ctl.X < 0 || ctl.X > 1 || ctl.Y < 0 || ctl.Y > 1 {
		return
	}
	c.lastPing = time.Now()
	out := proto.Control{T: "ping", X: ctl.X, Y: ctl.Y, Name: c.name}
	if ctl.Src == 0 {
		if rm.host != nil {
			rm.host.enqueueControl(out)
		}
		return
	}
	for p := range rm.presenters {
		if p.id == ctl.Src {
			p.enqueueControl(out)
		}
	}
}
