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

// Pip is one cell of a PipBar: a glyph in a palette colour (with optional bg).
type Pip struct {
	Ch rune
	FG int
	BG int
}

// PipBar draws a row of pips starting at (x,y), one cell each, clipped to w
// cells. Cells past len(pips) are filled with fillCh in fillAttr (use ' ' or a
// shade for the "pending" track). This is the per-host scan bar: each pip is a
// port's verdict glyph+colour.
func (s *Screen) PipBar(x, y, w int, pips []Pip, fillCh rune, fillAttr uint16) {
	for i := 0; i < w; i++ {
		if i < len(pips) {
			p := pips[i]
			s.Set(x+i, y, p.Ch, Attr(p.FG, p.BG))
		} else {
			s.Set(x+i, y, fillCh, fillAttr)
		}
	}
}

// GradientFill paints a w x h rectangle with a multi-stop colour ramp, using
// truecolor cells. If vertical is false the ramp runs left-to-right.
func (s *Screen) GradientFill(x, y, w, h int, stops []uint32, ch rune, vertical bool) {
	if w <= 0 || h <= 0 {
		return
	}
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			var t float64
			if vertical {
				if h > 1 {
					t = float64(yy) / float64(h-1)
				}
			} else if w > 1 {
				t = float64(xx) / float64(w-1)
			}
			col := Ramp(stops, t)
			s.SetRGB(x+xx, y+yy, ch, Attr(White, Black), 0, col)
		}
	}
}

// HBarRGB draws a horizontal bar with truecolor: the filled portion uses fill,
// the track uses track, sub-cell precise via the eighth-block glyphs.
func (s *Screen) HBarRGB(x, y, w int, frac float64, fill, track uint32) {
	if w <= 0 {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	eighths := []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}
	exact := frac * float64(w)
	full := int(exact)
	rem := exact - float64(full)
	for i := 0; i < w; i++ {
		switch {
		case i < full:
			s.SetRGB(x+i, y, '█', Attr(White, Black), fill, track)
		case i == full && rem > 0.05:
			s.SetRGB(x+i, y, eighths[int(rem*8)], Attr(White, Black), fill, track)
		default:
			s.SetRGB(x+i, y, ' ', Attr(White, Black), fill, track)
		}
	}
}

// Region is a rectangle on the screen, with simple split/inset helpers so views
// can compose layouts without hand-rolling coordinate math.
type Region struct{ X, Y, W, H int }

// Inset shrinks the region by n cells on every side.
func (r Region) Inset(n int) Region {
	return Region{r.X + n, r.Y + n, r.W - 2*n, r.H - 2*n}
}

// SplitTop returns (top n rows, the rest).
func (r Region) SplitTop(n int) (top, rest Region) {
	if n > r.H {
		n = r.H
	}
	return Region{r.X, r.Y, r.W, n}, Region{r.X, r.Y + n, r.W, r.H - n}
}

// SplitBottom returns (the rest, bottom n rows).
func (r Region) SplitBottom(n int) (rest, bottom Region) {
	if n > r.H {
		n = r.H
	}
	return Region{r.X, r.Y, r.W, r.H - n}, Region{r.X, r.Y + r.H - n, r.W, n}
}

// SplitLeft returns (left n cols, the rest).
func (r Region) SplitLeft(n int) (left, rest Region) {
	if n > r.W {
		n = r.W
	}
	return Region{r.X, r.Y, n, r.H}, Region{r.X + n, r.Y, r.W - n, r.H}
}

// SplitRight returns (the rest, right n cols).
func (r Region) SplitRight(n int) (rest, right Region) {
	if n > r.W {
		n = r.W
	}
	return Region{r.X, r.Y, r.W - n, r.H}, Region{r.X + r.W - n, r.Y, n, r.H}
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
