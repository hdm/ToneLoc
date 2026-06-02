package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

// Session is the resumable log of a scan: the job parameters, the dialed-target
// data file, and every discovered service (with nerva fingerprints and brutus
// results). It is written alongside the binary as toneloc-<id>.session.
type Session struct {
	ID       string    `json:"id"`
	Backend  string    `json:"backend"`
	Masks    []string  `json:"masks"`
	Ports    []uint16  `json:"ports"`
	Seed     uint64    `json:"seed"`
	Dat      string    `json:"dat"`      // serialized DatFile (dialed targets)
	Services []Service `json:"services"` // discovered services + recon state
}

func newSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

func sessionPath(id string) string { return "toneloc-" + id + ".session" }

// LoadSession reads a saved session by id.
func LoadSession(id string) (*Session, error) {
	data, err := os.ReadFile(sessionPath(id))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt session %s: %w", id, err)
	}
	return &s, nil
}

// JobFromSession rebuilds the scan job described by a session so it can resume.
func JobFromSession(s *Session) (Job, error) {
	var masks []*Mask
	for _, m := range s.Masks {
		pm, err := ParseMask(m)
		if err != nil {
			return Job{}, fmt.Errorf("session mask %q: %w", m, err)
		}
		masks = append(masks, pm)
	}
	if len(masks) == 0 {
		return Job{}, fmt.Errorf("session %s has no networks", s.ID)
	}
	return Job{Masks: masks, Ports: s.Ports, Backend: s.Backend, Seed: s.Seed, SessionID: s.ID}, nil
}

// Preload merges a session's prior results and services into a fresh engine so
// a resumed scan shows everything it found last time (and skips already-dialed
// targets).
func (e *Engine) Preload(s *Session) {
	if s.Dat != "" {
		if _, err := e.ImportDat([]byte(s.Dat)); err != nil {
			e.logf("restore: could not load prior results: %v", err)
		}
	}
	restored := 0
	for i := range s.Services {
		svc := s.Services[i]
		if addr, err := netip.ParseAddr(svc.IP); err == nil {
			svc.Addr = addr
		}
		svc.Brute = bruteStateFromName(svc.BruteName)
		if _, created := e.state.upsertService(&svc); created {
			restored++
		}
	}
	if restored > 0 {
		e.logf("restore: %d service(s) recovered from session %s", restored, s.ID)
	}
}

// saveSession snapshots the whole scan to the session file.
func (e *Engine) saveSession() {
	if e.sessionID == "" {
		return
	}
	e.state.mu.Lock()
	sess := Session{
		ID:      e.sessionID,
		Backend: e.job.Backend,
		Ports:   e.job.Ports,
		Seed:    e.job.Seed,
	}
	for _, m := range e.masks {
		sess.Masks = append(sess.Masks, m.CIDR().String())
	}
	if e.dat != nil {
		sess.Dat = string(e.dat.Bytes())
	}
	sess.Services = make([]Service, len(e.state.services))
	for i, svc := range e.state.services {
		sess.Services[i] = *svc
		sess.Services[i].BruteName = svc.Brute.String()
		sess.Services[i].Creds = append([]Cred(nil), svc.Creds...)
	}
	e.state.mu.Unlock()

	blob, err := json.MarshalIndent(sess, "", " ")
	if err != nil {
		return
	}
	tmp := sessionPath(e.sessionID) + "." + newSessionID() + ".tmp"
	if os.WriteFile(tmp, blob, 0o644) == nil {
		os.Rename(tmp, sessionPath(e.sessionID))
	} else {
		os.Remove(tmp)
	}
}

func bruteStateFromName(n string) BruteState {
	switch strings.ToLower(n) {
	case "done":
		return BruteDone
	case "failed":
		return BruteFailed
	default: // queued/running are not resumable -> back to idle
		return BruteIdle
	}
}
