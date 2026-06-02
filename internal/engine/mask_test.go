package engine

import (
	"net/netip"
	"testing"
)

func TestParseMaskWildcard(t *testing.T) {
	m, err := ParseMask("10.0.X.X")
	if err != nil {
		t.Fatal(err)
	}
	if m.Span() != 65536 {
		t.Fatalf("span = %d, want 65536", m.Span())
	}
	if got := m.Addr(0); got != netip.MustParseAddr("10.0.0.0") {
		t.Fatalf("Addr(0) = %v", got)
	}
	if got := m.Addr(65535); got != netip.MustParseAddr("10.0.255.255") {
		t.Fatalf("Addr(max) = %v", got)
	}
	if got := m.CIDR().String(); got != "10.0.0.0/16" {
		t.Fatalf("CIDR = %v, want 10.0.0.0/16", got)
	}
}

func TestParseMaskCIDR(t *testing.T) {
	m, err := ParseMask("198.51.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if m.Span() != 256 {
		t.Fatalf("span = %d, want 256", m.Span())
	}
	idx, ok := m.Index(netip.MustParseAddr("198.51.100.42"))
	if !ok || idx != 42 {
		t.Fatalf("Index = %d, %v; want 42, true", idx, ok)
	}
	if _, ok := m.Index(netip.MustParseAddr("198.51.99.42")); ok {
		t.Fatal("address outside mask reported inside")
	}
}

func TestParseMaskErrors(t *testing.T) {
	for _, bad := range []string{"", "10.0.0.1", "10.0.0", "999.0.X.X", "10.0.0.0/7"} {
		if _, err := ParseMask(bad); err == nil {
			t.Errorf("ParseMask(%q) expected error", bad)
		}
	}
}

// The cyclic dialer must cover the whole space exactly once with no repeats --
// the IP-space version of ToneLoc's "never dial the same number twice".
func TestDialerCoversSpaceOnce(t *testing.T) {
	m, _ := ParseMask("203.0.113.X") // 256 addresses
	d, err := NewDialer(m, []uint16{80, 443}, nil, 12345)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(256 * 2)
	if d.Total() != want {
		t.Fatalf("Total = %d, want %d", d.Total(), want)
	}
	seen := map[string]bool{}
	for {
		addr, port, ok := d.Next()
		if !ok {
			break
		}
		key := addr.String() + ":" + itoa(port)
		if seen[key] {
			t.Fatalf("duplicate target %s", key)
		}
		seen[key] = true
	}
	if uint64(len(seen)) != want {
		t.Fatalf("covered %d targets, want %d", len(seen), want)
	}
}

func TestDialerRangeRestriction(t *testing.T) {
	m, _ := ParseMask("203.0.113.X")
	r := Range{Lo: 10, Hi: 19}
	d, _ := NewDialer(m, []uint16{80}, &r, 99)
	count := 0
	for {
		addr, _, ok := d.Next()
		if !ok {
			break
		}
		idx, _ := m.Index(addr)
		if idx < 10 || idx > 19 {
			t.Fatalf("target %v idx %d outside /R:10-19", addr, idx)
		}
		count++
	}
	if count != 10 {
		t.Fatalf("range covered %d, want 10", count)
	}
}
