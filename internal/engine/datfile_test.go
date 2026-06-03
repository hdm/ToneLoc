package engine

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func TestDatFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.DAT")
	d := &DatFile{Path: path, Mask: "192.0.2.X", Ports: []uint16{23, 80}, Results: map[string]Result{}}
	d.record(Result{Addr: netip.MustParseAddr("192.0.2.18"), Port: 23, Response: RespCarrier, Rings: 1, Tries: 1})
	d.record(Result{Addr: netip.MustParseAddr("192.0.2.91"), Port: 80, Response: RespTone, Rings: 2, Tries: 1})
	d.record(Result{Addr: netip.MustParseAddr("192.0.2.5"), Port: 23, Response: RespBusy, Rings: 0, Tries: 1})
	d.record(Result{Addr: netip.MustParseAddr("192.0.2.9"), Port: 80, Response: RespAborted}) // must NOT persist
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mask != "192.0.2.X" {
		t.Errorf("mask = %q", got.Mask)
	}
	if len(got.Ports) != 2 {
		t.Errorf("ports = %v", got.Ports)
	}
	if len(got.Results) != 3 {
		t.Fatalf("expected 3 persisted results (aborted dropped), got %d", len(got.Results))
	}
	if r := got.Results["192.0.2.18:23"]; r.Response != RespCarrier || r.Rings != 1 {
		t.Errorf("carrier record round-tripped wrong: %+v", r)
	}
	if !got.Has("192.0.2.91:80") {
		t.Error("Has() should report a recorded target")
	}
}

func TestLoadDatMissingFileIsEmpty(t *testing.T) {
	d, err := LoadDat(filepath.Join(t.TempDir(), "nope.DAT"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(d.Results) != 0 {
		t.Fatalf("expected empty results, got %d", len(d.Results))
	}
}

func TestEngineResumesFromDat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.DAT")

	mask, _ := ParseMask("192.0.2.X") // 256 addrs
	ports := []uint16{80}

	// Pre-seed a data file with half the space already dialed.
	pre := &DatFile{Path: path, Mask: mask.Text(), Ports: ports, Results: map[string]Result{}}
	for i := 0; i < 128; i++ {
		pre.record(Result{Addr: mask.Addr(uint32(i)), Port: 80, Response: RespTimeout, Tries: 1})
	}
	pre.record(Result{Addr: mask.Addr(200), Port: 80, Response: RespCarrier, Rings: 1, Tries: 1})
	if err := pre.Save(); err != nil {
		t.Fatal(err)
	}

	job := Job{DataFile: path, Mask: mask, Ports: ports, WaitDelay: 5 * time.Millisecond,
		MaxRings: 2, Seed: 42, Backend: "sim", Resume: true}
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	// Stats should already reflect the loaded history before dialing.
	v := eng.State().Snapshot()
	if v.Stats.Dialed != 129 {
		t.Fatalf("seeded Dialed = %d, want 129", v.Stats.Dialed)
	}
	if v.Stats.Carriers != 1 {
		t.Fatalf("seeded Carriers = %d, want 1", v.Stats.Carriers)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eng.Run(ctx) // runs to completion over the remaining space

	final := eng.State().Snapshot()
	// The whole space (256) should now be covered exactly once.
	if final.Stats.Dialed != 256 {
		t.Fatalf("final Dialed = %d, want 256", final.Stats.Dialed)
	}

	// And the data file should hold every target.
	saved, err := LoadDat(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Results) != 256 {
		t.Fatalf("saved %d results, want 256", len(saved.Results))
	}
}
