package tui

import (
	"fmt"
	"strings"

	"github.com/hdm/toneloc/internal/dos"
	"github.com/hdm/toneloc/internal/engine"
)

// hostsView is the primary live view: every scanned host on its own row, with a
// dense per-port progress bar (one coloured pip per port: open / banner / reset
// / filtered / timeout / pending), sorted by IP address ascending, scrollable.
// It replaces the old single bottom progress strip -- you watch the whole sweep
// fill in, host by host, and drill into any one of them.
type hostsView struct {
	sel    int  // selected host index into the address-sorted list
	scroll int  // index of the first visible row
	follow bool // auto-track the host currently being dialed
	cap    int  // visible rows from the last draw (for PgUp/PgDn)
	count  int  // hosts from the last draw (for clamping)
}

func newHostsView() *hostsView { return &hostsView{follow: true} }

func (hv *hostsView) draw(s *dos.Screen, a *App, v engine.StateView) {
	rows := a.eng.State().HostScansByAddr()
	hv.count = len(rows)
	W, H := s.W, s.H
	s.Clear(dos.Attr(dos.LightGray, dos.Black))

	open := 0
	for _, r := range rows {
		if r.Open > 0 {
			open++
		}
	}

	// Global navigation bar with a hosts-specific status on the right.
	foll := "follow:off"
	if hv.follow {
		foll = "● following"
	}
	right := fmt.Sprintf("%d hosts · %d open · %s · %d/%d ", len(rows), open, foll, v.Stats.Dialed, v.Stats.Max)
	a.drawModeBar(modeHosts, right)

	// Column geometry.
	ipW := 16
	openW := 11
	svcW := 30
	if W < 80 {
		svcW = 18
	}
	barX := 1 + ipW + 1
	openX := W - svcW - openW - 1
	svcX := W - svcW
	barW := openX - barX - 1
	if barW < 8 {
		barW = 8
	}

	// Column header row.
	hy := 1
	s.Fill(0, hy, W, 1, ' ', dos.Attr(dos.LightGray, dos.Black))
	s.Print(1, hy, dos.Attr(dos.Yellow, dos.Black), "HOST")
	s.Print(barX, hy, dos.Attr(dos.Yellow, dos.Black), "PORTS")
	lx := barX + 6
	legend := []struct {
		r engine.Response
		s string
	}{
		{engine.RespCarrier, "open"}, {engine.RespTone, "banner"}, {engine.RespBusy, "reset"},
		{engine.RespVoice, "filt"}, {engine.RespTimeout, "time"}, {engine.RespUndialed, "pending"},
	}
	for _, it := range legend {
		if lx+len([]rune(it.s))+2 >= openX {
			s.Print(lx, hy, dos.Attr(dos.DarkGray, dos.Black), "…")
			break
		}
		ch, c := probePip(it.r)
		s.Set(lx, hy, ch, dos.Attr(c, dos.Black))
		s.Print(lx+1, hy, dos.Attr(dos.LightGray, dos.Black), it.s)
		lx += len([]rune(it.s)) + 2
	}
	s.Print(openX, hy, dos.Attr(dos.Yellow, dos.Black), "OPEN")
	s.Print(svcX, hy, dos.Attr(dos.Yellow, dos.Black), "TOP SERVICE")

	listTop := 2
	listH := H - 1 - listTop
	if listH < 1 {
		listH = 1
	}
	hv.cap = listH

	if len(rows) == 0 {
		s.Print(2, listTop+1, dos.Attr(dos.DarkGray, dos.Black), "sweeping… hosts appear here as they answer")
		hv.drawHints(s)
		return
	}

	// Follow the in-flight host, else keep the selection in view.
	if hv.follow {
		if ip, _, ok := strings.Cut(v.Target, ":"); ok {
			if idx := indexOfIP(rows, ip); idx >= 0 {
				hv.sel = idx
			}
		}
	}
	hv.sel = clamp(hv.sel, 0, len(rows)-1)
	if hv.sel < hv.scroll {
		hv.scroll = hv.sel
	}
	if hv.sel >= hv.scroll+listH {
		hv.scroll = hv.sel - listH + 1
	}
	hv.scroll = clamp(hv.scroll, 0, max0(len(rows)-listH))

	for i := 0; i < listH; i++ {
		ri := hv.scroll + i
		if ri >= len(rows) {
			break
		}
		hs := rows[ri]
		y := listTop + i
		sel := ri == hv.sel
		bg := dos.Black
		ipColor := dos.White
		if sel {
			bg = dos.Blue
			ipColor = dos.Yellow
		}
		s.Fill(0, y, W, 1, ' ', dos.Attr(dos.LightGray, bg))
		mark := " "
		if sel {
			mark = "▸"
		}
		s.Print(0, y, dos.Attr(dos.LightCyan, bg), mark)
		s.Print(1, y, dos.Attr(ipColor, bg), dos.Pad(hs.IP, ipW))
		// set), so the bar is exactly as wide as there are ports.
		pips := make([]dos.Pip, len(hs.Ports))
		for j, p := range hs.Ports {
			ch, c := probePip(engine.Response(p))
			pips[j] = dos.Pip{Ch: ch, FG: c, BG: bg}
		}
		bw := len(pips)
		if bw > barW {
			bw = barW
		}
		s.PipBar(barX, y, bw, pips, '·', dos.Attr(dos.DarkGray, bg))

		// Open count.
		oc := dos.DarkGray
		if hs.Open > 0 {
			oc = dos.LightGreen
		}
		s.Print(openX, y, dos.Attr(oc, bg), fmt.Sprintf("%d/%d", hs.Open, len(hs.Ports)))

		// Top service / banner. Last column is reserved for the scrollbar; pwned
		// hosts glow green so they stand out before you read the banner.
		svc := topService(a.eng, hs)
		svcCol := dos.LightCyan
		if sel {
			svcCol = dos.White
		} else if strings.Contains(svc, "PWNED") {
			svcCol = dos.LightGreen
		}
		s.Print(svcX, y, dos.Attr(svcCol, bg), dos.Pad(svc, svcW-1))
	}

	// Scrollbar.
	if len(rows) > listH {
		drawScrollbar(s, W-1, listTop, listH, hv.scroll, len(rows))
	}
	hv.drawHints(s)
}

