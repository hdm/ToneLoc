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
		Mask:  mask,
		Ports: []uint16{23, 80, 443}, WaitDelay: 200 * time.Millisecond,
		MaxRings: 6, Seed: 1337, Backend: "sim",
	}
	eng, err := engine.New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	go eng.Run(context.Background())
	time.Sleep(1500 * time.Millisecond)

	app := New(eng, nil)
	frame := app.Frame()
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(lines) != app.scr.H {
		t.Fatalf("expected %d rows, got %d", app.scr.H, len(lines))
	}
	for i, ln := range lines {
		if got := len([]rune(ln)); got != app.scr.W {
			t.Fatalf("row %d width = %d, want %d", i, got, app.scr.W)
		}
	}
	for _, want := range []string{"Activity Log", "Modem", "Statistics", "MaxDials"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q", want)
		}
	}
	// No activity line should bleed past the Activity Log's right border.
	for _, ln := range lines[1 : app.lActH-1] {
		r := []rune(ln)
		if r[app.lActW-1] != '║' {
			t.Errorf("activity border clobbered: %q", string(r[:app.lActW]))
			break
		}
	}

	// And a larger terminal should fill more rows/cols.
	app.SetSize(120, 40)
	big := strings.Split(strings.TrimRight(app.Frame(), "\n"), "\n")
	if len(big) != 40 || len([]rune(big[0])) != 120 {
		t.Fatalf("resize: got %dx%d, want 120x40", len([]rune(big[0])), len(big))
	}
}
