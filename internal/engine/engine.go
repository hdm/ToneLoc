package engine

import (
	"context"
	"fmt"
	"net/netip"
	"time"
)

// Engine runs a ToneLoc scan: it pulls targets from the zmap-go cyclic dialer,
// probes each one, classifies the verdict in phone-line terms, and keeps the
// shared State up to date for the renderer. Interactive control (pause, abort,
// redial, notes) arrives through the exported methods, which are safe to call
// from the input goroutine.
type Engine struct {
	job    Job
	probe  Probe
	dialer *Dialer
	state  *State

	ctrl    chan control
	redial  bool
	noteReq chan string
	done    chan struct{}

	dat      *DatFile
	dirty    bool
	lastSave time.Time
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
	if job.Mask == nil {
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

	dialer, err := NewDialer(job.Mask, job.Ports, job.Range, job.Seed)
	if err != nil {
		return nil, err
	}

	st := newState()
	st.Backend = job.Backend
	st.MaskText = job.Mask.Text()
	st.DataFile = job.DataFile
	st.tone = make([]uint8, job.Mask.Span())
	st.toneSpan = int(job.Mask.Span())
	st.Stats.Max = dialer.Total()
	if job.Range != nil {
		st.Stats.Max = uint64(job.Range.Hi-job.Range.Lo+1) * uint64(len(job.Ports))
	}
	if job.Limit > 0 && job.Limit < st.Stats.Max {
		st.Stats.Max = job.Limit
	}

	e := &Engine{
		job:    job,
		dialer: dialer,
		state:  st,
		ctrl:   make(chan control, 16),
		done:   make(chan struct{}),
	}

	e.banner()

	// Load any existing data file so an interrupted scan resumes where it left
	// off, exactly like the original .DAT behaviour.
	if path := datPathFor(job.DataFile); path != "" {
		dat, err := LoadDat(path)
		if err != nil {
			e.logf("Could not read %s: %v", path, err)
			dat = &DatFile{Path: path, Results: map[string]Result{}}
		}
		dat.Mask = job.Mask.Text()
		dat.Ports = job.Ports
		e.dat = dat
		if n := len(dat.Results); n > 0 {
			e.seedFromDat(dat)
			e.logf("Loaded %d previous results from %s", n, job.DataFile)
		}
	}

	e.probe = e.selectProbe(ctx)
	st.ModemName = e.probe.Name()
	e.logf("Modem: %s", e.probe.Name())
	e.logf("Initializing modem ... Done")
	return e, nil
}

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
			e.state.Found = append(e.state.Found, FoundEntry{Target: r.Target(), Resp: r.Response, When: dat.Updated})
		}
		if idx, ok := e.job.Mask.Index(r.Addr); ok {
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

// Mask is the address space being scanned (for the ToneMap to label cells).
func (e *Engine) Mask() *Mask { return e.job.Mask }

// Span is the number of addresses in the scan.
func (e *Engine) Span() uint32 { return e.job.Mask.Span() }

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
			addr, port, ok := e.dialer.Next()
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
		if pending == nil && e.dat != nil && e.dat.Has(tgt.addr.String()+":"+itoa(tgt.port)) {
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
	if e.dat == nil || !e.dirty {
		return
	}
	if !force && time.Since(e.lastSave) < 15*time.Second {
		return
	}
	if err := e.dat.Save(); err != nil {
		e.logf("Autosave failed: %v", err)
		return
	}
	e.dirty = false
	e.lastSave = time.Now()
	if !force {
		e.log("Autosaving")
	}
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
		e.state.Found = append(e.state.Found, FoundEntry{Target: res.Target(), Resp: res.Response, When: now})
		if len(e.state.Found) > 5 {
			e.state.Found = e.state.Found[len(e.state.Found)-5:]
		}
	}
	if e.dat != nil {
		e.dat.record(res)
		e.dirty = true
	}
	if idx, ok := e.job.Mask.Index(res.Addr); ok {
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
	e.logf("Mask used:   %s", e.job.Mask.Text())
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
