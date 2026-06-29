package tui

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// toneMap is the full-screen, zoomable network map. It paints a CONTIGUOUS,
// gap-free truecolor heatmap of the current mask's address space using the
// half-block Canvas (two addresses per character cell), and lets the user zoom
// in/out (+/-) and pan (arrows/hjkl/mouse) through the space. The cursor cell's
// address(es), verdict, and live services show in a side panel. There are no
// inter-cell gaps and the grid fills the whole viewport, at every zoom level.
type toneMap struct {
	start, count int    // visible index window [start,start+count) in the mask
	curX, curY   int    // cursor in char-cell coords within the grid
	gridX, gridY int    // grid origin on screen
	gridW, gridH int    // grid size in character cells
	perCell      int    // addresses per pixel from the last render
	panel        bool   // show the right side panel (legend + cursor detail)
	spanCache    uint32 // detect mask switch -> reset the window
}

func newToneMap() *toneMap { return &toneMap{panel: true} }

// ensure resets the window when the active mask (and thus span) changes, and
// clamps the window/cursor into range.
func (m *toneMap) ensure(span uint32) {
	if span == 0 {
		return
	}
	if span != m.spanCache {
		m.spanCache = span
		m.start = 0
		m.count = int(span)
		m.curX, m.curY = 0, 0
	}
	if m.count <= 0 || m.count > int(span) {
		m.count = int(span)
	}
	if m.start < 0 {
		m.start = 0
	}
	if m.start+m.count > int(span) {
		m.start = int(span) - m.count
	}
}

// total is the number of pixels in the canvas (2 per character row).
func (m *toneMap) total() int { return m.gridW * m.gridH * 2 }

// cursorIdx returns the address index of the top pixel under the cursor.
func (m *toneMap) cursorIdx() int {
	t := m.total()
	if t < 1 {
		return m.start
	}
	topc := (m.curY * 2) * m.gridW
	if m.gridW > 0 {
		topc += m.curX
	}
	return m.start + topc*m.count/t
}

// cursorBlock returns [loIdx, hiIdx) covered by the cursor character cell.
func (m *toneMap) cursorBlock() (int, int) {
	t := m.total()
	if t < 1 {
		return m.start, m.start + 1
	}
	topc := (m.curY*2)*m.gridW + m.curX
	botc := (m.curY*2+1)*m.gridW + m.curX
	lo := m.start + topc*m.count/t
	hi := m.start + (botc+1)*m.count/t
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

func (m *toneMap) draw(a *App, v engine.StateView) {
	s, eng, blink := a.scr, a.eng, a.blink
	span := eng.Span()
	m.ensure(span)
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	W, H := s.W, s.H
	if span == 0 || W < 8 || H < 6 {
		s.Print(2, 2, dos.Attr(dos.DarkGray, dos.Black), "mapping the address space… no results yet")
		return
	}

	panelW := 0
	if m.panel && W >= 64 {
		panelW = 28
	}
	m.gridX, m.gridY = 0, 1
	m.gridW = W - panelW
	m.gridH = H - 1 - m.gridY // leave the bottom row for hints
	if m.gridW < 1 || m.gridH < 1 {
		return
	}
	m.curX = clamp(m.curX, 0, m.gridW-1)
	m.curY = clamp(m.curY, 0, m.gridH-1)

	// Render the window into a half-block canvas: gridW px wide, gridH*2 tall.
	pxW, pxH := m.gridW, m.gridH*2
	cells, open, perCell := eng.State().RenderToneWindow(m.start, m.count, pxW, pxH)
	m.perCell = perCell
	cv := dos.NewCanvas(pxW, pxH)
	for i := range cells {
		x := i % pxW
		y := i / pxW
		cv.Set(x, y, cellColor(engine.Response(cells[i]), int(open[i]), perCell))
	}
	cv.Blit(s, m.gridX, m.gridY)

	// Navigation bar with map context on the right (after render so addr/cell
	// reflects the current zoom).
	a.drawModeBar(modeToneMap, m.titleRight(eng, v))

	// Crosshair: faint highlight along the cursor's row + column so the eye
	// snaps to it instantly across a dense space. Open cells underneath stay
	// visible; we only nudge the empty track.
	cx, cy := m.gridX+m.curX, m.gridY+m.curY
	for x := m.gridX; x < m.gridX+m.gridW; x++ {
		if x != cx {
			s.OverlayFG(x, cy, dos.RGB(0x60, 0x90, 0xa8))
		}
	}
	for y := m.gridY; y < m.gridY+m.gridH; y++ {
		if y != cy {
			s.OverlayFG(cx, y, dos.RGB(0x60, 0x90, 0xa8))
		}
	}
	// Pulsing reticle: cycle ◆/◇/✛ so the cursor reads as "live targeting".
	ret := []rune{'✛', '◆', '✛', '◇'}
	rc := dos.RGB(0xff, 0xe0, 0x40)
	if blink {
		rc = dos.RGB(0xff, 0xff, 0xff)
	}
	s.SetRGB(cx, cy, ret[(a.frame/3)%len(ret)], dos.Attr(dos.White, dos.Black), rc, dos.Ink)

	if panelW > 0 {
		m.drawPanel(s, eng, W-panelW, panelW)
	}

	hints := " +/-:zoom  hjkl:pan  ENTER:drill  a:fit  p:panel  M/TAB:view  ESC:close "
	s.Print(0, H-1, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, W))
}

