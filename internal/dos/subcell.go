package dos

// subcell.go gives the renderer sub-character resolution.
//
// The workhorse is Canvas: an RGB pixel buffer drawn with the upper-half-block
// glyph '▀'. Each character cell shows TWO vertically-stacked pixels in full
// 24-bit colour -- the top pixel as the glyph's foreground, the bottom as its
// background -- so a Canvas doubles vertical resolution with NO gaps between
// cells. This is how the network map paints an edge-to-edge colour heatmap.
//
// Braille (2x4 mono dots) is provided for thin line art (sparklines, waveforms).

const upperHalf = '▀' // U+2580: top half filled, bottom is the cell background

// Canvas is an RGB pixel grid. W is in pixels = character columns; H is in
// pixels = 2x character rows. Pixel (0,0) is top-left. A zero token renders as
// black.
type Canvas struct {
	W, H int
	px   []uint32
}

// NewCanvas allocates a w x h pixel canvas (h should usually be even).
func NewCanvas(w, h int) *Canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Canvas{W: w, H: h, px: make([]uint32, w*h)}
}

// Rows is the number of character rows the canvas occupies when blitted.
func (c *Canvas) Rows() int { return (c.H + 1) / 2 }

// Set paints pixel (x,y) to an RGB token (use dos.RGB / dos.Heat / palette).
func (c *Canvas) Set(x, y int, rgb uint32) {
	if x < 0 || y < 0 || x >= c.W || y >= c.H {
		return
	}
	c.px[y*c.W+x] = rgb
}

// At returns the token at (x,y) (0 if out of bounds).
func (c *Canvas) At(x, y int) uint32 {
	if x < 0 || y < 0 || x >= c.W || y >= c.H {
		return 0
	}
	return c.px[y*c.W+x]
}

// Fill paints the whole canvas one colour.
func (c *Canvas) Fill(rgb uint32) {
	for i := range c.px {
		c.px[i] = rgb
	}
}

// Blit renders the canvas into the screen at character cell (x0,y0). Two pixel
// rows collapse into one cell row via '▀'. Pixels are emitted with SetRGB so the
// colours are exact truecolor; the bottom pixel of an odd final row is black.
func (c *Canvas) Blit(s *Screen, x0, y0 int) {
	for cy := 0; cy*2 < c.H; cy++ {
		ty := cy * 2
		by := ty + 1
		for x := 0; x < c.W; x++ {
			top := c.px[ty*c.W+x]
			var bot uint32
			if by < c.H {
				bot = c.px[by*c.W+x]
			}
			s.SetRGB(x0+x, y0+cy, upperHalf, Attr(White, Black), forceRGB(top), forceRGB(bot))
		}
	}
}

// forceRGB makes sure a token carries the rgbSet flag (treat 0 as opaque black).
func forceRGB(token uint32) uint32 {
	if token == 0 {
		return RGB(0, 0, 0)
	}
	return token | rgbSet
}

// --- braille (2x4 mono dots) ------------------------------------------------

// Braille dot bit layout (Unicode U+2800 + bits):
//
//	(0,0)=0x01  (1,0)=0x08
//	(0,1)=0x02  (1,1)=0x10
//	(0,2)=0x04  (1,2)=0x20
//	(0,3)=0x40  (1,3)=0x80
var brailleBits = [4][2]uint8{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// BrailleRune returns the braille glyph for a 2x4 dot mask.
func BrailleRune(mask uint8) rune { return rune(0x2800 + int(mask)) }

// BrailleMask builds a dot mask from a 2-wide x 4-tall boolean grid (col,row).
func BrailleMask(dots [4][2]bool) uint8 {
	var m uint8
	for row := 0; row < 4; row++ {
		for col := 0; col < 2; col++ {
			if dots[row][col] {
				m |= brailleBits[row][col]
			}
		}
	}
	return m
}

// Sparkline draws a tiny line chart of vals into a single character row of width
// w using eighth-block glyphs, scaled to [min,max]. Good for dials/hour, rate.
func (s *Screen) Sparkline(x, y, w int, vals []float64, attr uint16) {
	if w <= 0 || len(vals) == 0 {
		return
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	if span <= 0 {
		span = 1
	}
	bars := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	n := len(vals)
	for i := 0; i < w; i++ {
		// sample the value series across the width
		vi := i * n / w
		if vi >= n {
			vi = n - 1
		}
		t := (vals[vi] - lo) / span
		bi := int(t*8 + 0.5)
		if bi < 0 {
			bi = 0
		}
		if bi > 8 {
			bi = 8
		}
		s.Set(x+i, y, bars[bi], attr)
	}
}
