package engine

import (
	"context"
	"hash/fnv"
	"math/rand"
	"net/netip"
	"time"
)

// SimProbe is a self-contained simulator. It needs no privileges, no network,
// and no zmap binary, so the full DOS experience -- and the ghostty.js web
// version -- runs for anyone, anywhere. Outcomes are drawn from a per-target
// hash so a given address:port always "answers" the same way within a seed,
// which keeps redials and persisted .DAT files consistent.
type SimProbe struct {
	seed   uint64
	modem  string
	banner []string
}

// NewSimProbe returns a simulator seeded for reproducibility (seed 0 -> live).
func NewSimProbe(seed uint64) *SimProbe {
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &SimProbe{
		seed:  seed,
		modem: "Hayes-compatible (simulated)",
		banner: []string{
			"login:", "Password:", "SSH-2.0-OpenSSH_9.6", "220 FTP ready",
			"HTTP/1.0 200 OK", "+OK POP3", "Welcome to VMS", "Username:",
			"@ Annex Command Line Interpreter", "Connected to BBS",
		},
	}
}

func (s *SimProbe) Name() string { return s.modem }

func (s *SimProbe) Close() error { return nil }

// roll produces a stable pseudo-random float in [0,1) for this target.
func (s *SimProbe) roll(addr netip.Addr, port uint16, salt uint64) float64 {
	h := fnv.New64a()
	var b [8]byte
	a := addr.As4()
	b[0], b[1], b[2], b[3] = a[0], a[1], a[2], a[3]
	b[4], b[5] = byte(port>>8), byte(port)
	b[6], b[7] = byte(s.seed), byte(s.seed>>8)
	_, _ = h.Write(b[:])
	var s2 [16]byte
	for i := 0; i < 8; i++ {
		s2[i] = byte(s.seed >> (8 * i))
		s2[i+8] = byte(salt >> (8 * i))
	}
	_, _ = h.Write(s2[:])
	return float64(h.Sum64()%1_000_000) / 1_000_000.0
}

func (s *SimProbe) Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result {
	res := Result{Addr: addr, Port: port, Tries: 1}

	// Pick the eventual outcome up front so we can pace the "rings" toward it.
	verdict := s.roll(addr, port, 1)
	rng := rand.New(rand.NewSource(int64(s.roll(addr, port, 7) * 1e9)))

	// How many rings before something happens, bounded by MaxRings.
	rings := 1 + rng.Intn(maxRing(maxRings))
	res.Rings = rings

	// Spread the wait across the rings so the meter fills believably; bail if
	// the user aborts (space/ESC) mid-dial.
	per := waitDelay / time.Duration(maxRing(maxRings))
	if per <= 0 {
		per = waitDelay
	}
	for i := 0; i < rings; i++ {
		select {
		case <-ctx.Done():
			res.Response = RespAborted
			res.Rings = i
			return res
		case <-time.After(jitter(rng, per)):
		}
	}

	switch {
	case verdict < 0.012: // ~1.2% carriers -- the prize
		res.Response = RespCarrier
		res.Banner = s.banner[rng.Intn(len(s.banner))]
	case verdict < 0.030: // ~1.8% tones (open + immediate banner)
		res.Response = RespTone
		res.Banner = s.banner[rng.Intn(len(s.banner))]
	case verdict < 0.230: // ~20% busy (RST / actively refused)
		res.Response = RespBusy
		res.Rings = 0
	case verdict < 0.260: // ~3% voice (odd answer)
		res.Response = RespVoice
	case verdict < 0.300: // ~4% no dialtone (no route to host)
		res.Response = RespNoDialtone
		res.Rings = 0
		res.Tries = 1 + rng.Intn(2)
	case verdict < 0.330: // ~3% ringout
		res.Response = RespRingout
		res.Rings = maxRing(maxRings)
	default: // the rest time out, like most of the phone book
		res.Response = RespTimeout
	}
	return res
}

func maxRing(m int) int {
	if m < 1 {
		return 1
	}
	return m
}

func jitter(rng *rand.Rand, base time.Duration) time.Duration {
	// +/- 40% so the meter doesn't tick like a metronome.
	delta := time.Duration(rng.Int63n(int64(base)*4/5+1)) - base*2/5
	d := base + delta
	if d < 0 {
		d = base / 4
	}
	return d
}
