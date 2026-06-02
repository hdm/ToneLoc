package tui

import (
	"fmt"
	"time"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// drawToneMap renders the ToneMap view: a dense grid where each cell is one (or,
// when downsampled, several) addresses coloured by the most interesting verdict
// found there, a colour legend, and a cursor read-out at the bottom -- a direct
// homage to the original TONEMAP.EXE. A large arrow cursor (driven by the mouse
// or the arrow/hjkl keys) points at the cell whose details show in the footer.
func (a *App) drawToneMap(v engine.StateView) {
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	// Title bar with live scan progress.
	title := fmt.Sprintf(" ToneMap  %s  (%d addresses)", v.MaskText, a.eng.Span())
	prog := fmt.Sprintf("Dialed %d/%d ", v.Stats.Dialed, v.Stats.Max)
	bar := dos.Pad(title, scrW-len([]rune(prog))) + prog
	s.Print(0, 0, dos.Attr(dos.Black, dos.LightGray), dos.Pad(bar, scrW))

	// Refresh the downsampled grid no more than ~8x/sec; the underlying space
	// can be millions of addresses.
	if time.Since(a.mapAt) > 120*time.Millisecond || a.mapCells == nil {
		a.mapCells, a.mapPerCell = a.eng.State().RenderTone(tmGW, tmGH)
		a.mapAt = time.Now()
	}
	span := int(a.eng.Span())

	// The grid.
	for r := 0; r < tmGH; r++ {
		for c := 0; c < tmGW; c++ {
			ci := r*tmGW + c
			start := ci * a.mapPerCell
			ch, color := ' ', dos.Black
			if start < span && ci < len(a.mapCells) {
				ch, color = toneGlyph(engine.Response(a.mapCells[ci]))
			}
			s.Set(tmGX+c, tmGY+r, ch, dos.Attr(color, dos.Black))
		}
	}

	a.drawLegend()
	a.drawMapCursor()
	a.drawMapFooter(v, span)
}

// toneGlyph maps a verdict to a coloured block, dense like the original map.
func toneGlyph(r engine.Response) (rune, int) {
	switch r {
	case engine.RespCarrier:
		return dos.BlockFull, dos.Yellow // the prize -- bright gold
	case engine.RespTone:
		return dos.BlockFull, dos.LightGreen
	case engine.RespVoice:
		return dos.ShadeDark, dos.LightMagenta
	case engine.RespBusy:
		return dos.ShadeDark, dos.LightRed
	case engine.RespNoDialtone:
		return dos.ShadeMedium, dos.LightBlue
	case engine.RespRingout:
		return dos.ShadeMedium, dos.LightCyan
	case engine.RespTimeout:
		return dos.ShadeLight, dos.Brown
	case engine.RespExcluded, engine.RespBlacklisted:
		return dos.ShadeLight, dos.Magenta
	default: // Undialed
		return '·', dos.DarkGray
	}
}

var legendItems = []struct {
	resp  engine.Response
	label string
}{
	{engine.RespCarrier, "Carrier"},
	{engine.RespTone, "Tone"},
	{engine.RespBusy, "Busy"},
	{engine.RespVoice, "Voice"},
	{engine.RespNoDialtone, "No Dialtone"},
	{engine.RespRingout, "Ringout"},
	{engine.RespTimeout, "Timeout"},
	{engine.RespUndialed, "Undialed"},
}

func (a *App) drawLegend() {
	s := a.scr
	s.Box(tmLegendX-1, tmGY-1, scrW-(tmLegendX-1), len(legendItems)+4, dos.Attr(dos.LightGray, dos.Black), true)
	s.Title(tmLegendX-1, tmGY-1, scrW-(tmLegendX-1), dos.Attr(dos.White, dos.Black), "Key")
	for i, it := range legendItems {
		ch, color := toneGlyph(it.resp)
		y := tmGY + i + 1
		s.Set(tmLegendX, y, ch, dos.Attr(color, dos.Black))
		s.Set(tmLegendX+1, y, ch, dos.Attr(color, dos.Black))
		s.Print(tmLegendX+3, y, dos.Attr(dos.LightGray, dos.Black), it.label)
	}
}

// drawMapCursor draws a large white arrow pointer at the hovered cell, with the
// tip on the cell whose data the footer is showing.
func (a *App) drawMapCursor() {
	s := a.scr
	cx := tmGX + a.curCol
	cy := tmGY + a.curRow
	white := dos.Attr(dos.White, dos.Black)
	if a.blink {
		white = dos.Attr(dos.White|dos.Blink, dos.Black)
	}
	// A blocky up-left pointer (2 wide x 3 tall); the tip is the hovered cell.
	s.Set(cx, cy, '█', white)
	s.Set(cx, cy+1, '█', white)
	s.Set(cx+1, cy+1, '▄', white)
	s.Set(cx, cy+2, '▀', white)
}

func (a *App) drawMapFooter(v engine.StateView, span int) {
	s := a.scr
	ci := a.curRow*tmGW + a.curCol
	idx := ci * a.mapPerCell

	resp := engine.RespUndialed
	if ci < len(a.mapCells) {
		resp = engine.Response(a.mapCells[ci])
	}
	ch, color := toneGlyph(resp)

	line := statRow - 1
	s.Print(0, line, dos.Attr(dos.LightGray, dos.Black), dos.Pad("", scrW))
	if idx < span {
		addr := a.eng.Mask().Addr(uint32(idx))
		s.Print(1, line, dos.Attr(dos.White, dos.Black), addr.String())
		s.Set(20, line, ch, dos.Attr(color, dos.Black))
		s.Print(22, line, dos.Attr(color, dos.Black), resp.Tag())
		if a.mapPerCell > 1 {
			s.Print(40, line, dos.Attr(dos.DarkGray, dos.Black),
				fmt.Sprintf("(%d addrs/cell)", a.mapPerCell))
		}
	} else {
		s.Print(1, line, dos.Attr(dos.DarkGray, dos.Black), "(beyond scan range)")
	}

	// Compact live tallies on the right, then the key hints below.
	tally := fmt.Sprintf("CD:%d Tn:%d Bsy:%d", v.Stats.Carriers, v.Stats.Tones, v.Stats.Busy)
	s.Print(scrW-len([]rune(tally))-1, line, dos.Attr(dos.LightCyan, dos.Black), tally)

	hints := " M/TAB:dialer   move: mouse / arrows / hjkl   ESC:quit "
	s.Print(0, statRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, scrW))
}
