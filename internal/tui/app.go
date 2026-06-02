// Package tui lays out and animates the classic ToneLoc screen -- the
// Activity Log on the left, the Modem and Statistics windows stacked on the
// right, and the progress meter along the bottom -- on top of the dos
// text-mode renderer. The exact same App drives both the local terminal and
// the ghostty.js browser terminal; only the io.Writer/input source differ.
package tui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

const (
	scrW, scrH = 80, 25

	// Activity Log (left half).
	actX, actY, actW, actH = 0, 0, 46, 22
	// Modem window (top right).
	modX, modY, modW, modH = 46, 0, 34, 8
	// Statistics window (bottom right).
	stX, stY, stW, stH = 46, 8, 34, 14
	// Bottom rows.
	meterRow = 22
	copyRow  = 23
	statRow  = 24

	// ToneMap grid: a dense block of cells (left) plus a legend (right),
	// echoing the original TONEMAP.EXE layout.
	tmGX, tmGY = 1, 2   // grid origin (col,row)
	tmGW, tmGH = 54, 19 // grid size in cells
	tmLegendX  = 57     // legend column
)

// App renders an engine.State to a dos.Screen and feeds keystrokes back to the
// engine.
type App struct {
	scr     *dos.Screen
	eng     *engine.Engine
	out     io.Writer
	paused  bool
	blink   bool
	frame   int
	scanCol int // copyright colour cycling, for that restless DOS feel

	mode    viewMode
	mouseOn bool

	// ToneMap cursor (in grid-cell coordinates) and downsample cache.
	curCol, curRow int
	mapCells       []uint8
	mapPerCell     int
	mapAt          time.Time

	// Input escape-sequence parser state (arrows + SGR mouse share ESC[).
	escState int // 0 normal, 1 saw ESC, 2 collecting CSI
	csiBuf   []byte
	escTime  time.Time

	// Boot splash (intro screen) timing.
	splashUntil time.Time
	splashDone  bool
}

type viewMode int

const (
	modeDialer viewMode = iota
	modeToneMap
)

// New builds an App that draws to out.
func New(eng *engine.Engine, out io.Writer) *App {
	s := dos.New(scrW, scrH)
	return &App{scr: s, eng: eng, out: out}
}

// Run renders at ~20fps and dispatches input keys until the engine is done or
// the context is cancelled. keys delivers raw bytes from the terminal/web.
func (a *App) Run(ctx context.Context, keys <-chan byte) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	// Enter the alternate screen + clear, so we own the whole 80x25 canvas.
	io.WriteString(a.out, "\x1b[?1049h\x1b[2J\x1b[H")
	defer func() {
		a.enableMouse(false)
		io.WriteString(a.out, "\x1b[0m\x1b[?25h\x1b[?1049l")
	}()

	// Show the intro splash for a couple of seconds (any key skips it).
	a.splashUntil = time.Now().Add(2800 * time.Millisecond)

	a.draw(a.eng.State().Snapshot())
	a.scr.Flush(a.out)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.eng.Done():
			// Final repaint then leave the screen up briefly.
			a.draw(a.eng.State().Snapshot())
			a.scr.Flush(a.out)
			return nil
		case b, ok := <-keys:
			if !ok {
				return nil
			}
			if a.feed(b) {
				a.eng.Quit()
			}
		case <-ticker.C:
			// A lone ESC (not the start of an arrow/mouse sequence) means quit.
			if a.escState == 1 && time.Since(a.escTime) > 80*time.Millisecond {
				a.escState = 0
				a.eng.Quit()
			}
			a.frame++
			a.draw(a.eng.State().Snapshot())
			a.scr.Flush(a.out)
		}
	}
}

