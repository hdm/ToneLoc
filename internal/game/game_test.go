package game

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// loadTestSeed reads the same seed.js the binary embeds.
func loadTestSeed(t *testing.T) *Seed {
	t.Helper()
	raw, err := os.ReadFile("../../game/seed.js")
	if err != nil {
		t.Fatalf("reading seed.js: %v", err)
	}
	seed, err := LoadSeed(raw)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	return seed
}

func TestLoadSeedAndBuildWorld(t *testing.T) {
	seed := loadTestSeed(t)
	if len(seed.Hosts) == 0 {
		t.Fatal("seed has no hosts")
	}
	w := buildWorld(seed, 1234, Difficulty{})
	if w.home == nil || w.uplink == nil {
		t.Fatal("world missing home/uplink")
	}
	if w.home.kind != "home" || w.uplink.kind != "uplink" {
		t.Fatal("home/uplink mis-kinded")
	}
	if w.obj == nil || w.obj.host != "MAINFRAME" || !w.obj.objective {
		t.Fatalf("objective not set up: %+v", w.obj)
	}
	// The uplink must link to at least one router so there's somewhere to start.
	if len(w.uplink.links) < 2 {
		t.Fatalf("uplink has too few links: %d", len(w.uplink.links))
	}
}

func TestDeterministicWorld(t *testing.T) {
	seed := loadTestSeed(t)
	a := buildWorld(seed, 42, Difficulty{})
	b := buildWorld(seed, 42, Difficulty{})
	if len(a.nodes) != len(b.nodes) {
		t.Fatalf("node counts differ: %d vs %d", len(a.nodes), len(b.nodes))
	}
	if a.obj.ip != b.obj.ip {
		t.Fatalf("objective differs: %s vs %s", a.obj.ip, b.obj.ip)
	}
	for i := range a.nodes {
		if a.nodes[i].ip != b.nodes[i].ip || a.nodes[i].kind != b.nodes[i].kind {
			t.Fatalf("node %d differs", i)
		}
	}
}

func TestTitleAndPlay(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 7, 0, 0, Difficulty{})
	if g.screen != scrTitle {
		t.Fatal("should start on the title screen")
	}
	g.draw()
	frame := g.Frame()
	for _, want := range []string{"war-dial the whole internet", "BEST HAUL", "TRACE"} {
		if !strings.Contains(frame, want) {
			t.Errorf("title frame missing %q", want)
		}
	}

	// ENTER jacks in.
	if quit := g.feed('\r'); quit {
		t.Fatal("ENTER should not quit")
	}
	if g.screen != scrMap {
		t.Fatal("ENTER should move to the map")
	}
	g.draw()
	if f := g.Frame(); !strings.Contains(f, "Network") || !strings.Contains(f, "Status") {
		t.Errorf("map frame missing panels:\n%s", f)
	}

	// '?' opens help, ESC returns to the map.
	g.feed('?')
	if g.screen != scrHelp {
		t.Fatal("? should open help")
	}
	g.feed(0x1b)
	if g.escState != 1 {
		t.Fatal("ESC should arm the escape state")
	}
	// A lone ESC is resolved by onKey (as the run loop does after the timeout).
	g.escState = 0
	g.onKey(keyMsg{kind: kEsc})
	if g.screen != scrMap {
		t.Fatal("ESC should leave help back to the map")
	}

	// Arrow keys move the selection without quitting.
	if quit := g.feed(0x1b); quit {
		t.Fatal("ESC byte should not quit immediately")
	}
	g.feed('[')
	g.feed('B') // down arrow
	if g.escState != 0 {
		t.Fatal("arrow sequence should have completed")
	}

	// 'q' quits.
	if quit := g.feed('q'); !quit {
		t.Fatal("q should quit")
	}
}

// safeWriter is a goroutine-safe buffer so the test can read while Run writes.
type safeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *safeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *safeWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestRunEntersAltScreenAndQuits(t *testing.T) {
	seed := loadTestSeed(t)
	keys := make(chan byte, 8)
	out := &safeWriter{}

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), out, keys, seed, 7, RunOptions{}) }()

	// Quit from the title screen.
	keys <- 'q'

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after 'q'")
	}

	s := out.String()
	if !strings.Contains(s, "\x1b[?1049h") {
		t.Error("Run should enter the alternate screen")
	}
	if !strings.Contains(s, "\x1b[?1049l") {
		t.Error("Run should leave the alternate screen on exit")
	}
}

