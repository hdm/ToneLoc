package engine

import (
	"sync"
	"time"
)

// Stats mirrors the counters in the original Statistics window.
type Stats struct {
	Carriers   int
	Tones      int
	Voice      int
	Busy       int
	NoDialtone int
	Ringout    int
	Timeout    int
	Dialed     int
	Max        uint64
}

// FoundEntry is one hit shown in the "Found" sub-window (last few carriers/tones).
type FoundEntry struct {
	Target string
	Resp   Response
	Banner string
	When   time.Time
}

// State is the shared, render-ready snapshot of a running scan. The engine
// writes it under lock; the TUI (terminal or web) reads it on a timer. Keeping
// rendering and scanning decoupled through this struct is what lets the very
// same draw loop power both the DOS terminal and the ghostty.js browser view.
type State struct {
	mu sync.Mutex

	Stats    Stats
	Activity *ring        // left-hand activity log
	Modem    *ring        // top-right modem window
	Found    []FoundEntry // last few hits (for the stats panel)
	hits     []FoundEntry // every carrier/tone found (for the Hall of Fame)

	Meter     float64 // 0..1 progress of the current dial
	Target    string  // current addr:port being dialed
	Rings     int     // rings so far on the current dial
	Tries     int     // try number on the current dial
	DialsHour int

	Status  string // transient bottom-line message
	Paused  bool
	Speaker bool
	Done    bool
	Aborted bool

	StartTime time.Time
	Backend   string
	ModemName string
	MaskText  string
	DataFile  string
	SessionID string

	// tone is the per-address verdict grid backing the ToneMap view: one byte
	// (a Response) per mask index, holding the highest-priority result seen
	// across all ports for that address.
	tone     []uint8
	toneSpan int

	// services is the recon registry: every TCP/UDP service discovered, with
	// its nerva fingerprint and brutus credential-testing state.
	services []*Service
	svcIndex map[string]*Service
}

func newState() *State {
	return &State{
		Activity:  newRing(500),
		Modem:     newRing(200),
		StartTime: time.Now(),
		Speaker:   true,
		svcIndex:  map[string]*Service{},
	}
}

// upsertService inserts or returns the service at proto/ip:port, holding the
// lock. The returned pointer is owned by the State; mutate it only via
// updateService so the renderer never sees a torn write.
func (s *State) upsertService(svc *Service) (*Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.svcIndex == nil {
		s.svcIndex = map[string]*Service{}
	}
	if ex, ok := s.svcIndex[svc.Key()]; ok {
		return ex, false
	}
	svc.IP = svc.Addr.String()
	if svc.First.IsZero() {
		svc.First = time.Now()
	}
	s.svcIndex[svc.Key()] = svc
	s.services = append(s.services, svc)
	return svc, true
}

// updateService runs fn against the live service under lock.
func (s *State) updateService(key string, fn func(*Service)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if svc, ok := s.svcIndex[key]; ok {
		fn(svc)
	}
}

// ServicesSnapshot returns copies of all discovered services for rendering.
func (s *State) ServicesSnapshot() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Service, len(s.services))
	for i, svc := range s.services {
		out[i] = *svc
		out[i].Creds = append([]Cred(nil), svc.Creds...)
	}
	return out
}

// ServiceCount returns how many services have been discovered.
func (s *State) ServiceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.services)
}

// Snapshot copies the volatile parts of the state for a consistent render.
func (s *State) Snapshot() StateView {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make([]FoundEntry, len(s.Found))
	copy(found, s.Found)
	return StateView{
		Stats:     s.Stats,
		Activity:  s.Activity.lines(),
		Modem:     s.Modem.lines(),
		Found:     found,
		Meter:     s.Meter,
		Target:    s.Target,
		Rings:     s.Rings,
		Tries:     s.Tries,
		DialsHour: s.DialsHour,
		Status:    s.Status,
		Paused:    s.Paused,
		Speaker:   s.Speaker,
		Done:      s.Done,
		Aborted:   s.Aborted,
		StartTime: s.StartTime,
		Backend:   s.Backend,
		ModemName: s.ModemName,
		MaskText:  s.MaskText,
		DataFile:  s.DataFile,
		SessionID: s.SessionID,
		Now:       time.Now(),
	}
}

// StateView is an immutable copy handed to the renderer.
type StateView struct {
	Stats     Stats
	Activity  []string
	Modem     []string
	Found     []FoundEntry
	Meter     float64
	Target    string
	Rings     int
	Tries     int
	DialsHour int
	Status    string
	Paused    bool
	Speaker   bool
	Done      bool
	Aborted   bool
	StartTime time.Time
	Backend   string
	ModemName string
	MaskText  string
	DataFile  string
	SessionID string
	Now       time.Time
}

// markTone records resp at mask index idx, keeping the highest-priority verdict
// (so a carrier found on any port colours that address as a carrier). Callers
// must hold s.mu.
func (s *State) markTone(idx int, resp Response) {
	if idx < 0 || idx >= len(s.tone) {
		return
	}
	if resp.Priority() >= Response(s.tone[idx]).Priority() {
		s.tone[idx] = uint8(resp)
	}
}

// HitsSnapshot returns a copy of every carrier/tone found so far, for the Hall
// of Fame view.
func (s *State) HitsSnapshot() []FoundEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FoundEntry, len(s.hits))
	copy(out, s.hits)
	return out
}

// ToneSpan is the number of addresses in the map.
func (s *State) ToneSpan() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toneSpan
}

// RenderTone downsamples the verdict grid into a cols x rows block, where each
// cell holds the highest-priority response among the addresses it covers. This
// keeps the ToneMap legible whether it is showing a /24 or a /8. The returned
// slice has len cols*rows (row-major); perCell is how many addresses each cell
// represents (>=1).
func (s *State) RenderTone(cols, rows int) (cells []uint8, perCell int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols < 1 || rows < 1 || s.toneSpan == 0 {
		return nil, 1
	}
	total := cols * rows
	perCell = (s.toneSpan + total - 1) / total
	if perCell < 1 {
		perCell = 1
	}
	cells = make([]uint8, total)
	for c := 0; c < total; c++ {
		start := c * perCell
		if start >= s.toneSpan {
			break
		}
		end := start + perCell
		if end > s.toneSpan {
			end = s.toneSpan
		}
		best := uint8(RespUndialed)
		bestP := -1
		for i := start; i < end; i++ {
			if p := Response(s.tone[i]).Priority(); p > bestP {
				bestP = p
				best = s.tone[i]
			}
		}
		cells[c] = best
	}
	return cells, perCell
}

// ring is a fixed-capacity line buffer (the windows scroll).
type ring struct {
	buf  []string
	max  int
	head int
	n    int
}

func newRing(max int) *ring { return &ring{buf: make([]string, max), max: max} }

func (r *ring) push(s string) {
	r.buf[r.head] = s
	r.head = (r.head + 1) % r.max
	if r.n < r.max {
		r.n++
	}
}

func (r *ring) lines() []string {
	out := make([]string, r.n)
	start := (r.head - r.n + r.max) % r.max
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(start+i)%r.max]
	}
	return out
}
