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
	Mask      *Mask         // address space to dial (single-mask convenience)
	Masks     []*Mask       // multiple address spaces (e.g. every local network)
	Ports     []uint16      // ports to "dial" on each address, in priority order
	Range     *Range        // optional /R restriction (single-mask only)
	Excludes  []*Mask       // optional /X exclude masks (subset of Mask)
	WaitDelay time.Duration // how long to listen on each dial (the meter length)
	MaxRings  int           // give up (Ringout) after this many rings
	Seed      uint64        // 0 = random permutation, else reproducible
	Limit     uint64        // max dials this run (0 = whole space)
	Backend   string        // "sim", "connect" or "zmap"
	SessionID string        // resume an existing session log (else a new id)
}

// maskList returns the masks to scan, preferring Masks but falling back to the
// single Mask.
func (j *Job) maskList() []*Mask {
	if len(j.Masks) > 0 {
		return j.Masks
	}
	if j.Mask != nil {
		return []*Mask{j.Mask}
	}
	return nil
}

// CommonPorts is the default port set when none is given: the services worth
// knocking on first, most-common first (web, SSH, Windows, RDP, mail, ...).
var CommonPorts = []uint16{80, 443, 22, 135, 445, 3389, 8080, 23, 21, 25, 110, 143, 53, 3306, 8443}

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
