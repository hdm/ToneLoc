package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hdm/toneloc/internal/engine"
)

func TestToneMapFrame(t *testing.T) {
	mask, _ := engine.ParseMask("203.0.113.X")
	job := engine.Job{Mask: mask, Ports: []uint16{80},
		WaitDelay: 5 * time.Millisecond, MaxRings: 4, Seed: 7, Backend: "sim", Limit: 200}
	eng, err := engine.New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	time.Sleep(1500 * time.Millisecond)

	app := New(eng, nil)
	app.setMode(modeToneMap)
	app.hoverAt(app.lTmGX+1, app.lTmGY) // hover the second cell (address index 1)
	frame := app.Frame()

	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(lines) != minH {
		t.Fatalf("expected %d rows, got %d", minH, len(lines))
	}
	for i, ln := range lines {
		if got := len([]rune(ln)); got != minW {
			t.Fatalf("row %d width = %d, want %d", i, got, minW)
		}
	}
	for _, want := range []string{"ToneMap", "203.0.113.X", "Carrier", "Undialed", "ESC:quit"} {
		if !strings.Contains(frame, want) {
			t.Errorf("ToneMap frame missing %q", want)
		}
	}
	// Hovering cell index 1 should surface that address in the footer.
	if !strings.Contains(frame, "203.0.113.1") {
		t.Errorf("expected hovered address 203.0.113.1 in footer:\n%s", frame)
	}
}

func TestModeToggleAndEscQuit(t *testing.T) {
	mask, _ := engine.ParseMask("203.0.113.X")
	job := engine.Job{Mask: mask, Ports: []uint16{80}, WaitDelay: time.Millisecond,
		MaxRings: 2, Seed: 1, Backend: "sim"}
	eng, _ := engine.New(context.Background(), job)
	app := New(eng, nil)

	if app.mode != modeDialer {
		t.Fatal("should start in dialer mode")
	}
	app.feed('m')
	if app.mode != modeToneMap {
		t.Fatal("M should switch to ToneMap")
	}
	app.feed('m')
	if app.mode != modeHallOfFame {
		t.Fatal("M again should switch to Hall of Fame")
	}
	app.feed('m')
	if app.mode != modeServices {
		t.Fatal("M again should switch to Services")
	}
	app.feed('\t')
	if app.mode != modeDialer {
		t.Fatal("TAB should cycle back to dialer")
	}

	// An arrow key must NOT be read as a quit, but a lone ESC byte (followed by
	// a non-'[' byte) should.
	if quit := app.feed(0x1b); quit {
		t.Fatal("ESC alone should defer, not quit immediately")
	}
	if quit := app.feed('['); quit {
		t.Fatal("ESC[ should begin a CSI, not quit")
	}
	if quit := app.feed('C'); quit { // right arrow
		t.Fatal("arrow key should not quit")
	}
	if quit := app.feed('q'); !quit {
		t.Fatal("q should quit")
	}
}