// feed pushes one input byte through the escape-sequence parser and returns
// true if the program should quit.
func (a *App) feed(b byte) bool {
	// Any key dismisses the intro splash without otherwise acting.
	if a.inSplash() {
		a.splashDone = true
		a.escState = 0
		return false
	}
	switch a.escState {
	case 2: // collecting a CSI / SS3 sequence
		a.csiBuf = append(a.csiBuf, b)
		if b >= 0x40 && b <= 0x7e { // final byte
			a.dispatchCSI(a.csiBuf)
			a.escState = 0
		} else if len(a.csiBuf) > 32 { // runaway guard
			a.escState = 0
		}
		return false
	case 1: // saw ESC; is it a sequence or a lone ESC?
		if b == '[' || b == 'O' {
			a.escState = 2
			a.csiBuf = a.csiBuf[:0]
			return false
		}
		a.escState = 0
		return true // ESC followed by a normal key -> treat as quit
	default:
		if b == 0x1b {
			a.escState = 1
			a.escTime = time.Now()
			return false
		}
		return a.handleNormal(b)
	}
}

// dispatchCSI handles a completed CSI sequence: arrow keys and SGR mouse events.
func (a *App) dispatchCSI(buf []byte) {
	if len(buf) == 1 {
		switch buf[0] {
		case 'A':
			a.moveCursor(0, -1)
		case 'B':
			a.moveCursor(0, 1)
		case 'C':
			a.moveCursor(1, 0)
		case 'D':
			a.moveCursor(-1, 0)
		}
		return
	}
	if buf[0] == '<' { // SGR mouse: <btn;col;row(M|m)
		a.handleMouse(buf)
	}
}

func (a *App) handleMouse(buf []byte) {
	// SGR mouse report body is "btn;col;row" between '<' and the final M/m.
	body := string(buf[1 : len(buf)-1])
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return
	}
	col, e1 := strconv.Atoi(parts[1])
	row, e2 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil {
		return
	}
	// Terminal coords are 1-based; map onto the ToneMap grid.
	a.hoverAt(col-1, row-1)
}

// handleNormal maps a plain keystroke to an action. Returns true to quit.
func (a *App) handleNormal(b byte) bool {
	switch b {
	case 'q', 'Q': // explicit quit
		return true
	case 'm', 'M', '\t': // toggle Dialer <-> ToneMap
		if a.mode == modeDialer {
			a.setMode(modeToneMap)
		} else {
			a.setMode(modeDialer)
		}
		return false
	}

	if a.mode == modeToneMap {
		switch b {
		case 'h':
			a.moveCursor(-1, 0)
		case 'l':
			a.moveCursor(1, 0)
		case 'k':
			a.moveCursor(0, -1)
		case 'j':
			a.moveCursor(0, 1)
		}
		return false
	}

	// Dialer-mode controls.
	switch b {
	case ' ': // abort current dial
		a.eng.Abort()
	case 'p', 'P': // pause / resume toggle
		if a.paused {
			a.eng.Resume()
		} else {
			a.eng.Pause()
		}
		a.paused = !a.paused
	case 's', 'S':
		a.eng.Speaker()
	case 'r', 'R':
		a.eng.Redial()
	case 'x', 'X':
		a.eng.AddWait()
	case 'n', 'N':
		a.eng.Note("Noted")
	case 'c', 'C':
		a.eng.Note("Carrier")
	case 'f', 'F':
		a.eng.Note("Fax")
	case 'g', 'G':
		a.eng.Note("Girl")
	case 'v', 'V':
		a.eng.Note("VMB")
	case 'y', 'Y':
		a.eng.Note("Yelling asshole")
	}
	return false
}

func (a *App) setMode(m viewMode) {
	a.mode = m
	a.enableMouse(m == modeToneMap)
}

// enableMouse toggles xterm any-event mouse reporting in SGR mode, so hovering
// the ToneMap streams motion events we can read off the same input channel.
func (a *App) enableMouse(on bool) {
	if a.out == nil || on == a.mouseOn {
		return
	}
	a.mouseOn = on
	if on {
		io.WriteString(a.out, "\x1b[?1003h\x1b[?1006h")
	} else {
		io.WriteString(a.out, "\x1b[?1003l\x1b[?1006l")
	}
}

func (a *App) moveCursor(dx, dy int) {
	a.curCol = clamp(a.curCol+dx, 0, tmGW-1)
	a.curRow = clamp(a.curRow+dy, 0, tmGH-1)
}

