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

	// Hosts view (the default): full-screen per-host progress bars.
	hosts := New(eng, nil)
	hosts.SetSize(150, 46)
	hosts.frame = 4
	write(t, dir+"/toneloc-hosts.svg", hosts.FrameSVG())

	// Dialer view, at a larger terminal to show the responsive layout.
	dialer := New(eng, nil)
	dialer.SetSize(132, 42)
	dialer.setMode(modeDialer)
	dialer.frame = 4
	write(t, dir+"/toneloc-dialer.svg", dialer.FrameSVG())

	// ToneMap view, full screen, whole network.
	tm := New(eng, nil)
	tm.SetSize(160, 48)
	tm.setMode(modeToneMap)
	tm.tm.curX, tm.tm.curY = 24, 12
	tm.frame = 4
	tm.draw(eng.State().Snapshot()) // first draw establishes the grid dims
	write(t, dir+"/toneloc-tonemap.svg", tm.FrameSVG())

	// ToneMap zoomed in twice around the cursor.
	tm.feed('+')
	tm.feed('+')
	tm.frame = 4
	write(t, dir+"/toneloc-tonemap-zoom.svg", tm.FrameSVG())

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
