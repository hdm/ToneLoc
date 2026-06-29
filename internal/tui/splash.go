package tui

import (
	"math"
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
	'D': {"████ ", "█   █", "█   █", "█   █", "████ "},
	'A': {" ███ ", "█   █", "█████", "█   █", "█   █"},
	'R': {"████ ", "█   █", "████ ", "█  █ ", "█   █"},
	'K': {"█   █", "█  █ ", "███  ", "█  █ ", "█   █"},
	'I': {"█████", "  █  ", "  █  ", "  █  ", "█████"},
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

// drawSplash paints the intro: a truecolor gradient block-letter logo with a
// sweeping highlight, a starfield, a carrier waveform, and a boot meter, over a
// faint scanline field. It's the flashy ANSI-art crack-intro DARKCIDR earns.
func (a *App) drawSplash() {
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))
	W, H := s.W, s.H

	// Faint scanline field for that CRT warm-up look, with a few twinkling stars.
	for y := 0; y < H; y++ {
		shade := dos.ShadeLight
		col := dos.DarkGray
		if y%2 == 0 {
			shade = ' '
		}
		for x := 0; x < W; x++ {
			s.Set(x, y, shade, dos.Attr(col, dos.Black))
		}
	}
	for i := 0; i < W*H/40; i++ {
		x := (i*73 + a.frame*2) % W
		y := (i * 31) % H
		if (i+a.frame/4)%7 == 0 {
			s.Set(x, y, '·', dos.Attr(dos.White, dos.Black))
		} else if i%3 == 0 {
			s.Set(x, y, '.', dos.Attr(dos.DarkGray, dos.Black))
		}
	}

	// Logo with a truecolor gradient + a bright sweep racing across the letters.
	logo := bannerLines("DARKCIDR")
	logoW := len([]rune(logo[0]))
	lx := (W - logoW) / 2
	ly := H/2 - 8
	if ly < 1 {
		ly = 1
	}
	stops := []uint32{dos.RGB(0x00, 0xff, 0xaa), dos.RGB(0x00, 0xc8, 0xff), dos.RGB(0x60, 0x80, 0xff), dos.RGB(0xc0, 0x40, 0xff)}
	sweep := (a.frame * 2) % (logoW + 24)
	for r, line := range logo {
		for x, ch := range []rune(line) {
			if ch == ' ' {
				continue
			}
			t := float64(x) / float64(logoW)
			col := dos.Ramp(stops, t)
			if d := x - sweep; d >= -3 && d <= 0 {
				col = dos.RGB(0xff, 0xff, 0xff) // the racing highlight
			}
			s.SetRGB(lx+x, ly+r, ch, dos.Attr(dos.White, dos.Black), col, 0)
		}
	}
	// Carrier waveform sweeping under the logo.
	wave := []rune{'⠁', '⠉', '⠋', '⠛', '⠟', '⠿', '⡿', '⣿'}
	for x := 0; x < logoW; x++ {
		v := (math.Sin(float64(x)*0.45-float64(a.frame)*0.4) + 1) / 2
		s.SetRGB(lx+x, ly+6, wave[int(v*float64(len(wave)-1))], dos.Attr(dos.White, dos.Black), dos.Ramp(stops, v), 0)
	}

	box := func(y int, attr uint16, text string) {
		s.Print((W-len([]rune(text)))/2, y, attr, text)
	}
	box(ly+8, dos.Attr(dos.White, dos.Black), "v1.10   ::   war-dialing the IPv4 phone book")
	box(ly+9, dos.Attr(dos.LightGray, dos.Black), "a port of ToneLoc by Minor Threat & Mucho Maas  ::  1994")
	box(ly+10, dos.Attr(dos.DarkGray, dos.Black), "scanner powered by zmap-go")

	// Boot meter, filling as the splash plays out.
	frac := 1.0
	if !a.splashUntil.IsZero() {
		left := time.Until(a.splashUntil).Seconds()
		frac = 1 - left/2.8
	}
	mw := 40
	mx := (W - mw) / 2
	s.HBarRGB(mx, ly+12, mw, frac, dos.RGB(0x00, 0xff, 0xaa), dos.RGB(0x10, 0x20, 0x30))
	dots := strings.Repeat(".", (a.frame/4)%4)
	init := "INITIALIZING MODEM " + dots
	attr := dos.Attr(dos.LightGreen, dos.Black)
	if a.blink {
		attr = dos.Attr(dos.LightGreen|dos.Blink, dos.Black)
	}
	box(ly+13, attr, init)
	box(ly+15, dos.Attr(dos.Yellow, dos.Black), "1-4 switch views · G drop into the game · M cycle · ESC quit")
	box(ly+16, dos.Attr(dos.DarkGray, dos.Black), "[ press any key to skip ]")
}
