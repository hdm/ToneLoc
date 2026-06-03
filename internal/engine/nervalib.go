package engine

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"

	"github.com/praetorian-inc/nerva/pkg/plugins"
	"github.com/praetorian-inc/nerva/pkg/scan"
)

// nervaLib drives nerva as an in-process library (no exec, single binary). It
// implements both Fingerprinter (TCP application fingerprinting) and UDPScanner
// (nerva's UDP plugins).
type nervaLib struct{}

func (nervaLib) Name() string { return "nerva" }

// Fingerprint asks nerva what is running on a TCP port.
func (nervaLib) Fingerprint(ctx context.Context, ip string, port uint16) (Fingerprint, bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Fingerprint{}, false
	}
	cfg := scan.Config{DefaultTimeout: 2 * time.Second, Workers: 1}
	results, err := scan.ScanTargets(ctx, []plugins.Target{
		{Address: netip.AddrPortFrom(addr, port)},
	}, cfg)
	if err != nil || len(results) == 0 {
		return Fingerprint{}, false
	}
	svc := results[0]
	return Fingerprint{App: svc.Protocol, Version: svc.Version, Banner: rawBanner(svc.Raw)}, svc.Protocol != ""
}

// ScanUDP runs nerva's UDP plugins across addr x port and emits responders.
func (nervaLib) ScanUDP(ctx context.Context, addrs []netip.Addr, ports []uint16, emit func(Service)) {
	if len(ports) == 0 {
		ports = commonUDPPorts
	}
	targets := make([]plugins.Target, 0, len(addrs)*len(ports))
	for _, a := range addrs {
		for _, p := range ports {
			targets = append(targets, plugins.Target{Address: netip.AddrPortFrom(a, p)})
		}
	}
	cfg := scan.Config{UDP: true, DefaultTimeout: 1500 * time.Millisecond, Workers: 50}
	results, err := scan.ScanTargets(ctx, targets, cfg)
	if err != nil {
		return
	}
	for _, svc := range results {
		// Only count UDP services nerva actually identified -- a bare "open|
		// filtered" with no protocol is almost always a false positive (e.g.
		// SNMP appearing on every dead IP), so skip those.
		if svc.Transport != "udp" || svc.Protocol == "" {
			continue
		}
		addr, _ := netip.ParseAddr(svc.IP)
		emit(Service{Addr: addr, IP: svc.IP, Port: uint16(svc.Port), Proto: "udp",
			App: svc.Protocol, Version: svc.Version, Banner: rawBanner(svc.Raw),
			Brutable: bruteProtocol(svc.Protocol) != ""})
	}
}

// rawBanner pulls a "banner" string out of nerva's per-service metadata blob.
func rawBanner(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if b, ok := m["banner"].(string); ok {
		return b
	}
	return ""
}
