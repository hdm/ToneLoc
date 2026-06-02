package engine

import (
	"fmt"
	"net/netip"
)

// Response is ToneLoc's verdict for a single dialed target, translated from
// the world of modems and phone lines to TCP/IP. The original response codes
// (see the header of TONESHIT.C) map onto scan outcomes like so:
//
//	Carrier   <- TCP SYN/ACK, port open and a service is listening
//	Tone      <- port open AND it volunteered a banner the instant we connected
//	Busy      <- TCP RST, the port actively refused us (the line was busy)
//	Voice     <- an unexpected/odd response that "answered" but wasn't a carrier
//	No Dialtone<- ICMP unreachable, there is no route / nothing answering the wire
//	Ringout   <- we waited MaxRings and gave up
//	Timeout   <- silence; dialed, rang, nothing picked up
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
		return "Busy"
	case RespVoice:
		return "Voice"
	case RespNoDialtone:
		return "No Dialtone"
	case RespRingout:
		return "Ringout"
	case RespTone:
		return "** TONE **"
	case RespCarrier:
		return "* CARRIER *"
	case RespExcluded:
		return "Excluded"
	case RespBlacklisted:
		return "* Blacklisted *"
	case RespAborted:
		return "Aborted"
	default:
		return "Undialed"
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
