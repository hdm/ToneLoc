package engine

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ZmapProbe drives the real zmap scanner (the binary built from the sibling
// github.com/hdm/zmap-go module) as ToneLoc's "modem". zmap is a stateless
// mass scanner, so rather than dial one number at a time we let it sweep the
// entire mask once up front -- exactly what it is good at -- and record every
// classified response. The interactive dialer then "redials" each target by
// looking up what zmap already heard. Targets zmap never heard back from are
// reported as timeouts, just like a phone that rings forever.
//
// A real SYN scan needs raw-socket privileges and a live network; when that is
// unavailable NewZmapProbe returns an error and the caller falls back to the
// simulator.
type ZmapProbe struct {
	bin     string
	hits    map[string]Response // "ip:port" -> Carrier/Busy
	scanned int
}

type zmapStatus struct {
	Found   func(string) // log line callback (carrier/busy summaries)
	Verbose bool
}

// NewZmapProbe locates or builds the zmap binary, runs it across the whole
// job mask/port set, and indexes the results for instant per-target lookup.
func NewZmapProbe(ctx context.Context, job Job, log func(string)) (*ZmapProbe, error) {
	bin, err := ensureZmapBinary(ctx, log)
	if err != nil {
		return nil, err
	}
	p := &ZmapProbe{bin: bin, hits: map[string]Response{}}

	cidr := job.Mask.CIDR()
	ports := make([]string, len(job.Ports))
	for i, pt := range job.Ports {
		ports[i] = strconv.Itoa(int(pt))
	}

	args := []string{
		"--probe-module", "tcp_synscan",
		"-p", strings.Join(ports, ","),
		"-O", "csv",
		"-f", "saddr,sport,classification,success",
		"--quiet", "--no-summary",
		cidr.String(),
	}
	if job.Limit > 0 {
		args = append(args, "-n", strconv.FormatUint(job.Limit, 10))
	}
	if job.Seed != 0 {
		args = append(args, "-e", strconv.FormatUint(job.Seed, 10))
	}
	if log != nil {
		log(fmt.Sprintf("Initializing modem ... zmap %s", strings.Join(args, " ")))
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("zmap failed to start (raw sockets need root/cap_net_raw): %w", err)
	}

	sc := bufio.NewScanner(stdout)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first { // CSV header
			first = false
			if strings.HasPrefix(line, "saddr") {
				continue
			}
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			continue
		}
		ip := strings.TrimSpace(fields[0])
		port := strings.TrimSpace(fields[1])
		class := strings.TrimSpace(fields[2])
		key := ip + ":" + port
		switch class {
		case "synack":
			p.hits[key] = RespCarrier
		case "rst":
			p.hits[key] = RespBusy
		}
		p.scanned++
	}
	if err := cmd.Wait(); err != nil {
		// If we never parsed a single response the scan truly failed; surface it.
		if len(p.hits) == 0 {
			return nil, fmt.Errorf("zmap exited without results (need privileges and a live network?): %w", err)
		}
	}
	if log != nil {
		log(fmt.Sprintf("zmap sweep complete: %d responses indexed", len(p.hits)))
	}
	return p, nil
}

func (z *ZmapProbe) Name() string { return "zmap tcp_synscan" }

func (z *ZmapProbe) Close() error { return nil }

func (z *ZmapProbe) Dial(ctx context.Context, addr netip.Addr, port uint16, waitDelay time.Duration, maxRings int) Result {
	res := Result{Addr: addr, Port: port, Tries: 1, Rings: 1}
	// A short, bounded pause so the meter still animates over the replayed
	// sweep -- the data is already in hand.
	select {
	case <-ctx.Done():
		res.Response = RespAborted
		return res
	case <-time.After(min(waitDelay/8, 120*time.Millisecond)):
	}
	if r, ok := z.hits[addr.String()+":"+itoa(port)]; ok {
		res.Response = r
		if r == RespBusy {
			res.Rings = 0
		}
		return res
	}
	res.Response = RespTimeout
	return res
}

// ensureZmapBinary returns a path to a runnable zmap, preferring one already on
// PATH and otherwise building it from the sibling zmap-go module.
func ensureZmapBinary(ctx context.Context, log func(string)) (string, error) {
	if p, err := exec.LookPath("zmap"); err == nil {
		return p, nil
	}
	// Build from the sibling module referenced by our go.mod replace directive.
	candidates := []string{"../zmap-go", "zmap-go"}
	if env := os.Getenv("ZMAP_GO_DIR"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	var srcDir string
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "cmd", "zmap")); err == nil && st.IsDir() {
			srcDir = c
			break
		}
	}
	if srcDir == "" {
		return "", fmt.Errorf("zmap binary not found on PATH and zmap-go source not found (set ZMAP_GO_DIR)")
	}
	out := filepath.Join(os.TempDir(), "toneloc-zmap")
	if log != nil {
		log("Building zmap from " + srcDir + " ...")
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/zmap")
	build.Dir = srcDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building zmap: %v: %s", err, strings.TrimSpace(string(b)))
	}
	return out, nil
}
