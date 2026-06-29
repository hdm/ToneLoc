package dos

import (
	"os"
	"strings"
	"testing"
)

// TestSetRGBFlush checks that a truecolor cell emits a 24-bit SGR and that a
// repeated flush is a no-op (the diff sees no change).
func TestSetRGBFlush(t *testing.T) {
	s := New(4, 1)
	s.SetRGB(0, 0, 'X', Attr(White, Black), RGB(0x39, 0xFF, 0x14), RGB(0x05, 0x07, 0x0C))
	var b strings.Builder
	if err := s.Flush(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "38;2;57;255;20") {
		t.Errorf("expected truecolor fg 38;2;57;255;20 in output, got: %q", out)
	}
	if !strings.Contains(out, "48;2;5;7;12") {
		t.Errorf("expected truecolor bg 48;2;5;7;12 in output, got: %q", out)
	}
	// A second flush with no changes should emit nothing but the cursor-hide.
	var b2 strings.Builder
	s.Flush(&b2)
	if strings.Contains(b2.String(), "38;2;") {
		t.Errorf("unchanged flush re-emitted colour: %q", b2.String())
	}
}

// TestCanvasBlit verifies the half-block canvas packs two pixel rows per cell.
func TestCanvasBlit(t *testing.T) {
	c := NewCanvas(2, 4)
	if c.Rows() != 2 {
		t.Fatalf("Rows()=%d want 2", c.Rows())
	}
	c.Set(0, 0, NeonGreen)   // top pixel of cell (0,0)
	c.Set(0, 1, NeonMagenta) // bottom pixel of cell (0,0)
	s := New(2, 2)
	c.Blit(s, 0, 0)
	if got := s.cur[0].ch; got != upperHalf {
		t.Errorf("cell glyph = %q want ▀", got)
	}
	if s.cur[0].fgRGB&rgbSet == 0 || s.cur[0].bgRGB&rgbSet == 0 {
		t.Errorf("blit did not set truecolor on the cell")
	}
}

// TestGenerateDosSample renders a showcase of the new primitives to SVG when
// TONELOC_SVG_OUT is set (a generator, skipped in normal runs).
func TestGenerateDosSample(t *testing.T) {
	dir := os.Getenv("TONELOC_SVG_OUT")
	if dir == "" {
		t.Skip("set TONELOC_SVG_OUT=<dir> to generate the dos primitives sample")
	}
	s := New(100, 40)
	s.Clear(Attr(LightGray, Black))

	// Gradient header band.
	s.GradientFill(0, 0, 100, 1, []uint32{DeepBlue, NeonCyan, NeonMagenta}, ' ', false)
	s.Print(2, 0, Attr(White, Black), " DARKCIDR  dos primitives ")

	// A heatmap canvas: 96 px wide x 24 px tall -> 12 char rows, no gaps.
	cv := NewCanvas(96, 24)
	for y := 0; y < cv.H; y++ {
		for x := 0; x < cv.W; x++ {
			t := float64(x) / float64(cv.W-1)
			d := float64(y) / float64(cv.H-1)
			cv.Set(x, y, Heat(t*(1-0.5*d)))
		}
	}
	cv.Blit(s, 2, 2)
	s.Print(2, 15, Attr(DarkGray, Black), "half-block heat canvas (2 px/row, truecolor, gapless)")

	// Per-host pip bars.
	mkpips := func(seedPorts []int) []Pip {
		ps := []Pip{}
		for _, v := range seedPorts {
			switch v {
			case 2:
				ps = append(ps, Pip{Ch: '█', FG: LightGreen})
			case 3:
				ps = append(ps, Pip{Ch: '█', FG: LightCyan})
			case 1:
				ps = append(ps, Pip{Ch: '▓', FG: LightRed})
			default:
				ps = append(ps, Pip{Ch: '░', FG: DarkGray})
			}
		}
		return ps
	}
	rows := [][]int{
		{2, 2, 1, 0, 0, 0, 2, 3, 0, 0, 1, 0, 0, 0, 0, 0},
		{1, 1, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0},
		{2, 3, 3, 2, 1, 1, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	ips := []string{"10.37.0.4", "10.37.0.9", "10.37.0.18"}
	for i, r := range rows {
		y := 17 + i
		s.Print(2, y, Attr(White, Black), Pad(ips[i], 16))
		s.PipBar(19, y, 40, mkpips(r), '·', Attr(DarkGray, Black))
	}

	// Truecolor progress bars + sparkline.
	s.Print(2, 22, Attr(Yellow, Black), "dials/hr")
	s.Sparkline(11, 22, 30, []float64{2, 4, 3, 7, 9, 12, 11, 15, 18, 22, 20, 26, 31}, Attr(LightGreen, Black))
	s.HBarRGB(2, 24, 60, 0.62, NeonGreen, Ash)
	s.HBarRGB(2, 25, 60, 0.28, NeonAmber, Ash)

	if err := os.WriteFile(dir+"/dos-primitives.svg", []byte(s.SVG()), 0o644); err != nil {
		t.Fatal(err)
	}
}
