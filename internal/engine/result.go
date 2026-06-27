package engine

import (
	"fmt"
	"net/netip"
)

// Response is DARKCIDR's verdict for a single probed target. The names keep the
// original ToneLoc enum (so .DAT files stay compatible), but they're displayed
// in real network/socket terms:
//
//	Carrier    -> OPEN       TCP SYN/ACK, the port is open
//	Tone       -> BANNER     open AND it volunteered a banner (an app handshake)
//	Busy       -> RESET      TCP RST, the port refused the connection
//	Voice      -> FILTERED   an odd/partial answer (probably a filter/middlebox)
//	NoDialtone -> UNREACH    ICMP unreachable / no route to host
//	Ringout    -> NO-REPLY   probed, no response within the deadline
//	Timeout    -> TIMEOUT    connection attempt timed out
type Response int

const (
	RespUndialed Response = iota
	RespTimeout
	RespBusy
	RespVoice
	RespNoDialtone
	RespRingout
	RespTone
	RespCarrier
	RespExcluded
	RespBlacklisted
	RespAborted
)

// Found reports whether the response is something worth keeping (a hit).
func (r Response) Found() bool { return r == RespTone || r == RespCarrier }

// Priority orders responses for the ToneMap, where many targets (across ports)
// collapse into one cell: the most interesting verdict wins. Carriers and tones
// outrank everything; undialed loses to anything real.
func (r Response) Priority() int {
	switch r {
	case RespCarrier:
		return 9
	case RespTone:
		return 8
	case RespVoice:
		return 6
	case RespBusy:
		return 5
	case RespNoDialtone:
		return 4
	case RespRingout:
		return 3
	case RespTimeout:
		return 2
	case RespExcluded, RespBlacklisted:
		return 1
	default: // Undialed, Aborted
		return 0
	}
}

// Tag is the short text rendered in the activity log, matching the flavour of
// the original messages.
func (r Response) Tag() string {
	switch r {
	case RespTimeout:
		return "Timeout"
	case RespBusy:
		return "Reset"
	case RespVoice:
		return "Filtered"
	case RespNoDialtone:
		return "Unreachable"
	case RespRingout:
		return "No-reply"
	case RespTone:
		return "** BANNER **"
	case RespCarrier:
		return "* OPEN *"
	case RespExcluded:
		return "Excluded"
	case RespBlacklisted:
		return "* Blacklisted *"
	case RespAborted:
		return "Aborted"
	default:
		return "Unscanned"
	}
}

// Result is one completed dial.
type Result struct {
	Addr     netip.Addr
	Port     uint16
	Response Response
	Rings    int    // number of rings before the verdict
	Tries    int    // attempt number (No Dialtone retries)
	Banner   string // first bytes of any banner ("tone"), for the modem window
}

// Target is the addr:port pair the original program would have dialed.
func (r Result) Target() string {
	return fmt.Sprintf("%s:%d", r.Addr, r.Port)
}
