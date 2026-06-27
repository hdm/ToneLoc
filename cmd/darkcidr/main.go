// Command darkcidr is a Go port of the classic 1994 ToneLoc war-dialer by Minor
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

	assets "github.com/hdm/toneloc"
	"github.com/hdm/toneloc/internal/engine"
	"github.com/hdm/toneloc/internal/game"
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
			fmt.Printf("DARKCIDR %s -- IPv4 war-dialer powered by zmap-go\n", version)
			return nil
		}
	}

	// The game needs no mask, so handle --game before the rest of the command
	// line is parsed. There is exactly one game implementation -- the native
	// terminal TONESTORM. With no listen address on a real terminal it plays
	// right here in the CLI; given an address (or when stdin isn't a TTY) the
	// same terminal game is served to a browser through ghostty.js.
	//
	// In the CLI the game uses ACTUAL network data by default: it scans the live
	// network (a given target, or the local networks) and builds the world from
	// what it finds. Pass --sim to play the embedded sample world instead. The
	// browser build always uses the sample world (a server can't scan your LAN).
	if addr, explicit, ok := gameFlag(args); ok {
		cliGame := !explicit && term.IsTerminal(int(os.Stdin.Fd()))
		diff := gameDifficulty(args)
		if cliGame && !wantsSim(args) {
			if err := runRealGame(gameRealBackend(args), gameTarget(args), diff); err != nil {
				fmt.Fprintln(os.Stderr, "game: "+err.Error())
				fmt.Fprintln(os.Stderr, "falling back to the sample world (use --sim to skip the live scan)")
			} else {
				return nil
			}
		}
		seed, err := game.LoadSeed(assets.SeedJS())
		if err != nil {
			return err
		}
		if cliGame {
			return runTerminalGame(seed, diff)
		}
		tls, domain, cert, key := tlsFlagsFromArgs(args)
		return web.ServeGame(buildOptions(addr, ":8090", tls, domain, cert, key), seed)
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
			case "resume":
				j.Resume = true
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
		return opt, fmt.Errorf("a data file / mask is required (e.g. darkcidr 192.168.1.X)")
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

// gameFlag scans for "--game" / "--game=addr" / "--game addr". It returns the
// listen address for the web server (default :8090), whether an address was
// given explicitly (which forces the browser version), and whether --game was
// present at all.
func gameFlag(args []string) (addr string, explicit, ok bool) {
	for i, a := range args {
		if a == "--game" {
			if i+1 < len(args) && looksLikeAddr(args[i+1]) {
				return args[i+1], true, true
			}
			return ":8090", false, true
		}
		if strings.HasPrefix(a, "--game=") {
			return strings.TrimPrefix(a, "--game="), true, true
		}
	}
	return "", false, false
}

// wantsSim reports whether the player explicitly asked for the sample world
// (--sim or --backend sim) instead of a live scan.
func wantsSim(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--sim", a == "--backend=sim":
			return true
		case a == "--backend" && i+1 < len(args) && args[i+1] == "sim":
			return true
		}
	}
	return false
}

// gameRealBackend chooses the scan backend for the live game. The default is the
// privilege-free TCP connect sweep -- it's reliable and snappy for the game's
// quick, bounded recon; --zmap opts into the raw-socket SYN scanner (needs root).
func gameRealBackend(args []string) string {
	for i, a := range args {
		switch {
		case a == "--connect":
			return "connect"
		case a == "--zmap":
			return "zmap"
		case a == "--backend" && i+1 < len(args):
			if v := args[i+1]; v == "connect" || v == "zmap" {
				return v
			}
		case strings.HasPrefix(a, "--backend="):
			if v := strings.TrimPrefix(a, "--backend="); v == "connect" || v == "zmap" {
				return v
			}
		}
	}
	return "connect"
}

// gameDifficulty reads --easy / --hard (default normal) for the game.
func gameDifficulty(args []string) game.Difficulty {
	for _, a := range args {
		switch a {
		case "--easy":
			return game.DifficultyByName("easy")
		case "--hard":
			return game.DifficultyByName("hard")
		case "--normal":
			return game.DifficultyByName("normal")
		}
	}
	return game.DifficultyByName("normal")
}

