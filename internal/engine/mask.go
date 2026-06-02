package engine

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Mask describes the IPv4 address space ToneLoc will "dial". In the original
// ToneLoc a mask looked like 555-1XXX, where each X was a wildcard digit. Here
// each octet is either a fixed value (0-255) or a wildcard 'X' that ToneLoc
// fills in, exactly the same idea translated to IP space:
//
//	192.168.1.X    -> 192.168.1.0 .. 192.168.1.255
//	10.0.X.X       -> 10.0.0.0    .. 10.0.255.255
//	172.16.0.0/24  -> CIDR form, equivalent to 172.16.0.X
//
// A /R:start-end range further constrains the LAST wildcard octet, and /X
// exclude masks remove sub-ranges, mirroring the classic command line.
type Mask struct {
	text    string
	octets  [4]octet // per-octet constraint
	base    uint32   // value with all wildcards = 0
	span    uint32   // number of distinct addresses (1 << wildcardBits)
	wildPos []int    // octet indexes that are wildcards, most-significant first
}

type octet struct {
	wild bool
	val  byte
}

// ParseMask parses an IP mask in either CIDR or X-wildcard notation.
func ParseMask(s string) (*Mask, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty mask")
	}
	if strings.Contains(s, "/") {
		return parseCIDR(s)
	}
	return parseWildcard(s)
}

func parseWildcard(s string) (*Mask, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return nil, fmt.Errorf("mask %q must have 4 octets (use X for wildcards, e.g. 10.0.X.X)", s)
	}
	m := &Mask{text: s}
	bits := 0
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("mask %q has an empty octet", s)
		}
		if strings.EqualFold(p, "X") || p == "*" {
			m.octets[i] = octet{wild: true}
			m.wildPos = append(m.wildPos, i)
			bits += 8
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return nil, fmt.Errorf("mask %q has invalid octet %q (0-255 or X)", s, p)
		}
		m.octets[i] = octet{val: byte(v)}
		m.base |= uint32(v) << (8 * (3 - i))
	}
	if bits == 0 {
		return nil, fmt.Errorf("mask %q has no wildcards; nothing to scan", s)
	}
	if bits > 24 {
		return nil, fmt.Errorf("mask %q is too large (%d wildcard bits); keep it under a /8", s, bits)
	}
	m.span = uint32(1) << bits
	return m, nil
}

func parseCIDR(s string) (*Mask, error) {
	pfx, err := netip.ParsePrefix(s)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", s, err)
	}
	if !pfx.Addr().Is4() {
		return nil, fmt.Errorf("only IPv4 is supported (got %q)", s)
	}
	pfx = pfx.Masked()
	hostBits := 32 - pfx.Bits()
	if hostBits == 0 {
		return nil, fmt.Errorf("CIDR %q covers a single host; nothing to scan", s)
	}
	if hostBits > 24 {
		return nil, fmt.Errorf("CIDR %q is too large; keep it /8 or smaller", s)
	}
	a := pfx.Addr().As4()
	m := &Mask{
		text: s,
		base: uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]),
		span: uint32(1) << hostBits,
	}
	// Mark whole octets wild where they fall entirely inside the host portion.
	for i := 0; i < 4; i++ {
		shift := 8 * (3 - i)
		if shift+8 <= hostBits {
			m.octets[i] = octet{wild: true}
			m.wildPos = append(m.wildPos, i)
		} else {
			m.octets[i] = octet{val: a[i]}
		}
	}
	return m, nil
}

// Text returns the original mask string.
func (m *Mask) Text() string { return m.text }

// Span is the number of distinct addresses the mask covers.
func (m *Mask) Span() uint32 { return m.span }

// Base is the lowest address in the mask (all wildcards zeroed).
func (m *Mask) Base() uint32 { return m.base }

// CIDR renders the mask as an IPv4 prefix. Every ToneLoc mask is CIDR-aligned
// (wildcards are whole octets and CIDR input is masked), so the base address
// plus the wildcard-bit count is exactly the prefix the real zmap scanner
// wants on its command line.
func (m *Mask) CIDR() netip.Prefix {
	bits := 0
	for s := m.span; s > 1; s >>= 1 {
		bits++
	}
	a := netip.AddrFrom4([4]byte{byte(m.base >> 24), byte(m.base >> 16), byte(m.base >> 8), byte(m.base)})
	return netip.PrefixFrom(a, 32-bits)
}

// Addr returns the i'th address in the mask, 0 <= i < Span().
func (m *Mask) Addr(i uint32) netip.Addr {
	v := m.base | (i & (m.span - 1))
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// Index returns the mask-relative index of addr, or false if addr is outside
// the mask. The index is the value of the wildcard octets packed together,
// i.e. addr - base interpreted within the wildcard span.
func (m *Mask) Index(addr netip.Addr) (uint32, bool) {
	if !addr.Is4() {
		return 0, false
	}
	a := addr.As4()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	// Fixed octets must match base exactly.
	fixedMask := ^(m.span - 1)
	if v&fixedMask != m.base&fixedMask {
		return 0, false
	}
	return v & (m.span - 1), true
}

// Range restricts the dialed indexes to [lo, hi] inclusive, where lo/hi are
// values of the least-significant varying portion. Mirrors ToneLoc's /R.
type Range struct {
	Lo, Hi uint32
}

// ParseRange parses "lo-hi" (decimal) for the /R option.
func ParseRange(s string) (Range, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		// A single value is a one-element range.
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
		if err != nil {
			return Range{}, fmt.Errorf("invalid range %q (want lo-hi)", s)
		}
		return Range{uint32(v), uint32(v)}, nil
	}
	lo, err := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
	if err != nil {
		return Range{}, fmt.Errorf("invalid range start %q", a)
	}
	hi, err := strconv.ParseUint(strings.TrimSpace(b), 10, 32)
	if err != nil {
		return Range{}, fmt.Errorf("invalid range end %q", b)
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	return Range{uint32(lo), uint32(hi)}, nil
}

// Contains reports whether index i is inside the range.
func (r Range) Contains(i uint32) bool { return i >= r.Lo && i <= r.Hi }

// ParsePorts parses a comma/space separated list of ports, e.g. "23,80,443".
func ParsePorts(s string) ([]uint16, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) == 0 {
		return nil, fmt.Errorf("no ports given")
	}
	var ports []uint16
	seen := map[uint16]bool{}
	for _, f := range fields {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || v < 1 || v > 65535 {
			return nil, fmt.Errorf("invalid port %q (1-65535)", f)
		}
		if !seen[uint16(v)] {
			seen[uint16(v)] = true
			ports = append(ports, uint16(v))
		}
	}
	return ports, nil
}
