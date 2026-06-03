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
	"sync"
	"time"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// minW/minH are the smallest grid we lay out for; smaller terminals still work
// but may clip. There is no upper clamp -- ToneLoc fills whatever space it gets.
const minW, minH = 80, 25

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

	// Layout, recomputed for the current terminal size (relayout). The screen
	// is no longer fixed at 80x25 -- it fills the real terminal / browser size.
	lActW, lActH                           int
	lModX, lModW, lModH                    int
	lStX, lStY, lStW, lStH                 int
	lMeterRow, lCopyRow, lStatRow          int
	lTmGX, lTmGY, lTmGW, lTmGH, lTmLegendX int

	mode    viewMode
	mouseOn bool

	// ToneMap cursor (in grid-cell coordinates) and downsample cache.
	curCol, curRow int
	mapCells       []uint8
	mapPerCell     int
	mapAt          time.Time

	// Hall of Fame selection + scroll.
	hofSel    int
	hofScroll int

	// Services view selection/scroll/detail.
	svcSel    int
	svcScroll int
	svcDetail bool

	// Input escape-sequence parser state (arrows + SGR mouse share ESC[).
	escState int // 0 normal, 1 saw ESC, 2 collecting CSI
	csiBuf   []byte
	escTime  time.Time

	// Boot splash (intro screen) timing.
	splashUntil time.Time
	splashDone  bool

	// confirmQuit is true while the "are you sure?" exit dialog is showing.
	confirmQuit bool

	// Sound-cue edge detection (the web view turns these into modem audio).
	prevCarriers, prevTones, prevBusy int
	prevTarget                        string

	// Pending resize (terminal SIGWINCH or browser resize), applied on the
	// render goroutine.
	resizeMu     sync.Mutex
	pendW, pendH int
}

// Resize requests a new screen size; applied on the next render tick, so it is
// safe to call from a signal handler or the websocket reader.
func (a *App) Resize(w, h int) {
	a.resizeMu.Lock()
	a.pendW, a.pendH = w, h
	a.resizeMu.Unlock()
}

func (a *App) takeResize() (w, h int, ok bool) {
	a.resizeMu.Lock()
	defer a.resizeMu.Unlock()
	if a.pendW == 0 {
		return 0, 0, false
	}
	w, h, a.pendW, a.pendH = a.pendW, a.pendH, 0, 0
	return w, h, true
}

type viewMode int

const (
	modeDialer viewMode = iota
	modeToneMap
	modeHallOfFame
	modeServices
	modeCount
)

// New builds an App that draws to out at the default 80x25; call SetSize to
// grow it to the real terminal.
func New(eng *engine.Engine, out io.Writer) *App {
	a := &App{eng: eng, out: out}
	a.SetSize(minW, minH)
	return a
}

// SetSize resizes the screen and recomputes the layout for w x h cells (e.g.
// from the terminal size or a browser resize). Sizes below the 80x25 floor are
// clamped up.
func (a *App) SetSize(w, h int) {
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if a.scr != nil && a.scr.W == w && a.scr.H == h {
		return
	}
	a.scr = dos.New(w, h)
	a.relayout(w, h)
}