func TestResponsiveFullWidth(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 7, 140, 45, Difficulty{})
	g.screen = scrMap
	g.draw()
	lines := strings.Split(strings.TrimRight(g.Frame(), "\n"), "\n")
	if len(lines) != 45 {
		t.Fatalf("expected 45 rows, got %d", len(lines))
	}
	for i, ln := range lines {
		if w := len([]rune(ln)); w != 140 {
			t.Fatalf("row %d width = %d, want 140", i, w)
		}
	}
	// The Network box must stretch: its right border sits well past column 50.
	// Find a '╗'/'╔' style corner near the far right of the panel area.
	if !strings.Contains(g.Frame(), "Network") || !strings.Contains(g.Frame(), "Status") {
		t.Error("map should still have Network + Status panels at large sizes")
	}

	// Shrinking below the minimum clamps to 80x25.
	if changed := g.setSize(10, 10); !changed {
		t.Fatal("setSize should report a change shrinking to the minimum")
	}
	if g.w != 80 || g.h != 25 {
		t.Fatalf("expected clamp to 80x25, got %dx%d", g.w, g.h)
	}
}

// fakeRecon is a deterministic ReconSource for testing RunReal.
type fakeRecon struct {
	mu      sync.Mutex
	svcs    []SeedService
	dialed  int
	done    bool
	stopped bool
}

func (f *fakeRecon) Dialed() int { f.mu.Lock(); defer f.mu.Unlock(); return f.dialed }
func (f *fakeRecon) Total() int  { return 5632 }
func (f *fakeRecon) Done() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.done }
func (f *fakeRecon) Stop()       { f.mu.Lock(); defer f.mu.Unlock(); f.stopped = true }
func (f *fakeRecon) Found() []SeedService {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SeedService(nil), f.svcs...)
}
func (f *fakeRecon) Hosts(max int) []HostScan {
	f.mu.Lock()
	defer f.mu.Unlock()
	byIP := map[string]*HostScan{}
	var order []string
	for _, s := range f.svcs {
		hs := byIP[s.IP]
		if hs == nil {
			hs = &HostScan{IP: s.IP, Ports: []uint8{PortBanner, PortClosed, PortOpen}}
			byIP[s.IP] = hs
			order = append(order, s.IP)
		}
		hs.Open++
	}
	var out []HostScan
	for _, ip := range order {
		out = append(out, *byIP[ip])
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func TestRunRealReconToPlay(t *testing.T) {
	src := &fakeRecon{
		svcs: []SeedService{
			{IP: "10.1.1.5", Port: 22, Svc: "ssh", Banner: "OpenSSH"},
			{IP: "10.1.1.5", Port: 80, Svc: "http", Banner: "nginx"},
			{IP: "10.1.1.9", Port: 23, Svc: "telnet"},
			{IP: "10.1.1.40", Port: 443, Svc: "https"},
			{IP: "10.1.2.7", Port: 21, Svc: "ftp"},
		},
		dialed: 1234,
		done:   true,
	}
	keys := make(chan byte, 8)
	out := &safeWriter{}
	done := make(chan error, 1)
	go func() {
		done <- RunReal(context.Background(), out, keys, src, "10.1.0.0/16", 7, RunOptions{})
	}()

	// The recon screen should auto-proceed (src is Done with services) into the
	// map; once it's there, quit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "Network") {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "Network") {
		t.Fatalf("RunReal never reached the map screen:\n%s", out.String())
	}
	keys <- 'q'
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunReal error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunReal did not exit after q")
	}
	if !src.stopped {
		t.Error("RunReal should Stop() the recon source")
	}
}

func TestObjectiveNeverHoneypot(t *testing.T) {
	// A real-scan seed has no honeypots, so the builder synthesizes some -- but
	// the MAINFRAME must never be an (unbreachable) trap.
	var svcs []SeedService
	for i := 0; i < 40; i++ {
		ip := "10.9." + strconv.Itoa(i/8) + "." + strconv.Itoa(10+i%8)
		svcs = append(svcs, SeedService{IP: ip, Port: 22, Svc: "ssh"})
	}
	seed := NewSeed("10.9.0.0/16", svcs)
	sawHoneypot := false
	for s := uint32(1); s <= 30; s++ {
		w := buildWorld(seed, s, Difficulty{})
		if w.obj == nil {
			t.Fatalf("seed %d produced no objective", s)
		}
		if w.obj.honeypot {
			t.Fatalf("seed %d: objective is a honeypot (unwinnable)", s)
		}
		for _, n := range w.nodes {
			if n.honeypot {
				sawHoneypot = true
			}
		}
	}
	if !sawHoneypot {
		t.Error("expected synthesized honeypots somewhere across seeds")
	}
}

