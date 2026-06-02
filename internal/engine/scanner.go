package engine

import (
	"context"
	"net/netip"
	"time"
)

// Job is everything ToneLoc needs to start a scan -- the IP-space equivalent of
// the original command line (DataFile /M /R /X /p ...).
type Job struct {
	DataFile  string        // .DAT-style persistence file (named like the original)
	Mask      *Mask         // address space to dial
	Ports     []uint16      // ports to "dial" on each address
	Range     *Range        // optional /R restriction
	Excludes  []*Mask       // optional /X exclude masks (subset of Mask)
	WaitDelay time.Duration // how long to listen on each dial (the meter length)
	MaxRings  int           // give up (Ringout) after this many rings
	Seed      uint64        // 0 = random permutation, else reproducible
	Limit     uint64        // max dials this run (0 = whole space)
	Backend   string        // "sim", "connect" or "zmap"
}

// Probe dials a single target and returns ToneLoc's verdict. Implementations
// should honour ctx cancellation (the user hitting space/ESC mid-dial) and
// must not block longer than the job's WaitDelay plus a small grace period.
type Probe interface {
	// Name is shown in the modem window ("USRobotics", "zmap", ...).
	Name() string
	// Dial probes addr:port. waitDelay bounds how long to listen.
	Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result
	// Close releases any resources (open sockets, child processes).
	Close() error
}

// excluded reports whether addr falls inside any of the job's /X masks.
func (j *Job) excluded(addr netip.Addr) bool {
	for _, ex := range j.Excludes {
		if _, in := ex.Index(addr); in {
			return true
		}
	}
	return false
}
