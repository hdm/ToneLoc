package tui

import (
	"fmt"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// drawHallOfFame lists every carrier and tone the scan has turned up -- the
// trophy case. In the original you went hunting through the .DAT with ToneMap;
// here it's one keypress away (M to cycle, or F to jump straight here).
func (a *App) drawHallOfFame(v engine.StateView) {
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	hits := a.eng.State().HitsSnapshot()

	// Title bar.
	title := fmt.Sprintf(" Hall of Fame   %d carriers / tones bagged ", len(hits))
	s.Print(0, 0, dos.Attr(dos.Black, dos.Yellow), dos.Pad(title, a.scr.W))

	// A little ANSI-art trophy banner up top.
	art := []string{
		"   .-=========-.     ___ ___  ___ ___ ___ ___ ___  ___ ",
		"   \\.--------./     | _ ) __|/ __| __/ _ \\ _ \\ __|/ __|",
		"    )        (      | _ \\ _|| (__| _| (_) |   / _| \\__ \\",
		"   /'--------'\\     |___/___|\\___|___\\___/|_|_\\___||___/",
		"   `-========-'                                         ",
	}
	for i, ln := range art {
		c := dos.LightCyan
		if i%2 == 0 {
			c = dos.Yellow
		}
		s.Print(2, 1+i, dos.Attr(c, dos.Black), ln)
	}

	listTop := 7
	listH := a.lStatRow - 1 - listTop // rows available for the list
	s.Box(0, listTop-1, a.scr.W, listH+2, dos.Attr(dos.LightGreen, dos.Black), true)
	s.Title(0, listTop-1, a.scr.W, dos.Attr(dos.White, dos.Black), "Carriers & Tones")

	if len(hits) == 0 {
		s.Print(3, listTop+1, dos.Attr(dos.DarkGray, dos.Black),
			"No carriers yet. Keep dialing -- the good stuff is out there.")
	}

	// Selection (newest-first) drives the scroll window.
	if a.hofSel >= len(hits) {
		a.hofSel = len(hits) - 1
	}
	if a.hofSel < 0 {
		a.hofSel = 0
	}
	if a.hofSel < a.hofScroll {
		a.hofScroll = a.hofSel
	}
	if a.hofSel >= a.hofScroll+listH {
		a.hofScroll = a.hofSel - listH + 1
	}
	for row := 0; row < listH; row++ {
		idx := row + a.hofScroll // selection index (newest-first)
		i := len(hits) - 1 - idx // into hits slice
		if i < 0 {
			break
		}
		h := hits[i]
		y := listTop + row
		sel := idx == a.hofSel
		bg := dos.Black
		if sel {
			bg = dos.Blue
		}
		s.Print(1, y, dos.Attr(dos.LightGray, bg), dos.Pad("", a.scr.W-2))
		s.Print(1, y, dos.Attr(dos.White, bg), map[bool]string{true: "►", false: " "}[sel])
		s.Print(2, y, dos.Attr(dos.DarkGray, bg), fmt.Sprintf("%3d.", idx+1))
		tagColor := dos.LightGreen
		if h.Resp == engine.RespTone {
			tagColor = dos.Yellow
		}
		s.Print(7, y, dos.Attr(dos.White, bg), dos.Pad(h.Target, 22))
		s.Print(30, y, dos.Attr(tagColor, bg), dos.Pad(h.Resp.Tag(), 12))
		if h.Banner != "" {
			s.Print(43, y, dos.Attr(dos.LightCyan, bg), dos.Pad(h.Banner, a.scr.W-44))
		}
	}

	hints := " M/TAB:next view   j/k or arrows:select   ENTER:detail   B:brute   ESC:quit "
	s.Print(0, a.lStatRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, a.scr.W))
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
