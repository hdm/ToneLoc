package engine

import (
	"context"
	"testing"
	"time"
)

// runToDone runs a job to completion (or a timeout) and returns the engine.
func runToDone(t *testing.T, job Job, timeout time.Duration) *Engine {
	t.Helper()
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout + time.Second):
		t.Fatal("engine.Run did not finish")
	}
	return eng
}

// TestConcurrentSweepHonorsLimit checks the parallel sweep dials exactly Limit
// targets and finishes cleanly.
func TestConcurrentSweepHonorsLimit(t *testing.T) {
	mask, _ := ParseMask("10.5.X.X")
	job := Job{
		Mask: mask, Ports: []uint16{80}, WaitDelay: 5 * time.Millisecond, MaxRings: 2,
		Seed: 42, Backend: "sim", Concurrency: 32, Limit: 300,
	}
	eng := runToDone(t, job, 8*time.Second)
	st := eng.State().Snapshot()
	if !st.Done {
		t.Fatal("scan did not mark Done")
	}
	if st.Stats.Dialed != 300 {
		t.Errorf("Dialed = %d, want exactly the limit 300", st.Stats.Dialed)
	}
}

// TestConcurrentSweepExcludes checks /X exclude masks are skipped in the
// parallel path (they don't count toward the dialed total).
func TestConcurrentSweepExcludes(t *testing.T) {
	mask, _ := ParseMask("10.6.0.X")  // 256 addresses
	ex, _ := ParseMask("10.6.0.0/25") // exclude the lower 128
	job := Job{
		Mask: mask, Excludes: []*Mask{ex}, Ports: []uint16{80},
		WaitDelay: 3 * time.Millisecond, MaxRings: 2, Seed: 7, Backend: "sim", Concurrency: 16,
	}
	eng := runToDone(t, job, 8*time.Second)
	st := eng.State().Snapshot()
	if st.Stats.Dialed != 128 {
		t.Errorf("Dialed = %d, want 128 (256 minus the excluded /25)", st.Stats.Dialed)
	}
}

// TestConcurrentSweepFasterThanSerial verifies the sweep really overlaps probes:
// the same workload with high concurrency finishes well under the serial time.
func TestConcurrentSweepFasterThanSerial(t *testing.T) {
	mk := func(conc int) time.Duration {
		mask, _ := ParseMask("10.7.0.X")
		job := Job{
			Mask: mask, Ports: []uint16{80}, WaitDelay: 8 * time.Millisecond, MaxRings: 3,
			Seed: 5, Backend: "sim", Concurrency: conc, Limit: 128,
		}
		start := time.Now()
		runToDone(t, job, 30*time.Second)
		return time.Since(start)
	}
	serial := mk(1)
	parallel := mk(64)
	t.Logf("serial=%v parallel=%v", serial, parallel)
	if parallel >= serial {
		t.Errorf("parallel sweep (%v) should be faster than serial (%v)", parallel, serial)
	}
}
