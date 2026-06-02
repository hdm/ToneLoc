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
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

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
	job     engine.Job
	webAddr string
	useWeb  bool
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "/?" {
		usage()
		return nil
	}
	if args[0] == "-V" || args[0] == "--version" {
		fmt.Printf("ToneLoc/Go %s -- IPv4 war-dialer powered by zmap-go\n", version)
		return nil
	}

	opt, err := parseArgs(args)
	if err != nil {
		return err
	}

	if opt.useWeb {
		addr := opt.webAddr
		if addr == "" {
			addr = ":8080"
		}
		return web.Serve(addr, opt.job)
	}
	return runTerminal(opt.job)
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

func runTerminal(job engine.Job) error {
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
	return app.Run(ctx, keys)
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
  --wait 4s                    listen time per dial (the meter length)
  --rings 6                    rings before Ringout
  --seed N                     reproducible scan order (0 = random)
  --limit N                    stop after N dials
  --web [addr]                 serve the UI in a browser (ghostty.js), default :8080
  -V, --help

KEYS WHILE DIALING:
  ESC quit   SPACE abort   P pause   R redial   S speaker   X +5s wait
  N/C/F/G/V/Y annotate the current number

EXAMPLES:
  toneloc 192.168.1.X /p:22,80,443
  toneloc CORP /M:10.0.X.X /R:1-50 --connect --wait 2s
  toneloc 198.51.100.0/24 --zmap --seed 1337
  toneloc 203.0.113.X --web :9000
`)
}