// ownable reports whether a node can ever be compromised: routers and
// non-honeypot hosts always can (tools/password); a honeypot only if it has a
// login service to spray (tools can't breach a honeypot).
func ownable(n *node) bool {
	if n.kind == "home" || n.kind == "uplink" || n.kind == "router" {
		return true
	}
	return !n.honeypot || n.hasLogin
}

// reachable does the same BFS the player would: own a node, reveal its links,
// own the ownable ones, repeat -- and reports whether the objective is winnable.
func reachable(w *world) bool {
	owned := map[int]bool{w.home.id: true, w.uplink.id: true}
	queue := []*node{w.home, w.uplink}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, idx := range n.links {
			m := w.nodes[idx]
			if owned[m.id] || !ownable(m) {
				continue
			}
			owned[m.id] = true
			queue = append(queue, m)
		}
	}
	return w.obj != nil && owned[w.obj.id]
}

func TestWorldAlwaysWinnable(t *testing.T) {
	sample := loadTestSeed(t)

	var realSvcs []SeedService
	for i := 0; i < 60; i++ {
		ip := "10.7." + strconv.Itoa(i/6) + "." + strconv.Itoa(10+i%6)
		svc := "http"
		if i%3 == 0 {
			svc = "ssh" // some login services so the world isn't all web
		}
		realSvcs = append(realSvcs, SeedService{IP: ip, Port: 80, Svc: svc})
	}
	real := NewSeed("10.7.0.0/16", realSvcs)

	for _, tc := range []struct {
		name string
		seed *Seed
	}{{"sample", sample}, {"real", real}} {
		for s := uint32(1); s <= 50; s++ {
			w := buildWorld(tc.seed, s, Difficulty{})
			if !reachable(w) {
				t.Fatalf("%s seed %d: objective is NOT reachable -- world unwinnable", tc.name, s)
			}
		}
	}
}

func TestBotCanWin(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 7, 0, 0, Difficulty{})
	g.screen = scrMap
	g.connected = true

	// Compromise everything reachable (owned() reveals neighbours) and loot it.
	for step := 0; step < 500; step++ {
		acted := false
		for _, e := range g.curList() {
			n := e.n
			if n.state == "discovered" && ownable(n) {
				g.owned(n)
				acted = true
			}
			if n.state == "owned" && n.loot > 0 && !n.looted {
				g.loot(n)
			}
		}
		if !acted {
			break
		}
	}
	if !g.objectiveTaken {
		t.Fatal("a thorough player should be able to take the objective")
	}

	// Going dark with the objective in hand wins, with a positive score.
	g.connected = true
	g.toggleConnect()
	if g.screen != scrWin {
		t.Fatalf("going dark after the objective should WIN, got screen %d", g.screen)
	}
	if g.score <= 0 {
		t.Fatalf("a win should score > 0, got %d", g.score)
	}
}

func TestEventsAreSafeAndKeepWinnable(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 11, 0, 0, Difficulty{})
	g.screen = scrMap
	g.connected = true

	gotMsg := 0
	for i := 0; i < 300; i++ {
		g.fireEvent()
		if g.msg != "" {
			gotMsg++
		}
		// Invariants: nothing goes out of bounds.
		if g.trace < 0 || g.trace > 100 {
			t.Fatalf("event drove trace out of range: %v", g.trace)
		}
		for _, n := range g.world.nodes {
			if n.shieldCur < 0 {
				t.Fatalf("event drove %s shield negative", n.host)
			}
		}
		for id, c := range g.inv {
			if c < 0 {
				t.Fatalf("event drove inv[%s] negative", id)
			}
		}
	}
	if gotMsg == 0 {
		t.Fatal("events should surface messages")
	}
	// Events must never strand the objective.
	if !reachable(g.world) {
		t.Fatal("events made the world unwinnable")
	}
}

func TestBreachShowsToolIntel(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 5, 0, 0, Difficulty{})
	g.screen = scrMap
	g.connected = true
	// Own a router to reveal hosts, then breach the first discovered host.
	for _, e := range g.curList() {
		if e.n.kind == "router" {
			g.owned(e.n)
			break
		}
	}
	var target *node
	for _, e := range g.curList() {
		if e.n.state == "discovered" && e.n.kind != "router" {
			target = e.n
			break
		}
	}
	if target == nil {
		t.Skip("no discovered host to breach")
	}
	g.startInfiltrate(target)
	if g.screen != scrInf {
		t.Fatal("startInfiltrate should open the breach screen")
	}
	g.inf.tool = 0 // first tool = dict
	g.draw()
	if !strings.Contains(g.Frame(), "Dictionary attack") {
		t.Errorf("breach screen should show the selected tool's real-world note:\n%s", g.Frame())
	}
}