func (hv *hostsView) drawHints(s *dos.Screen) {
	hints := " j/k:scroll  g/e:top/end  f:follow  ENTER:detail  B:brute  G:GAME  ESC:close "
	s.Print(0, s.H-1, dos.Attr(dos.Black, dos.LightGray), dos.Pad(hints, s.W))
}

// --- scrolling input --------------------------------------------------------

func (hv *hostsView) move(d int) {
	hv.follow = false
	hv.sel += d
	if hv.sel < 0 {
		hv.sel = 0
	}
	if hv.count > 0 && hv.sel >= hv.count {
		hv.sel = hv.count - 1
	}
}

func (hv *hostsView) page(dir int) { hv.move(dir * max0(hv.cap-1)) }

func (hv *hostsView) home() { hv.follow = false; hv.sel = 0 }
func (hv *hostsView) end()  { hv.follow = false; hv.sel = max0(hv.count - 1) }

// --- helpers ----------------------------------------------------------------

func indexOfIP(rows []engine.HostScan, ip string) int {
	for i := range rows {
		if rows[i].IP == ip {
			return i
		}
	}
	return -1
}

// topService returns a short "app/port banner" summary for a host row, using
// the recon registry; falls back to the first banner the scan saw.
func topService(eng *engine.Engine, hs engine.HostScan) string {
	svcs := eng.State().ServicesForIP(hs.IP)
	if len(svcs) == 0 {
		if hs.Open > 0 {
			return "(fingerprinting…)"
		}
		return "—"
	}
	// Prefer a compromised service, else the first with an app name.
	best := svcs[0]
	for _, sv := range svcs {
		if sv.Compromised {
			best = sv
			break
		}
		if best.App == "" && sv.App != "" {
			best = sv
		}
	}
	app := best.App
	if app == "" {
		app = "?"
	}
	out := fmt.Sprintf("%s/%d", app, best.Port)
	if best.Compromised {
		return out + " ** PWNED **"
	}
	if b := firstBanner(best); b != "" {
		out += " " + b
	}
	return out
}

func drawScrollbar(s *dos.Screen, x, y, h, scroll, total int) {
	if total <= h {
		return
	}
	thumb := h * h / total
	if thumb < 1 {
		thumb = 1
	}
	pos := y + (h-thumb)*scroll/max0(total-h)
	for i := 0; i < h; i++ {
		ch, c := '░', dos.DarkGray
		if y+i >= pos && y+i < pos+thumb {
			ch, c = '█', dos.LightCyan
		}
		s.Set(x, y+i, ch, dos.Attr(c, dos.Black))
	}
}
