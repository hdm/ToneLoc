package tui

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hdm/toneloc/internal/engine"
)

// TestGenerateScreenshot writes SVGs of the dialer and ToneMap views when
// TONELOC_SVG_OUT (a directory) is set; it's a generator, not an assertion, so
// it's skipped in normal runs.
func TestGenerateScreenshot(t *testing.T) {
	dir := os.Getenv("TONELOC_SVG_OUT")
	if dir == "" {
		t.Skip("set TONELOC_SVG_OUT=<dir> to generate screenshots")
	}

	// A /16 so the ToneMap fills densely; tiny wait so it dials fast.
	mask, _ := engine.ParseMask("10.37.X.X")
	job := engine.Job{Mask: mask, Ports: []uint16{23, 80, 443},
		WaitDelay: 1 * time.Millisecond, MaxRings: 6, Seed: 1337, Backend: "sim", Limit: 12000}
	eng, err := engine.New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	time.Sleep(3 * time.Second) // let plenty of results accumulate

	// Intro splash.
	splash := New(eng, nil)
	splash.splashUntil = time.Now().Add(time.Hour)
	splash.frame = 9
	write(t, dir+"/toneloc-splash.svg", splash.FrameSVG())

	// Dialer view, at a larger terminal to show the responsive layout.
	dialer := New(eng, nil)
	dialer.SetSize(132, 42)
	dialer.frame = 4
	write(t, dir+"/toneloc-dialer.svg", dialer.FrameSVG())

	// ToneMap view, cursor parked on a populated cell.
	tm := New(eng, nil)
	tm.setMode(modeToneMap)
	tm.curCol, tm.curRow = 18, 9
	tm.frame = 4
	write(t, dir+"/toneloc-tonemap.svg", tm.FrameSVG())

	// Hall of Fame.
	hof := New(eng, nil)
	hof.setMode(modeHallOfFame)
	hof.frame = 4
	write(t, dir+"/toneloc-halloffame.svg", hof.FrameSVG())

	// Services view: kick off a few brutes so statuses/compromise show, then
	// render the list and a detail card.
	count := 0
	for _, sv := range eng.State().ServicesSnapshot() {
		if sv.Brutable {
			eng.StartBrute(sv.Key())
			if count++; count >= 12 {
				break
			}
		}
	}
	time.Sleep(2500 * time.Millisecond) // let some brutes finish/compromise

	svcView := New(eng, nil)
	svcView.SetSize(132, 42)
	svcView.setMode(modeServices)
	svcView.frame = 4
	write(t, dir+"/toneloc-services.svg", svcView.FrameSVG())

	// Detail card for the first compromised (or brutable) service.
	detail := New(eng, nil)
	detail.SetSize(132, 42)
	detail.setMode(modeServices)
	svcs := eng.State().ServicesSnapshot()
	for i, sv := range svcs {
		if sv.Compromised {
			detail.svcSel = i
			break
		} else if sv.Brutable && detail.svcSel == 0 {
			detail.svcSel = i
		}
	}
	detail.svcDetail = true
	detail.frame = 4
	write(t, dir+"/toneloc-service-detail.svg", detail.FrameSVG())
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