// titleRight is the concise map context shown on the right of the nav bar:
// the visible address window and how many addresses each cell covers.
func (m *toneMap) titleRight(eng *engine.Engine, v engine.StateView) string {
	first := eng.Mask().Addr(uint32(m.start))
	last := eng.Mask().Addr(uint32(m.start + m.count - 1))
	addrPerCell := m.perCell * 2
	if addrPerCell < 1 {
		addrPerCell = 1
	}
	return fmt.Sprintf("%s–%s · %d/cell · %d/%d ", first, last, addrPerCell, v.Stats.Dialed, v.Stats.Max)
}

func (m *toneMap) drawPanel(s *dos.Screen, eng *engine.Engine, x, w int) {
	H := s.H
	s.Fill(x, 1, w, H-2, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Box(x, 1, w, H-2, dos.Attr(dos.LightCyan, dos.Black), true)
	s.Title(x, 1, w, dos.Attr(dos.White, dos.Black), "Inspect")

	lo, hi := m.cursorBlock()
	ip := eng.Mask().Addr(uint32(lo))
	block := hi - lo
	row := 3
	put := func(label, val string, c int) {
		s.Print(x+2, row, dos.Attr(dos.Yellow, dos.Black), label)
		s.Print(x+2+len([]rune(label)), row, dos.Attr(c, dos.Black), dos.Pad(val, w-3-len([]rune(label))))
		row++
	}

	if block <= 1 {
		put("host ", ip.String(), dos.White)
		resp := m.verdictAt(eng, lo)
		ch, col := toneGlyph(resp)
		s.Set(x+2, row, ch, dos.Attr(col, dos.Black))
		s.Print(x+4, row, dos.Attr(col, dos.Black), verdictLabel(resp))
		row += 2
		svcs := eng.State().ServicesForIP(ip.String())
		if len(svcs) == 0 {
			if resp.Found() {
				s.Print(x+2, row, dos.Attr(dos.DarkGray, dos.Black), "fingerprinting…")
			} else {
				s.Print(x+2, row, dos.Attr(dos.DarkGray, dos.Black), "no services")
			}
			row++
		}
		for _, sv := range svcs {
			if row >= H-7 {
				break
			}
			app := sv.App
			if app == "" {
				app = "?"
			}
			c := dos.LightCyan
			if sv.Compromised {
				c = dos.LightGreen
			}
			s.Print(x+2, row, dos.Attr(c, dos.Black), dos.Pad(fmt.Sprintf("%d/%s %s", sv.Port, strings.ToLower(sv.Proto), app), w-3))
			row++
			if b := firstBanner(sv); b != "" && row < H-7 {
				s.Print(x+3, row, dos.Attr(dos.DarkGray, dos.Black), dos.Pad("“"+b+"”", w-4))
				row++
			}
		}
	} else {
		last := eng.Mask().Addr(uint32(hi - 1))
		put("block ", ip.String(), dos.White)
		put("  ..   ", last.String(), dos.LightGray)
		put("size  ", fmt.Sprintf("%d addrs", block), dos.LightGray)
		// aggregate open over the block
		o, p := m.blockStats(eng, lo, hi)
		put("open  ", fmt.Sprintf("%d", o), dos.LightGreen)
		put("probed ", fmt.Sprintf("%d", p), dos.LightCyan)
	}

	// Compact legend pinned to the bottom of the panel.
	ly := H - 6
	s.HLine(x, ly-1, w, dos.Attr(dos.LightCyan, dos.Black))
	leg := []struct {
		r engine.Response
		s string
	}{{engine.RespCarrier, "open"}, {engine.RespTone, "banner"}, {engine.RespBusy, "reset"}, {engine.RespVoice, "filter"}, {engine.RespTimeout, "timeout"}, {engine.RespUndialed, "pending"}}
	for i, it := range leg {
		ch, col := toneGlyph(it.r)
		yy := ly + i/2
		xx := x + 2 + (i%2)*13
		s.Set(xx, yy, ch, dos.Attr(col, dos.Black))
		s.Print(xx+2, yy, dos.Attr(dos.LightGray, dos.Black), it.s)
	}
	// Heat-ramp swatch so dim-vs-bright reads as density, not noise.
	s.Print(x+2, H-3, dos.Attr(dos.DarkGray, dos.Black), "heat")
	for i := 0; i < 8 && x+7+i < x+w-1; i++ {
		s.SetRGB(x+7+i, H-3, '█', dos.Attr(dos.White, dos.Black), dos.Black, dos.Heat(float64(i)/7))
	}
	s.Print(x+7+9, H-3, dos.Attr(dos.DarkGray, dos.Black), "dense")
}

// verdictAt returns the best verdict recorded for the address at index idx.
func (m *toneMap) verdictAt(eng *engine.Engine, idx int) engine.Response {
	cells, _, _ := eng.State().RenderToneWindow(idx, 1, 1, 1)
	if len(cells) == 0 {
		return engine.RespUndialed
	}
	return engine.Response(cells[0])
}

// blockStats sums open hits and probed addresses across [lo,hi).
func (m *toneMap) blockStats(eng *engine.Engine, lo, hi int) (open, probed int) {
	n := hi - lo
	if n < 1 {
		return 0, 0
	}
	// One cell per address keeps it exact for reasonable block sizes.
	cells, opens, _ := eng.State().RenderToneWindow(lo, n, n, 1)
	for i := range cells {
		if engine.Response(cells[i]) != engine.RespUndialed {
			probed++
		}
		open += int(opens[i])
	}
	return open, probed
}

// --- input -----------------------------------------------------------------

// arrow moves the cursor; at a grid edge it pans the window instead.
func (m *toneMap) arrow(dx, dy int) {
	nx, ny := m.curX+dx, m.curY+dy
	if nx < 0 {
		m.panBy(-1, 0)
		nx = 0
	} else if nx >= m.gridW {
		m.panBy(1, 0)
		nx = m.gridW - 1
	}
	if ny < 0 {
		m.panBy(0, -1)
		ny = 0
	} else if ny >= m.gridH {
		m.panBy(0, 1)
		ny = m.gridH - 1
	}
	m.curX = clamp(nx, 0, m.gridW-1)
	m.curY = clamp(ny, 0, m.gridH-1)
}

// panBy shifts the visible window by one cell column / character row, in the
// row-major address layout (right = +perCell, down = +2*pxW*perCell).
func (m *toneMap) panBy(cx, cy int) {
	span := int(m.spanCache)
	if span == 0 {
		return
	}
	pc := m.perCell
	if pc < 1 {
		pc = 1
	}
	m.start += cx * pc
	m.start += cy * 2 * m.gridW * pc
	m.start = clamp(m.start, 0, span-m.count)
}

// key handles map-specific keys (zoom, fit, panel, drill-in). Returns true if
// it consumed the key.
func (m *toneMap) key(b byte) bool {
	span := int(m.spanCache)
	switch b {
	case '+', '=':
		m.zoomAt(m.cursorIdx(), m.count/4)
	case '-', '_':
		m.zoomAt(m.cursorIdx(), m.count*4)
	case '\r', '\n':
		lo, hi := m.cursorBlock()
		m.zoomAt(lo, hi-lo)
		_ = lo
	case 'a', 'A':
		m.start, m.count = 0, span
		m.curX, m.curY = 0, 0
	case 'p', 'P':
		m.panel = !m.panel
	default:
		return false
	}
	return true
}

// zoomAt sets the window to ~newCount addresses centred on idx, clamped and
// aligned so the cursor's neighbourhood stays put.
func (m *toneMap) zoomAt(idx, newCount int) {
	span := int(m.spanCache)
	if span == 0 {
		return
	}
	minCount := 16
	if newCount < minCount {
		newCount = minCount
	}
	if newCount > span {
		newCount = span
	}
	m.count = newCount
	m.start = clamp(idx-newCount/2, 0, span-newCount)
	m.curX, m.curY = clamp(m.curX, 0, max0(m.gridW-1)), clamp(m.curY, 0, max0(m.gridH-1))
}

// mouse maps a screen cell to the grid cursor; wheel (+1/-1) zooms.
func (m *toneMap) mouse(col, row, wheel int) {
	if wheel > 0 {
		m.zoomAt(m.cursorIdx(), m.count/4)
		return
	}
	if wheel < 0 {
		m.zoomAt(m.cursorIdx(), m.count*4)
		return
	}
	cx := col - m.gridX
	cy := row - m.gridY
	if cx < 0 || cy < 0 || cx >= m.gridW || cy >= m.gridH {
		return
	}
	m.curX, m.curY = cx, cy
}

// --- colour / glyph mapping -------------------------------------------------

// cellColor maps a downsampled map cell to a truecolor token: single addresses
// use their verdict's neon colour; aggregated cells with hits glow on the heat
// ramp by open density; otherwise the dominant verdict's (muted) colour.
func cellColor(r engine.Response, open, perCell int) uint32 {
	if r == engine.RespUndialed {
		return dos.Ash
	}
	if r.Found() {
		if perCell <= 1 {
			if r == engine.RespTone {
				return dos.NeonCyan
			}
			return dos.NeonGreen
		}
		t := float64(open) / float64(perCell)
		if t > 1 {
			t = 1
		}
		if t < 0.18 {
			t = 0.18 // a lone hit still glows
		}
		return dos.Heat(t)
	}
	return verdictRGB(r)
}

func verdictRGB(r engine.Response) uint32 {
	switch r {
	case engine.RespCarrier:
		return dos.NeonGreen
	case engine.RespTone:
		return dos.NeonCyan
	case engine.RespBusy:
		return dos.RGB(0xcc, 0x33, 0x33) // reset / refused
	case engine.RespVoice:
		return dos.RGB(0xb0, 0x3a, 0xb0) // filtered
	case engine.RespNoDialtone:
		return dos.RGB(0x2e, 0x40, 0x90) // unreachable
	case engine.RespRingout:
		return dos.RGB(0x16, 0x6e, 0x68) // no-reply
	case engine.RespTimeout:
		return dos.RGB(0x46, 0x33, 0x16) // timeout
	case engine.RespExcluded, engine.RespBlacklisted:
		return dos.RGB(0x44, 0x24, 0x44)
	default:
		return dos.Ash
	}
}

// toneGlyph maps a verdict to a coloured block for the legend / readout.
func toneGlyph(r engine.Response) (rune, int) {
	switch r {
	case engine.RespCarrier:
		return dos.BlockFull, dos.LightGreen
	case engine.RespTone:
		return dos.BlockFull, dos.LightCyan
	case engine.RespBusy:
		return dos.ShadeDark, dos.LightRed
	case engine.RespVoice:
		return dos.ShadeDark, dos.LightMagenta
	case engine.RespNoDialtone:
		return dos.ShadeMedium, dos.LightBlue
	case engine.RespRingout:
		return dos.ShadeMedium, dos.Cyan
	case engine.RespTimeout:
		return dos.ShadeLight, dos.Brown
	case engine.RespExcluded, engine.RespBlacklisted:
		return dos.ShadeLight, dos.Magenta
	default:
		return '·', dos.DarkGray
	}
}

// verdictLabel is the short network-term name for the readout.
func verdictLabel(r engine.Response) string {
	switch r {
	case engine.RespCarrier:
		return "OPEN"
	case engine.RespTone:
		return "BANNER"
	case engine.RespBusy:
		return "RESET"
	case engine.RespVoice:
		return "FILTERED"
	case engine.RespNoDialtone:
		return "UNREACH"
	case engine.RespRingout:
		return "NO-REPLY"
	case engine.RespTimeout:
		return "TIMEOUT"
	default:
		return "unscanned"
	}
}

func firstBanner(sv engine.Service) string {
	if sv.Banner != "" {
		return sv.Banner
	}
	return sv.ConnectBanner
}

// --- /24 helpers (still used by the hosts view + grouping) ------------------

func sub24(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() {
		return ""
	}
	b := a.As4()
	return fmt.Sprintf("%d.%d.%d", b[0], b[1], b[2])
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
