package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"sync"
	"time"
)

// Engine runs a ToneLoc scan: it pulls targets from the zmap-go cyclic dialer,
// probes each one, classifies the verdict in phone-line terms, and keeps the
// shared State up to date for the renderer. Interactive control (pause, abort,
// redial, notes) arrives through the exported methods, which are safe to call
// from the input goroutine.
type Engine struct {
	job   Job
	probe Probe
	state *State

	// The scan is a list of (mask, port) segments walked in order: mask-outer,
	// port-inner, so a whole network is swept one port at a time, most-common
	// port first. Each segment has its own zmap-go cyclic dialer over the
	// addresses for that one port.
	masks      []*Mask
	segments   []segment
	si         int // current segment index
	curMaskIdx int // mask the current segment belongs to (drives the ToneMap)

	ctrl    chan control
	redial  bool
	noteReq chan string
	done    chan struct{}

	dat      *DatFile
	dirty    bool
	lastSave time.Time

	toolkit   Toolkit         // nerva (fingerprint/udp) + brutus (creds)
	bgCtx     context.Context // long-lived context for tool goroutines
	sessionID string          // resumable session log id
	nervaOn   bool            // nerva UDP discovery + fingerprinting enabled
	brutusOn  bool            // brutus credential testing enabled

	bruteMu      sync.Mutex                    // guards bruteCancels
	bruteCancels map[string]context.CancelFunc // per-service brute cancellers
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "off"
}

// NervaEnabled reports whether nerva discovery/fingerprinting is on.
func (e *Engine) NervaEnabled() bool { return e.nervaOn }

// BrutusEnabled reports whether brutus credential testing is allowed.
func (e *Engine) BrutusEnabled() bool { return e.brutusOn }

type segment struct {
	maskIdx int
	mask    *Mask
	port    uint16
	dialer  *Dialer
}

type control struct {
	kind controlKind
	note string
}

type controlKind int

const (
	ctlAbort controlKind = iota
	ctlPause
	ctlResume
	ctlRedial
	ctlSpeaker
	ctlNote
	ctlQuit
	ctlAddWait
)

// New builds an engine for job, choosing/initialising the scan backend. It
// always returns a usable engine: if the requested real backend can't start
// (no privileges, no network, no zmap), it logs why and falls back to the
// simulator so the DOS experience -- and the web demo -- still runs.
func New(ctx context.Context, job Job) (*Engine, error) {
	masks := job.maskList()
	if len(masks) == 0 {
		return nil, fmt.Errorf("job has no mask")
	}
	if len(job.Ports) == 0 {
		job.Ports = []uint16{23}
	}
	if job.WaitDelay <= 0 {
		job.WaitDelay = 4 * time.Second
	}
	if job.MaxRings <= 0 {
		job.MaxRings = 6
	}
	single := len(masks) == 1

	// Build the segment list and tally the total search space.
	var segments []segment
	var max uint64
	for mi, m := range masks {
		span := uint64(m.Span())
		if single && job.Range != nil {
			span = uint64(job.Range.Hi - job.Range.Lo + 1)
		}
		max += span * uint64(len(job.Ports))
		for _, p := range job.Ports {
			var rng *Range
			if single {
				rng = job.Range
			}
			d, err := NewDialer(m, []uint16{p}, rng, job.Seed)
			if err != nil {
				return nil, err
			}
			segments = append(segments, segment{maskIdx: mi, mask: m, port: p, dialer: d})
		}
	}
	if job.Limit > 0 && job.Limit < max {
		max = job.Limit
	}

	st := newState()
	st.Backend = job.Backend
	st.MaskText = masks[0].Text()
	st.DataFile = job.DataFile
	st.tone = make([]uint8, masks[0].Span())
	st.toneSpan = int(masks[0].Span())
	st.Stats.Max = max

	e := &Engine{
		job:      job,
		masks:    masks,
		segments: segments,
		state:    st,
		ctrl:     make(chan control, 16),
		done:     make(chan struct{}),
	}

	e.banner()

	// Always keep a DatFile in memory so the scan is recorded (and can be saved
	// / downloaded). When a path is set we also load any prior results to resume,
	// exactly like the original .DAT behaviour, and autosave back to it.
	path := datPathFor(job.DataFile)
	dat := &DatFile{Path: path, Results: map[string]Result{}}
	if path != "" {
		if loaded, err := LoadDat(path); err != nil {
			e.logf("Could not read %s: %v", path, err)
		} else {
			dat = loaded
		}
	}
	dat.Mask = masks[0].Text()
	dat.Ports = job.Ports
	e.dat = dat
	if n := len(dat.Results); n > 0 {
		e.seedFromDat(dat)
		e.logf("Loaded %d previous results from %s", n, job.DataFile)
	}

	e.probe = e.selectProbe(ctx)
	st.ModemName = e.probe.Name()
	e.logf("Modem: %s", e.probe.Name())
	e.logf("Initializing modem ... Done")

	// Recon tools: nerva (UDP discovery + fingerprinting) and brutus (creds).
	e.toolkit = NewToolkit(job.Backend, job.Seed)
	e.bgCtx = context.Background()
	e.nervaOn = job.nervaEnabled()
	e.brutusOn = job.brutusEnabled()
	e.logf("Recon: nerva %s, brutus %s   (%s)", onOff(e.nervaOn), onOff(e.brutusOn), e.toolkit.Names)
	e.sessionID = job.SessionID
	if e.sessionID == "" {
		e.sessionID = newSessionID()
	}
	st.SessionID = e.sessionID
	e.logf("Session: %s  (resume with --restore %s)", e.sessionID, e.sessionID)
	return e, nil
}

