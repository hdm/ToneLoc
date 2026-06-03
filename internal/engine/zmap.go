package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	"github.com/hdm/zmap-go/pkg/gateway"
	"github.com/hdm/zmap-go/pkg/packet"
	"github.com/hdm/zmap-go/pkg/probe"
	"github.com/hdm/zmap-go/pkg/raw"
	"github.com/hdm/zmap-go/pkg/validate"
)

// ZmapProbe drives zmap-go IN-PROCESS as a library (no subprocess, no building
// a binary). It opens a raw socket, sends a tcp_synscan SYN to every target up
// front -- exactly what a stateless mass scanner is good at -- and records every
// classified response. The interactive dialer then "redials" each target by
// looking up what the sweep heard; targets that never answered are timeouts.
//
// A real SYN scan needs raw-socket privileges and a live network; when that is
// unavailable NewZmapProbe returns an error and the caller falls back to the
// simulator.
type ZmapProbe struct {
	hits    map[string]Response // "ip:port" -> Carrier/Busy
	scanned int
}

const zmapSrcPortFirst, zmapSrcPortLast = 32768, 61000

// NewZmapProbe runs an in-process SYN sweep over the job's networks/ports using
// zmap-go's packet, raw, probe and validate packages, and indexes the responses.
func NewZmapProbe(ctx context.Context, job Job, log func(string)) (*ZmapProbe, error) {
	intf, srcIP, srcMAC, err := localInterface()
	if err != nil {
		return nil, fmt.Errorf("zmap: %w", err)
	}
	conn, err := raw.ListenPacket(intf.Name)
	if err != nil {
		return nil, fmt.Errorf("zmap: raw socket on %s (needs root/cap_net_raw): %w", intf.Name, err)
	}
	defer conn.Close()

	gwIP, err := gateway.DefaultGateway(intf)
	if err != nil {
		return nil, fmt.Errorf("zmap: default gateway: %w", err)
	}
	gwMAC, err := gateway.ResolveMACWithConn(conn, srcIP, srcMAC, gwIP, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("zmap: resolve gateway MAC: %w", err)
	}
	v, err := validate.New()
	if err != nil {
		return nil, err
	}
	m := &probe.TCPSyn{Style: packet.StyleWindows, TTL: 255,
		SrcPortFirst: zmapSrcPortFirst, SrcPortLast: zmapSrcPortLast}

	if log != nil {
		log(fmt.Sprintf("Initializing modem ... zmap tcp_synscan (library) via %s -> gw %s", intf.Name, gwIP))
	}

	p := &ZmapProbe{hits: map[string]Response{}}
	var mu sync.Mutex
	done := make(chan struct{})

	// Receiver: validate replies and record carriers (synack) / busies (rst).
	lt := layers.LayerTypeEthernet
	if conn.LinkType() == "loopback" || conn.LinkType() == "null" {
		lt = layers.LayerTypeLoopback
	}
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for {
			n, err := conn.ReadFrom(buf)
			if err != nil {
				return // conn closed after cooldown
			}
			pkt := gopacket.NewPacket(buf[:n], lt, gopacket.NoCopy)
			r, ok := m.ValidatePacket(pkt, v, srcIP, zmapSrcPortFirst, zmapSrcPortLast)
			if !ok {
				continue
			}
			resp := RespBusy
			if r.Success {
				resp = RespCarrier
			}
			key := r.SrcIP.String() + ":" + itoa(r.SrcPort)
			mu.Lock()
			p.hits[key] = resp
			mu.Unlock()
		}
	}()

	// Sender: one SYN per (target, port), across every network in the job.
	srcU := ipBE(srcIP)
	const maxProbes = 1 << 20 // safety cap
	sent := 0
	for _, mask := range job.maskList() {
		span := mask.Span()
		for i := uint32(0); i < span && sent < maxProbes; i++ {
			select {
			case <-ctx.Done():
				goto cooldown
			default:
			}
			a := mask.Addr(i).As4()
			dstIP := net.IPv4(a[0], a[1], a[2], a[3])
			dstU := ipBE(dstIP)
			for _, port := range job.Ports {
				t := v.GenWords(srcU, dstU, uint32(port), 0)
				frame, _, berr := m.BuildProbe(srcIP, dstIP, port, srcMAC, gwMAC, uint16(t[2]), t)
				if berr != nil {
					continue
				}
				_, _ = conn.WriteTo(frame)
				sent++
			}
		}
	}
	p.scanned = sent

cooldown:
	// Give late replies a moment, then close the conn to unblock the receiver.
	select {
	case <-ctx.Done():
	case <-time.After(4 * time.Second):
	}
	conn.Close()
	<-done

	mu.Lock()
	n := len(p.hits)
	mu.Unlock()
	if log != nil {
		log(fmt.Sprintf("zmap sweep complete: %d SYNs sent, %d responses indexed", sent, n))
	}
	return p, nil
}

func (z *ZmapProbe) Name() string { return "zmap tcp_synscan (lib)" }

func (z *ZmapProbe) Close() error { return nil }

func (z *ZmapProbe) Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result {
	res := Result{Addr: addr, Port: port, Tries: 1, Rings: 1}
	// A short, bounded pause so the meter still animates over the replayed
	// sweep -- the data is already in hand.
	select {
	case <-ctx.Done():
		res.Response = RespAborted
		return res
	case <-time.After(min(waitDelay/8, 120*time.Millisecond)):
	}
	if r, ok := z.hits[addr.String()+":"+itoa(port)]; ok {
		res.Response = r
		if r == RespBusy {
			res.Rings = 0
		}
		return res
	}
	res.Response = RespTimeout
	return res
}

// localInterface picks the first up, non-loopback interface with an IPv4
// address, returning it plus its source IP and MAC for SYN crafting.
func localInterface() (*net.Interface, net.IP, net.HardwareAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range ifaces {
		in := ifaces[i]
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 || len(in.HardwareAddr) == 0 {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
				return &in, ip4, in.HardwareAddr, nil
			}
		}
	}
	return nil, nil, nil, fmt.Errorf("no usable IPv4 interface found")
}

// ipBE returns the IPv4 address as a big-endian uint32 (how the validator's AES
// tuple sees it), matching zmap-go's own derivation.
func ipBE(ip net.IP) uint32 {
	a := ip.To4()
	if a == nil {
		return 0
	}
	return binary.BigEndian.Uint32(a)
}
