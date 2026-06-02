package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Common credential lists used by the simulator and handed to the real brutus
// via -U/-P wordlist files.
var commonUsers = []string{"root", "admin", "administrator", "user", "guest", "oracle", "postgres", "sa", "vagrant"}
var commonPasswords = []string{"", "password", "123456", "admin", "root", "toor", "changeme", "letmein", "P@ssw0rd", "qwerty", "welcome", "default"}

// brutusExec drives the real brutus CLI (github.com/praetorian-inc/brutus):
//
//	brutus creds --target IP:PORT --protocol P -U users -P passwords --json
//
// It emits JSONL of valid credentials only.
type brutusExec struct{ bin string }

func (b *brutusExec) Name() string { return "brutus" }

func (b *brutusExec) Supports(app string) bool { return bruteProtocol(app) != "" }

type brutusJSON struct {
	Protocol string `json:"protocol"`
	Target   string `json:"target"`
	Username string `json:"username"`
	Password string `json:"password"`
	Key      bool   `json:"key"`
	Banner   string `json:"banner"`
}

func (b *brutusExec) Brute(ctx context.Context, svc Service, progress func(int)) ([]Cred, error) {
	proto := bruteProtocol(svc.App)
	if proto == "" {
		return nil, nil
	}
	uf, err := writeList(commonUsers)
	if err != nil {
		return nil, err
	}
	defer os.Remove(uf)
	pf, err := writeList(commonPasswords)
	if err != nil {
		return nil, err
	}
	defer os.Remove(pf)

	cmd := exec.CommandContext(ctx, b.bin, "creds",
		"--target", svc.Target(), "--protocol", proto,
		"-U", uf, "-P", pf, "--json", "-t", "8")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	progress(0)
	var creds []Cred
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r brutusJSON
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		creds = append(creds, Cred{User: r.Username, Pass: r.Password, Key: r.Key})
	}
	cmd.Wait()
	return creds, nil
}

func writeList(items []string) (string, error) {
	f, err := os.CreateTemp("", "toneloc-wl-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	f.WriteString(strings.Join(items, "\n") + "\n")
	return f.Name(), nil
}

// --- simulated brutus (methods on simTools) -------------------------------

func (s *simTools) Supports(app string) bool { return bruteProtocol(app) != "" }

// Brute simulates testing the common-credential matrix against a service,
// reporting progress as it goes. A deterministic ~18% of brutable services have
// a weak credential that it eventually finds.
func (s *simTools) Brute(ctx context.Context, svc Service, progress func(int)) ([]Cred, error) {
	if bruteProtocol(svc.App) == "" {
		return nil, nil
	}
	key := svc.Key()
	weak := hashf(key+"#weak", s.seed) < 0.18
	total := len(commonUsers) * len(commonPasswords)
	hitAt := total + 1
	if weak {
		hitAt = 3 + int(hashf(key+"#n", s.seed)*14)
	}
	tried := 0
	for u := 0; u < len(commonUsers); u++ {
		for p := 0; p < len(commonPasswords); p++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(55 * time.Millisecond):
			}
			tried++
			progress(tried)
			if tried >= hitAt {
				ui := int(hashf(key+"#u", s.seed) * float64(len(commonUsers)))
				pi := int(hashf(key+"#p", s.seed) * float64(len(commonPasswords)))
				return []Cred{{User: commonUsers[ui%len(commonUsers)], Pass: commonPasswords[pi%len(commonPasswords)]}}, nil
			}
		}
	}
	return nil, nil
}