// SessionID returns this scan's resumable session id.
func (e *Engine) SessionID() string { return e.sessionID }

// seedFromDat pre-populates the live stats and Found list from a loaded data
// file, so a resumed scan shows its history rather than starting from zero.
func (e *Engine) seedFromDat(dat *DatFile) {
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	st := &e.state.Stats
	for _, r := range dat.Results {
		st.Dialed++
		switch r.Response {
		case RespCarrier:
			st.Carriers++
		case RespTone:
			st.Tones++
		case RespVoice:
			st.Voice++
		case RespBusy:
			st.Busy++
		case RespNoDialtone:
			st.NoDialtone++
		case RespRingout:
			st.Ringout++
		case RespTimeout:
			st.Timeout++
		}
		if r.Response.Found() {
			entry := FoundEntry{Target: r.Target(), Resp: r.Response, When: dat.Updated}
			e.state.Found = append(e.state.Found, entry)
			e.state.hits = append(e.state.hits, entry)
		}
		if idx, ok := e.Mask().Index(r.Addr); ok {
			e.state.markTone(int(idx), r.Response)
		}
	}
	if len(e.state.Found) > 5 {
		e.state.Found = e.state.Found[len(e.state.Found)-5:]
	}
}

func (e *Engine) selectProbe(ctx context.Context) Probe {
	switch e.job.Backend {
	case "connect":
		return NewConnectProbe(400 * time.Millisecond)
	case "zmap":
		p, err := NewZmapProbe(ctx, e.job, func(s string) { e.log(s) })
		if err != nil {
			e.logf("zmap backend unavailable: %v", err)
			e.logf("Falling back to simulator.")
			return NewSimProbe(e.job.Seed)
		}
		return p
	default:
		return NewSimProbe(e.job.Seed)
	}
}

// State exposes the shared render state.
func (e *Engine) State() *State { return e.state }

// Mask is the address space currently being scanned (for the ToneMap to label
// cells); with multiple networks this is the one in flight.
func (e *Engine) Mask() *Mask { return e.masks[e.curMaskIdx] }

// Span is the number of addresses in the current mask.
func (e *Engine) Span() uint32 { return e.masks[e.curMaskIdx].Span() }

// nextTarget walks the segment list, switching the active mask (and resetting
// the ToneMap grid) at each network boundary, and returns the next address to
// dial.
func (e *Engine) nextTarget() (netip.Addr, uint16, bool) {
	for e.si < len(e.segments) {
		seg := e.segments[e.si]
		if seg.maskIdx != e.curMaskIdx {
			e.switchMask(seg.maskIdx)
		}
		if addr, port, ok := seg.dialer.Next(); ok {
			return addr, port, true
		}
		e.si++
	}
	return netip.Addr{}, 0, false
}