// relayout positions the windows for the current size: the right-hand column
// (Modem over Statistics) keeps a readable fixed-ish width while the Activity
// Log soaks up the rest, and the meter/status rows pin to the bottom.
func (a *App) relayout(w, h int) {
	right := w * 42 / 100
	if right < 34 {
		right = 34
	}
	if right > 52 {
		right = 52
	}
	a.lActW = w - right
	a.lActH = h - 3
	a.lModX, a.lModW = a.lActW, right
	a.lModH = (h - 3) / 3
	if a.lModH < 7 {
		a.lModH = 7
	}
	if a.lModH > 14 {
		a.lModH = 14
	}
	a.lStX, a.lStY, a.lStW = a.lActW, a.lModH, right
	a.lStH = (h - 3) - a.lModH
	a.lMeterRow, a.lCopyRow, a.lStatRow = h-3, h-2, h-1
	a.lTmGX, a.lTmGY = 1, 2
	a.lTmLegendX = w - 23
	a.lTmGW = a.lTmLegendX - 2
	a.lTmGH = h - 5
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
		case b, ok := <-keys:
			// The UI stays up after the scan finishes so you can browse services
			// and run brutus; only an explicit quit (ESC/Q) exits.
			if !ok {
				return nil
			}
			if a.feed(b) {
				a.eng.Quit()
				return nil
			}
		case <-ticker.C:
			// A lone ESC (not the start of an arrow/mouse sequence): close an open
			// dialog, or quit if there's nothing to close.
			if a.escState == 1 && time.Since(a.escTime) > 80*time.Millisecond {
				a.escState = 0
				if a.onEscape() {
					a.eng.Quit()
					return nil
				}
			}
			if w, h, ok := a.takeResize(); ok {
				a.SetSize(w, h)
				io.WriteString(a.out, "\x1b[2J") // clear; new screen repaints fully
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
		return a.onEscape() // ESC + normal key: close a dialog, else quit
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
	// While the quit-confirmation dialog is up, only Y/ENTER (quit) or C/N
	// (cancel) do anything; everything else is swallowed.
	if a.confirmQuit {
		switch b {
		case 'y', 'Y', '\r', '\n':
			return true // confirmed -> quit
		case 'c', 'C', 'n', 'N':
			a.confirmQuit = false
		}
		return false
	}
	switch b {
	case 'q', 'Q': // explicit quit -> ask for confirmation too
		a.confirmQuit = true
		return false
	case 'm', 'M', '\t': // cycle Dialer -> ToneMap -> Hall of Fame -> Services
		a.setMode((a.mode + 1) % modeCount)
		return false
	}

	if a.mode == modeServices {
		switch b {
		case 'k':
			a.moveCursor(0, -1)
		case 'j':
			a.moveCursor(0, 1)
		case '\r', '\n': // enter -> toggle full detail
			a.svcDetail = !a.svcDetail
		case 'b', 'B': // launch brutus against the selected service
			a.bruteSelected()
		case 'x', 'X': // cancel an in-progress brute
			a.cancelSelected()
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
	if a.mode == modeHallOfFame {
		switch b {
		case 'k':
			a.moveCursor(0, -1)
		case 'j':
			a.moveCursor(0, 1)
		case '\r', '\n': // jump to the selected hit's full service detail
			a.openHitDetail()
		case 'b', 'B':
			a.bruteHit()
		case 'x', 'X':
			if sv, ok := a.hitService(); ok {
				a.eng.CancelBrute(sv.Key())
			}
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

// drawModal draws a centered dialog box with margins and a drop shadow over the
// current screen, returning the inner content rectangle.
func (a *App) drawModal(title string, frame int) (ix, iy, iw, ih int) {
	s := a.scr
	mx := a.scr.W / 8
	if mx < 4 {
		mx = 4
	}
	my := a.scr.H / 8
	if my < 2 {
		my = 2
	}
	bx, by := mx, my
	bw, bh := a.scr.W-2*mx, a.scr.H-2*my
	// Drop shadow (down-right) for depth.
	for y := by + 1; y <= by+bh && y < a.scr.H; y++ {
		s.Set(bx+bw, y, ' ', dos.Attr(dos.Black, dos.DarkGray))
	}
	for x := bx + 1; x <= bx+bw && x < a.scr.W; x++ {
		s.Set(x, by+bh, ' ', dos.Attr(dos.Black, dos.DarkGray))
	}
	s.Fill(bx, by, bw, bh, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Box(bx, by, bw, bh, dos.Attr(frame, dos.Black), true)
	s.Print(bx+(bw-len([]rune(title))-2)/2, by, dos.Attr(dos.White, dos.Black), " "+title+" ")
	return bx + 3, by + 2, bw - 6, bh - 4
}

// onEscape handles ESC. It never quits directly: it closes an open detail, or
// toggles the quit-confirmation dialog (so a second ESC cancels it). Actually
// quitting requires confirming with Y/ENTER. Always returns false.
func (a *App) onEscape() bool {
	switch {
	case a.mode == modeServices && a.svcDetail:
		a.svcDetail = false
	case a.confirmQuit:
		a.confirmQuit = false // a second ESC cancels the quit prompt
	default:
		a.confirmQuit = true // first ESC asks "are you sure?"
	}
	return false
}

// cancelSelected cancels an in-progress brute on the highlighted service.
func (a *App) cancelSelected() {
	svcs := a.eng.State().ServicesSnapshot()
	if a.svcSel >= 0 && a.svcSel < len(svcs) {
		a.eng.CancelBrute(svcs[a.svcSel].Key())
	}
}

// bruteSelected launches brutus against the highlighted service.
func (a *App) bruteSelected() {
	svcs := a.eng.State().ServicesSnapshot()
	if len(svcs) == 0 {
		return
	}
	i := a.svcSel
	if i < 0 {
		i = 0
	}
	if i >= len(svcs) {
		i = len(svcs) - 1
	}
	if svcs[i].Brutable {
		a.eng.StartBrute(svcs[i].Key())
	}
}

// hitService maps the selected Hall-of-Fame hit to its discovered Service, if
// one exists.
func (a *App) hitService() (engine.Service, bool) {
	hits := a.eng.State().HitsSnapshot()
	if a.hofSel < 0 || a.hofSel >= len(hits) {
		return engine.Service{}, false
	}
	tgt := hits[len(hits)-1-a.hofSel].Target // list is newest-first
	for _, sv := range a.eng.State().ServicesSnapshot() {
		if sv.Target() == tgt {
			return sv, true
		}
	}
	return engine.Service{}, false
}

// openHitDetail jumps from the Hall of Fame to the selected hit's full service
// detail in the Services view.
func (a *App) openHitDetail() {
	sv, ok := a.hitService()
	if !ok {
		return
	}
	svcs := a.eng.State().ServicesSnapshot()
	for i := range svcs {
		if svcs[i].Key() == sv.Key() {
			a.svcSel = i
			a.svcDetail = true
			a.setMode(modeServices)
			return
		}
	}
}

func (a *App) bruteHit() {
	if sv, ok := a.hitService(); ok && sv.Brutable {
		a.eng.StartBrute(sv.Key())
	}
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
	if a.mode == modeServices {
		a.svcSel += dy
		if a.svcSel < 0 {
			a.svcSel = 0
		}
		return
	}
	if a.mode == modeHallOfFame {
		a.hofSel += dy // selection; scroll is derived at draw time
		if a.hofSel < 0 {
			a.hofSel = 0
		}
		return
	}
	a.curCol = clamp(a.curCol+dx, 0, a.lTmGW-1)
	a.curRow = clamp(a.curRow+dy, 0, a.lTmGH-1)
}

// hoverAt maps absolute screen coords to a ToneMap cell (if inside the grid).
func (a *App) hoverAt(sx, sy int) {
	if a.mode != modeToneMap {
		return
	}
	cx := sx - a.lTmGX
	cy := sy - a.lTmGY
	if cx < 0 || cy < 0 || cx >= a.lTmGW || cy >= a.lTmGH {
		return
	}
	a.curCol = cx
	a.curRow = cy
}

// emitCues detects newly-completed dials and emits a private OSC sequence per
// event. The ghostty.js page intercepts these and synthesizes modem audio (dial
// tones, the handshake screech, busy tones); a real terminal harmlessly ignores
// them. Cues are gated on the Speaker toggle, so pressing S silences them.
func (a *App) emitCues(v engine.StateView) {
	if a.out != nil && v.Speaker && !a.inSplash() {
		if v.Stats.Carriers > a.prevCarriers {
			a.cue("connect")
		}
		if v.Stats.Tones > a.prevTones {
			a.cue("tone")
		}
		if v.Stats.Busy > a.prevBusy {
			a.cue("busy")
		}
		if v.Target != a.prevTarget && v.Target != "" {
			a.cue("dial")
		}
	}
	a.prevCarriers, a.prevTones, a.prevBusy = v.Stats.Carriers, v.Stats.Tones, v.Stats.Busy
	a.prevTarget = v.Target
}

func (a *App) cue(event string) {
	io.WriteString(a.out, "\x1b]1337;"+event+"\x07")
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
	a.emitCues(v)
	switch {
	case a.inSplash():
		a.drawSplash()
	case a.mode == modeToneMap:
		a.drawToneMap(v)
	case a.mode == modeHallOfFame:
		a.drawHallOfFame(v)
	case a.mode == modeServices:
		a.drawServices(v)
	default:
		s := a.scr
		s.Clear(dos.Attr(dos.LightGray, dos.Black))
		a.drawActivity(v)
		a.drawModem(v)
		a.drawStats(v)
		a.drawMeter(v)
		a.drawChrome(v)
	}
	if a.confirmQuit && !a.inSplash() {
		a.drawQuitConfirm()
	}
}

// drawQuitConfirm overlays a small centered "are you sure?" dialog.
func (a *App) drawQuitConfirm() {
	s := a.scr
	w, h := 44, 6
	x, y := (a.scr.W-w)/2, (a.scr.H-h)/2
	// shadow + box
	for yy := y + 1; yy <= y+h && yy < a.scr.H; yy++ {
		s.Set(x+w, yy, ' ', dos.Attr(dos.Black, dos.DarkGray))
	}
	for xx := x + 1; xx <= x+w && xx < a.scr.W; xx++ {
		s.Set(xx, y+h, ' ', dos.Attr(dos.Black, dos.DarkGray))
	}
	s.Fill(x, y, w, h, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Box(x, y, w, h, dos.Attr(dos.LightRed, dos.Black), true)
	title := " Quit ToneLoc? "
	s.Print(x+(w-len([]rune(title)))/2, y, dos.Attr(dos.White, dos.Black), title)
	msg := "Really exit and end this scan?"
	s.Print(x+(w-len([]rune(msg)))/2, y+2, dos.Attr(dos.LightGray, dos.Black), msg)
	opt := "[ Y / ENTER ] quit     [ ESC / C ] cancel"
	c := dos.LightCyan
	if a.blink {
		c = dos.White
	}
	s.Print(x+(w-len([]rune(opt)))/2, y+4, dos.Attr(c, dos.Black), opt)
}

func (a *App) drawActivity(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightCyan, dos.Blue)
	s.Fill(0, 0, a.lActW, a.lActH, ' ', dos.Attr(dos.LightGray, dos.Blue))
	s.Box(0, 0, a.lActW, a.lActH, frame, true)
	s.Title(0, 0, a.lActW, dos.Attr(dos.Yellow, dos.Blue), "Activity Log")

	innerH := a.lActH - 2
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
		s.Print(0+2, 0+1+i, attr, dos.Pad(ln, a.lActW-3))
	}
}

func (a *App) drawModem(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightGreen, dos.Black)
	s.Fill(a.lModX, 0, a.lModW, a.lModH, ' ', dos.Attr(dos.Green, dos.Black))
	s.Box(a.lModX, 0, a.lModW, a.lModH, frame, true)
	s.Title(a.lModX, 0, a.lModW, dos.Attr(dos.White, dos.Black), "Modem")

	innerH := a.lModH - 2
	lines := v.Modem
	if len(lines) > innerH {
		lines = lines[len(lines)-innerH:]
	}
	for i, ln := range lines {
		s.Print(a.lModX+2, 0+1+i, dos.Attr(dos.LightGreen, dos.Black), dos.Pad(ln, a.lModW-3))
	}
}

func (a *App) drawStats(v engine.StateView) {
	s := a.scr
	frame := dos.Attr(dos.LightCyan, dos.Black)
	itemAttr := dos.Attr(dos.Yellow, dos.Black)
	valAttr := dos.Attr(dos.White, dos.Black)

	s.Fill(a.lStX, a.lStY, a.lStW, a.lStH, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Box(a.lStX, a.lStY, a.lStW, a.lStH, frame, true)
	s.Title(a.lStX, a.lStY, a.lStW, dos.Attr(dos.White, dos.Black), "Statistics")

	col := a.lStX + 2
	row := a.lStY + 1
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
	s.HLine(a.lStX, row, a.lStW, frame)
	s.Print(a.lStX+(a.lStW-7)/2, row, dos.Attr(dos.LightMagenta, dos.Black), "►Found◄")
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
	for i := len(v.Found) - 1; i >= 0 && row < a.lStY+a.lStH-1; i-- {
		f := v.Found[i]
		c := dos.Attr(dos.LightGreen, dos.Black)
		if f.Resp == engine.RespTone {
			c = dos.Attr(dos.Yellow, dos.Black)
		}
		s.Print(col, row, c, dos.Pad(" "+f.Target, a.lStW-3))
		row++
	}
}

func (a *App) drawMeter(v engine.StateView) {
	s := a.scr
	label := "Dialing: " + dos.Pad(v.Target, 21)
	s.Print(0, a.lMeterRow, dos.Attr(dos.White, dos.Black), label)
	mx := 0 + len([]rune(label)) + 1
	mw := a.scr.W - mx - 1
	if mw < 8 {
		mw = 8
	}
	fg := dos.LightGreen
	if v.Stats.Max > 0 && v.Stats.Dialed >= int(v.Stats.Max) {
		fg = dos.Yellow
	}
	s.Meter(mx, a.lMeterRow, mw, v.Meter, fg, dos.DarkGray, dos.Black)
}

func (a *App) drawChrome(v engine.StateView) {
	s := a.scr
	// Copyright line, colour drifting like the original's random hue.
	cr := fmt.Sprintf("ToneLoc/Go 1.10 [%s] \"war-dialing the IPv4 phone book\"", backendName(v.Backend))
	colors := []int{dos.LightCyan, dos.LightMagenta, dos.Yellow, dos.LightGreen, dos.White}
	cc := colors[(a.frame/8)%len(colors)]
	s.Print((a.scr.W-len([]rune(cr)))/2, a.lCopyRow, dos.Attr(cc, dos.Black), cr)

	// Bottom status line: keys on the left, transient message blinking right.
	keys := " ESC:quit  SPC:abort  P:pause  R:redial  S:speaker  X:+wait  N/C/F/G/V/Y:note "
	s.Print(0, a.lStatRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(keys, a.scr.W))

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
		s.Print(a.scr.W-len([]rune(msg))-1, a.lStatRow, attr, msg)
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
