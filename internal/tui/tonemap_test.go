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
	for _, want := range []string{"DARKCIDR", "MAP", "Inspect", "zoom", "close", "203.0.113.0"} {
		if !strings.Contains(frame, want) {
			t.Errorf("map frame missing %q:\n%s", want, frame)
		}
	}

	// Zooming in must shrink the visible window; 'a' fits the whole network back.
	full := app.tm.count
	app.feed('+')
	if app.tm.count >= full {
		t.Errorf("'+' should zoom in (count %d should be < %d)", app.tm.count, full)
	}
	app.feed('a')
	if app.tm.count != full {
		t.Errorf("'a' should fit the whole network (count %d, want %d)", app.tm.count, full)
	}
}

func TestModeToggleAndEscQuit(t *testing.T) {
	mask, _ := engine.ParseMask("203.0.113.X")
	job := engine.Job{Mask: mask, Ports: []uint16{80}, WaitDelay: time.Millisecond,
		MaxRings: 2, Seed: 1, Backend: "sim"}
	eng, _ := engine.New(context.Background(), job)
	app := New(eng, nil)

	if app.mode != modeHosts {
		t.Fatal("should start in hosts mode")
	}
	app.feed('m')
	if app.mode != modeToneMap {
		t.Fatal("M should switch to ToneMap")
	}
	app.feed('m')
	if app.mode != modeServices {
		t.Fatal("M again should switch to Services")
	}
	app.feed('m')
	if app.mode != modeDialer {
		t.Fatal("M again should switch to the classic Dialer")
	}
	app.feed('\t')
	if app.mode != modeHosts {
		t.Fatal("TAB should cycle back to hosts")
	}

	// An arrow key must NOT be read as a quit, and a CSI sequence is not a quit.
	if quit := app.feed('['); quit {
		t.Fatal("ESC[ should begin a CSI, not quit")
	}
	if quit := app.feed('C'); quit { // right arrow
		t.Fatal("arrow key should not quit")
	}

	// Quit now requires confirmation: 'q' opens the prompt (no quit yet).
	if quit := app.feed('q'); quit {
		t.Fatal("q should open the confirm prompt, not quit immediately")
	}
	if !app.confirmQuit {
		t.Fatal("q should set confirmQuit")
	}
	if quit := app.feed('c'); quit || app.confirmQuit {
		t.Fatal("c should cancel the quit prompt")
	}
	// ESC opens the prompt; a second ESC cancels it (double-ESC = cancel).
	app.onEscape()
	if !app.confirmQuit {
		t.Fatal("ESC should open the quit prompt")
	}
	app.onEscape()
	if app.confirmQuit {
		t.Fatal("a second ESC should cancel the quit prompt")
	}
	// Confirm with Y actually quits.
	app.onEscape() // open prompt
	if quit := app.feed('y'); !quit {
		t.Fatal("Y should confirm quit")
	}
}
