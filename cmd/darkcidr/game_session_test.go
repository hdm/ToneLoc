package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hdm/toneloc/internal/engine"
)

// TestSeedFromSession builds a game world from a saved session's services with
// no scanning -- the `--game --restore <id>` path.
func TestSeedFromSession(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	sess := engine.Session{
		ID:    "testsess",
		Masks: []string{"10.0.0.0/24"},
		Ports: []uint16{22, 80},
		Services: []engine.Service{
			{IP: "10.0.0.5", Port: 22, Proto: "tcp", App: "ssh", Banner: "OpenSSH_9.6"},
			{IP: "10.0.0.6", Port: 80, Proto: "tcp", App: "http", ConnectBanner: "nginx"},
			{IP: "10.0.0.7", Port: 161, Proto: "udp"}, // no app -> port name fallback
		},
	}
	blob, _ := json.MarshalIndent(sess, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "darkcidr-testsess.session"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	seed, err := seedFromSession("testsess")
	if err != nil {
		t.Fatalf("seedFromSession: %v", err)
	}
	if seed.Mask != "10.0.0.0/24" {
		t.Errorf("mask = %q, want 10.0.0.0/24", seed.Mask)
	}
	for _, key := range []string{"10.0.0.5:22", "10.0.0.6:80", "10.0.0.7:161"} {
		if _, ok := seed.Hosts[key]; !ok {
			t.Errorf("seed missing host %q (have %d hosts)", key, len(seed.Hosts))
		}
	}

	// An empty session is a friendly error, not a panic.
	empty := engine.Session{ID: "emptysess", Masks: []string{"10.0.0.0/24"}}
	blob, _ = json.MarshalIndent(empty, "", " ")
	os.WriteFile(filepath.Join(dir, "darkcidr-emptysess.session"), blob, 0o644)
	if _, err := seedFromSession("emptysess"); err == nil {
		t.Error("expected an error for a session with no services")
	}
}
