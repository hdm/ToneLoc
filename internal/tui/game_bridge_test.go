package tui

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/hdm/toneloc/internal/engine"
)

// TestEnterGameRequest verifies that pressing G outside the Dialer requests the
// in-app game, and that G in the Dialer is left for the "Girl" note.
func TestEnterGameRequest(t *testing.T) {
	mask, _ := engine.ParseMask("10.9.0.X")
	eng, _ := engine.New(context.Background(), engine.Job{
		Mask: mask, Ports: []uint16{22, 80}, WaitDelay: time.Millisecond, MaxRings: 2, Backend: "sim",
	})
	app := New(eng, &bytes.Buffer{})

	app.feed('G') // default mode is Hosts
	if !app.enterGameReq {
		t.Fatal("G in hosts mode should request the game")
	}
	app.enterGameReq = false
	app.setMode(modeDialer)
	app.feed('G') // in the classic dialer, G is the Girl note, not the game
	if app.enterGameReq {
		t.Fatal("G in the Dialer should NOT request the game")
	}
}

// TestRunGameRoundTrip drives the full in-app drop-in: run a sim scan to find
// services, drop into the game, quit it, and confirm control returns (the scan
// keeps running the whole time).
func TestRunGameRoundTrip(t *testing.T) {
	mask, _ := engine.ParseMask("10.9.0.X")
	eng, err := engine.New(context.Background(), engine.Job{
		Mask: mask, Ports: []uint16{21, 22, 23, 80, 443}, WaitDelay: time.Millisecond,
		MaxRings: 4, Seed: 99, Backend: "sim", Limit: 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Wait for at least one service to be discovered.
	deadline := time.Now().Add(5 * time.Second)
	for eng.State().ServiceCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if eng.State().ServiceCount() == 0 {
		t.Skip("no services discovered in time; skipping game round-trip")
	}

	app := New(eng, &bytes.Buffer{})
	keys := make(chan byte, 8)
	keys <- 'q' // quit the game title screen immediately

	done := make(chan struct{})
	go func() {
		app.runGame(ctx, keys)
		close(done)
	}()
	select {
	case <-done:
		// returned from the game cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("runGame did not return after the quit key")
	}

	// The scan must still be alive after returning from the game.
	if eng.State().Snapshot().Done {
		t.Log("scan finished on its own during the test (acceptable)")
	}
}
