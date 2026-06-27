package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMultiMaskScansEveryNetwork(t *testing.T) {
	m1, _ := ParseMask("203.0.113.0/29")  // 8 addrs
	m2, _ := ParseMask("198.51.100.0/29") // 8 addrs
	job := Job{Masks: []*Mask{m1, m2}, Ports: []uint16{80},
		WaitDelay: 2 * time.Millisecond, MaxRings: 2, Seed: 5, Backend: "sim"}
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if eng.State().Snapshot().Stats.Max != 16 {
		t.Fatalf("Max = %d, want 16", eng.State().Snapshot().Stats.Max)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	eng.Run(ctx)
	if got := eng.State().Snapshot().Stats.Dialed; got != 16 {
		t.Fatalf("dialed %d, want 16 (both /29s)", got)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	m, _ := ParseMask("192.0.2.X")
	job := Job{Mask: m, Ports: []uint16{80}, WaitDelay: 2 * time.Millisecond,
		MaxRings: 2, Seed: 9, Backend: "sim", Limit: 30}
	eng, err := New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	eng.Run(context.Background())

	name, blob := eng.ExportDat()
	if !strings.HasPrefix(string(blob), "# DARKCIDR data file") {
		t.Fatalf("export missing header: %q", string(blob[:40]))
	}
	if name == "" {
		t.Fatal("export filename empty")
	}

	// A fresh engine importing that state should reflect the dialed count.
	eng2, _ := New(context.Background(), job)
	before := eng2.State().Snapshot().Stats.Dialed
	merged, err := eng2.ImportDat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if merged == 0 {
		t.Fatal("expected to merge results")
	}
	after := eng2.State().Snapshot().Stats.Dialed
	if after != before+merged {
		t.Fatalf("Dialed = %d, want %d", after, before+merged)
	}
}
