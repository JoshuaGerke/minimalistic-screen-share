package server

import (
	"crypto/subtle"
	"encoding/binary"
	"sort"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/proto"
)

// Der Sprachkanal hängt am Raum: Mitreden dürfen freigegebene Zuschauer und
// das Bedienpanel des Streamers. Das Panel weist sich mit einem Schlüssel aus,
// den nur die Host-App kennt (sie schickt ihn als "voicekey").

// setVoiceKey merkt sich den Schlüssel für das Panel des Hosts.
func (s *Server) setVoiceKey(host *client, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if host.room != nil && host.room.host == host && len(key) >= 16 {
		host.room.voiceKey = key
	}
}

// joinPanelLocked nimmt das Panel des Hosts auf. Der Aufrufer hält s.mu.
func (s *Server) joinPanelLocked(c *client, rm *room) bool {
	if rm == nil || rm.voiceKey == "" || subtle.ConstantTimeCompare([]byte(c.token), []byte(rm.voiceKey)) != 1 {
		return false
	}
	rm.nextID++
	c.id = rm.nextID
	c.room = rm
	rm.panels[c] = struct{}{}
	c.enqueueControl(proto.Control{T: "joined", Room: rm.code, ID: c.id})
	c.enqueueControl(rm.voiceRoster())
	return true
}

// canVoice: nur wer drin ist (freigegeben oder Panel), darf mitreden.
func (rm *room) canVoice(c *client) bool {
	if _, ok := rm.viewers[c]; ok {
		return true
	}
	_, ok := rm.panels[c]
	return ok
}

// voiceJoin betritt oder verlässt den Sprachkanal.
func (s *Server) voiceJoin(c *client, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil || !rm.canVoice(c) {
		return
	}
	if on {
		rm.voice[c] = struct{}{}
		c.voicePkts = 0
		c.enqueueControl(proto.Control{T: "voice-you", ID: c.id})
		s.log.Printf("Raum %q: %s im Sprachkanal (%d)", rm.code, c.name, len(rm.voice))
	} else {
		delete(rm.voice, c)
		c.voiceMuted, c.voiceDeaf = false, false
	}
	s.sendVoiceRosterLocked(rm)
}

// voiceState übernimmt Stumm/Taub eines Teilnehmers.
func (s *Server) voiceState(c *client, muted, deaf bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil {
		return
	}
	if _, ok := rm.voice[c]; !ok {
		return
	}
	c.voiceMuted, c.voiceDeaf = muted, deaf
	s.sendVoiceRosterLocked(rm)
}

// leaveVoiceLocked räumt einen Teilnehmer aus Sprachkanal und Panel-Liste.
// Der Aufrufer hält s.mu.
func (s *Server) leaveVoiceLocked(rm *room, c *client) {
	_, inVoice := rm.voice[c]
	delete(rm.voice, c)
	delete(rm.panels, c)
	if inVoice {
		s.sendVoiceRosterLocked(rm)
	}
}

// relayVoice verteilt ein Sprachpaket an alle anderen im Kanal. Die
// Sprecher-ID setzt der Relay selbst – niemand kann sich als jemand anders
// ausgeben.
func (s *Server) relayVoice(c *client, msg []byte) {
	if len(msg) <= proto.VoiceHeaderLen {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := c.room
	if rm == nil {
		return
	}
	if _, ok := rm.voice[c]; !ok || c.voiceMuted {
		return
	}
	if c.voicePkts++; c.voicePkts == 1 {
		s.log.Printf("Raum %q: Ton von %s kommt im Sprachkanal an", rm.code, c.name)
	}
	out := append([]byte(nil), msg...)
	binary.LittleEndian.PutUint32(out[1:], uint32(c.id))
	for m := range rm.voice {
		if m != c && !m.voiceDeaf {
			m.enqueue(out, true)
		}
	}
}

func (rm *room) voiceRoster() proto.Control {
	members := make([]proto.VoiceMember, 0, len(rm.voice))
	for m := range rm.voice {
		members = append(members, proto.VoiceMember{ID: m.id, Name: m.name, Muted: m.voiceMuted, Deaf: m.voiceDeaf})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return proto.Control{T: "voice", Members: members}
}

// sendVoiceRosterLocked zeigt allen im Raum, wer gerade im Sprachkanal ist.
func (s *Server) sendVoiceRosterLocked(rm *room) {
	ctl := rm.voiceRoster()
	for v := range rm.viewers {
		v.enqueueControl(ctl)
	}
	for p := range rm.panels {
		p.enqueueControl(ctl)
	}
}

// setTrusted übernimmt die dauerhaft freigegebenen Tokens des Hosts.
func (s *Server) setTrusted(host *client, tokens []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := host.room
	if rm == nil || rm.host != host {
		return
	}
	rm.trusted = map[string]bool{}
	for _, t := range tokens {
		if len(t) >= 16 {
			rm.trusted[t] = true
		}
	}
	// Wer vertraut ist und gerade wartet, darf sofort rein.
	for p := range rm.pending {
		if rm.trusted[p.token] {
			delete(rm.pending, p)
			s.admitLocked(rm, p)
			host.enqueueControl(proto.Control{T: "knock-gone", ID: p.id})
		}
	}
	s.sendViewersLocked(rm)
}
