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
	s.Clear(dos.Attr(dos.LightGray, dos.Black))
	svcs := a.eng.State().ServicesSnapshot()

	comp := 0
	for _, sv := range svcs {
		if sv.Compromised {
			comp++
		}
	}
	a.drawModeBar(modeServices, fmt.Sprintf("★ HALL OF FAME · %d found · %d pwned · %s ", len(svcs), comp, v.SessionID))

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

	// Full-screen detail: a clean centered modal on a cleared background (no
	// list behind it, so nothing bleeds through).
	if a.svcDetail {
		a.drawServiceDetail(svcs[a.svcSel])
		return
	}

	// Scrolling list. The box spans top..bottom; row 2 (inside the frame) holds
	// the column header so it never punches the top border, and rows start at 3.
	top, rows := 3, a.lStatRow-4
	if a.svcSel < a.svcScroll {
		a.svcScroll = a.svcSel
	}
	if a.svcSel >= a.svcScroll+rows {
		a.svcScroll = a.svcSel - rows + 1
	}
	s.Box(0, 1, a.scr.W, rows+3, dos.Attr(dos.LightCyan, dos.Black), true)
	s.Title(0, 1, a.scr.W, dos.Attr(dos.White, dos.Black), "Discovered Services · Hall of Fame")
	s.Fill(1, 2, a.scr.W-2, 1, ' ', dos.Attr(dos.DarkGray, dos.Black))
	s.Print(2, 2, dos.Attr(dos.Yellow, dos.Black), "  target               proto app          status")

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
		s.Print(1, y, dos.Attr(fg, bg), dos.Pad((map[bool]string{true: "►", false: " "}[sel])+" "+sv.Label(), a.scr.W-2))
	}
	a.svcHints()
}

func (a *App) drawServiceDetail(sv engine.Service) {
	s := a.scr
	ix, iy, iw, ih := a.drawModal(sv.Proto+" "+sv.Target(), svcColor(sv))
	x, y := ix, iy
	maxY := iy + ih - 2
	row := func(label, val string, c int) {
		if y > maxY {
			return
		}
		s.Print(x, y, dos.Attr(dos.Yellow, dos.Black), label)
		s.Print(x+len([]rune(label)), y, dos.Attr(c, dos.Black), trunc(val, iw-len([]rune(label))))
		y++
	}
	section := func(name string) {
		if y > maxY {
			return
		}
		s.Print(x, y, dos.Attr(dos.LightCyan, dos.Black), "── "+name+" "+repeat("─", iw-len([]rune(name))-4))
		y++
	}

	section("connect")
	if sv.ConnectBanner != "" {
		row("  banner  : ", sv.ConnectBanner, dos.LightGreen)
	} else {
		row("  result  : ", "open (no banner grabbed)", dos.DarkGray)
	}

	section("nerva")
	row("  app     : ", sv.App, dos.LightCyan)
	if sv.Version != "" {
		row("  version : ", sv.Version, dos.White)
	}
	if sv.Banner != "" {
		row("  banner  : ", sv.Banner, dos.LightGreen)
	}

	section("brutus")
	bs := sv.Brute
	bc := map[engine.BruteState]int{engine.BruteRunning: dos.Yellow, engine.BruteDone: dos.LightGreen, engine.BruteFailed: dos.LightRed}[bs]
	if bc == 0 {
		bc = dos.LightGray
	}
	row("  state   : ", bs.String(), bc)
	if bs == engine.BruteRunning {
		// live progress meter.
		s.Print(x, y, dos.Attr(dos.Yellow, dos.Black), fmt.Sprintf("  testing : %d creds", sv.Tried))
		y++
		s.Meter(x+2, y, iw-6, float64(sv.Tried%108)/108.0, dos.Yellow, dos.DarkGray, dos.Black)
		y++
	}
	switch {
	case sv.Compromised:
		blink := dos.LightGreen
		if a.blink {
			blink = dos.LightGreen | dos.Blink
		}
		s.Print(x+2, y, dos.Attr(blink, dos.Black), "*** COMPROMISED ***")
		y++
		for _, c := range sv.Creds {
			row("    creds : ", c.String(), dos.LightGreen)
		}
	case !sv.Brutable:
		row("  note    : ", "no brutus plugin for "+sv.App, dos.DarkGray)
	case !a.eng.BrutusEnabled():
		row("  note    : ", "brutus disabled (restart with --brutus)", dos.DarkGray)
	case bs == engine.BruteDone:
		row("  note    : ", "no valid credentials", dos.LightGray)
	}

	// Modal footer hints, inside the box.
	hint := " ENTER/ESC close   B brute   X cancel "
	if bs == engine.BruteRunning {
		hint = " X cancel brute   ENTER/ESC close "
	}
	s.Print(x+(iw-len([]rune(hint)))/2, iy+ih-1, dos.Attr(dos.Black, dos.LightGray), hint)
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = []rune(s)[0]
	}
	return string(out)
}

func (a *App) svcHints() {
	a.scr.Print(0, a.lStatRow, dos.Attr(dos.Black, dos.LightGray),
		dos.Pad(" M/TAB:next view   j/k or arrows:select   ENTER:detail   B:brute   X:cancel   ESC:close", a.scr.W))
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
	s.Print((s.W-len([]rune(str)))/2, y, attr, str)
}
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
