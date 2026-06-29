package engine

import (
	"os"
	"testing"
	"time"
)

// TestZmapFallsBackWithoutRoot confirms the zmap backend degrades gracefully when
// raw sockets aren't available (no root): it falls back to the simulator and the
// scan still runs to completion -- i.e. the new streaming Run branch never traps
// a non-ZmapScanner probe.
func TestZmapFallsBackWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; zmap would open a real raw socket")
	}
	mask, _ := ParseMask("203.0.113.X")
	job := Job{
		Mask: mask, Ports: []uint16{80}, WaitDelay: time.Millisecond, MaxRings: 2,
		Backend: "zmap", Seed: 3, Limit: 50,
	}
	eng := runToDone(t, job, 8*time.Second)
	if !eng.State().Snapshot().Done {
		t.Fatal("zmap fallback (no root) did not complete the scan")
	}
}
