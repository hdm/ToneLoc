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
	When   time.Time
}

// State is the shared, render-ready snapshot of a running scan. The engine
// writes it under lock; the TUI (terminal or web) reads it on a timer. Keeping
// rendering and scanning decoupled through this struct is what lets the very
// same draw loop power both the DOS terminal and the ghostty.js browser view.
type State struct {
	mu sync.Mutex

	Stats    Stats
	Activity *ring // left-hand activity log
	Modem    *ring // top-right modem window
	Found    []FoundEntry

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
}

func newState() *State {
	return &State{
		Activity:  newRing(500),
		Modem:     newRing(200),
		StartTime: time.Now(),
		Speaker:   true,
	}
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
	Now       time.Time
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
