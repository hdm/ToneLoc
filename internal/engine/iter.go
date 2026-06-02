package engine

import (
	"fmt"
	"net/netip"

	"github.com/hdm/zmap-go/pkg/cyclic"
	"github.com/hdm/zmap-go/pkg/iterator"
	"github.com/hdm/zmap-go/pkg/shard"
)

// Dialer walks the (address, port) space the way ToneLoc walked a phone mask:
// in a pseudo-random order that never repeats a number until the whole space
// is exhausted. We get that for free from zmap-go's cyclic multiplicative
// group iterator -- the exact same machinery the real scanner uses to permute
// the IPv4 space -- which is a remarkably good fit for the original program's
// "never dial the same random number twice" promise.
type Dialer struct {
	shard   *shard.Shard
	total   uint64
	mask    *Mask
	ports   []uint16
	rng     *Range // optional /R restriction on the wildcard index
	seed    uint64
	started bool
}

// NewDialer builds the randomized target walker over mask x ports. seed=0 asks
// for a cryptographically random permutation; any other value is reproducible.
func NewDialer(mask *Mask, ports []uint16, rng *Range, seed uint64) (*Dialer, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("at least one port is required")
	}
	var source cyclic.Uint64Source = cyclic.SecureSource{}
	if seed != 0 {
		source = cyclic.NewSeedSource(seed)
	}

	numAddrs := uint64(mask.Span())
	it, err := iterator.New(iterator.Config{
		NumThreads:      1,
		ShardIndex:      0,
		NumShards:       1,
		NumAddrs:        numAddrs,
		Ports:           ports,
		MaxTotalTargets: 0,
		Source:          source,
		LookupIndex: func(index uint64) (netip.Addr, error) {
			if index >= numAddrs {
				return netip.Addr{}, fmt.Errorf("index %d out of range", index)
			}
			return mask.Addr(uint32(index)), nil
		},
	})
	if err != nil {
		return nil, err
	}
	sh, err := it.Shard(0)
	if err != nil {
		return nil, err
	}
	return &Dialer{
		shard: sh,
		total: numAddrs * uint64(len(ports)),
		mask:  mask,
		ports: ports,
		rng:   rng,
		seed:  seed,
	}, nil
}

// nextRaw yields the next (addr, port) from the cyclic shard. The shard's very
// first element is only exposed through CurrentTarget -- NextTarget advances
// past it -- so we emit the current element once before stepping, exactly as
// zmap's own scan loop does. Without this the last number in the phone book
// would never get dialed.
func (d *Dialer) nextRaw() (netip.Addr, uint16, bool) {
	if !d.started {
		d.started = true
		t, err := d.shard.CurrentTarget()
		if err == nil && t.Status == shard.OK {
			return t.IP, t.Port, true
		}
	}
	t, err := d.shard.NextTarget()
	if err != nil || t.Status == shard.Done {
		return netip.Addr{}, 0, false
	}
	return t.IP, t.Port, true
}

// Total is the size of the (address, port) space before /R restriction.
func (d *Dialer) Total() uint64 { return d.total }

// Next returns the next target to dial, or ok=false when the space is
// exhausted. Targets outside the /R range are skipped here so the engine only
// ever sees numbers it is meant to dial.
func (d *Dialer) Next() (addr netip.Addr, port uint16, ok bool) {
	for {
		ip, port, ok := d.nextRaw()
		if !ok {
			return netip.Addr{}, 0, false
		}
		if d.rng != nil {
			idx, in := d.mask.Index(ip)
			if !in || !d.rng.Contains(idx) {
				continue
			}
		}
		return ip, port, true
	}
}
