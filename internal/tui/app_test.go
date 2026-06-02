package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hdm/toneloc/internal/engine"
)

func TestFrameLayout(t *testing.T) {
	mask, err := engine.ParseMask("204.13.7.X")
	if err != nil {
		t.Fatal(err)
	}
	job := engine.Job{
		DataFile: "204_13_7.DAT", Mask: mask,
		Ports: []uint16{23, 80, 443}, WaitDelay: 200 * time.Millisecond,
		MaxRings: 6, Seed: 1337, Backend: "sim",
	}
	eng, err := engine.New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	go eng.Run(context.Background())
	time.Sleep(1500 * time.Millisecond)

	frame := New(eng, nil).Frame()
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(lines) != scrH {
		t.Fatalf("expected %d rows, got %d", scrH, len(lines))
	}
	for i, ln := range lines {
		if got := len([]rune(ln)); got != scrW {
			t.Fatalf("row %d width = %d, want %d", i, got, scrW)
		}
	}
	for _, want := range []string{"Activity Log", "Modem", "Statistics", "ToneLoc started", "MaxDials"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q", want)
		}
	}
	// No activity line should bleed past the Activity Log's right border.
	for _, ln := range lines[1 : actH-1] {
		r := []rune(ln)
		if r[actW-1] != '║' {
			t.Errorf("activity border clobbered: %q", string(r[:actW]))
			break
		}
	}
}
