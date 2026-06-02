package tui

import (
	"fmt"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// drawServices renders the recon results: every TCP/UDP service nerva/connect
// found, summarized one per line. Select one and ENTER for full detail; press B
// to launch brutus against it. Compromised services glow.
func (a *App) drawServices(v engine.StateView) {
	s := a.scr
	svcs := a.eng.State().ServicesSnapshot()

	comp := 0
	for _, sv := range svcs {
		if sv.Compromised {
			comp++
		}
	}
	hdr := fmt.Sprintf(" Services   %d found   %d compromised   session %s", len(svcs), comp, v.SessionID)
	s.Print(0, 0, dos.Attr(dos.Black, dos.LightCyan), dos.Pad(hdr, scrW))

	if len(svcs) == 0 {
		center(s, 11, dos.Attr(dos.LightGray, dos.Black), "no services discovered yet -- scanning...")
		center(s, 12, dos.Attr(dos.DarkGray, dos.Black), "(connect/zmap finds TCP, nerva finds UDP + fingerprints)")
		a.svcHints()
		return
	}

	if a.svcSel >= len(svcs) {
		a.svcSel = len(svcs) - 1
	}
	if a.svcSel < 0 {
		a.svcSel = 0
	}

	if a.svcDetail {
		a.drawServiceDetail(svcs[a.svcSel])
		a.svcHints()
		return
	}

	// Scrolling list.
	top, rows := 2, statRow-3
	if a.svcSel < a.svcScroll {
		a.svcScroll = a.svcSel
	}
	if a.svcSel >= a.svcScroll+rows {
		a.svcScroll = a.svcSel - rows + 1
	}
	s.Box(0, 1, scrW, rows+2, dos.Attr(dos.LightCyan, dos.Black), true)
	s.Title(0, 1, scrW, dos.Attr(dos.White, dos.Black), "Discovered Services")
	s.Print(2, 1, dos.Attr(dos.DarkGray, dos.Black), " target               proto app          status ")

	for i := 0; i < rows; i++ {
		idx := i + a.svcScroll
		if idx >= len(svcs) {
			break
		}
		sv := svcs[idx]
		sel := idx == a.svcSel
		bg := dos.Black
		if sel {
			bg = dos.Blue
		}
		fg := svcColor(sv)
		y := top + i
		s.Print(1, y, dos.Attr(fg, bg), dos.Pad((map[bool]string{true: "►", false: " "}[sel])+" "+sv.Label(), scrW-2))
	}
	a.svcHints()
}

func (a *App) drawServiceDetail(sv engine.Service) {
	s := a.scr
	s.Box(2, 2, scrW-4, statRow-3, dos.Attr(svcColor(sv), dos.Black), true)
	s.Title(2, 2, scrW-4, dos.Attr(dos.White, dos.Black), sv.Target())
	x, y := 5, 4
	row := func(label, val string, c int) {
		s.Print(x, y, dos.Attr(dos.Yellow, dos.Black), label)
		s.Print(x+len([]rune(label)), y, dos.Attr(c, dos.Black), val)
		y++
	}
	row("target      : ", sv.Target(), dos.White)
	row("transport   : ", sv.Proto, dos.White)
	row("application : ", sv.App, dos.LightCyan)
	if sv.Version != "" {
		row("version     : ", sv.Version, dos.White)
	}
	if sv.Banner != "" {
		row("banner      : ", trunc(sv.Banner, scrW-24), dos.LightGreen)
	}
	row("brutable    : ", yesno(sv.Brutable)+map[bool]string{true: "  (brutus: " + bruteProtoName(sv.App) + ")", false: ""}[sv.Brutable], dos.White)
	y++
	bs := sv.Brute
	bc := dos.LightGray
	switch bs {
	case engine.BruteRunning:
		bc = dos.Yellow
	case engine.BruteDone:
		bc = dos.LightGreen
	case engine.BruteFailed:
		bc = dos.LightRed
	}
	row("brute state : ", bs.String()+fmt.Sprintf("   (%d creds tried)", sv.Tried), bc)
	if sv.Compromised {
		s.Print(x, y, dos.Attr(dos.LightGreen|dos.Blink, dos.Black), "*** COMPROMISED ***")
		y += 2
		for _, c := range sv.Creds {
			s.Print(x+2, y, dos.Attr(dos.LightGreen, dos.Black), "✓ "+c.String())
			y++
		}
	} else if sv.Brutable && bs == engine.BruteIdle {
		s.Print(x, y, dos.Attr(dos.LightMagenta, dos.Black), "press B to run brutus against this service")
	} else if bs == engine.BruteRunning {
		s.Print(x, y, dos.Attr(dos.Yellow, dos.Black), "brutus is testing credentials...")
	} else if bs == engine.BruteDone {
		s.Print(x, y, dos.Attr(dos.LightGray, dos.Black), "brutus finished -- no valid credentials")
	}
}

func (a *App) svcHints() {
	a.scr.Print(0, statRow, dos.Attr(dos.Black, dos.LightGray),
		dos.Pad(" M/TAB:next view   j/k or arrows:select   ENTER:detail   B:brute   ESC:quit", scrW))
}

func svcColor(sv engine.Service) int {
	switch {
	case sv.Compromised:
		return dos.LightGreen
	case sv.Brute == engine.BruteRunning:
		return dos.Yellow
	case sv.Proto == "udp":
		return dos.LightCyan
	case sv.Brutable:
		return dos.White
	default:
		return dos.LightGray
	}
}

func center(s *dos.Screen, y int, attr uint16, str string) {
	s.Print((scrW-len([]rune(str)))/2, y, attr, str)
}
func bruteProtoName(app string) string { return app }
func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
