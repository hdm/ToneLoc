package engine

import "testing"

func TestBrutableDetection(t *testing.T) {
	cases := []struct {
		app, proto string
		port       uint16
		want       bool
	}{
		{"ssh", "tcp", 22, true},
		{"", "tcp", 22, true},                    // port fallback even with no fingerprint
		{"SSH-2.0-OpenSSH_9.6", "tcp", 22, true}, // nerva-decorated name
		{"http/1.1", "tcp", 80, true},            // prefix match
		{"", "tcp", 7, false},                    // echo: not brutable
		{"snmp", "udp", 161, true},
		{"tftp", "udp", 69, false},        // must NOT match "ftp"
		{"postgresql", "tcp", 5432, true}, // prefix match
	}
	for _, c := range cases {
		if got := brutableFor(c.app, c.proto, c.port); got != c.want {
			t.Errorf("brutableFor(%q,%q,%d) = %v, want %v", c.app, c.proto, c.port, got, c.want)
		}
	}
}
