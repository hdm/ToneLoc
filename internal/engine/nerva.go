package engine

import (
	"context"
	"net/netip"
)

// simTools is the simulated nerva+brutus used when the backend is "sim" (and in
// the game) -- so the recon pipeline, the UI, and the game all work with no
// network and no live services. The real tools are the nervaLib/brutusLib
// library adapters in nervalib.go / brutuslib.go.
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
		// Only a minority of hosts are "up" for UDP -- otherwise (e.g.) SNMP
		// would appear to answer on every dead address.
		if hashf("udp-up/"+a.String(), s.seed) >= 0.15 {
			continue
		}
		for _, p := range use {
			key := "udp/" + a.String() + ":" + itoa(p)
			if hashf(key, s.seed) >= 0.25 {
				continue
			}
			app := appForPort("udp", p)
			banner := s.banner(app, key)
			if banner == "" {
				continue // only report UDP services that returned data
			}
			emit(Service{Addr: a, IP: a.String(), Port: p, Proto: "udp",
				App: app, Banner: banner, Brutable: bruteProtocol(app) != ""})
		}
	}
}

// Supports / Brute (the simulated brutus) live in brutus.go.