// switchMask points the ToneMap and stats at a new network as the scan moves on
// to it. Each network is swept fully (all ports) before the next, so the grid
// can simply be reset here.
func (e *Engine) switchMask(mi int) {
	m := e.masks[mi]
	e.state.mu.Lock()
	e.state.tone = make([]uint8, m.Span())
	e.state.toneSpan = int(m.Span())
	e.state.MaskText = m.Text()
	e.state.mu.Unlock()
	e.curMaskIdx = mi
	if len(e.masks) > 1 {
		e.logf("Scanning %s ...", m.Text())
	}
}

// Done is closed when the scan loop exits.
func (e *Engine) Done() <-chan struct{} { return e.done }

// --- interactive controls (called from the input goroutine) ---

func (e *Engine) Abort()   { e.send(control{kind: ctlAbort}) }
func (e *Engine) Pause()   { e.send(control{kind: ctlPause}) }
func (e *Engine) Resume()  { e.send(control{kind: ctlResume}) }
func (e *Engine) Redial()  { e.send(control{kind: ctlRedial}) }
func (e *Engine) Speaker() { e.send(control{kind: ctlSpeaker}) }
func (e *Engine) AddWait() { e.send(control{kind: ctlAddWait}) }
func (e *Engine) Quit()    { e.send(control{kind: ctlQuit}) }
func (e *Engine) Note(n string) {
	e.send(control{kind: ctlNote, note: n})
}

func (e *Engine) send(c control) {
	select {
	case e.ctrl <- c:
	default:
	}
}

// Run drives the scan to completion (or until ctx/quit). It blocks; callers
// typically run it in a goroutine while the renderer reads State.
func (e *Engine) Run(ctx context.Context) {
	defer close(e.done)
	defer e.probe.Close()

	e.bgCtx = ctx
	go e.discoverUDP(ctx) // nerva UDP sweep, concurrent with the TCP dialer

	waitDelay := e.job.WaitDelay
	var pending *target // a target to (re)dial before pulling the next

	for {
		// Drain control messages that apply between dials.
		select {
		case <-ctx.Done():
			e.finish("Escaped")
			return
		case c := <-e.ctrl:
			if e.handleBetweenDials(c) {
				e.finish("ToneLoc Exiting ...")
				return
			}
		default:
		}

		// Honour pause.
		for e.isPaused() {
			select {
			case <-ctx.Done():
				e.finish("Escaped")
				return
			case c := <-e.ctrl:
				if e.handleBetweenDials(c) {
					e.finish("ToneLoc Exiting ...")
					return
				}
			case <-time.After(100 * time.Millisecond):
			}
		}

		// Choose the next target.
		var tgt target
		if pending != nil {
			tgt = *pending
			pending = nil
		} else {
			addr, port, ok := e.nextTarget()
			if !ok {
				e.logf("All %d targets exhausted", e.state.Stats.Max)
				e.finish("ToneLoc Exiting ...")
				return
			}
			tgt = target{addr: addr, port: port}
		}

		if e.job.excluded(tgt.addr) {
			continue // /X excluded -- silently skip, like the original
		}
		if pending == nil && e.datHas(tgt.addr.String()+":"+itoa(tgt.port)) {
			continue // already dialed in a previous run -- resume past it
		}
		if e.job.Limit > 0 && uint64(e.state.Stats.Dialed) >= e.job.Limit {
			e.logf("Reached dial limit of %d", e.job.Limit)
			e.finish("ToneLoc Exiting ...")
			return
		}

		res, action := e.dialOne(ctx, tgt, waitDelay)
		switch action {
		case actQuit:
			e.record(res)
			e.finish("ToneLoc Exiting ...")
			return
		case actRedial:
			pending = &tgt
			continue
		case actAddWait:
			waitDelay += 5 * time.Second
			pending = &tgt
			e.setStatus("WaitDelay +5s")
			continue
		}
		e.record(res)
		e.autosave(false)
	}
}

