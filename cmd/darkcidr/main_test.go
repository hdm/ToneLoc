package main

import (
	"net"
	"testing"
)

func ipnet(ip string, ones int) *net.IPNet {
	return &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(ones, 32)}
}

func TestLocalSubnetsAllIPv4(t *testing.T) {
	addrs := []net.Addr{
		ipnet("192.168.1.50", 24), // home LAN
		ipnet("192.168.1.51", 24), // same subnet on another iface -> deduped
		ipnet("10.0.5.9", 16),     // a /16 office net, kept whole
		ipnet("172.17.0.1", 16),   // docker bridge
		ipnet("100.64.0.2", 10),   // CGNAT-wide -> clamped to /16
		ipnet("127.0.0.1", 8),     // loopback -> skipped
		ipnet("169.254.10.1", 16), // link-local -> skipped
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}, // IPv6 -> skipped
	}
	got := map[string]bool{}
	for _, m := range localSubnets(addrs) {
		got[m.CIDR().String()] = true
	}

	want := []string{"192.168.1.0/24", "10.0.0.0/16", "172.17.0.0/16", "100.64.0.0/16"}
	for _, w := range want {
		if !got[w] {
			t.Errorf("expected subnet %s in %v", w, keys(got))
		}
	}
	for _, bad := range []string{"127.0.0.0/8", "169.254.0.0/16"} {
		if got[bad] {
			t.Errorf("did not expect %s (loopback/link-local) in %v", bad, keys(got))
		}
	}
	if len(got) != len(want) {
		t.Errorf("expected %d distinct subnets, got %d: %v", len(want), len(got), keys(got))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
