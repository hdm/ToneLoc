package engine

import (
	"context"
	"errors"
	"net/netip"
)

// onOpenTCP records an open TCP port as a Service and launches nerva to
// fingerprint it. Called from the dial loop when a carrier/tone is found.
func (e *Engine) onOpenTCP(res Result) {
	app := appForPort("tcp", res.Port)
	// res.Banner is what connect/zmap grabbed; nerva (below) may overwrite App
	// and add its own banner/version.
	svc := &Service{Addr: res.Addr, Port: res.Port, Proto: "tcp", App: app, ConnectBanner: res.Banner}
	svc.Brutable = brutableFor(app, "tcp", res.Port)
	s, created := e.state.upsertService(svc)
	if created && e.nervaOn {
		go e.fingerprint(s.Key(), res.Addr.String(), res.Port)
	}
}

// fingerprint asks nerva what is running on a port and folds the answer back
// into the service.
func (e *Engine) fingerprint(key, ip string, port uint16) {
	fp, ok := e.toolkit.Finger.Fingerprint(e.bgCtx, ip, port)
	if !ok {
		return
	}
	e.state.updateService(key, func(s *Service) {
		if fp.App != "" {
			s.App = fp.App
		}
		if fp.Banner != "" {
			s.Banner = fp.Banner
		}
		s.Version = fp.Version
		s.Brutable = brutableFor(s.App, s.Proto, s.Port)
	})
	v := fp.Version
	if v != "" {
		v = " " + v
	}
	e.logf("nerva: %s:%d -> %s%s", ip, port, fp.App, v)
}

// discoverUDP runs nerva's UDP probes across the whole address space and
// registers any responders as UDP services.
func (e *Engine) discoverUDP(ctx context.Context) {
	if !e.nervaOn {
		return
	}
	addrs := e.allAddrs(1 << 16)
	if len(addrs) == 0 {
		return
	}
	e.toolkit.UDP.ScanUDP(ctx, addrs, commonUDPPorts, func(svc Service) {
		svc.Brutable = bruteProtocol(svc.App) != ""
		if _, created := e.state.upsertService(&svc); created {
			e.logf("nerva/udp: %s/%d open (%s)", svc.IP, svc.Port, svc.App)
		}
	})
}

// allAddrs flattens every mask's addresses into a slice (capped at max).
func (e *Engine) allAddrs(max int) []netip.Addr {
	var out []netip.Addr
	for _, m := range e.masks {
		n := m.Span()
		for i := uint32(0); i < n; i++ {
			out = append(out, m.Addr(i))
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}

// StartBrute launches brutus against a discovered service in the background,
// streaming progress into the UI and marking the service compromised if a valid
// credential turns up.
func (e *Engine) StartBrute(key string) {
	if !e.brutusOn {
		e.logf("brutus is disabled (enable with --brutus)")
		return
	}
	var svc Service
	start := false
	e.state.updateService(key, func(s *Service) {
		if !s.Brutable || s.Brute == BruteRunning || s.Brute == BruteQueued {
			return
		}
		s.Brute = BruteQueued
		s.Tried = 0
		svc = *s
		start = true
	})
	if !start {
		return
	}
	ctx, cancel := context.WithCancel(e.bgCtx)
	e.bruteMu.Lock()
	if e.bruteCancels == nil {
		e.bruteCancels = map[string]context.CancelFunc{}
	}
	e.bruteCancels[key] = cancel
	e.bruteMu.Unlock()

	go func() {
		e.state.updateService(key, func(s *Service) { s.Brute = BruteRunning })
		e.logf("brutus: testing %s creds on %s ...", svc.App, svc.Target())
		creds, err := e.toolkit.Brute.Brute(ctx, svc, func(tried int) {
			e.state.updateService(key, func(s *Service) { s.Tried = tried })
		})
		cancelled := errors.Is(err, context.Canceled)
		e.bruteMu.Lock()
		delete(e.bruteCancels, key)
		e.bruteMu.Unlock()
		cancel()

		e.state.updateService(key, func(s *Service) {
			switch {
			case cancelled:
				s.Brute = BruteIdle // cancelled -> back to idle, retriable
			case err != nil:
				s.Brute = BruteFailed
			default:
				s.Brute = BruteDone
				s.Creds = creds
				s.Compromised = len(creds) > 0
			}
		})
		switch {
		case cancelled:
			e.logf("brutus: cancelled on %s (after %d creds)", svc.Target(), svc.Tried)
		case err != nil:
			e.logf("brutus: %s errored on %s", svc.App, svc.Target())
		case len(creds) > 0:
			e.logf("brutus: ** %s COMPROMISED ** %s @ %s", svc.App, creds[0].String(), svc.Target())
		default:
			e.logf("brutus: no valid creds on %s", svc.Target())
		}
		e.saveSession() // persist brute outcome even after the scan loop ends
	}()
}

// CancelBrute stops an in-progress brute against a service (the X key).
func (e *Engine) CancelBrute(key string) {
	e.bruteMu.Lock()
	cancel := e.bruteCancels[key]
	delete(e.bruteCancels, key)
	e.bruteMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
