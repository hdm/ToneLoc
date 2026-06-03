// Command toneloc is a Go port of the classic 1994 ToneLoc war-dialer by Minor
// Threat & Mucho Maas -- except instead of dialing telephone numbers looking
// for carriers and tones, it "dials" IPv4 addresses and ports looking for open
// services, using the zmap-go scanner under the hood. The screen keeps the
// original's 90s MS-DOS look: Activity Log, Modem, and Statistics windows with
// a progress meter. Run with --web to serve the same UI in a browser via the
// ghostty.js terminal.
package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	tonelocassets "github.com/hdm/toneloc"
	"github.com/hdm/toneloc/internal/engine"
	"github.com/hdm/toneloc/internal/tui"
	"github.com/hdm/toneloc/internal/web"
)

const version = "1.10"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "toneloc: "+err.Error())
		os.Exit(1)
	}
}

type options struct {
	job      engine.Job
	webAddr  string
	useWeb   bool
	gameAddr string
	useGame  bool
	tls      bool
	domain   string
	tlsCert  string
	tlsKey   string
}

func run(args []string) error {
	// -h / --help / -help / help / /? anywhere on the command line prints usage.
	for _, a := range args {
		switch a {
		case "-h", "--help", "-help", "help", "/?", "/h":
			usage()
			return nil
		case "-V", "--version", "-version":
			fmt.Printf("ToneLoc/Go %s -- IPv4 war-dialer powered by zmap-go\n", version)
			return nil
		}
	}

	// The standalone JS game needs no mask, so handle --game before the rest of
	// the command line is parsed. It is served from the binary's embedded assets.
	if addr, ok := gameFlag(args); ok {
		fsys, err := tonelocassets.GameFS()
		if err != nil {
			return err
		}
		tls, domain, cert, key := tlsFlagsFromArgs(args)
		return web.ServeGameFS(buildOptions(addr, ":8090", tls, domain, cert, key), fsys)
	}

	// Resume a previous scan from its session log.
	if id, ok := restoreFlag(args); ok {
		return runRestore(id)
	}

	// No arguments: sweep every local network this machine is on, hitting the
	// most common ports first.
	if len(args) == 0 {
		return runLocalScan()
	}

	opt, err := parseArgs(args)
	if err != nil {
		return err
	}

	if opt.useWeb {
		return web.Serve(buildOptions(opt.webAddr, ":8080", opt.tls, opt.domain, opt.tlsCert, opt.tlsKey), opt.job)
	}
	return runTerminal(opt.job)
}

// buildOptions assembles the web server options, defaulting the listen address
// to defaultPlain only for plain HTTP (TLS/ACME default to :443 in web.listen).
func buildOptions(addr, defaultPlain string, tls bool, domain, cert, key string) web.Options {
	o := web.Options{Addr: addr, TLS: tls || cert != "", Domain: domain, CertFile: cert, KeyFile: key}
	if o.Addr == "" && !o.TLS {
		o.Addr = defaultPlain
	}
	return o
}

// tlsFlagsFromArgs extracts --tls / --domain / --tls-cert / --tls-key, for the
// --game path which is handled before the main flag parser.
func tlsFlagsFromArgs(args []string) (tls bool, domain, cert, key string) {
	val := func(i int, name string) string {
		a := args[i]
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			return a[eq+1:]
		}
		if i+1 < len(args) {
			return args[i+1]
		}
		_ = name
		return ""
	}
	for i, a := range args {
		switch {
		case a == "--tls":
			tls = true
		case a == "--domain" || strings.HasPrefix(a, "--domain="):
			domain = val(i, "domain")
		case a == "--tls-cert" || strings.HasPrefix(a, "--tls-cert="):
			cert = val(i, "tls-cert")
		case a == "--tls-key" || strings.HasPrefix(a, "--tls-key="):
			key = val(i, "tls-key")
		}
	}
	return
}

