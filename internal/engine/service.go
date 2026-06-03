package engine

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// BruteState tracks where a service is in the brutus credential-testing
// lifecycle, for display in the UI.
type BruteState int

const (
	BruteIdle    BruteState = iota // never attempted
	BruteQueued                    // requested, not yet started
	BruteRunning                   // brutus is hammering it now
	BruteDone                      // finished (see Creds for hits)
	BruteFailed                    // brutus errored / unsupported
)

func (b BruteState) String() string {
	switch b {
	case BruteQueued:
		return "queued"
	case BruteRunning:
		return "BRUTING"
	case BruteDone:
		return "done"
	case BruteFailed:
		return "failed"
	default:
		return "idle"
	}
}

// Cred is a valid credential brutus recovered.
type Cred struct {
	User string `json:"user"`
	Pass string `json:"pass,omitempty"`
	Key  bool   `json:"key,omitempty"` // SSH key auth rather than a password
}

func (c Cred) String() string {
	if c.Key {
		return c.User + ":<key>"
	}
	if c.Pass == "" {
		return c.User + ":<blank>"
	}
	return c.User + ":" + c.Pass
}

// Service is a discovered network service -- the unit the new recon pipeline
// works with. connect/zmap find open TCP ports; nerva finds open UDP ports and
// fingerprints the application/banner on every port; brutus (on demand) tests
// common credentials. Everything about a host:port lives here.
type Service struct {
	Addr          netip.Addr `json:"-"`
	IP            string     `json:"ip"`
	Port          uint16     `json:"port"`
	Proto         string     `json:"proto"`                    // "tcp" | "udp"
	App           string     `json:"app"`                      // nerva fingerprint, e.g. "ssh", "http", "mysql"
	Banner        string     `json:"banner,omitempty"`         // nerva banner
	Version       string     `json:"version,omitempty"`        // nerva version
	ConnectBanner string     `json:"connect_banner,omitempty"` // banner grabbed by connect/zmap

	Brutable    bool       `json:"brutable"`              // brutus supports this protocol
	Brute       BruteState `json:"-"`                     // live state (not persisted as int)
	BruteName   string     `json:"brute,omitempty"`       // persisted state name
	Tried       int        `json:"tried,omitempty"`       // credentials attempted so far
	Creds       []Cred     `json:"creds,omitempty"`       // valid credentials found
	Compromised bool       `json:"compromised,omitempty"` // at least one valid cred

	First time.Time `json:"first"`
}

// Key uniquely identifies a service: proto/ip:port.
func (s *Service) Key() string { return s.Proto + "/" + s.IP + ":" + itoa(s.Port) }

// Target is the ip:port brutus/nerva want on their command line.
func (s *Service) Target() string { return s.IP + ":" + itoa(s.Port) }

// Label is a one-line summary for the services list.
func (s *Service) Label() string {
	app := s.App
	if app == "" {
		app = "?"
	}
	tag := ""
	switch {
	case s.Compromised:
		tag = " ** PWNED **"
	case s.Brute == BruteRunning:
		tag = fmt.Sprintf(" [bruting %d]", s.Tried)
	case s.Brute == BruteDone:
		tag = " [brute: none]"
	case s.Brutable:
		tag = " (brutable)"
	}
	return fmt.Sprintf("%-21s %-4s %-12s%s", s.Target(), strings.ToUpper(s.Proto), app, tag)
}

// brutableApps is the set of nerva application names brutus can test, derived
// from brutus's supported protocol list.
var brutableApps = map[string]string{
	"ssh": "ssh", "ftp": "ftp", "telnet": "telnet", "vnc": "vnc", "rdp": "rdp",
	"snmp": "snmp", "mysql": "mysql", "postgres": "postgres", "postgresql": "postgres",
	"mssql": "mssql", "mongodb": "mongodb", "mongo": "mongodb", "redis": "redis",
	"neo4j": "neo4j", "cassandra": "cassandra", "couchdb": "couchdb",
	"smb": "smb", "ldap": "ldap", "winrm": "winrm", "http": "http", "https": "https",
	"smtp": "smtp", "imap": "imap", "pop3": "pop3", "elasticsearch": "elasticsearch",
	"influxdb": "influxdb",
}

// bruteProtocol maps a fingerprinted app to the brutus --protocol value, or ""
// if brutus does not support it.
func bruteProtocol(app string) string { return brutableApps[strings.ToLower(app)] }