// hoverAt maps absolute screen coords to a ToneMap cell (if inside the grid).
func (a *App) hoverAt(sx, sy int) {
	if a.mode != modeToneMap {
		return
	}
	cx := sx - tmGX
	cy := sy - tmGY
	if cx < 0 || cy < 0 || cx >= tmGW || cy >= tmGH {
		return
	}
	a.curCol = cx
	a.curRow = cy
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Frame draws the current engine state and returns it as plain text. Handy for
// tests and for previewing the layout without a terminal.
func (a *App) Frame() string {
	a.draw(a.eng.State().Snapshot())
	return a.scr.Plain()
}

// FrameSVG draws the current state and returns it as a standalone SVG image.
func (a *App) FrameSVG() string {
	a.draw(a.eng.State().Snapshot())
	return a.scr.SVG()
}

func (a *App) draw(v engine.StateView) {
	a.blink = (a.frame/10)%2 == 0
	if a.inSplash() {
		a.drawSplash()
		return
	}
	if a.mode == modeToneMap {
		a.drawToneMap(v)
		return
	}
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	a.drawActivity(v)
	a.drawModem(v)
	a.drawStats(v)
	a.drawMeter(v)
	a.drawChrome(v)
}

func (a *App) drawActivity(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightCyan, dos.Blue)
	s.Fill(actX, actY, actW, actH, ' ', dos.Attr(dos.LightGray, dos.Blue))
	s.Box(actX, actY, actW, actH, frame, true)
	s.Title(actX, actY, actW, dos.Attr(dos.Yellow, dos.Blue), "Activity Log")

	innerH := actH - 2
	lines := v.Activity
	if len(lines) > innerH {
		lines = lines[len(lines)-innerH:]
	}
	for i, ln := range lines {
		attr := dos.Attr(dos.LightGray, dos.Blue)
		switch {
		case contains(ln, "CARRIER"):
			attr = dos.Attr(dos.LightGreen, dos.Blue)
		case contains(ln, "TONE"):
			attr = dos.Attr(dos.Yellow, dos.Blue)
		case contains(ln, "Busy"):
			attr = dos.Attr(dos.LightRed, dos.Blue)
		case contains(ln, "Noted"):
			attr = dos.Attr(dos.LightMagenta, dos.Blue)
		}
		s.Print(actX+2, actY+1+i, attr, dos.Pad(ln, actW-3))
	}
}

func (a *App) drawModem(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightGreen, dos.Black)
	s.Fill(modX, modY, modW, modH, ' ', dos.Attr(dos.Green, dos.Black))
	s.Box(modX, modY, modW, modH, frame, true)
	s.Title(modX, modY, modW, dos.Attr(dos.White, dos.Black), "Modem")

	innerH := modH - 2
	lines := v.Modem
	if len(lines) > innerH {
		lines = lines[len(lines)-innerH:]
	}
	for i, ln := range lines {
		s.Print(modX+2, modY+1+i, dos.Attr(dos.LightGreen, dos.Black), dos.Pad(ln, modW-3))
	}
}