func TestDifficultyTuning(t *testing.T) {
	seed := loadTestSeed(t)

	easy := newGameState(seed, 7, 0, 0, DifficultyByName("easy"))
	hard := newGameState(seed, 7, 0, 0, DifficultyByName("hard"))
	if easy.inv["bof"] <= hard.inv["bof"] {
		t.Errorf("easy should start with more BOF charges than hard (%d vs %d)", easy.inv["bof"], hard.inv["bof"])
	}
	if easy.inv["zero"] < 1 || hard.inv["zero"] != 0 {
		t.Errorf("unexpected starting 0-days easy=%d hard=%d", easy.inv["zero"], hard.inv["zero"])
	}
	if DifficultyByName("hard").traceMul <= DifficultyByName("easy").traceMul {
		t.Error("hard should trace faster than easy")
	}

	// Synthesized honeypots: hard denser than easy on the same real world.
	var svcs []SeedService
	for i := 0; i < 120; i++ {
		ip := "10.5." + strconv.Itoa(i/10) + "." + strconv.Itoa(10+i%10)
		svcs = append(svcs, SeedService{IP: ip, Port: 80, Svc: "http"})
	}
	rs := NewSeed("10.5.0.0/16", svcs)
	count := func(d Difficulty) int {
		n := 0
		for _, x := range buildWorld(rs, 1, d).nodes {
			if x.honeypot {
				n++
			}
		}
		return n
	}
	if count(DifficultyByName("hard")) <= count(DifficultyByName("easy")) {
		t.Error("hard should synthesize more honeypots than easy")
	}
}

func TestRouterFootholdSoftensSubnet(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 7, 0, 0, Difficulty{})
	g.screen = scrMap

	// Find a router with at least one breachable subnet host (shieldCur > 1).
	var router *node
	for _, n := range g.world.nodes {
		if n.kind != "router" {
			continue
		}
		for _, m := range g.world.nodes {
			if m.subnet == n.subnet && m != n && m.shieldCur > 1 {
				router = n
				break
			}
		}
		if router != nil {
			break
		}
	}
	if router == nil {
		t.Skip("no suitable router/subnet in this world")
	}
	before := map[int]int{}
	for _, m := range g.world.nodes {
		if m.subnet == router.subnet && m != router {
			before[m.id] = m.shieldCur
		}
	}
	g.owned(router)
	softened := 0
	for _, m := range g.world.nodes {
		b, ok := before[m.id]
		if !ok {
			continue
		}
		if m.shieldCur == b-1 {
			softened++
		}
		if m.shieldCur < 1 {
			t.Fatalf("foothold drove %s shield below 1", m.host)
		}
	}
	if softened == 0 {
		t.Error("owning a gateway should soften at least one subnet host")
	}
}

func TestRankThresholds(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{{0, "LAMER"}, {200, "SCRIPT KIDDIE"}, {500, "PHREAK"}, {900, "ICE BREAKER"}, {1500, "GHOST IN THE WIRE"}}
	for _, c := range cases {
		if got, _, _ := rankFor(c.score); got != c.want {
			t.Errorf("rankFor(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestScanRevealsAndTrace(t *testing.T) {
	seed := loadTestSeed(t)
	g := newGameState(seed, 99, 0, 0, Difficulty{})
	g.newGame(99)
	g.screen = scrMap

	// HOME is owned; scanning it should not error and trace climbs.
	before := g.trace
	g.sel = 0 // home
	g.scan(g.selNode())
	if g.trace <= before {
		t.Fatal("scanning should advance the trace")
	}

	// Going dark then trying to scan is refused.
	g.toggleConnect()
	if g.connected {
		t.Fatal("toggleConnect should have gone dark")
	}
	msgBefore := g.msg
	g.scan(g.selNode())
	if g.msg == msgBefore && msgBefore != "" {
		// scan while dark should set a refusal message
		t.Log("scan-while-dark message:", g.msg)
	}
	if !strings.Contains(g.msg, "DARK") {
		t.Errorf("expected a DARK refusal, got %q", g.msg)
	}
}