// autosave writes the data file at most every 15s while a scan runs (and always
// when force is set, e.g. on exit), so progress survives a crash or Ctrl-C.
func (e *Engine) autosave(force bool) {
	if !force && time.Since(e.lastSave) < 15*time.Second {
		return
	}
	wasDirty := e.dirty
	if e.dat != nil && e.dirty {
		e.state.mu.Lock() // serialize with web export/import + session writes
		err := e.dat.Save()
		e.state.mu.Unlock()
		if err != nil {
			e.logf("Autosave failed: %v", err)
		} else {
			e.dirty = false
		}
	}
	e.lastSave = time.Now()
	e.saveSession() // also captures services found by background recon
	if !force && wasDirty {
		e.log("Autosaving")
	}
}

// datHas reports (under lock) whether a target has already been recorded.
func (e *Engine) datHas(target string) bool {
	if e.dat == nil {
		return false
	}
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	return e.dat.Has(target)
}

// ExportDat returns a filename and the serialized data file for the web
// download (a snapshot of everything dialed so far).
func (e *Engine) ExportDat() (string, []byte) {
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	name := filepath.Base(e.job.DataFile)
	if name == "" || name == "." {
		name = "toneloc.DAT"
	}
	if e.dat == nil {
		d := &DatFile{Mask: e.masks[0].Text(), Ports: e.job.Ports, Results: map[string]Result{}}
		return name, d.Bytes()
	}
	return name, e.dat.Bytes()
}