// parseArgs understands the original ToneLoc slash-options (/M /R /D /X /p with
// ':' '-' or no delimiter) plus a few modern long flags.
func parseArgs(args []string) (options, error) {
	var opt options
	j := &opt.job
	j.Backend = "sim"
	j.WaitDelay = 4 * time.Second
	j.MaxRings = 6
	j.Ports = []uint16{23}

	var maskText, dataFile string
	var rangeText string
	var excludeTexts []string
	portsSet := false

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "/"):
			key, val := splitSlash(a[1:])
			switch strings.ToUpper(key) {
			case "M":
				maskText = val
			case "R":
				rangeText = val
			case "D": // /D:lo-hi -> exclude range, expressed via /R complement: treat as exclude mask not supported; map to range exclusion
				return opt, fmt.Errorf("/D range-exclude is not supported; use /X exclude masks or /R to bound the scan")
			case "X":
				excludeTexts = append(excludeTexts, val)
			case "P":
				ports, err := engine.ParsePorts(val)
				if err != nil {
					return opt, err
				}
				j.Ports = ports
				portsSet = true
			case "C":
				// Config file: accepted for nostalgia, currently a no-op.
			case "S", "E", "H", "T", "K":
				// Time-window / scan-type options accepted but not yet wired.
			default:
				return opt, fmt.Errorf("unknown option /%s", key)
			}
		case strings.HasPrefix(a, "--"):
			name := a[2:]
			needVal := func() (string, error) {
				if eq := strings.IndexByte(name, '='); eq >= 0 {
					return name[eq+1:], nil
				}
				i++
				if i >= len(args) {
					return "", fmt.Errorf("--%s needs a value", name)
				}
				return args[i], nil
			}
			base := name
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				base = name[:eq]
			}
			switch base {
			case "backend":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				if v != "sim" && v != "connect" && v != "zmap" {
					return opt, fmt.Errorf("--backend must be sim, connect or zmap")
				}
				j.Backend = v
			case "sim":
				j.Backend = "sim"
			case "connect":
				j.Backend = "connect"
			case "zmap":
				j.Backend = "zmap"
			case "seed":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				s, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return opt, fmt.Errorf("invalid --seed %q", v)
				}
				j.Seed = s
			case "wait":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				d, err := time.ParseDuration(v)
				if err != nil {
					return opt, fmt.Errorf("invalid --wait %q (try 4s)", v)
				}
				j.WaitDelay = d
			case "rings":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return opt, fmt.Errorf("invalid --rings %q", v)
				}
				j.MaxRings = n
			case "limit":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return opt, fmt.Errorf("invalid --limit %q", v)
				}
				j.Limit = n
			case "ports":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				ports, err := engine.ParsePorts(v)
				if err != nil {
					return opt, err
				}
				j.Ports = ports
				portsSet = true
			case "web":
				opt.useWeb = true
				if eq := strings.IndexByte(name, '='); eq >= 0 {
					opt.webAddr = name[eq+1:]
				} else if i+1 < len(args) && looksLikeAddr(args[i+1]) {
					i++
					opt.webAddr = args[i]
				}
			case "nerva":
				j.NoNerva = false
			case "no-nerva":
				j.NoNerva = true
			case "brutus":
				j.Brutus = true
			case "no-brutus":
				j.NoBrutus = true
			case "tls":
				opt.tls = true
			case "domain":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				opt.domain = v
			case "tls-cert":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				opt.tlsCert = v
			case "tls-key":
				v, err := needVal()
				if err != nil {
					return opt, err
				}
				opt.tlsKey = v
			case "restore":
				v, err := needVal() // handled earlier in run(); accept here too
				if err != nil {
					return opt, err
				}
				_ = v
			default:
				return opt, fmt.Errorf("unknown flag --%s", base)
			}
		default:
			if dataFile == "" {
				dataFile = a
			} else if maskText == "" {
				maskText = a
			}
		}
		i++
	}

	if dataFile == "" {
		return opt, fmt.Errorf("a data file / mask is required (e.g. toneloc 192.168.1.X)")
	}
	if maskText == "" {
		maskText = dataFile
	}

	mask, err := engine.ParseMask(maskText)
	if err != nil {
		return opt, err
	}
	j.Mask = mask
	j.DataFile = datName(dataFile)

	if rangeText != "" {
		r, err := engine.ParseRange(rangeText)
		if err != nil {
			return opt, err
		}
		j.Range = &r
	}
	for _, ex := range excludeTexts {
		em, err := engine.ParseMask(ex)
		if err != nil {
			return opt, fmt.Errorf("exclude mask: %w", err)
		}
		j.Excludes = append(j.Excludes, em)
	}
	_ = portsSet
	return opt, nil
}

// gameFlag scans for "--game" / "--game=addr" / "--game addr" and returns the
// listen address to serve the standalone game on (default :8090).
func gameFlag(args []string) (string, bool) {
	for i, a := range args {
		if a == "--game" {
			if i+1 < len(args) && looksLikeAddr(args[i+1]) {
				return args[i+1], true
			}
			return ":8090", true
		}
		if strings.HasPrefix(a, "--game=") {
			return strings.TrimPrefix(a, "--game="), true
		}
	}
	return "", false
}

