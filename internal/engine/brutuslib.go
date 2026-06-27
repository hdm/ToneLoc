package engine

import (
	"context"
	"time"

	"github.com/praetorian-inc/brutus/pkg/brutus"
	_ "github.com/praetorian-inc/brutus/pkg/builtins" // register all protocol plugins
)

// brutusProtocols is the set of protocols brutus can actually test, captured
// once from its registry.
var brutusProtocols = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range brutus.ListPlugins() {
		m[p] = true
	}
	return m
}()

// brutusLib drives brutus as an in-process library (no exec, single binary).
type brutusLib struct{}

func (brutusLib) Name() string { return "brutus" }

func (brutusLib) Supports(app string) bool {
	p := bruteProtocol(app)
	return p != "" && brutusProtocols[p]
}

// Brute tests the common-credential matrix one credential at a time via the
// protocol plugin, reporting live progress and stopping on the first hit.
func (brutusLib) Brute(ctx context.Context, svc Service, progress func(int)) ([]Cred, error) {
	proto := bruteProtocol(svc.App)
	if proto == "" || !brutusProtocols[proto] {
		return nil, nil
	}
	plugin, err := brutus.GetPlugin(proto)
	if err != nil {
		return nil, err
	}
	pcfg := brutus.PluginConfig{TLSMode: "disable"}
	tried := 0
	for _, u := range commonUsers {
		for _, p := range commonPasswords {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			tried++
			progress(tried)
			r := plugin.Test(ctx, svc.Target(), u, p, 6*time.Second, pcfg)
			if r != nil && r.Success {
				return []Cred{{User: r.Username, Pass: r.Password, Key: len(r.Key) > 0}}, nil
			}
		}
	}
	return nil, nil
}
