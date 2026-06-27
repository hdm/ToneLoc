package tui

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// drawToneMap renders the network map. Two views, toggled with [a]:
//   - per-subnet: one /24 as a real 16x16 grid (last octet = row*16+col), each
//     cell coloured by that address's best probe verdict; a small cursor roams
//     and the footer shows the selected IP's ports/protocols/banners.
//   - all-networks: a list of every /24 seen, each with a 16-cell sparkline.
func (a *App) drawToneMap(v engine.StateView) {
	s := a.scr
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	// Refresh the per-IP verdict cache a few times a second.
	if time.Since(a.mapAt) > 250*time.Millisecond || a.mapVerdicts == nil {
		a.mapVerdicts = a.eng.State().HostVerdicts()
		a.mapSubnets = a.subnetsSeen(v.Target)
		a.mapAt = time.Now()
	}
	if a.mapSubnet >= len(a.mapSubnets) {
		a.mapSubnet = 0
	}

	if a.mapAllNets {
		a.drawAllNets(v)
	} else {
		a.drawSubnetMap(v)
	}
}

// subnetsSeen returns the sorted distinct /24 prefixes among scanned IPs, plus
// the /24 of the address currently in flight so it shows up immediately.
func (a *App) subnetsSeen(curTarget string) []string {
	set := map[string]bool{}
	for ip := range a.mapVerdicts {
		if p := sub24(ip); p != "" {
			set[p] = true
		}
	}
	if ip, _, ok := strings.Cut(curTarget, ":"); ok {
		if p := sub24(ip); p != "" {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return less24(out[i], out[j]) })
	return out
}

func (a *App) drawSubnetMap(v engine.StateView) {
	s := a.scr
	sub := ""
	if a.mapSubnet < len(a.mapSubnets) {
		sub = a.mapSubnets[a.mapSubnet]
	}
	title := fmt.Sprintf(" MAP  %s.0/24", sub)
	if len(a.mapSubnets) > 1 {
		title += fmt.Sprintf("   subnet %d/%d  ([n]ext [p]rev)", a.mapSubnet+1, len(a.mapSubnets))
	}
	prog := fmt.Sprintf("[a] all nets   Dialed %d/%d ", v.Stats.Dialed, v.Stats.Max)
	bar := dos.Pad(title, a.scr.W-len([]rune(prog))) + prog
	s.Print(0, 0, dos.Attr(dos.Black, dos.LightGray), dos.Pad(bar, a.scr.W))

	// Column/row hex guides + the 16x16 grid.
	guide := dos.Attr(dos.DarkGray, dos.Black)
	for c := 0; c < 16; c++ {
		s.Print(mapGX+c*mapCellW, mapGY-1, guide, fmt.Sprintf("%X", c))
	}
	for r := 0; r < 16; r++ {
		for c := 0; c < 16; c++ {
			octet := r*16 + c
			ip := sub + "." + strconv.Itoa(octet)
			resp := engine.RespUndialed
			if vb, ok := a.mapVerdicts[ip]; ok {
				resp = engine.Response(vb)
			}
			ch, color := toneGlyph(resp)
			x, y := mapGX+c*mapCellW, mapGY+r
			if r == a.curRow && c == a.curCol {
				// small, distinct cursor: the cell inverted and framed in brackets.
				br := dos.Attr(dos.White, dos.Black)
				if a.blink {
					br = dos.Attr(dos.Yellow, dos.Black)
				}
				s.Set(x-1, y, '[', br)
				s.Set(x, y, ch, dos.Attr(dos.Black, dos.White))
				s.Set(x+1, y, ']', br)
			} else {
				s.Set(x, y, ch, dos.Attr(color, dos.Black))
			}
		}
		s.Print(mapGX-2, mapGY+r, guide, fmt.Sprintf("%X", r))
	}

	a.drawLegend()
	a.drawSubnetFooter(v, sub)
}

