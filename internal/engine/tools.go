package engine

import (
	"context"
	"hash/fnv"
	"net/netip"
	"os/exec"
)

// Fingerprint is what nerva tells us about a service.
type Fingerprint struct {
	App     string
	Banner  string
	Version string
}

// Fingerprinter identifies the application on an open port (nerva).
type Fingerprinter interface {
	Name() string
	Fingerprint(ctx context.Context, ip string, port uint16) (Fingerprint, bool)
}

// UDPScanner finds open UDP ports and fingerprints them (nerva -U).
type UDPScanner interface {
	ScanUDP(ctx context.Context, addrs []netip.Addr, ports []uint16, emit func(Service))
}

// BruteForcer tests common credentials against a service (brutus).
type BruteForcer interface {
	Name() string
	Supports(app string) bool
	// Brute tests credentials, calling progress(tried) as it goes, and returns
	// any valid credentials found.
	Brute(ctx context.Context, svc Service, progress func(tried int)) ([]Cred, error)
}

// Toolkit bundles the recon tools. nerva provides both fingerprinting and UDP
// discovery; brutus provides credential testing.
type Toolkit struct {
	Finger Fingerprinter
	UDP    UDPScanner
	Brute  BruteForcer
	Names  string // human description, e.g. "nerva, brutus" or "simulated"
}

// commonUDPPorts are the UDP services nerva knocks on by default.
var commonUDPPorts = []uint16{53, 123, 161, 137, 138, 500, 1900, 5353, 514, 69}

// NewToolkit selects real (exec'd) nerva/brutus when available, otherwise
// simulated equivalents. backend=="sim" forces the simulator for everything so
// the demo and game never touch the network.
func NewToolkit(backend string, seed uint64) Toolkit {
	if backend == "sim" {
		sim := newSimTools(seed)
		return Toolkit{Finger: sim, UDP: sim, Brute: sim, Names: "simulated nerva+brutus"}
	}
	tk := Toolkit{}
	names := ""
	if path, err := exec.LookPath("nerva"); err == nil {
		n := &nervaExec{bin: path}
		tk.Finger, tk.UDP = n, n
		names = "nerva"
	} else {
		sim := newSimTools(seed)
		tk.Finger, tk.UDP = sim, sim
		names = "nerva(sim)"
	}
	if path, err := exec.LookPath("brutus"); err == nil {
		tk.Brute = &brutusExec{bin: path}
		names += ", brutus"
	} else {
		tk.Brute = newSimTools(seed)
		names += ", brutus(sim)"
	}
	tk.Names = names
	return tk
}

// --- shared helpers -------------------------------------------------------

// appForPort guesses the application from a well-known port (used by the
// simulator and as a fallback when nerva returns no name).
func appForPort(proto string, port uint16) string {
	if proto == "udp" {
		switch port {
		case 53:
			return "dns"
		case 123:
			return "ntp"
		case 161:
			return "snmp"
		case 137, 138:
			return "netbios"
		case 500:
			return "ike"
		case 1900:
			return "ssdp"
		case 5353:
			return "mdns"
		case 514:
			return "syslog"
		case 69:
			return "tftp"
		}
		return "udp"
	}
	switch port {
	case 22:
		return "ssh"
	case 21:
		return "ftp"
	case 23:
		return "telnet"
	case 25, 587:
		return "smtp"
	case 80, 8080:
		return "http"
	case 443, 8443:
		return "https"
	case 110:
		return "pop3"
	case 143:
		return "imap"
	case 389, 636:
		return "ldap"
	case 445:
		return "smb"
	case 1433:
		return "mssql"
	case 3306:
		return "mysql"
	case 5432:
		return "postgres"
	case 3389:
		return "rdp"
	case 5900:
		return "vnc"
	case 5985, 5986:
		return "winrm"
	case 6379:
		return "redis"
	case 27017:
		return "mongodb"
	case 9200:
		return "elasticsearch"
	case 8086:
		return "influxdb"
	case 7687:
		return "neo4j"
	case 9042:
		return "cassandra"
	case 5984:
		return "couchdb"
	}
	return "tcp"
}

func hashf(s string, seed uint64) float64 {
	h := fnv.New64a()
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(seed >> (8 * i))
	}
	h.Write(b[:])
	h.Write([]byte(s))
	return float64(h.Sum64()%1_000_000) / 1_000_000.0
}