// runLocalScan discovers the networks this machine is attached to and scans
// them (zmap if we can open raw sockets, otherwise a TCP connect sweep), common
// ports first, with nerva discovering UDP services and fingerprinting as it
// goes.
func runLocalScan() error {
	masks, err := localNetworks()
	if err != nil || len(masks) == 0 {
		return fmt.Errorf("could not detect any local networks; give a target, e.g. `toneloc 192.168.1.X` (try --help)")
	}
	backend := defaultScanBackend()
	fmt.Fprintf(os.Stderr, "No target given -- sweeping %d local network(s) with %s + nerva, common ports first (%s)...\n",
		len(masks), backend, portsLabel(engine.CommonPorts))
	job := engine.Job{
		DataFile:  "LOCALNET.DAT",
		Masks:     masks,
		Ports:     engine.CommonPorts,
		Backend:   backend,
		WaitDelay: time.Second,
		MaxRings:  4,
	}
	return runTerminal(job)
}

// defaultScanBackend picks zmap when we likely have raw-socket access (root),
// otherwise the privilege-free connect scan.
func defaultScanBackend() string {
	if os.Geteuid() == 0 {
		return "zmap"
	}
	return "connect"
}

// restoreFlag extracts the session id from --restore <id> / --restore=<id>.
func restoreFlag(args []string) (string, bool) {
	for i, a := range args {
		if a == "--restore" && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, "--restore=") {
			return strings.TrimPrefix(a, "--restore="), true
		}
	}
	return "", false
}

// runRestore resumes a scan from its session log.
func runRestore(id string) error {
	sess, err := engine.LoadSession(id)
	if err != nil {
		return fmt.Errorf("restore %s: %w", id, err)
	}
	job, err := engine.JobFromSession(sess)
	if err != nil {
		return err
	}
	job.WaitDelay = time.Second
	job.MaxRings = 4
	fmt.Fprintf(os.Stderr, "Restoring session %s -- %d network(s), %d service(s) recovered...\n",
		id, len(job.Masks), len(sess.Services))
	return runTerminalWith(job, sess)
}

// localNetworks returns the IPv4 networks attached to this host's interfaces,
// skipping loopback/link-local and clamping anything wider than a /16 down to
// the host's /24 so a stray big prefix doesn't launch a million-host sweep.
func localNetworks() ([]*engine.Mask, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var masks []*engine.Mask
	seen := map[string]bool{}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		addr, ok := netip.AddrFromSlice(ip4)
		if !ok {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		if ones < 24 { // clamp huge ranges to the host's own /24
			ones = 24
		}
		pfx := netip.PrefixFrom(addr, ones).Masked()
		if seen[pfx.String()] {
			continue
		}
		seen[pfx.String()] = true
		if m, err := engine.ParseMask(pfx.String()); err == nil {
			masks = append(masks, m)
		}
	}
	return masks, nil
}

func portsLabel(ps []uint16) string {
	parts := make([]string, 0, len(ps))
	for i, p := range ps {
		if i >= 5 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return strings.Join(parts, ",")
}

func runTerminal(job engine.Job) error { return runTerminalWith(job, nil) }

// runTerminalWith launches the TUI, optionally preloading a restored session.
func runTerminalWith(job engine.Job, sess *engine.Session) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal; use --web to run the browser version")
	}
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("entering raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	eng, err := engine.New(ctx, job)
	if err != nil {
		return err
	}
	if sess != nil {
		eng.Preload(sess)
	}
	go eng.Run(ctx)

	keys := make(chan byte, 64)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case keys <- buf[0]:
				default:
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	app := tui.New(eng, os.Stdout)
	// Fill the real terminal instead of a fixed 80x25, and follow resizes. Read
	// the size from whichever of stdout/stdin is a terminal (stdout is what we
	// actually draw to).
	sizeFD := terminalFD()
	if w, h, err := term.GetSize(sizeFD); err == nil && w > 0 && h > 0 {
		app.SetSize(w, h)
	}
	go watchTerminalResize(sizeFD, app)
	return app.Run(ctx, keys)
}

// terminalFD returns the fd to query for the terminal size: stdout if it is a
// terminal, else stdin.
func terminalFD() int {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return int(os.Stdout.Fd())
	}
	return int(os.Stdin.Fd())
}

// --- small parsing helpers ---

func splitSlash(s string) (key, val string) {
	// /M:mask, /M-mask or /Mmask
	if len(s) == 0 {
		return "", ""
	}
	key = string(s[0])
	rest := s[1:]
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimPrefix(rest, "-")
	return key, rest
}