// ImportDat merges an uploaded data file into the running scan: known targets
// are skipped from here on and the stats/ToneMap/Hall of Fame reflect them.
// Returns how many new results were merged.
func (e *Engine) ImportDat(data []byte) (int, error) {
	d, err := ParseDat(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	e.state.mu.Lock()
	if e.dat == nil {
		e.dat = &DatFile{Mask: e.masks[0].Text(), Ports: e.job.Ports, Results: map[string]Result{}}
	}
	merged := 0
	st := &e.state.Stats
	for k, r := range d.Results {
		if _, ok := e.dat.Results[k]; ok {
			continue
		}
		e.dat.Results[k] = r
		merged++
		st.Dialed++
		switch r.Response {
		case RespCarrier:
			st.Carriers++
		case RespTone:
			st.Tones++
		case RespVoice:
			st.Voice++
		case RespBusy:
			st.Busy++
		case RespNoDialtone:
			st.NoDialtone++
		case RespRingout:
			st.Ringout++
		case RespTimeout:
			st.Timeout++
		}
		if r.Response.Found() {
			entry := FoundEntry{Target: r.Target(), Resp: r.Response, When: d.Updated}
			e.state.Found = append(e.state.Found, entry)
			if len(e.state.Found) > 5 {
				e.state.Found = e.state.Found[len(e.state.Found)-5:]
			}
			e.state.hits = append(e.state.hits, entry)
		}
		if idx, ok := e.Mask().Index(r.Addr); ok {
			e.state.markTone(int(idx), r.Response)
		}
	}
	e.dirty = true
	e.state.mu.Unlock()
	e.logf("Loaded %d result(s) from uploaded state", merged)
	return merged, nil
}

type target struct {
	addr netip.Addr
	port uint16
}

type dialAction int

const (
	actNone dialAction = iota
	actQuit
	actRedial
	actAddWait
)

// dialOne probes a single target while animating the meter and servicing
// interactive keys. It returns the verdict and any control action that should
// alter the loop.
func (e *Engine) dialOne(ctx context.Context, tgt target, waitDelay time.Duration) (Result, dialAction) {
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()

	target := tgt.addr.String() + ":" + itoa(tgt.port)
	e.state.mu.Lock()
	e.state.Target = target
	e.state.Meter = 0
	e.state.Rings = 0
	e.state.Tries = 1
	e.state.Status = ""
	e.state.mu.Unlock()

	// Authentic modem chatter: echo the dial command (shows in both the
	// terminal and the ghostty.js web view).
	e.modem("ATDT " + target)

	resCh := make(chan Result, 1)
	start := time.Now()
	go func() {
		resCh <- e.probe.Dial(dctx, tgt.addr, tgt.port, waitDelay, e.job.MaxRings)
	}()

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	action := actNone
	var noted string
	for {
		select {
		case <-ctx.Done():
			cancel()
			res := <-resCh
			res.Response = RespAborted
			return res, actQuit

		case res := <-resCh:
			e.state.mu.Lock()
			e.state.Meter = 1
			e.state.mu.Unlock()
			if noted != "" && (res.Response == RespAborted) {
				res.Response = RespVoice // a "note" answer
				res.Banner = noted
			}
			return res, action

		case <-ticker.C:
			frac := float64(time.Since(start)) / float64(waitDelay)
			if frac > 1 {
				frac = 1
			}
			e.state.mu.Lock()
			e.state.Meter = frac
			e.state.Rings = int(frac*float64(e.job.MaxRings)) + 1
			e.state.mu.Unlock()

		case c := <-e.ctrl:
			switch c.kind {
			case ctlAbort:
				cancel()
				e.setStatus("Aborted")
			case ctlRedial:
				cancel()
				action = actRedial
				e.setStatus("Redialing")
			case ctlAddWait:
				cancel()
				action = actAddWait
			case ctlQuit:
				cancel()
				res := <-resCh
				res.Response = RespAborted
				return res, actQuit
			case ctlPause:
				e.setPaused(true)
				e.setStatus("Paused - press P to continue")
				e.waitResume(ctx)
			case ctlSpeaker:
				e.toggleSpeaker()
			case ctlNote:
				noted = c.note
				cancel()
				e.setStatus("* Noted: " + c.note + " *")
			}
		}
	}
}

func (e *Engine) waitResume(ctx context.Context) {
	for e.isPaused() {
		select {
		case <-ctx.Done():
			return
		case c := <-e.ctrl:
			if c.kind == ctlResume || c.kind == ctlPause {
				e.setPaused(false)
				e.setStatus("")
				return
			}
			if c.kind == ctlQuit {
				e.setPaused(false)
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (e *Engine) handleBetweenDials(c control) (quit bool) {
	switch c.kind {
	case ctlQuit, ctlAbort:
		if c.kind == ctlQuit {
			return true
		}
	case ctlPause:
		e.setPaused(true)
	case ctlResume:
		e.setPaused(false)
	case ctlSpeaker:
		e.toggleSpeaker()
	}
	return false
}

// record applies a verdict to the stats, activity log, found list, and modem
// window -- the four places the original program surfaced a result.
func (e *Engine) record(res Result) {
	now := time.Now()
	e.state.mu.Lock()
	st := &e.state.Stats
	st.Dialed++
	switch res.Response {
	case RespCarrier:
		st.Carriers++
	case RespTone:
		st.Tones++
	case RespVoice:
		st.Voice++
	case RespBusy:
		st.Busy++
	case RespNoDialtone:
		st.NoDialtone++
	case RespRingout:
		st.Ringout++
	case RespTimeout:
		st.Timeout++
	}
	if res.Response.Found() {
		entry := FoundEntry{Target: res.Target(), Resp: res.Response, Banner: res.Banner, When: now}
		e.state.Found = append(e.state.Found, entry)
		if len(e.state.Found) > 5 {
			e.state.Found = e.state.Found[len(e.state.Found)-5:]
		}
		e.state.hits = append(e.state.hits, entry)
	}
	if e.dat != nil {
		e.dat.record(res)
		e.dirty = true
	}
	if idx, ok := e.Mask().Index(res.Addr); ok {
		e.state.markTone(int(idx), res.Response)
	}
	// dials/hour
	elapsed := now.Sub(e.state.StartTime).Hours()
	if elapsed > 0 {
		e.state.DialsHour = int(float64(st.Dialed) / elapsed)
	}
	e.state.mu.Unlock()

	// Modem result code, the way a real Hayes-compatible modem would answer.
	e.modem(modemResultCode(res))

	// Activity log line, in the spirit of the original messages.
	switch res.Response {
	case RespCarrier, RespTone:
		e.logTarget(res, res.Response.Tag())
		if res.Banner != "" {
			e.modem(res.Banner)
		}
		e.onOpenTCP(res) // register the service + kick off nerva fingerprinting
	case RespBusy, RespVoice:
		e.logTarget(res, res.Response.Tag())
	case RespNoDialtone:
		e.logTarget(res, fmt.Sprintf("No Dialtone #%d", res.Tries))
	case RespRingout:
		e.logTarget(res, fmt.Sprintf("Ringout (%d)", res.Rings))
	case RespTimeout:
		e.logTarget(res, fmt.Sprintf("Timeout (%d)", res.Rings))
	case RespAborted:
		e.logTarget(res, "Aborted")
	}
}

func (e *Engine) logTarget(res Result, tag string) {
	e.logf("%-19s - %s", res.Target(), tag)
}

// modemResultCode renders a result as the Hayes AT result code a modem would
// have printed -- pure 1990s flavour for the Modem window.
func modemResultCode(res Result) string {
	switch res.Response {
	case RespCarrier:
		speeds := []string{"CONNECT 2400", "CONNECT 9600", "CONNECT 14400", "CONNECT 33600", "CONNECT 57600"}
		return speeds[int(res.Port)%len(speeds)]
	case RespTone:
		return "CONNECT 33600/ARQ"
	case RespBusy:
		return "BUSY"
	case RespNoDialtone:
		return "NO DIALTONE"
	case RespVoice:
		return "VOICE"
	case RespRingout, RespTimeout:
		return "NO CARRIER"
	case RespAborted:
		return "+++"
	default:
		return "OK"
	}
}

func (e *Engine) finish(msg string) {
	e.autosave(true)
	e.state.mu.Lock()
	e.state.Done = true
	e.state.Status = msg
	dialed := e.state.Stats.Dialed
	dph := e.state.DialsHour
	e.state.mu.Unlock()
	e.logf("Dials/hour : %d", dph)
	if e.dat != nil && e.dat.Path != "" {
		e.logf("Saved %d results to %s", len(e.dat.Results), e.dat.Path)
	}
	e.logf("%s (%d dialed)", msg, dialed)
}

// --- small state helpers ---

func (e *Engine) banner() {
	e.logf("ToneLoc started on %s", time.Now().Format("02-Jan-06"))
	e.logf("Data file:   %s", e.job.DataFile)
	if len(e.masks) == 1 {
		e.logf("Mask used:   %s", e.masks[0].Text())
	} else {
		e.logf("Networks:    %d local network(s)", len(e.masks))
		for i, m := range e.masks {
			if i >= 6 {
				e.logf("  ... and %d more", len(e.masks)-6)
				break
			}
			e.logf("  %s", m.Text())
		}
	}
	ports := ""
	for i, p := range e.job.Ports {
		if i > 0 {
			ports += ","
		}
		ports += itoa(p)
	}
	e.logf("Ports:       %s", ports)
}

func (e *Engine) log(s string) {
	e.state.mu.Lock()
	e.state.Activity.push(time.Now().Format("15:04:05") + " " + s)
	e.state.mu.Unlock()
}

func (e *Engine) logf(format string, a ...any) { e.log(fmt.Sprintf(format, a...)) }

func (e *Engine) modem(s string) {
	e.state.mu.Lock()
	e.state.Modem.push(s)
	e.state.mu.Unlock()
}

func (e *Engine) setStatus(s string) {
	e.state.mu.Lock()
	e.state.Status = s
	e.state.mu.Unlock()
}

func (e *Engine) isPaused() bool {
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	return e.state.Paused
}

func (e *Engine) setPaused(p bool) {
	e.state.mu.Lock()
	e.state.Paused = p
	e.state.mu.Unlock()
}

func (e *Engine) toggleSpeaker() {
	e.state.mu.Lock()
	e.state.Speaker = !e.state.Speaker
	on := e.state.Speaker
	e.state.mu.Unlock()
	if on {
		e.setStatus("Speaker ON")
	} else {
		e.setStatus("Speaker OFF")
	}
}
