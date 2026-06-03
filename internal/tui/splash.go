package tui

import (
	"strings"
	"time"

	"github.com/hdm/toneloc/internal/dos"
)

// A 5-row block font, just enough to spell the title. Old-school ANSI-art
// intros were a rite of passage; this is ToneLoc/Go's.
var blockFont = map[rune][]string{
	'T': {"█████", "  █  ", "  █  ", "  █  ", "  █  "},
	'O': {" ███ ", "█   █", "█   █", "█   █", " ███ "},
	'N': {"█   █", "██  █", "█ █ █", "█  ██", "█   █"},
	'E': {"█████", "█    ", "███  ", "█    ", "█████"},
	'L': {"█    ", "█    ", "█    ", "█    ", "█████"},
	'C': {" ████", "█    ", "█    ", "█    ", " ████"},
	'G': {" ████", "█    ", "█  ██", "█   █", " ███ "},
	' ': {"  ", "  ", "  ", "  ", "  "},
}

func bannerLines(word string) []string {
	rows := make([]string, 5)
	for _, ch := range word {
		g, ok := blockFont[ch]
		if !ok {
			g = blockFont[' ']
		}
		for r := 0; r < 5; r++ {
			rows[r] += g[r] + " "
		}
	}
	return rows
}

// inSplash reports whether the boot splash should still be shown.
func (a *App) inSplash() bool {
	return !a.splashDone && !a.splashUntil.IsZero() && time.Now().Before(a.splashUntil)
}

// drawSplash paints the intro: a colour-cycling block-letter logo, credits, and
// a blinking modem-init line, over a faint scanline field.
func (a *App) drawSplash() {
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	// Faint scanline field for that CRT warm-up look.
	for y := 0; y < a.scr.H; y++ {
		shade := dos.ShadeLight
		col := dos.DarkGray
		if y%2 == 0 {
			shade = ' '
		}
		for x := 0; x < a.scr.W; x++ {
			s.Set(x, y, shade, dos.Attr(col, dos.Black))
		}
	}

	// Logo, colour cycling per frame.
	logo := bannerLines("TONELOC")
	logoW := len([]rune(logo[0]))
	lx := (a.scr.W - logoW) / 2
	ly := 4
	palette := []int{dos.LightCyan, dos.Cyan, dos.LightBlue, dos.LightMagenta, dos.Magenta, dos.Yellow}
	for r, line := range logo {
		c := palette[(a.frame/3+r)%len(palette)]
		s.Print(lx, ly+r, dos.Attr(c, dos.Black), line)
	}

	// "/GO" tag to the right of the logo baseline.
	s.Print(lx+logoW-3, ly+5, dos.Attr(dos.LightGreen, dos.Black), "//Go")

	box := func(y int, attr uint16, text string) {
		s.Print((a.scr.W-len([]rune(text)))/2, y, attr, text)
	}
	box(11, dos.Attr(dos.White, dos.Black), "T o n e   L o c a t o r   v1.10   ::   IPv4 War Dialer")
	box(13, dos.Attr(dos.LightGray, dos.Black), "original by Minor Threat & Mucho Maas  ::  1994")
	box(14, dos.Attr(dos.LightGray, dos.Black), "Go port  ::  scanner powered by zmap-go")

	// Animated modem-init line.
	dots := strings.Repeat(".", (a.frame/4)%4)
	init := "INITIALIZING MODEM " + dots
	attr := dos.Attr(dos.LightGreen, dos.Black)
	if a.blink {
		attr = dos.Attr(dos.LightGreen|dos.Blink, dos.Black)
	}
	box(18, attr, init)
	box(22, dos.Attr(dos.DarkGray, dos.Black), "[ press any key to skip ]")
}
