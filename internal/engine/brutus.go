package engine

import (
	"context"
	"time"
)

// Common credential lists used by the simulator and (together with brutus's own
// embedded defaults) by the real brutusLib adapter.
var commonUsers = []string{"root", "admin", "administrator", "user", "guest", "oracle", "postgres", "sa", "vagrant"}
var commonPasswords = []string{"", "password", "123456", "admin", "root", "toor", "changeme", "letmein", "P@ssw0rd", "qwerty", "welcome", "default"}

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