func (a *App) drawStats(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightCyan, dos.Black)
	itemAttr := dos.Attr(dos.Yellow, dos.Black)
	valAttr := dos.Attr(dos.White, dos.Black)

	s.Fill(stX, stY, stW, stH, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Box(stX, stY, stW, stH, frame, true)
	s.Title(stX, stY, stW, dos.Attr(dos.White, dos.Black), "Statistics")

	col := stX + 2
	row := stY + 1
	put := func(label, val string) {
		s.Print(col, row, itemAttr, label)
		s.Print(col+len([]rune(label)), row, valAttr, val)
		row++
	}

	put(" Started : ", v.StartTime.Format("15:04:05"))
	put(" Current : ", v.Now.Format("15:04:05"))
	put(" MaxDials: ", fmt.Sprintf("%d", v.Stats.Max))
	put(" Dialed  : ", fmt.Sprintf("%d", v.Stats.Dialed))
	put(" Dials/Hr: ", fmt.Sprintf("%d", v.DialsHour))
	put(" ETA     : ", eta(v))

	// Divider with "Found" label, like the original.
	s.HLine(stX, row, stW, frame)
	s.Print(stX+(stW-7)/2, row, dos.Attr(dos.LightMagenta, dos.Black), "►Found◄")
	row++

	twoCol := func(l1 string, v1 int, l2 string, v2 int) {
		s.Print(col, row, itemAttr, l1)
		s.Print(col+len([]rune(l1)), row, dos.Attr(dos.LightGreen, dos.Black), dos.Right(fmt.Sprintf("%d", v1), 4))
		s.Print(col+15, row, itemAttr, l2)
		s.Print(col+15+len([]rune(l2)), row, dos.Attr(dos.LightCyan, dos.Black), dos.Right(fmt.Sprintf("%d", v2), 4))
		row++
	}
	twoCol("CD's :", v.Stats.Carriers, "Tone:", v.Stats.Tones)
	twoCol("Busy :", v.Stats.Busy, "Voic:", v.Stats.Voice)
	twoCol("NoDT :", v.Stats.NoDialtone, "Ring:", v.Stats.Ringout)

	// Last hits list fills any remaining rows.
	for i := len(v.Found) - 1; i >= 0 && row < stY+stH-1; i-- {
		f := v.Found[i]
		c := dos.Attr(dos.LightGreen, dos.Black)
		if f.Resp == engine.RespTone {
			c = dos.Attr(dos.Yellow, dos.Black)
		}
		s.Print(col, row, c, dos.Pad(" "+f.Target, stW-3))
		row++
	}
}

func (a *App) drawMeter(v engine.StateView) {
	s := a.scr
	label := "Dialing: " + dos.Pad(v.Target, 21)
	s.Print(actX, meterRow, dos.Attr(dos.White, dos.Black), label)
	mx := actX + len([]rune(label)) + 1
	mw := scrW - mx - 1
	if mw < 8 {
		mw = 8
	}
	fg := dos.LightGreen
	if v.Stats.Max > 0 && v.Stats.Dialed >= int(v.Stats.Max) {
		fg = dos.Yellow
	}
	s.Meter(mx, meterRow, mw, v.Meter, fg, dos.DarkGray, dos.Black)
}

func (a *App) drawChrome(v engine.StateView) {
	s := a.scr
	// Copyright line, colour drifting like the original's random hue.
	cr := fmt.Sprintf("ToneLoc/Go 1.10 [%s] \"war-dialing the IPv4 phone book\"", backendName(v.Backend))
	colors := []int{dos.LightCyan, dos.LightMagenta, dos.Yellow, dos.LightGreen, dos.White}
	cc := colors[(a.frame/8)%len(colors)]
	s.Print((scrW-len([]rune(cr)))/2, copyRow, dos.Attr(cc, dos.Black), cr)

	// Bottom status line: keys on the left, transient message blinking right.
	keys := " ESC:quit  SPC:abort  P:pause  R:redial  S:speaker  X:+wait  N/C/F/G/V/Y:note "
	s.Print(0, statRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(keys, scrW))

	msg := v.Status
	if v.Paused {
		msg = "*** PAUSED - press P to continue ***"
	}
	if v.Done {
		msg = "ToneLoc finished - press ESC to exit"
	}
	if msg != "" {
		attr := dos.Attr(dos.LightRed|dos.Blink, dos.LightGray)
		if !a.blink && !v.Done {
			attr = dos.Attr(dos.Red, dos.LightGray)
		}
		s.Print(scrW-len([]rune(msg))-1, statRow, attr, msg)
	}
}

func backendName(b string) string {
	switch b {
	case "zmap":
		return "zmap"
	case "connect":
		return "connect"
	default:
		return "sim"
	}
}

func eta(v engine.StateView) string {
	if v.DialsHour <= 0 || v.Stats.Max == 0 {
		return "--:--"
	}
	remaining := int(v.Stats.Max) - v.Stats.Dialed
	if remaining <= 0 {
		return "00:00"
	}
	hours := float64(remaining) / float64(v.DialsHour)
	h := int(hours)
	m := int((hours - float64(h)) * 60)
	return fmt.Sprintf("%02d:%02d", h, m)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