func (a *App) drawSubnetFooter(v engine.StateView, sub string) {
	s := a.scr
	line := a.lStatRow - 1
	s.Print(0, line, dos.Attr(dos.LightGray, dos.Black), dos.Pad("", a.scr.W))

	octet := a.curRow*16 + a.curCol
	ip := sub + "." + strconv.Itoa(octet)
	resp := engine.RespUndialed
	if vb, ok := a.mapVerdicts[ip]; ok {
		resp = engine.Response(vb)
	}
	ch, color := toneGlyph(resp)
	s.Print(1, line, dos.Attr(dos.White, dos.Black), dos.Pad(ip, 16))
	s.Set(17, line, ch, dos.Attr(color, dos.Black))
	s.Print(19, line, dos.Attr(color, dos.Black), dos.Pad(verdictLabel(resp), 12))

	// Services on this IP: app/port proto, with the first banner.
	x := 33
	svcs := a.eng.State().ServicesForIP(ip)
	if len(svcs) == 0 && resp.Found() {
		s.Print(x, line, dos.Attr(dos.DarkGray, dos.Black), "(fingerprinting…)")
	}
	for i, sv := range svcs {
		if x > a.scr.W-14 {
			break
		}
		app := sv.App
		if app == "" {
			app = "?"
		}
		seg := fmt.Sprintf("%s/%d %s", app, sv.Port, strings.ToLower(sv.Proto))
		c := dos.LightCyan
		if sv.Compromised {
			c = dos.LightGreen
		}
		s.Print(x, line, dos.Attr(c, dos.Black), seg)
		x += len([]rune(seg)) + 2
		if i == 0 {
			if b := firstBanner(sv); b != "" && x < a.scr.W-8 {
				bs := "“" + b + "”"
				s.Print(x, line, dos.Attr(dos.DarkGray, dos.Black), dos.Pad(bs, a.scr.W-x-1))
			}
		}
	}

	hints := " [a]ll-nets  [n/p] subnet  move: arrows / hjkl / mouse   M/TAB:dialer   ESC:quit "
	s.Print(0, a.lStatRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, a.scr.W))
}

func firstBanner(sv engine.Service) string {
	if sv.Banner != "" {
		return sv.Banner
	}
	return sv.ConnectBanner
}

// drawAllNets lists every /24 seen with a 16-cell sparkline summarising it.
func (a *App) drawAllNets(v engine.StateView) {
	s := a.scr
	title := fmt.Sprintf(" MAP  all networks  (%d /24s seen)", len(a.mapSubnets))
	prog := fmt.Sprintf("[a] one net   Dialed %d/%d ", v.Stats.Dialed, v.Stats.Max)
	bar := dos.Pad(title, a.scr.W-len([]rune(prog))) + prog
	s.Print(0, 0, dos.Attr(dos.Black, dos.LightGray), dos.Pad(bar, a.scr.W))

	if a.curRow >= len(a.mapSubnets) {
		a.curRow = max0(len(a.mapSubnets) - 1)
	}
	rows := a.scr.H - 4
	start := 0
	if a.curRow >= rows {
		start = a.curRow - rows + 1
	}
	for i := 0; i < rows; i++ {
		si := start + i
		if si >= len(a.mapSubnets) {
			break
		}
		sub := a.mapSubnets[si]
		y := 2 + i
		sel := si == a.curRow
		bg := dos.Black
		nameAttr := dos.Attr(dos.LightGray, dos.Black)
		if sel {
			bg = dos.Blue
			nameAttr = dos.Attr(dos.White, dos.Blue)
		}
		s.Fill(0, y, a.scr.W, 1, ' ', dos.Attr(dos.LightGray, bg))
		mark := "  "
		if sel {
			mark = "► "
		}
		s.Print(1, y, nameAttr, mark+dos.Pad(sub+".0/24", 16))
		// 16-cell sparkline: each cell = best verdict over 16 consecutive addrs.
		open, hosts := 0, 0
		for c := 0; c < 16; c++ {
			best := engine.RespUndialed
			for k := 0; k < 16; k++ {
				ip := sub + "." + strconv.Itoa(c*16+k)
				if vb, ok := a.mapVerdicts[ip]; ok {
					hosts++
					if engine.Response(vb).Priority() > best.Priority() {
						best = engine.Response(vb)
					}
					if engine.Response(vb).Found() {
						open++
					}
				}
			}
			ch, color := toneGlyph(best)
			s.Set(22+c, y, ch, dos.Attr(color, bg))
		}
		s.Print(40, y, dos.Attr(dos.LightGreen, bg), fmt.Sprintf("open %-4d", open))
		s.Print(52, y, dos.Attr(dos.DarkGray, bg), fmt.Sprintf("probed %d", hosts))
	}
	if len(a.mapSubnets) == 0 {
		s.Print(2, 3, dos.Attr(dos.DarkGray, dos.Black), "scanning… no subnets mapped yet")
	}

	hints := " ENTER: open subnet   [a] one-net view   ↑↓/jk: select   M/TAB:dialer   ESC:quit "
	s.Print(0, a.lStatRow, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, a.scr.W))
}

