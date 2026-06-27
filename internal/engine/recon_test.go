package engine

import (
	"context"
	"os"
	"testing"
	"time"
)

// The sim pipeline should: find open TCP ports as services, fingerprint them,
// discover some UDP services, and -- on a brutable service -- run brutus to
// completion, marking it compromised when weak creds exist.
func TestReconPipelineSim(t *testing.T) {
	chdirTemp(t)
	mask, _ := ParseMask("192.0.2.X")
	job := Job{DataFile: "recon.DAT", Mask: mask, Ports: CommonPorts, Limit: 300,
		WaitDelay: time.Millisecond, MaxRings: 2, Seed: 1234, Backend: "sim"}
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	eng.Run(ctx) // scans to completion (UDP discovery runs concurrently)

	// Give the concurrent UDP discovery a beat to register.
	deadline := time.Now().Add(5 * time.Second)
	for eng.State().ServiceCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	svcs := eng.State().ServicesSnapshot()
	if len(svcs) == 0 {
		t.Fatal("expected some services discovered")
	}
	var tcp, udp, brutable int
	var pick *Service
	for i := range svcs {
		switch svcs[i].Proto {
		case "tcp":
			tcp++
		case "udp":
			udp++
		}
		if svcs[i].Brutable && pick == nil {
			pick = &svcs[i]
		}
		if svcs[i].Brutable {
			brutable++
		}
	}
	if tcp == 0 {
		t.Error("expected open TCP services")
	}
	t.Logf("services: %d (tcp=%d udp=%d brutable=%d)", len(svcs), tcp, udp, brutable)
	if pick == nil {
		t.Skip("no brutable service in this sample")
	}

	// Brute the picked service and wait for it to finish.
	eng.StartBrute(pick.Key())
	done := time.Now().Add(30 * time.Second)
	var final Service
	for time.Now().Before(done) {
		for _, s := range eng.State().ServicesSnapshot() {
			if s.Key() == pick.Key() {
				final = s
			}
		}
		if final.Brute == BruteDone || final.Brute == BruteFailed {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if final.Brute != BruteDone {
		t.Fatalf("brute did not finish: state=%v tried=%d", final.Brute, final.Tried)
	}
	if final.Tried == 0 {
		t.Error("expected brutus to report tried credentials")
	}
	t.Logf("brute %s: compromised=%v creds=%v", final.Target(), final.Compromised, final.Creds)
}

func TestSessionSaveRestore(t *testing.T) {
	dir := chdirTemp(t)
	_ = dir
	mask, _ := ParseMask("198.51.100.X")
	job := Job{DataFile: "sess.DAT", Mask: mask, Ports: []uint16{22, 80},
		WaitDelay: 2 * time.Millisecond, MaxRings: 2, Seed: 77, Backend: "sim"}
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	id := eng.SessionID()
	eng.Run(context.Background()) // writes the session on finish
	time.Sleep(200 * time.Millisecond)

	sess, err := LoadSession(id)
	if err != nil {
		t.Fatalf("load session %s: %v", id, err)
	}
	if len(sess.Masks) == 0 || sess.Backend != "sim" {
		t.Fatalf("session missing job info: %+v", sess.Masks)
	}

	// Restore into a fresh engine: prior services + dialed counts come back.
	job2, err := JobFromSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	if job2.SessionID != id {
		t.Fatalf("restored session id = %q, want %q", job2.SessionID, id)
	}
	eng2, err := New(context.Background(), job2)
	if err != nil {
		t.Fatal(err)
	}
	eng2.Preload(sess)
	if eng2.State().ServiceCount() != len(sess.Services) {
		t.Fatalf("restored %d services, session had %d", eng2.State().ServiceCount(), len(sess.Services))
	}
	if eng2.State().Snapshot().Stats.Dialed == 0 {
		t.Error("expected restored dialed count > 0")
	}
}

// chdirTemp runs the test in a temp dir so .DAT/.session files don't litter the
// package directory.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}
