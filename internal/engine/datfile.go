package engine

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DatFile is ToneLoc/Go's equivalent of the original .DAT data file: the record
// of every number that has been dialed and what answered. The 1994 format was a
// fixed 10016-byte blob indexed by phone-mask position; that layout is married
// to phone masks, so here we use a small, human-readable text format keyed by
// "addr:port" instead. The behaviour it enables is the same one that made the
// original so useful: stop a scan, come back later, and pick up exactly where
// you left off without re-dialing numbers you already know about.
//
// File format (one record per line, after a header):
//
//	# ToneLoc/Go data file v1
//	mask 192.168.1.X
//	ports 23,80,443
//	updated 2026-06-02T08:24:18Z
//	r 192.168.1.18:23 carrier 1 1
//	r 192.168.1.91:443 tone 2 1
//	r 192.168.1.43:80 nodialtone 0 1
type DatFile struct {
	Path    string
	Mask    string
	Ports   []uint16
	Updated time.Time
	Results map[string]Result // key: addr:port
}

// LoadDat reads a data file. A missing file yields an empty DatFile (a fresh
// scan), not an error -- exactly how ToneLoc treats a brand-new data filename.
func LoadDat(path string) (*DatFile, error) {
	d := &DatFile{Path: path, Results: map[string]Result{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "mask":
			if len(fields) > 1 {
				d.Mask = fields[1]
			}
		case "ports":
			if len(fields) > 1 {
				if ps, err := ParsePorts(fields[1]); err == nil {
					d.Ports = ps
				}
			}
		case "updated":
			if len(fields) > 1 {
				d.Updated, _ = time.Parse(time.RFC3339, fields[1])
			}
		case "r":
			r, ok := parseRecord(fields)
			if ok {
				d.Results[r.Target()] = r
			}
		}
	}
	return d, sc.Err()
}

func parseRecord(fields []string) (Result, bool) {
	// r <addr:port> <resp> <rings> <tries>
	if len(fields) < 3 {
		return Result{}, false
	}
	addrPort := fields[1]
	host, portStr, ok := strings.Cut(addrPort, ":")
	if !ok {
		return Result{}, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return Result{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return Result{}, false
	}
	r := Result{Addr: addr, Port: uint16(port), Response: responseFromName(fields[2]), Tries: 1, Rings: 0}
	if len(fields) > 3 {
		if v, err := strconv.Atoi(fields[3]); err == nil {
			r.Rings = v
		}
	}
	if len(fields) > 4 {
		if v, err := strconv.Atoi(fields[4]); err == nil {
			r.Tries = v
		}
	}
	return r, true
}

// Save atomically writes the data file (write to a temp file, then rename), so
// an interrupted autosave never corrupts an existing scan.
func (d *DatFile) Save() error {
	if d.Path == "" {
		return nil
	}
	d.Updated = time.Now()
	tmp := d.Path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "# ToneLoc/Go data file v1")
	fmt.Fprintf(w, "mask %s\n", d.Mask)
	ports := make([]string, len(d.Ports))
	for i, p := range d.Ports {
		ports[i] = strconv.Itoa(int(p))
	}
	fmt.Fprintf(w, "ports %s\n", strings.Join(ports, ","))
	fmt.Fprintf(w, "updated %s\n", d.Updated.UTC().Format(time.RFC3339))

	keys := make([]string, 0, len(d.Results))
	for k := range d.Results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := d.Results[k]
		fmt.Fprintf(w, "r %s %s %d %d\n", r.Target(), responseName(r.Response), r.Rings, r.Tries)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, d.Path)
}

// Has reports whether a target has already been dialed (and so should be
// skipped on resume).
func (d *DatFile) Has(target string) bool {
	_, ok := d.Results[target]
	return ok
}

// record stores a fresh verdict.
func (d *DatFile) record(r Result) {
	if r.Response == RespAborted || r.Response == RespUndialed {
		return // don't persist non-verdicts
	}
	d.Results[r.Target()] = r
}

func responseName(r Response) string {
	switch r {
	case RespTimeout:
		return "timeout"
	case RespBusy:
		return "busy"
	case RespVoice:
		return "voice"
	case RespNoDialtone:
		return "nodialtone"
	case RespRingout:
		return "ringout"
	case RespTone:
		return "tone"
	case RespCarrier:
		return "carrier"
	case RespExcluded:
		return "excluded"
	case RespBlacklisted:
		return "blacklisted"
	default:
		return "undialed"
	}
}

func responseFromName(s string) Response {
	switch s {
	case "timeout":
		return RespTimeout
	case "busy":
		return RespBusy
	case "voice":
		return RespVoice
	case "nodialtone":
		return RespNoDialtone
	case "ringout":
		return RespRingout
	case "tone":
		return RespTone
	case "carrier":
		return RespCarrier
	case "excluded":
		return RespExcluded
	case "blacklisted":
		return RespBlacklisted
	default:
		return RespUndialed
	}
}

// datPathFor resolves the data file name to an absolute-ish path in the cwd.
func datPathFor(name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	return name
}