// gameTarget returns a mask/target given on the --game command line (e.g.
// "darkcidr --game --connect 192.168.1.X"), or "" to scan the local networks.
func gameTarget(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "--game" {
			continue
		}
		if _, err := engine.ParseMask(a); err == nil {
			return a
		}
	}
	return ""
}

// runRealGame scans the live network (the given target, or the local networks)
// on the in-game RECON screen, then plays the world it discovers.
func runRealGame(backend, target string, diff game.Difficulty) error {
	var masks []*engine.Mask
	label := target
	if target != "" {
		m, err := engine.ParseMask(target)
		if err != nil {
			return err
		}
		masks = []*engine.Mask{m}
	} else {
		ms, err := localNetworks()
		if err != nil || len(ms) == 0 {
			return fmt.Errorf("could not detect any local networks; give a target, e.g. `darkcidr --game --connect 192.168.1.X`")
		}
		masks = ms
		label = fmt.Sprintf("%d local net(s)", len(masks))
	}

	ports := []uint16{21, 22, 23, 25, 53, 80, 110, 135, 139, 143, 443, 445,
		993, 995, 1433, 3306, 3389, 5432, 5900, 6379, 8080, 8443}
	job := engine.Job{
		DataFile:  "TONESTORM.DAT",
		Masks:     masks,
		Ports:     ports,
		Backend:   backend,
		WaitDelay: 500 * time.Millisecond,
		MaxRings:  1,
		NoBrutus:  true,
		Limit:     8000,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	scanCtx, scanCancel := context.WithTimeout(ctx, 45*time.Second)
	defer scanCancel()

	eng, err := engine.New(scanCtx, job)
	if err != nil {
		return err
	}
	go eng.Run(scanCtx)

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("entering raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	keys := readKeys()
	sizeFD := terminalFD()
	w, h, _ := term.GetSize(sizeFD)
	resize := func() (int, int) {
		w, h, err := term.GetSize(sizeFD)
		if err != nil {
			return 0, 0
		}
		return w, h
	}
	src := &engineRecon{eng: eng, cancel: scanCancel}
	return game.RunReal(ctx, os.Stdout, keys, src, label, uint32(time.Now().UnixNano()),
		game.RunOptions{Width: w, Height: h, Resize: resize, Diff: diff})
}

// engineRecon adapts the scan engine to game.ReconSource.
type engineRecon struct {
	eng    *engine.Engine
	cancel context.CancelFunc
}

func (e *engineRecon) Dialed() int { return e.eng.State().Snapshot().Stats.Dialed }
func (e *engineRecon) Total() int  { return int(e.eng.State().Snapshot().Stats.Max) }
func (e *engineRecon) Done() bool  { return e.eng.State().Snapshot().Done }
func (e *engineRecon) Stop()       { e.cancel() }
func (e *engineRecon) Hosts(max int) []game.HostScan {
	rows := e.eng.State().HostScansSnapshot(max)
	out := make([]game.HostScan, len(rows))
	for i, r := range rows {
		ports := make([]uint8, len(r.Ports))
		for j, resp := range r.Ports {
			ports[j] = gamePortState(engine.Response(resp))
		}
		out[i] = game.HostScan{IP: r.IP, Ports: ports, Open: r.Open, Banner: r.Banner}
	}
	return out
}

// gamePortState maps a scan Response code to the game's recon pip state.
func gamePortState(r engine.Response) uint8 {
	switch {
	case r == engine.RespTone:
		return game.PortBanner
	case r.Found():
		return game.PortOpen
	case r == engine.RespUndialed:
		return game.PortPending
	default:
		return game.PortClosed
	}
}
func (e *engineRecon) Found() []game.SeedService {
	var svcs []game.SeedService
	for _, s := range e.eng.State().ServicesSnapshot() {
		banner := s.Banner
		if banner == "" {
			banner = s.ConnectBanner
		}
		svc := s.App
		if svc == "" {
			svc = appForGamePort(s.Port)
		}
		svcs = append(svcs, game.SeedService{IP: s.IP, Port: int(s.Port), Svc: svc, Banner: banner})
	}
	return svcs
}

// appForGamePort names a service by port when nerva hasn't fingerprinted it, so
// the game can match login/web vectors to the right tools.
func appForGamePort(port uint16) string {
	switch port {
	case 21:
		return "ftp"
	case 22:
		return "ssh"
	case 23:
		return "telnet"
	case 25:
		return "smtp"
	case 80, 8080:
		return "http"
	case 110:
		return "pop3"
	case 143:
		return "imap"
	case 443, 8443:
		return "https"
	case 992:
		return "telnets"
	case 3306:
		return "mysql"
	case 3389:
		return "rdp"
	case 5432:
		return "postgres"
	case 5900:
		return "vnc"
	case 6379:
		return "redis"
	}
	return ""
}

// readKeys streams raw stdin bytes onto a channel (closed on EOF/error), for the
// game's input loop.
func readKeys() <-chan byte {
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
	return keys
}

// runLocalScan discovers the networks this machine is attached to and scans
// them (zmap if we can open raw sockets, otherwise a TCP connect sweep), common
// ports first, with nerva discovering UDP services and fingerprinting as it
// goes.
func runLocalScan() error {
	masks, err := localNetworks()
	if err != nil || len(masks) == 0 {
		return fmt.Errorf("could not detect any local networks; give a target, e.g. `darkcidr 192.168.1.X` (try --help)")
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

// localNetworks returns every IPv4 subnet this host is attached to, used as the
// default targets when no mask is given.
func localNetworks() ([]*engine.Mask, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	return localSubnets(addrs), nil
}

// localSubnets converts interface addresses into the distinct IPv4 subnets to
// scan: every non-loopback, non-link-local IPv4 network the host is on, at its
// real netmask -- but never wider than a /16, so a stray huge prefix (a /8 route,
// say) can't launch a multi-million-host sweep. Pure, so it can be unit-tested.
func localSubnets(addrs []net.Addr) []*engine.Mask {
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
		if ones < 16 { // ceiling: don't scan anything wider than a /16
			ones = 16
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
	return masks
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

// runTerminalGame plays TONESTORM in the terminal, building its world from the
// embedded seed (the same dataset the browser version uses).
func runTerminalGame(seed *game.Seed, diff game.Difficulty) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("entering raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	keys := readKeys()

	// Fill the whole terminal (not a fixed 80x25) and follow resizes.
	sizeFD := terminalFD()
	w, h, _ := term.GetSize(sizeFD)
	resize := func() (int, int) {
		w, h, err := term.GetSize(sizeFD)
		if err != nil {
			return 0, 0
		}
		return w, h
	}
	return game.Run(ctx, os.Stdout, keys, seed, uint32(time.Now().UnixNano()),
		game.RunOptions{Width: w, Height: h, Resize: resize, Diff: diff})
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
	fmt.Print(`DARKCIDR ` + version + ` -- war-dialing the IPv4 phone book (powered by zmap-go)

USAGE:
  darkcidr <DataFile|Mask> [/M:mask] [/R:lo-hi] [/X:exmask] [/p:ports] [flags]

  With NO arguments, DARKCIDR sweeps every local network this machine is on
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
  --game [addr]                play TONESTORM, filling the terminal on a TTY (or
                               serve the browser version if given an address).
                               By default it builds the world from a LIVE TCP
                               connect scan of your network (a target, or the
                               local nets), e.g. darkcidr --game 192.168.1.X
                               (--zmap = raw SYN scan, needs root). Pass --sim to
                               play the embedded sample world.
  --easy / --hard              game difficulty (trace speed, traps, starting
                               exploits); default normal
  --tls                        serve HTTPS; issues an ACME cert for --domain
  --domain <name>              domain to issue the ACME certificate for
  --tls-cert <file> --tls-key <file>   use a static cert/key instead of ACME
  --resume                     resume from the .DAT, skipping already-dialed
                               targets (default OFF: re-running re-scans)
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
  Results are saved to <DataFile>.DAT (autosaves every 15s and on exit). By
  default a re-run RE-SCANS the range (the .DAT is overwritten); pass --resume
  to skip already-dialed targets instead.

EXAMPLES:
  darkcidr 192.168.1.X /p:22,80,443
  darkcidr CORP /M:10.0.X.X /R:1-50 --connect --wait 2s
  darkcidr 198.51.100.0/24 --zmap --seed 1337
  darkcidr 203.0.113.X --web :9000
`)
}