func looksLikeAddr(s string) bool {
	return strings.HasPrefix(s, ":") || strings.Contains(s, ":") && !strings.Contains(s, ".")
}

func datName(s string) string {
	if strings.Contains(s, ".") && !strings.HasSuffix(strings.ToUpper(s), ".DAT") {
		// Looks like an IP/mask -- make a tidy .DAT name from it.
		return strings.NewReplacer(".", "_", "/", "-", "*", "X").Replace(s) + ".DAT"
	}
	if !strings.HasSuffix(strings.ToUpper(s), ".DAT") {
		return s + ".DAT"
	}
	return s
}

func usage() {
	fmt.Print(`ToneLoc/Go ` + version + ` -- war-dialing the IPv4 phone book (powered by zmap-go)

USAGE:
  toneloc <DataFile|Mask> [/M:mask] [/R:lo-hi] [/X:exmask] [/p:ports] [flags]

  With NO arguments, ToneLoc sweeps every local network this machine is on
  (TCP connect scan), hitting the most common ports first (80,443,22,135,...).

MASKS (the IP equivalent of the original 555-1XXX phone masks):
  192.168.1.X        dial 192.168.1.0 .. 192.168.1.255
  10.0.X.X           dial 10.0.0.0    .. 10.0.255.255
  172.16.0.0/24      CIDR form
  X is a wildcard octet; the mask is dialed in random, non-repeating order.

OPTIONS:
  /M:mask            mask override (else the DataFile name is the mask)
  /R:lo-hi           restrict the last varying octet (e.g. /R:10-99)
  /X:exmask          exclude a sub-mask (repeatable)
  /p:23,80,443       ports to dial on each address (default 23)

FLAGS:
  --backend sim|connect|zmap   scan backend (default sim)
  --sim --connect --zmap       shorthands for --backend
  --nerva / --no-nerva         nerva UDP + fingerprinting (default ON)
  --brutus / --no-brutus       brutus credential testing (default OFF; ON in sim)
  --wait 4s                    listen time per dial (the meter length)
  --rings 6                    rings before Ringout
  --seed N                     reproducible scan order (0 = random)
  --limit N                    stop after N dials
  --web [addr]                 serve the UI in a browser (ghostty.js), default :8080
  --game [addr]                serve the standalone JS game (embedded), default :8090
  --tls                        serve HTTPS; issues an ACME cert for --domain
  --domain <name>              domain to issue the ACME certificate for
  --tls-cert <file> --tls-key <file>   use a static cert/key instead of ACME
  --restore <id>               resume a previous scan from its session log
  -V, --help

  (With --tls/--domain, the server listens on :443 and answers ACME HTTP-01
  challenges on :80 -- both must be reachable from the internet.)

RECON PIPELINE:
  Open TCP ports come from connect/zmap; nerva finds open UDP ports and
  fingerprints the application/banner on every service. Select a service and
  press ENTER for full details; press B to launch brutus against it (if the
  protocol is supported) -- it tests common credentials in the background and
  marks the service compromised if it gets in. Everything is written to a
  resumable session log (--restore <id>).

KEYS WHILE DIALING:
  ESC quit   SPACE abort   P pause   R redial   S speaker   X +5s wait
  N/C/F/G/V/Y annotate the current number
  M or TAB   cycle views: Dialer -> ToneMap -> Hall of Fame -> Services

VIEWS:
  ToneMap       a grid of the whole scan coloured by result; hover with the
                mouse (or move with the arrow keys / hjkl) and the cell's
                address + verdict show at the bottom.
  Hall of Fame  every carrier and tone found, selectable (j/k); ENTER opens its
                service detail, B brutes it.
  Services      every TCP/UDP service found; select (j/k), ENTER for the full
                detail (connect banner, nerva fingerprint, brutus creds), B to
                run brutus.

In the web (--web) version the Speaker toggle (S) drives synthesized modem
audio: dial tones, busy signals, and the handshake screech on a carrier.

DATA FILES:
  Results are saved to <DataFile>.DAT and reloaded on the next run, so an
  interrupted scan resumes where it left off (already-dialed targets are
  skipped). Autosaves every 15s and on exit.

EXAMPLES:
  toneloc 192.168.1.X /p:22,80,443
  toneloc CORP /M:10.0.X.X /R:1-50 --connect --wait 2s
  toneloc 198.51.100.0/24 --zmap --seed 1337
  toneloc 203.0.113.X --web :9000
`)
}
