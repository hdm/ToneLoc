// Package dos renders a text-mode, 80x25, 16-colour MS-DOS style screen to an
// ANSI/VT stream. It is a tiny self-contained "BIOS": you draw into a cell
// buffer (characters + CGA colour attributes, including blink), and Flush emits
// only the escape sequences needed to update what changed. CP437 box-drawing
// characters are mapped to their Unicode equivalents so the same bytes render
// identically in a local terminal and in the ghostty.js browser terminal.
package dos

import (
	"bytes"
	"io"
	"strconv"
	"unicode/utf8"
)

// CGA/EGA 16-colour palette (the classic DOS attribute colours).
const (
	Black = iota
	Blue
	Green
	Cyan
	Red
	Magenta
	Brown
	LightGray
	DarkGray
	LightBlue
	LightGreen
	LightCyan
	LightRed
	LightMagenta
	Yellow
	White
)

// Blink is OR'd into a foreground colour to make the cell blink, exactly like
// the high bit of the DOS attribute byte.
const Blink = 0x100

// Attr packs a foreground (with optional Blink) and background colour.
func Attr(fg, bg int) uint16 {
	return uint16((fg&0x1FF)<<7 | (bg & 0x0F))
}

func attrFG(a uint16) int { return int(a>>7) & 0x1FF }
func attrBG(a uint16) int { return int(a) & 0x0F }

type cell struct {
	ch   rune
	attr uint16
}

// Screen is a fixed-size cell grid.
type Screen struct {
	W, H int
	cur  []cell
	prev []cell
	// truecolor selects 24-bit SGR (crisper retro palette) vs 16-colour SGR.
	truecolor bool
}

// New returns a w x h screen (use 80x25 for the authentic look).
func New(w, h int) *Screen {
	s := &Screen{W: w, H: h, truecolor: true}
	s.cur = make([]cell, w*h)
	s.prev = make([]cell, w*h)
	s.Clear(Attr(LightGray, Black))
	for i := range s.prev {
		s.prev[i].ch = 0 // force first full paint
	}
	return s
}

// SetTrueColor toggles 24-bit colour output (default on).
func (s *Screen) SetTrueColor(v bool) { s.truecolor = v }

// Clear fills the whole screen with spaces in attr.
func (s *Screen) Clear(attr uint16) {
	for i := range s.cur {
		s.cur[i] = cell{ch: ' ', attr: attr}
	}
}

// Fill paints a rectangle with ch/attr.
func (s *Screen) Fill(x, y, w, h int, ch rune, attr uint16) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			s.Set(xx, yy, ch, attr)
		}
	}
}

// Set writes a single cell (bounds-checked).
func (s *Screen) Set(x, y int, ch rune, attr uint16) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	if ch == 0 {
		ch = ' '
	}
	s.cur[y*s.W+x] = cell{ch: ch, attr: attr}
}

// Print writes a string starting at x,y, clipped to the row.
func (s *Screen) Print(x, y int, attr uint16, str string) {
	for _, r := range str {
		if x >= s.W {
			break
		}
		s.Set(x, y, r, attr)
		x++
	}
}

// PrintRunes writes pre-decoded runes (handy for box pieces).
func (s *Screen) PrintRunes(x, y int, attr uint16, rs []rune) {
	for _, r := range rs {
		if x >= s.W {
			break
		}
		s.Set(x, y, r, attr)
		x++
	}
}

// Flush emits the minimal ANSI to turn prev into cur and swaps buffers. The
// first call paints everything; subsequent calls only touch changed cells.
func (s *Screen) Flush(w io.Writer) error {
	var b bytes.Buffer
	b.WriteString("\x1b[?25l") // hide cursor

	lastAttr := uint16(0xFFFF)
	cursorX, cursorY := -1, -1

	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			i := y*s.W + x
			if s.cur[i] == s.prev[i] {
				continue
			}
			if cursorY != y || cursorX != x {
				b.WriteString("\x1b[")
				b.WriteString(strconv.Itoa(y + 1))
				b.WriteByte(';')
				b.WriteString(strconv.Itoa(x + 1))
				b.WriteByte('H')
			}
			if s.cur[i].attr != lastAttr {
				s.writeSGR(&b, s.cur[i].attr)
				lastAttr = s.cur[i].attr
			}
			var tmp [4]byte
			n := utf8.EncodeRune(tmp[:], s.cur[i].ch)
			b.Write(tmp[:n])
			cursorX, cursorY = x+1, y
		}
	}
	b.WriteString("\x1b[0m")
	copy(s.prev, s.cur)
	_, err := w.Write(b.Bytes())
	return err
}

// Plain returns the current buffer as plain text (no colour), for tests and
// for verifying layout.
func (s *Screen) Plain() string {
	var b bytes.Buffer
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			b.WriteRune(s.cur[y*s.W+x].ch)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Repaint forces the next Flush to redraw every cell (after a resize/clear).
func (s *Screen) Repaint() {
	for i := range s.prev {
		s.prev[i].ch = 0
	}
}

func (s *Screen) writeSGR(b *bytes.Buffer, attr uint16) {
	fg := attrFG(attr)
	bg := attrBG(attr)
	blink := fg&Blink != 0
	fg &= 0x0F

	b.WriteString("\x1b[0")
	if blink {
		b.WriteString(";5")
	}
	if s.truecolor {
		r, g, bl := dosRGB[fg][0], dosRGB[fg][1], dosRGB[fg][2]
		b.WriteString(";38;2;")
		writeByte3(b, r, g, bl)
		r, g, bl = dosRGB[bg][0], dosRGB[bg][1], dosRGB[bg][2]
		b.WriteString(";48;2;")
		writeByte3(b, r, g, bl)
	} else {
		// 16-colour SGR: 30-37/90-97 fg, 40-47/100-107 bg.
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(ansiFG(fg)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(ansiBG(bg)))
	}
	b.WriteByte('m')
}

func writeByte3(b *bytes.Buffer, r, g, bl byte) {
	b.WriteString(strconv.Itoa(int(r)))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(int(g)))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(int(bl)))
}

func ansiFG(c int) int {
	if c < 8 {
		return 30 + c
	}
	return 90 + (c - 8)
}

func ansiBG(c int) int {
	if c < 8 {
		return 40 + c
	}
	return 100 + (c - 8)
}

// dosRGB is the canonical VGA text-mode palette for the 16 colours.
var dosRGB = [16][3]byte{
	{0x00, 0x00, 0x00}, // black
	{0x00, 0x00, 0xAA}, // blue
	{0x00, 0xAA, 0x00}, // green
	{0x00, 0xAA, 0xAA}, // cyan
	{0xAA, 0x00, 0x00}, // red
	{0xAA, 0x00, 0xAA}, // magenta
	{0xAA, 0x55, 0x00}, // brown
	{0xAA, 0xAA, 0xAA}, // light gray
	{0x55, 0x55, 0x55}, // dark gray
	{0x55, 0x55, 0xFF}, // light blue
	{0x55, 0xFF, 0x55}, // light green
	{0x55, 0xFF, 0xFF}, // light cyan
	{0xFF, 0x55, 0x55}, // light red
	{0xFF, 0x55, 0xFF}, // light magenta
	{0xFF, 0xFF, 0x55}, // yellow
	{0xFF, 0xFF, 0xFF}, // white
}