// toneGlyph maps a verdict to a coloured block for the map.
func toneGlyph(r engine.Response) (rune, int) {
	switch r {
	case engine.RespCarrier:
		return dos.BlockFull, dos.LightGreen // OPEN
	case engine.RespTone:
		return dos.BlockFull, dos.LightCyan // BANNER
	case engine.RespBusy:
		return dos.ShadeDark, dos.LightRed // RESET
	case engine.RespVoice:
		return dos.ShadeDark, dos.LightMagenta // FILTERED
	case engine.RespNoDialtone:
		return dos.ShadeMedium, dos.LightBlue // UNREACH
	case engine.RespRingout:
		return dos.ShadeMedium, dos.Cyan // NO-REPLY
	case engine.RespTimeout:
		return dos.ShadeLight, dos.Brown // TIMEOUT
	case engine.RespExcluded, engine.RespBlacklisted:
		return dos.ShadeLight, dos.Magenta
	default: // Unscanned
		return '·', dos.DarkGray
	}
}

// verdictLabel is the short network-term name for the footer.
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

var legendItems = []struct {
	resp  engine.Response
	label string
}{
	{engine.RespCarrier, "open"},
	{engine.RespTone, "banner"},
	{engine.RespBusy, "reset"},
	{engine.RespVoice, "filtered"},
	{engine.RespNoDialtone, "unreach"},
	{engine.RespRingout, "no-reply"},
	{engine.RespTimeout, "timeout"},
	{engine.RespUndialed, "unscanned"},
}

func (a *App) drawLegend() {
	s := a.scr
	bx := a.lTmLegendX - 1
	w := a.scr.W - bx
	s.Box(bx, mapGY-1, w, len(legendItems)+4, dos.Attr(dos.LightGray, dos.Black), true)
	s.Title(bx, mapGY-1, w, dos.Attr(dos.White, dos.Black), "Key")
	for i, it := range legendItems {
		ch, color := toneGlyph(it.resp)
		y := mapGY + i + 1
		s.Set(a.lTmLegendX, y, ch, dos.Attr(color, dos.Black))
		s.Set(a.lTmLegendX+1, y, ch, dos.Attr(color, dos.Black))
		s.Print(a.lTmLegendX+3, y, dos.Attr(dos.LightGray, dos.Black), it.label)
	}
}

// --- /24 helpers ---

func sub24(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() {
		return ""
	}
	b := a.As4()
	return fmt.Sprintf("%d.%d.%d", b[0], b[1], b[2])
}

func less24(a, b string) bool {
	pa, ea := netip.ParseAddr(a + ".0")
	pb, eb := netip.ParseAddr(b + ".0")
	if ea != nil || eb != nil {
		return a < b
	}
	return pa.Less(pb)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
