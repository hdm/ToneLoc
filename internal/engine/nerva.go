package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

// nervaExec drives the real nerva CLI (github.com/praetorian-inc/nerva). nerva
// assumes the port is reachable and reports the application/transport/metadata;
// with -U it also probes UDP. We invoke it per target with --json.
type nervaExec struct{ bin string }

type nervaJSON struct {
	Host      string         `json:"host"`
	IP        string         `json:"ip"`
	Port      int            `json:"port"`
	Protocol  string         `json:"protocol"`
	Transport string         `json:"transport"`
	Banner    string         `json:"banner"`
	Metadata  map[string]any `json:"metadata"`
}

func (n *nervaExec) Name() string { return "nerva" }

func (n *nervaExec) Fingerprint(ctx context.Context, ip string, port uint16) (Fingerprint, bool) {
	out, err := exec.CommandContext(ctx, n.bin, "-t", ip+":"+itoa(port), "--json", "-w", "2000").Output()
	if err != nil {
		return Fingerprint{}, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r nervaJSON
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		fp := Fingerprint{App: r.Protocol, Banner: r.Banner}
		if v, ok := r.Metadata["version"].(string); ok {
			fp.Version = v
		}
		return fp, fp.App != ""
	}
	return Fingerprint{}, false
}

// ScanUDP runs nerva with -U over host:port candidates and emits the ones that
// answer as UDP services.
func (n *nervaExec) ScanUDP(ctx context.Context, addrs []netip.Addr, ports []uint16, emit func(Service)) {
	// Feed candidates on stdin (host:port per line); nerva reports responders.
	var sb strings.Builder
	for _, a := range addrs {
		for _, p := range ports {
			sb.WriteString(a.String())
			sb.WriteByte(':')
			sb.WriteString(itoa(p))
			sb.WriteByte('\n')
		}
	}
	cmd := exec.CommandContext(ctx, n.bin, "-U", "--json", "-w", "1500")
	cmd.Stdin = strings.NewReader(sb.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if cmd.Start() != nil {
		return
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r nervaJSON
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		addr, _ := netip.ParseAddr(r.IP)
		if !addr.IsValid() {
			addr, _ = netip.ParseAddr(r.Host)
		}
		app := r.Protocol
		if app == "" {
			app = appForPort("udp", uint16(r.Port))
		}
		emit(Service{Addr: addr, IP: r.IP, Port: uint16(r.Port), Proto: "udp",
			App: app, Banner: r.Banner, Brutable: bruteProtocol(app) != ""})
	}
	cmd.Wait()
}

// --- simulator ------------------------------------------------------------

// simTools is the simulated nerva+brutus used when the binaries are absent or
// the backend is "sim" -- so the recon pipeline, the UI, and the game all work
// with no network and no external tools.
type simTools struct{ seed uint64 }

func newSimTools(seed uint64) *simTools {
	if seed == 0 {
		seed = 0x744f4e45
	}
	return &simTools{seed: seed}
}

func (s *simTools) Name() string { return "simulated" }

var simBanners = map[string][]string{
	"ssh":      {"SSH-2.0-OpenSSH_9.6", "SSH-2.0-OpenSSH_7.4", "SSH-1.99-Cisco-1.25"},
	"ftp":      {"220 ProFTPD 1.3.5", "220 vsFTPd 3.0.3", "220 Pure-FTPd"},
	"telnet":   {"login:", "Username:", "Welcome to VMS"},
	"http":     {"Apache/2.4.41", "nginx/1.18.0", "Microsoft-IIS/10.0"},
	"https":    {"nginx/1.25 (TLS)", "Apache/2.4 (TLS)"},
	"mysql":    {"5.7.38-log", "8.0.32", "10.6.12-MariaDB"},
	"postgres": {"PostgreSQL 14.7", "PostgreSQL 12.14"},
	"redis":    {"Redis 7.0.11", "Redis 6.2.6"},
	"smb":      {"Windows Server 2019", "Samba 4.15"},
	"rdp":      {"Windows 10 RDP", "Windows Server 2022 RDP"},
	"snmp":     {"public community", "SNMPv2c"},
	"dns":      {"BIND 9.16", "dnsmasq-2.85"},
	"ntp":      {"ntpd 4.2.8p15"},
	"vnc":      {"RFB 003.008", "TightVNC"},
	"mongodb":  {"MongoDB 5.0.14"},
}

func (s *simTools) banner(app, key string) string {
	bs := simBanners[app]
	if len(bs) == 0 {
		return ""
	}
	return bs[int(hashf(key+"#b", s.seed)*float64(len(bs)))%len(bs)]
}

func (s *simTools) Fingerprint(ctx context.Context, ip string, port uint16) (Fingerprint, bool) {
	app := appForPort("tcp", port)
	return Fingerprint{App: app, Banner: s.banner(app, ip+":"+itoa(port))}, true
}

// ScanUDP synthesizes a believable scatter of open UDP services across the
// address space (deterministic per seed).
func (s *simTools) ScanUDP(ctx context.Context, addrs []netip.Addr, ports []uint16, emit func(Service)) {
	use := ports
	if len(use) == 0 {
		use = commonUDPPorts
	}
	for _, a := range addrs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		for _, p := range use {
			key := "udp/" + a.String() + ":" + itoa(p)
			if hashf(key, s.seed) < 0.04 { // ~4% of UDP probes answer
				app := appForPort("udp", p)
				emit(Service{Addr: a, IP: a.String(), Port: p, Proto: "udp",
					App: app, Banner: s.banner(app, key), Brutable: bruteProtocol(app) != ""})
			}
		}
	}
}

// Supports / Brute (the simulated brutus) live in brutus.go.

var _ = strconv.Itoa
