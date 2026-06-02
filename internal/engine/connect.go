package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// ConnectProbe performs a real TCP connect() scan. It needs no special
// privileges (unlike a raw SYN scan), which makes it the honest "live" backend
// for the interactive dialer. The connect outcome is translated into ToneLoc's
// phone-line vocabulary:
//
//	connection established      -> Carrier (and Tone if a banner arrives)
//	connection refused (RST)    -> Busy
//	no route / host unreachable -> No Dialtone
//	deadline exceeded (silence) -> Timeout, or Ringout at MaxRings
type ConnectProbe struct {
	bannerWait time.Duration
}

// NewConnectProbe returns a connect-scan probe. bannerWait is how long to wait
// for a service to volunteer a banner before calling it a plain carrier.
func NewConnectProbe(bannerWait time.Duration) *ConnectProbe {
	if bannerWait <= 0 {
		bannerWait = 400 * time.Millisecond
	}
	return &ConnectProbe{bannerWait: bannerWait}
}

func (c *ConnectProbe) Name() string { return "TCP connect()" }

func (c *ConnectProbe) Close() error { return nil }

func (c *ConnectProbe) Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result {
	res := Result{Addr: addr, Port: port, Tries: 1, Rings: 1}

	dctx, cancel := context.WithTimeout(ctx, waitDelay)
	defer cancel()

	d := net.Dialer{}
	conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(addr.String(), itoa(port)))
	if err != nil {
		res.Response = classifyDialErr(err, ctx, maxRings)
		if res.Response == RespBusy || res.Response == RespNoDialtone {
			res.Rings = 0
		}
		if res.Response == RespRingout {
			res.Rings = maxRing(maxRings)
		}
		return res
	}
	defer conn.Close()

	// Connected -- at minimum a carrier. Listen briefly for a banner ("tone").
	_ = conn.SetReadDeadline(time.Now().Add(c.bannerWait))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if n > 0 {
		res.Response = RespTone
		res.Banner = sanitizeBanner(buf[:n])
	} else {
		res.Response = RespCarrier
	}
	return res
}

func classifyDialErr(err error, parent context.Context, maxRings int) Response {
	if parent.Err() != nil {
		return RespAborted
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return RespBusy
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return RespNoDialtone
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return RespRingout
	}
	// Anything else that "answered" oddly.
	if strings.Contains(err.Error(), "refused") {
		return RespBusy
	}
	return RespTimeout
}

func sanitizeBanner(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, r := range string(b) {
		if r == '\r' || r == '\n' {
			break
		}
		if r < 0x20 || r > 0x7e {
			r = '.'
		}
		out = append(out, r)
		if len(out) >= 40 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

func itoa(p uint16) string {
	if p == 0 {
		return "0"
	}
	var b [5]byte
	i := len(b)
	for p > 0 {
		i--
		b[i] = byte('0' + p%10)
		p /= 10
	}
	return string(b[i:])
}
