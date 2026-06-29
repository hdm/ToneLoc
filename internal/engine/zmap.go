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

// ZmapScanner drives zmap-go IN-PROCESS as a library (no subprocess, no building
// a binary). It is the real stateless SYN scanner: it opens a raw socket and
// blasts a tcp_synscan SYN at every (target, port), classifying the AES-validated
// replies. Crucially it STREAMS -- each open/reset is recorded the instant the
// reply arrives, so the map and host bars fill in real time during the sweep --
// and any target that never answers is marked timeout once the sweep cools down.
//
// A real SYN scan needs raw-socket privileges and a live network; when that is
// unavailable NewZmapScanner returns an error and the caller falls back to the
// simulator. Setup (interface/gateway/MAC) happens up front; the sweep itself is
// driven later by StreamMask, so it never blocks engine startup.
type ZmapScanner struct {
	intf   *net.Interface
	srcIP  net.IP
	srcMAC net.HardwareAddr
	gwMAC  net.HardwareAddr
	v      *validate.Validator
	m      *probe.TCPSyn
	log    func(string)
}

const zmapSrcPortFirst, zmapSrcPortLast = 32768, 61000

// NewZmapScanner resolves the interface, default gateway and validator needed for
// raw SYN crafting. It does NOT sweep -- that is StreamMask's job -- so it returns
// fast and the UI can come up immediately.
func NewZmapScanner(ctx context.Context, job Job, log func(string)) (*ZmapScanner, error) {
	intf, srcIP, srcMAC, err := localInterface()
	if err != nil {
		return nil, fmt.Errorf("zmap: %w", err)
	}
	conn, err := raw.ListenPacket(intf.Name)
	if err != nil {
		return nil, fmt.Errorf("zmap: raw socket on %s (needs root/cap_net_raw): %w", intf.Name, err)
	}
	defer conn.Close() // only needed here to resolve the gateway MAC

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
		log(fmt.Sprintf("Initializing modem ... zmap tcp_synscan (streaming) via %s -> gw %s", intf.Name, gwIP))
	}
	return &ZmapScanner{intf: intf, srcIP: srcIP, srcMAC: srcMAC, gwMAC: gwMAC, v: v, m: m, log: log}, nil
}

func (z *ZmapScanner) Name() string { return "zmap tcp_synscan (lib)" }

func (z *ZmapScanner) Close() error { return nil }

// Dial is unused in streaming mode (the engine drives StreamMask directly); it
// exists only so ZmapScanner satisfies the Probe interface.
func (z *ZmapScanner) Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result {
	return Result{Addr: addr, Port: port, Response: RespTimeout, Tries: 1, Rings: 1}
}

// StreamMask runs a stateless SYN sweep of one mask across the given ports. Every
// classified reply is reported to onResult AS IT ARRIVES (open = Carrier, reset =
// Busy), so the UI fills live; after the sweep cools down, every target that
// never answered is reported as RespTimeout. progress is called periodically with
// (sent, total, answered). It honours ctx cancellation throughout.
func (z *ZmapScanner) StreamMask(ctx context.Context, mask *Mask, ports []uint16, cooldown time.Duration,
	onResult func(Result), progress func(sent, total, answered int)) {

	conn, err := raw.ListenPacket(z.intf.Name)
	if err != nil {
		if z.log != nil {
			z.log("zmap: reopen raw socket: " + err.Error())
		}
		return
	}
	lt := layers.LayerTypeEthernet
	if conn.LinkType() == "loopback" || conn.LinkType() == "null" {
		lt = layers.LayerTypeLoopback
	}

	var mu sync.Mutex
	seen := make(map[string]bool)
	answered := 0
	done := make(chan struct{})

	// Receiver: validate replies and stream carriers (synack) / resets (rst).
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for {
			n, rerr := conn.ReadFrom(buf)
			if rerr != nil {
				return // conn closed after cooldown
			}
			pkt := gopacket.NewPacket(buf[:n], lt, gopacket.NoCopy)
			r, ok := z.m.ValidatePacket(pkt, z.v, z.srcIP, zmapSrcPortFirst, zmapSrcPortLast)
			if !ok {
				continue
			}
			a, ok2 := netip.AddrFromSlice(r.SrcIP.To4())
			if !ok2 {
				continue
			}
			port := uint16(r.SrcPort)
			key := a.String() + ":" + itoa(port)
			mu.Lock()
			dup := seen[key]
			if !dup {
				seen[key] = true
				answered++
			}
			mu.Unlock()
			if dup {
				continue
			}
			resp := RespBusy
			if r.Success {
				resp = RespCarrier
			}
			res := Result{Addr: a, Port: port, Response: resp, Tries: 1, Rings: 1}
			if resp == RespBusy {
				res.Rings = 0
			}
			onResult(res)
		}
	}()

	// Sender: one SYN per (target, port).
	srcU := ipBE(z.srcIP)
	span := mask.Span()
	total := int(span) * len(ports)
	sent := 0
sendLoop:
	for i := uint32(0); i < span; i++ {
		select {
		case <-ctx.Done():
			break sendLoop
		default:
		}
		a := mask.Addr(i).As4()
		dstIP := net.IPv4(a[0], a[1], a[2], a[3])
		dstU := ipBE(dstIP)
		for _, port := range ports {
			t := z.v.GenWords(srcU, dstU, uint32(port), 0)
			frame, _, berr := z.m.BuildProbe(z.srcIP, dstIP, port, z.srcMAC, z.gwMAC, uint16(t[2]), t)
			if berr != nil {
				continue
			}
			_, _ = conn.WriteTo(frame)
			sent++
			if progress != nil && sent%512 == 0 {
				mu.Lock()
				ans := answered
				mu.Unlock()
				progress(sent, total, ans)
			}
		}
	}
	if progress != nil {
		mu.Lock()
		ans := answered
		mu.Unlock()
		progress(sent, total, ans)
	}

	// Cooldown to catch stragglers, then unblock the receiver.
	select {
	case <-ctx.Done():
	case <-time.After(cooldown):
	}
	conn.Close()
	<-done

	if z.log != nil {
		z.log(fmt.Sprintf("zmap sweep of %s: %d SYNs, %d answered", mask.Text(), sent, answered))
	}

	// Everything we never heard from is a timeout -- fill the rest of the map.
	for i := uint32(0); i < span; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		addr := mask.Addr(i)
		base := addr.String() + ":"
		for _, port := range ports {
			if seen[base+itoa(port)] { // receiver is done; safe to read unlocked
				continue
			}
			onResult(Result{Addr: addr, Port: port, Response: RespTimeout, Tries: 1, Rings: 1})
		}
	}
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
