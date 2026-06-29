// Package dos renders a text-mode, 80x25, 16-colour MS-DOS style screen to an
// ANSI/VT stream. It is a tiny self-contained "BIOS": you draw into a cell
// buffer (characters + CGA colour attributes, including blink), and Flush emits
// only the escape sequences needed to update what changed. CP437 box-drawing
// characters are mapped to their Unicode equivalents so the same bytes render
// identically in a local terminal and in the ghostty.js browser terminal.
package dos

import (
	"bytes"
	"fmt"
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
	ch    rune
	attr  uint16
	fgRGB uint32 // 0 = use palette attr; else rgbSet|0xRRGGBB (truecolor override)
	bgRGB uint32 // 0 = use palette attr; else rgbSet|0xRRGGBB (truecolor override)
}

// rgbSet flags a colour token as a 24-bit truecolor override rather than a
// palette index. The zero value (0) means "use the palette attribute", so every
// existing Set/Fill/Print call (which leaves these fields zero) is unaffected.
const rgbSet = 0x0100_0000

// RGB packs a 24-bit colour into the token SetRGB expects.
func RGB(r, g, b byte) uint32 { return rgbSet | uint32(r)<<16 | uint32(g)<<8 | uint32(b) }

// RGBval extracts the (r,g,b) bytes from a colour token (set or not).
func RGBval(token uint32) (r, g, b byte) {
	return byte(token >> 16), byte(token >> 8), byte(token)
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

// SetRGB writes a cell with explicit 24-bit foreground/background colours
// (truecolor), bypassing the 16-colour palette. Pass 0 for fgRGB or bgRGB to
// fall back to the palette colour carried in attr. Honoured in truecolor mode
// (the default) and in SVG export; in 16-colour mode the palette attr is used.
func (s *Screen) SetRGB(x, y int, ch rune, attr uint16, fgRGB, bgRGB uint32) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	if ch == 0 {
		ch = ' '
	}
	s.cur[y*s.W+x] = cell{ch: ch, attr: attr, fgRGB: fgRGB, bgRGB: bgRGB}
}

// OverlayFG tints a cell's background toward col, keeping its glyph and fore-
// ground, so views can draw a crosshair/scan-line without clobbering data
// underneath. Cells that already carry a truecolor background are left alone,
// so open hits stay vivid.
func (s *Screen) OverlayFG(x, y int, col uint32) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	if c := &s.cur[y*s.W+x]; c.bgRGB == 0 {
		c.bgRGB = col
	}
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

	last := style{attr: 0xFFFF} // impossible attr forces the first SGR
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
			cs := style{attr: s.cur[i].attr, fg: s.cur[i].fgRGB, bg: s.cur[i].bgRGB}
			if cs != last {
				s.writeStyle(&b, cs)
				last = cs
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

// SVG renders the current screen as a standalone SVG image, faithful to the
// VGA text-mode palette and cell grid. Handy for documentation and for showing
// off the look without a terminal. Blinking cells are drawn solid.
func (s *Screen) SVG() string {
	const cw, ch = 10, 20 // cell box in px
	const fs = 16         // font size
	w, h := s.W*cw, s.H*ch

	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="DejaVu Sans Mono, Consolas, Menlo, monospace" font-size="%d">`, w, h, w, h, fs)
	// Black backdrop.
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#000000"/>`, w, h)

	// Background rectangles: coalesce horizontal runs of the same non-black bg.
	for y := 0; y < s.H; y++ {
		x := 0
		for x < s.W {
			bg := bgHex(s.cur[y*s.W+x])
			if bg == "#000000" {
				x++
				continue
			}
			run := 1
			for x+run < s.W && bgHex(s.cur[y*s.W+x+run]) == bg {
				run++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`,
				x*cw, y*ch, run*cw, ch, bg)
			x += run
		}
	}

	// Glyphs: one <text> per non-space cell, centred in its box.
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			c := s.cur[y*s.W+x]
			if c.ch == ' ' || c.ch == 0 {
				continue
			}
			fmt.Fprintf(&b, `<text x="%d" y="%d" fill="%s" text-anchor="middle">%s</text>`,
				x*cw+cw/2, y*ch+fs-2, fgHex(c), escapeXML(c.ch))
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func hexColor(c int) string {
	rgb := dosRGB[c&0x0F]
	return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
}

// bgHex/fgHex resolve a cell's colour to a hex string for SVG, honouring any
// 24-bit override and otherwise using the VGA palette.
func bgHex(c cell) string {
	if c.bgRGB&rgbSet != 0 {
		return fmt.Sprintf("#%06x", c.bgRGB&0xFFFFFF)
	}
	return hexColor(attrBG(c.attr))
}

func fgHex(c cell) string {
	if c.fgRGB&rgbSet != 0 {
		return fmt.Sprintf("#%06x", c.fgRGB&0xFFFFFF)
	}
	return hexColor(attrFG(c.attr) & 0x0F)
}

func escapeXML(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	default:
		return string(r)
	}
}

// Repaint forces the next Flush to redraw every cell (after a resize/clear).
func (s *Screen) Repaint() {
	for i := range s.prev {
		s.prev[i].ch = 0
	}
}

// style is the emitted appearance of a cell: its packed palette attr plus any
// 24-bit RGB overrides. The flusher re-emits an SGR only when this changes.
type style struct {
	attr   uint16
	fg, bg uint32
}

func (s *Screen) writeStyle(b *bytes.Buffer, st style) {
	fg := attrFG(st.attr)
	bg := attrBG(st.attr)
	blink := fg&Blink != 0
	fg &= 0x0F

	b.WriteString("\x1b[0")
	if blink {
		b.WriteString(";5")
	}
	if s.truecolor {
		fr, fgc, fb := resolveRGB(st.fg, fg)
		b.WriteString(";38;2;")
		writeByte3(b, fr, fgc, fb)
		br, bgc, bb := resolveRGB(st.bg, bg)
		b.WriteString(";48;2;")
		writeByte3(b, br, bgc, bb)
	} else {
		// 16-colour SGR: 30-37/90-97 fg, 40-47/100-107 bg. RGB overrides ignored.
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(ansiFG(fg)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(ansiBG(bg)))
	}
	b.WriteByte('m')
}

// resolveRGB returns the 24-bit colour to emit: an explicit override token if
// set, otherwise the VGA palette entry for the index.
func resolveRGB(token uint32, idx int) (r, g, b byte) {
	if token&rgbSet != 0 {
		return byte(token >> 16), byte(token >> 8), byte(token)
	}
	p := dosRGB[idx&0x0F]
	return p[0], p[1], p[2]
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
