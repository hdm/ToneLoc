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
	s.Print(0, 0, dos.Attr(dos.Black, dos.Yellow), dos.Pad(title, scrW))

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
	listH := statRow - 1 - listTop // rows available for the list
	s.Box(0, listTop-1, scrW, listH+2, dos.Attr(dos.LightGreen, dos.Black), true)
	s.Title(0, listTop-1, scrW, dos.Attr(dos.White, dos.Black), "Carriers & Tones")

	if len(hits) == 0 {
		s.Print(3, listTop+1, dos.Attr(dos.DarkGray, dos.Black),
			"No carriers yet. Keep dialing -- the good stuff is out there.")
	}

	// Clamp scroll and render the visible window (newest first).
	maxScroll := len(hits) - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.hofScroll > maxScroll {
		a.hofScroll = maxScroll
	}
	for row := 0; row < listH; row++ {
		i := len(hits) - 1 - (row + a.hofScroll)
		if i < 0 {
			break
		}
		h := hits[i]
		y := listTop + row
		num := fmt.Sprintf("%3d.", len(hits)-(row+a.hofScroll))
		s.Print(2, y, dos.Attr(dos.DarkGray, dos.Black), num)

		tagColor := dos.LightGreen
		if h.Resp == engine.RespTone {
			tagColor = dos.Yellow
		}
		s.Print(7, y, dos.Attr(dos.White, dos.Black), dos.Pad(h.Target, 22))
		s.Print(30, y, dos.Attr(tagColor, dos.Black), dos.Pad(h.Resp.Tag(), 12))
		if h.Banner != "" {
			s.Print(43, y, dos.Attr(dos.LightCyan, dos.Black), dos.Pad(h.Banner, scrW-44))
		}
	}

	// Scroll indicator.
	if maxScroll > 0 {
		ind := fmt.Sprintf(" %d-%d of %d ", a.hofScroll+1,
			min2(a.hofScroll+listH, len(hits)), len(hits))
		s.Print(scrW-len([]rune(ind))-2, listTop-1, dos.Attr(dos.LightGreen, dos.Black), ind)
	}

	hints := " M/TAB:next view   F:hall of fame   j/k or arrows:scroll   ESC:quit "
	s.Print(0, statRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, scrW))
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
