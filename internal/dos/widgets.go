package dos

import "strings"

// CP437 box-drawing characters as Unicode, the pieces ToneLoc's CXL windowing
// library drew with. Double-line frames are the signature DOS-utility look.
const (
	// Double-line frame
	dTL = '╔'
	dTR = '╗'
	dBL = '╚'
	dBR = '╝'
	dH  = '═'
	dV  = '║'
	// Single-line frame
	sTL = '┌'
	sTR = '┐'
	sBL = '└'
	sBR = '┘'
	sH  = '─'
	sV  = '│'
	// Single tee joints into a double border
	teeL = '╟'
	teeR = '╢'
	// Shaded blocks for the progress meter / backgrounds
	ShadeLight  = '░'
	ShadeMedium = '▒'
	ShadeDark   = '▓'
	BlockFull   = '█'
)

// Box draws a frame around the rectangle (x,y,w,h). If double is true it uses
// the double-line set, matching the original Activity/Modem/Statistics windows.
func (s *Screen) Box(x, y, w, h int, attr uint16, double bool) {
	tl, tr, bl, br, hh, vv := sTL, sTR, sBL, sBR, sH, sV
	if double {
		tl, tr, bl, br, hh, vv = dTL, dTR, dBL, dBR, dH, dV
	}
	s.Set(x, y, tl, attr)
	s.Set(x+w-1, y, tr, attr)
	s.Set(x, y+h-1, bl, attr)
	s.Set(x+w-1, y+h-1, br, attr)
	for i := 1; i < w-1; i++ {
		s.Set(x+i, y, hh, attr)
		s.Set(x+i, y+h-1, hh, attr)
	}
	for i := 1; i < h-1; i++ {
		s.Set(x, y+i, vv, attr)
		s.Set(x+w-1, y+i, vv, attr)
	}
}

// Title centers a title string on the top border of a box, in the ►◄ wrapped
// style of the original ("► Activity Log ◄").
func (s *Screen) Title(x, y, w int, attr uint16, title string) {
	t := "► " + title + " ◄"
	tx := x + (w-len([]rune(t)))/2
	s.Print(tx, y, attr, t)
}

// HLine draws a horizontal divider inside a double box, tying into the sides
// with tee joints (like the line above the "Found" sub-panel).
func (s *Screen) HLine(x, y, w int, attr uint16) {
	s.Set(x, y, teeL, attr)
	s.Set(x+w-1, y, teeR, attr)
	for i := 1; i < w-1; i++ {
		s.Set(x+i, y, dH, attr)
	}
}

// Meter draws ToneLoc's signature progress bar: a shaded track that fills with
// solid blocks as the current dial elapses.
func (s *Screen) Meter(x, y, w int, frac float64, fg, trackFG, bg int) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(w))
	for i := 0; i < w; i++ {
		if i < filled {
			s.Set(x+i, y, BlockFull, Attr(fg, bg))
		} else {
			s.Set(x+i, y, ShadeMedium, Attr(trackFG, bg))
		}
	}
}

// Pad right-pads (or truncates) a string to width n runes.
func Pad(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

// Right left-pads a string to width n runes (for right-aligned numbers).
func Right(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[len(r)-n:])
	}
	return strings.Repeat(" ", n-len(r)) + s
}
