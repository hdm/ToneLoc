// Package game is DARKCIDR -- the ToneLoc/Go war-dialing roguelike.
// It is the single implementation of the game: it renders to the same dos
// text-mode screen the scanner TUI uses, so `toneloc --game` plays right in the
// CLI, and the web (--game <addr>) build runs this very same code over the
// ghostty.js terminal. The world is built from the window.TONELOC_SEED dataset
// (game/seed.js), parsed from JSON.
package game

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hdm/toneloc/internal/dos"
)

// minW, minH is the smallest screen the layout is designed for (the classic
// 80x25). On a bigger terminal the game uses the full width and height.
const minW, minH = 80, 25

// Seed mirrors window.TONELOC_SEED.
type Seed struct {
	Mask      string                 `json:"mask"`
	Ports     []int                  `json:"ports"`
	Hosts     map[string]seedHost    `json:"hosts"`
	Honeypots []string               `json:"honeypots"`
	Meta      map[string]interface{} `json:"meta"`
}
type seedHost struct {
	Cls    string `json:"cls"`
	Banner string `json:"banner"`
	Svc    string `json:"svc"`
}

// LoadSeed parses the JSON object out of a seed.js file (window.TONELOC_SEED = {...};).
func LoadSeed(js []byte) (*Seed, error) {
	s := string(js)
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return nil, fmt.Errorf("seed.js: no JSON object found")
	}
	var seed Seed
	if err := json.Unmarshal([]byte(s[i:j+1]), &seed); err != nil {
		return nil, fmt.Errorf("seed.js: %w", err)
	}
	if len(seed.Hosts) == 0 {
		return nil, fmt.Errorf("seed.js: no hosts")
	}
	return &seed, nil
}

// SeedService is one discovered service used to build a world from a real scan.
type SeedService struct {
	IP     string
	Port   int
	Svc    string // service/app name (ssh, http, ...); "" -> tcp/<port>
	Banner string
}

// NewSeed builds a Seed from the services a real scan discovered, so the game
// can be played against the live network instead of the embedded sample world.
func NewSeed(mask string, svcs []SeedService) *Seed {
	s := &Seed{
		Mask:  mask,
		Hosts: map[string]seedHost{},
		Meta:  map[string]interface{}{"source": "live scan"},
	}
	for _, sv := range svcs {
		svc := sv.Svc
		if svc == "" {
			svc = fmt.Sprintf("tcp/%d", sv.Port)
		}
		s.Hosts[sv.IP+":"+strconv.Itoa(sv.Port)] = seedHost{Svc: svc, Banner: sv.Banner}
	}
	return s
}

// --- rng / hash (mulberry32 + FNV-1a, matching the seed generator) ---------

func hash32(s string) uint32 {
	h := fnv.New32a() // FNV-1a 32, same constants as the JS
	h.Write([]byte(s))
	return h.Sum32()
}

// mulberry32: returns a [0,1) generator identical to the JS one.
func mulberry32(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6D2B79F5
		t := a
		t = (t ^ (t >> 15)) * (t | 1)
		t = (t + (t^(t>>7))*(t|61)) ^ t
		return float64(t^(t>>14)) / 4294967296.0
	}
}

func pick[T any](r func() float64, arr []T) T { return arr[int(r()*float64(len(arr)))%len(arr)] }

// --- world ----------------------------------------------------------------

var pwPool = []string{"password", "123456", "admin", "root", "letmein", "qwerty", "dragon", "ncc1701",
	"trustno1", "hunter2", "changeme", "monkey", "master", "joshua", "wargames", "pencil", "setec", "z1on0101"}
var kinds = []string{"workstation", "fileserver", "webserver", "mailserver", "database", "dc"}
var kindGlyph = map[string]rune{"home": '⌂', "uplink": '↑', "router": '#', "workstation": '■',
	"fileserver": '▦', "webserver": '◰', "mailserver": '✉', "database": '▤', "dc": '★'}

type port struct {
	port   int
	svc    string
	banner string
}
type node struct {
	id                          int
	kind, host, ip, subnet      string
	ports                       []port
	state                       string // hidden|discovered|owned
	shield, shieldCur, alert    int
	alertMax, loot, depth       int
	honeypot, objective, looted bool
	hasLogin                    bool
	secret, tool                string
	intelFor                    int // node id, -1 none
	links                       []int
}
type world struct {
	nodes             []*node
	home, uplink, obj *node
}

func isLogin(svc string) bool {
	switch svc {
	case "telnet", "ssh", "ftp", "pop3", "telnets":
		return true
	}
	return false
}
func hostname(kind, ip string, r func() float64) string {
	tags := []string{"corp", "acme", "dev", "ops", "hr", "fin", "lab", "vpn", "mail", "web", "db", "ad"}
	last := ip[strings.LastIndexByte(ip, '.')+1:]
	k := kind
	if len(k) > 3 {
		k = k[:3]
	}
	return pick(r, tags) + "-" + k + last
}

func buildWorld(seed *Seed, seedNum uint32, diff Difficulty) *world {
	diff = diff.normalized()
	r := mulberry32(seedNum)
	byIP := map[string][]port{}
	for key, h := range seed.Hosts {
		ip, ps, _ := strings.Cut(key, ":")
		var pn int
		fmt.Sscanf(ps, "%d", &pn)
		svc := h.Svc
		if svc == "" {
			svc = fmt.Sprintf("tcp/%d", pn)
		}
		byIP[ip] = append(byIP[ip], port{port: pn, svc: svc, banner: h.Banner})
	}
	bySub := map[string][]string{}
	for ip := range byIP {
		net := ip[:strings.LastIndexByte(ip, '.')]
		bySub[net] = append(bySub[net], ip)
	}
	type sub struct {
		net string
		ips []string
	}
	var subs []sub
	for net, ips := range bySub {
		sort.Strings(ips)
		subs = append(subs, sub{net, ips})
	}
	sort.Slice(subs, func(i, j int) bool {
		if len(subs[i].ips) != len(subs[j].ips) {
			return len(subs[i].ips) > len(subs[j].ips)
		}
		return subs[i].net < subs[j].net
	})
	if len(subs) > 6 {
		subs = subs[:6]
	}
	honey := map[string]bool{}
	for _, h := range seed.Honeypots {
		ip, _, _ := strings.Cut(h, ":")
		honey[ip] = true
	}

	w := &world{}
	id := 0
	mk := func(n *node) *node { n.id = id; id++; n.intelFor = -1; w.nodes = append(w.nodes, n); return n }
	link := func(a, b *node) {
		a.links = append(a.links, b.id)
		b.links = append(b.links, a.id)
	}
	w.home = mk(&node{kind: "home", host: "HOME", ip: "127.0.0.1", state: "owned"})
	w.uplink = mk(&node{kind: "uplink", host: "uplink-gw", ip: "0.0.0.0", state: "owned"})
	link(w.home, w.uplink)

	var routers []*node
	for si, s := range subs {
		shield := 2
		if si > 2 {
			shield = 3
		}
		rr := mk(&node{kind: "router", host: "gw." + s.net, ip: s.net + ".1", subnet: s.net,
			ports: []port{{443, "https", ""}, {23, "telnet", ""}}, shield: shield, alertMax: 4,
			loot: 40 + int(r()*40), depth: 1, state: "hidden"})
		routers = append(routers, rr)
		ips := s.ips
		if len(ips) > 5 {
			ips = ips[:5]
		}
		for _, ip := range ips {
			kr := mulberry32(hash32(ip))
			ps := byIP[ip]
			if len(ps) > 4 {
				ps = ps[:4]
			}
			kind := pick(kr, kinds)
			hasLogin := false
			for _, p := range ps {
				if isLogin(p.svc) {
					hasLogin = true
				}
			}
			tool := ""
			if kr() < 0.3 {
				tool = pick(kr, []string{"bof", "zero"})
			}
			n := mk(&node{kind: kind, host: hostname(kind, ip, kr), ip: ip, subnet: s.net, ports: ps,
				shield: 2 + int(kr()*3), alertMax: 3 + int(kr()*2), loot: 20 + int(kr()*100),
				honeypot: honey[ip], depth: 2, secret: pwPool[int(kr()*float64(len(pwPool)))%len(pwPool)],
				hasLogin: hasLogin, tool: tool, state: "hidden"})
			link(rr, n)
		}
	}
	// Real-scan worlds carry no honeypot list; synthesize a deterministic few so
	// the trap mechanic still bites. (Sim worlds bring their own honeypots.) Done
	// before the backbone is wired so honeypots can be kept off the pivot path.
	if len(seed.Honeypots) == 0 {
		for _, n := range w.nodes {
			if n.kind != "home" && n.kind != "uplink" && n.kind != "router" {
				if hash32(n.ip+"#hp")%100 < diff.honeyPct {
					n.honeypot = true
				}
			}
		}
	}

	for i, rr := range routers {
		if i < 2 {
			link(w.uplink, rr)
			continue
		}
		// Deeper routers hang off a host you must own first. Pivots must be
		// breachable, so never pick a (possibly login-less) honeypot -- otherwise
		// the only path to the objective could be a dead end.
		var reach []*node
		for _, x := range routers[:i] {
			for _, idx := range x.links {
				n := w.nodes[idx]
				if n.kind != "router" && n.kind != "uplink" && !n.honeypot {
					reach = append(reach, n)
				}
			}
		}
		pivot := w.uplink
		if len(reach) > 0 {
			pivot = reach[int(r()*float64(len(reach)))%len(reach)]
		}
		link(pivot, rr)
		rr.depth = pivot.depth + 1
		for _, n := range w.nodes {
			if n.subnet == rr.subnet && n != rr {
				n.depth = rr.depth + 1
			}
		}
	}

	// Objective: deepest, juiciest host.
	var cands []*node
	for _, n := range w.nodes {
		if n.kind != "home" && n.kind != "uplink" && n.kind != "router" {
			cands = append(cands, n)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].depth != cands[j].depth {
			return cands[i].depth > cands[j].depth
		}
		return cands[i].loot > cands[j].loot
	})
	if len(cands) > 0 {
		o := cands[0]
		o.objective = true
		o.honeypot = false // the MAINFRAME is never an unbreachable trap
		o.kind = "dc"
		o.host = "MAINFRAME"
		o.loot = 600
		o.shield = 5
		o.alertMax = 5
		w.obj = o
	}
	// Intel: some hosts reveal a linked node's password.
	for _, n := range w.nodes {
		if n.loot > 0 {
			lr := mulberry32(hash32(n.ip + "#i"))
			if lr() < 0.4 {
				var nb []*node
				for _, idx := range n.links {
					if x := w.nodes[idx]; x.secret != "" && x != n {
						nb = append(nb, x)
					}
				}
				if len(nb) > 0 {
					n.intelFor = nb[int(lr()*float64(len(nb)))%len(nb)].id
				}
			}
		}
	}
	return w
}

// --- colours (DOS palette, aliased onto the dos package constants) ---------

const (
	black  = dos.Black
	blue   = dos.Blue
	green  = dos.Green
	cyan   = dos.Cyan
	red    = dos.Red
	brown  = dos.Brown
	lgray  = dos.LightGray
	dgray  = dos.DarkGray
	lblue  = dos.LightBlue
	lgreen = dos.LightGreen
	lcyan  = dos.LightCyan
	lred   = dos.LightRed
	lmag   = dos.LightMagenta
	yellow = dos.Yellow
	white  = dos.White
)

// --- tools / exploits (ported from the JS TOOLS table) ---------------------

type tool struct {
	name             string
	glyph            rune
	vs               []string
	dmg, alert, heat int
	odds             float64
	limited          bool
	minSkill         int    // skill rank needed before it can be selected
	desc             string // a true-to-life one-liner about the technique
}

// toolOrder preserves the JS object key order (Object.keys insertion order).
var toolOrder = []string{"dict", "web", "creds", "sqli", "bof", "phish", "rce", "zero"}
var tools = map[string]tool{
	"dict":  {"brutus (dict)", '▤', []string{"telnet", "ssh", "ftp", "pop3", "telnets"}, 1, 1, 3, .65, false, 0, "Dictionary attack: replay a wordlist of common passwords at a login."},
	"web":   {"Web Exploit", '◰', []string{"http", "https", "http-alt"}, 2, 1, 4, .7, false, 0, "Web exploit: SQL injection, path traversal, or auth bypass on a web app."},
	"creds": {"Default Creds", '▣', []string{"telnet", "ssh", "ftp", "http-alt", "https"}, 2, 2, 2, .55, false, 0, "Default creds: vendor admin/admin nobody ever changed. Loud if wrong."},
	"sqli":  {"SQL Inject", '◳', []string{"http", "https", "mysql", "postgres", "http-alt"}, 3, 1, 4, .68, false, 1, "Stacked SQLi: dump tables, then write a shell. Skill unlock at rank 1."},
	"bof":   {"Buffer Ovflw", '✚', []string{"*"}, 3, 2, 6, .5, true, 0, "Buffer overflow: overrun a fixed buffer to hijack execution. One-shot."},
	"phish": {"Phishing", '✦', []string{"*"}, 1, 0, 1, .45, false, 0, "Phishing: bait a human into running your payload. Quiet, but a long shot."},
	"rce":   {"Remote RCE", '⚷', []string{"*"}, 5, 1, 5, .8, true, 2, "Chained RCE kit. Hits almost anything -- skill unlock at rank 2, or loot one."},
	"zero":  {"0-DAY", '★', []string{"*"}, 9, 0, 8, 1, true, 0, "Zero-day: an unknown, unpatched bug. Silent and lethal -- spend it wisely."},
}

func applies(t tool, svc string) bool {
	for _, v := range t.vs {
		if v == "*" || v == svc {
			return true
		}
	}
	return false
}

// --- difficulty -------------------------------------------------------------

// Difficulty tunes the run. The zero value normalises to "normal".
type Difficulty struct {
	Name      string
	traceMul  float64 // trace climb-rate multiplier
	honeyPct  uint32  // honeypot density in synthesised (real-scan) worlds
	startBof  int     // starting Buffer Overflow charges
	startZero int     // starting 0-day charges
}

var (
	diffEasy   = Difficulty{"easy", 0.7, 6, 2, 1}
	diffNormal = Difficulty{"normal", 1.0, 12, 1, 0}
	diffHard   = Difficulty{"hard", 1.45, 20, 0, 0}
)

func (d Difficulty) normalized() Difficulty {
	if d.Name == "" {
		return diffNormal
	}
	return d
}

// DifficultyByName maps a flag value to a preset (default normal).
func DifficultyByName(name string) Difficulty {
	switch name {
	case "easy":
		return diffEasy
	case "hard":
		return diffHard
	default:
		return diffNormal
	}
}

// --- game state ------------------------------------------------------------

const (
	scrTitle = iota
	scrMap
	scrInf
	scrWin
	scrLose
	scrHelp
)

type infState struct {
	n     *node
	tool  int
	vec   int
	mode  string // "tools" | "pw"
	pwSel int
	log   []string
}

type listEntry struct {
	n     *node
	depth int
}

// Game is the running DARKCIDR session, drawn onto a dos.Screen.
type Game struct {
	scr     *dos.Screen
	w, h    int // current screen size (>= minW x minH)
	seed    *Seed
	seedNum uint32
	diff    Difficulty

	screen         int
	world          *world
	sel, scroll    int
	credits        int
	objectiveTaken bool
	connected      bool
	trace          float64
	heatSpike      float64
	inv            map[string]int
	knownPw        map[int]bool
	skill          int // rank: rises as you own hosts, unlocks better exploits
	stolenKits     int // RCE/0-day kits looted off rivals' machines
	msg            string
	msgUntil       time.Time
	inf            *infState
	logLines       []string
	frame          int
	high           int
	score          int

	// "live wire" random events while connected
	eventSeq  int
	nextEvent time.Time

	// input state machine (mirrors the TUI's escape-sequence parser)
	escState int
	escTime  time.Time
	csiBuf   []byte
	lastTs   time.Time
}

func newGameState(seed *Seed, seedNum uint32, w, h int, diff Difficulty) *Game {
	g := &Game{seed: seed, diff: diff.normalized()}
	g.setSize(w, h)
	g.high = loadHigh()
	g.newGame(seedNum)
	g.screen = scrTitle
	return g
}

// setSize clamps to the minimum playable screen and (re)allocates the cell
// buffer when the dimensions change, forcing a full repaint.
func (g *Game) setSize(w, h int) bool {
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if g.scr != nil && w == g.w && h == g.h {
		return false
	}
	g.w, g.h = w, h
	g.scr = dos.New(w, h)
	return true
}

func (g *Game) newGame(seedNum uint32) {
	d := g.diff.normalized()
	w := buildWorld(g.seed, seedNum, d)
	for _, n := range w.nodes {
		n.shieldCur = n.shield
	}
	// reveal the uplink's direct links so there's somewhere to start.
	for _, i := range w.uplink.links {
		if w.nodes[i].state == "hidden" {
			w.nodes[i].state = "discovered"
		}
	}
	g.world = w
	g.seedNum = seedNum
	g.sel, g.scroll = 0, 0
	g.credits = 0
	g.objectiveTaken = false
	g.connected = true
	g.trace = 0
	g.heatSpike = 0
	g.inv = map[string]int{"dict": 99, "web": 99, "creds": 99, "phish": 99, "bof": d.startBof, "zero": d.startZero, "sqli": 99, "rce": 0}
	g.knownPw = map[int]bool{}
	g.skill = 0
	g.stolenKits = 0
	g.msg = ""
	g.msgUntil = time.Time{}
	g.inf = nil
	g.logLines = []string{"DARKCIDR online. Welcome home, operator."}
	g.score = 0
	g.eventSeq = 0
	g.nextEvent = time.Now().Add(12 * time.Second)
}

func (g *Game) logf(s string) {
	g.logLines = append(g.logLines, s)
	if len(g.logLines) > 120 {
		g.logLines = g.logLines[1:]
	}
}
func (g *Game) setMsg(s string, ms int) {
	g.msg = s
	g.msgUntil = time.Now().Add(time.Duration(ms) * time.Millisecond)
}

// --- queries ----------------------------------------------------------------

func (g *Game) curList() []listEntry {
	var out []listEntry
	w := g.world
	out = append(out, listEntry{w.home, 0}, listEntry{w.uplink, 1})
	for _, rr := range w.nodes {
		if rr.kind == "router" && rr.state != "hidden" {
			out = append(out, listEntry{rr, 2})
			for _, h := range w.nodes {
				if h.subnet == rr.subnet && h != rr && h.state != "hidden" {
					out = append(out, listEntry{h, 3})
				}
			}
		}
	}
	return out
}

func (g *Game) selNode() *node {
	l := g.curList()
	if len(l) == 0 {
		return nil
	}
	i := g.sel
	if i > len(l)-1 {
		i = len(l) - 1
	}
	if i < 0 {
		i = 0
	}
	return l[i].n
}

func (g *Game) ownedDepth() int {
	d := 0
	for _, n := range g.world.nodes {
		if n.state == "owned" && n.kind != "home" && n.kind != "uplink" && n.depth > d {
			d = n.depth
		}
	}
	return d
}

func (g *Game) toolIds() []string {
	var out []string
	for _, id := range toolOrder {
		t := tools[id]
		if t.limited && g.inv[id] <= 0 {
			continue // spent / never looted
		}
		if t.minSkill > g.skill {
			continue // not learned yet -- earn the rank
		}
		out = append(out, id)
	}
	return out
}

func (g *Game) curVecSvc(n *node, vi int) string {
	if len(n.ports) > 0 {
		if vi > len(n.ports)-1 {
			vi = len(n.ports) - 1
		}
		return n.ports[vi].svc
	}
	return "*"
}

func (g *Game) firstTool(n *node) int {
	ids := g.toolIds()
	for idx, id := range ids {
		if applies(tools[id], g.curVecSvc(n, 0)) {
			return idx
		}
	}
	return 0
}

func (g *Game) pwList(n *node) []string {
	r := mulberry32(hash32(n.ip + "#pw"))
	set := []string{n.secret}
	has := func(s string) bool {
		for _, x := range set {
			if x == s {
				return true
			}
		}
		return false
	}
	for len(set) < 5 {
		c := pwPool[int(r()*float64(len(pwPool)))%len(pwPool)]
		if !has(c) {
			set = append(set, c)
		}
	}
	for i := len(set) - 1; i > 0; i-- {
		j := int(r() * float64(i+1))
		set[i], set[j] = set[j], set[i]
	}
	return set
}

// --- actions ----------------------------------------------------------------

func (g *Game) scan(n *node) {
	if !g.connected {
		g.setMsg("You are DARK. Reconnect (R) to scan.", 1600)
		return
	}
	if n.state != "owned" {
		g.setMsg("Can only scan from a node you OWN.", 1500)
		return
	}
	found := 0
	for _, i := range n.links {
		if m := g.world.nodes[i]; m.state == "hidden" {
			m.state = "discovered"
			found++
		}
	}
	g.trace = math.Min(100, g.trace+2)
	g.logf("$ scan " + n.ip + "  ::  " + strconv.Itoa(found) + " new node(s) found")
	if found > 0 {
		g.setMsg("SCAN: "+strconv.Itoa(found)+" node(s) revealed", 1400)
	} else {
		g.setMsg("scan: nothing new", 1400)
	}
}

// stealKit decides whether a compromised box hands you a rival's exploit kit,
// keyed off the services the SCAN actually found on it: web/db boxes tend to
// hide RCE chains; deep, juicy hosts sometimes a 0-day. Deterministic per host.
func (g *Game) stealKit(n *node) string {
	r := mulberry32(hash32(n.ip + "#kit"))
	hot := n.objective || n.loot > 120
	web := false
	for _, p := range n.ports {
		switch p.svc {
		case "http", "https", "http-alt", "mysql", "postgres":
			web = true
		}
	}
	switch {
	case hot && r() < 0.5:
		return "zero"
	case web && r() < 0.45:
		return "rce"
	case r() < 0.2:
		return "rce"
	}
	return ""
}

func (g *Game) loot(n *node) {
	if n.state != "owned" {
		g.setMsg("Compromise it first.", 1400)
		return
	}
	if n.looted {
		g.setMsg("Already looted.", 1200)
		return
	}
	n.looted = true
	g.credits += n.loot
	g.logf("$ exfil " + n.ip + "  ::  +" + strconv.Itoa(n.loot) + " cr")
	extra := ""
	if n.tool != "" {
		if _, ok := g.inv[n.tool]; ok {
			g.inv[n.tool]++
			extra = " + " + tools[n.tool].name
		}
	}
	// Surprise loot: rivals leave their toolkits behind. What you find is keyed
	// off what the box actually runs (its scanned services), so the network you
	// found shapes your arsenal. Web/db boxes drop RCE kits; juicy hosts, 0-days.
	if kit := g.stealKit(n); kit != "" {
		g.inv[kit]++
		g.stolenKits++
		extra += " + STOLEN " + tools[kit].name
		g.logf("  found a rival's " + tools[kit].name + " on disk -- reusing it")
	}
	if n.intelFor >= 0 && !g.knownPw[n.intelFor] {
		g.knownPw[n.intelFor] = true
		extra += " + creds intel"
	}
	if n.objective {
		g.objectiveTaken = true
		g.logf("*** OBJECTIVE DATA EXFILTRATED -- now go DARK to win! ***")
		g.logf("MAINFRAME: GREETINGS PROFESSOR FALKEN. SHALL WE PLAY A GAME?")
		g.setMsg("OBJECTIVE TAKEN! Press R to go DARK and bank the win", 3000)
	} else {
		g.setMsg("Looted +"+strconv.Itoa(n.loot)+" cr"+extra, 1800)
	}
}

func (g *Game) toggleConnect() {
	g.connected = !g.connected
	if g.connected {
		g.logf("link UP -- you are live on the wire.")
		g.setMsg("CONNECTED -- trace active", 1400)
	} else {
		g.logf("link DOWN -- going dark. Trace cooling.")
		g.setMsg("DARK -- safe, but blind to the net", 1600)
		if g.objectiveTaken {
			g.win()
			return
		}
		// Cutting the line cools the trace, but pulling out fast burns your
		// intermediate footholds: a deep pivot can go cold while you're away.
		g.dropPivot()
	}
}

// dropPivot loses your DEEPEST non-router foothold when going dark -- the price
// of cooling off. Routers/home/uplink and the objective hold; you can re-own
// from a kept node, so it never strands the win.
func (g *Game) dropPivot() {
	var deep *node
	for _, n := range g.world.nodes {
		if n.state == "owned" && n.kind != "home" && n.kind != "uplink" && n.kind != "router" && !n.objective {
			if deep == nil || n.depth > deep.depth {
				deep = n
			}
		}
	}
	if deep != nil && deep.depth >= 3 && hash32(deep.ip+"#drk"+strconv.Itoa(deep.alert))%5 < 3 {
		deep.state = "discovered"
		deep.shieldCur = max(1, deep.shield/2)
		deep.looted = false
		g.logf("- lost foothold on " + deep.host + " while dark (relay went cold)")
		g.setMsg("WENT DARK: lost grip on "+deep.host+". Re-own it later.", 2400)
	}
}

func (g *Game) startInfiltrate(n *node) {
	if !g.connected {
		g.setMsg("You are DARK. Reconnect (R) first.", 1500)
		return
	}
	if n.state == "owned" {
		g.setMsg("Already pwned. Loot it (L) or scan (S).", 1500)
		return
	}
	if n.state != "discovered" {
		g.setMsg("Scan to reveal it first.", 1400)
		return
	}
	if n.kind == "uplink" || n.kind == "home" {
		return
	}
	n.alert = 0
	svcline := "no services"
	if len(n.ports) > 0 {
		var parts []string
		for _, p := range n.ports {
			parts = append(parts, p.svc+"/"+strconv.Itoa(p.port))
		}
		svcline = strings.Join(parts, "  ")
	}
	g.inf = &infState{n: n, tool: g.firstTool(n), vec: 0, mode: "tools", log: []string{
		"Connected to " + n.ip + " (" + n.host + ")",
		"nerva: " + svcline,
	}}
	g.screen = scrInf
	g.logf("$ nerva " + n.ip + "  ::  brutus armed ...")
}

func (g *Game) infiltrateAttempt() {
	inf, n := g.inf, g.inf.n
	ids := g.toolIds()
	ti := inf.tool
	if ti > len(ids)-1 {
		ti = len(ids) - 1
	}
	id := ids[ti]
	t := tools[id]
	svc := g.curVecSvc(n, inf.vec)
	if t.limited && g.inv[id] <= 0 {
		inf.log = append(inf.log, "! out of "+t.name)
		return
	}
	ok := applies(t, svc)
	roll := mulberry32(hash32(n.ip + id + svc + strconv.Itoa(n.alert) + strconv.Itoa(len(inf.log))))()
	g.trace = math.Min(100, g.trace+float64(t.heat)*0.25)
	// A precious limited kit is only spent when it can actually bite -- firing a
	// 0-day at a service it can't touch just keeps it in the bag.
	if t.limited && !ok {
		inf.log = append(inf.log, "» "+t.name+": no vector here -- kit kept")
		n.alert++
		if n.alert >= n.alertMax {
			g.kicked()
		}
		return
	}
	if t.limited {
		g.inv[id]--
	}
	// Rank sharpens every exploit: +4% odds per rank earned, capped, so deeper
	// runs feel measurably stronger.
	eff := math.Min(0.95, t.odds+float64(g.skill)*0.04)
	if n.honeypot {
		n.alert += 2
		g.trace = math.Min(100, g.trace+22)
		g.heatSpike = 12
		inf.log = append(inf.log, "!! IDS TRIPPED -- this is a HONEYPOT! trace +22")
		if n.alert >= n.alertMax {
			g.kicked()
		}
		return
	}
	if ok && roll < eff {
		n.shieldCur -= t.dmg
		inf.log = append(inf.log, "» "+t.name+" vs "+svc+": BREACH (-"+strconv.Itoa(t.dmg)+" shield)")
	} else {
		a := t.alert
		if a == 0 {
			a = 1 // JS: tool.alert || 1
		}
		n.alert += a
		if ok {
			inf.log = append(inf.log, "» "+t.name+" vs "+svc+": failed")
		} else {
			inf.log = append(inf.log, "» "+t.name+" vs "+svc+": no effect on "+svc)
		}
	}
	if n.shieldCur <= 0 {
		g.owned(n)
		return
	}
	if n.alert >= n.alertMax {
		g.kicked()
	}
}

func (g *Game) tryPassword(idx int) {
	inf, n := g.inf, g.inf.n
	list := g.pwList(n)
	guess := list[idx]
	g.trace = math.Min(100, g.trace+1)
	if guess == n.secret {
		inf.log = append(inf.log, "» login "+guess+" : ACCESS GRANTED")
		n.shieldCur = 0
		g.owned(n)
	} else {
		n.alert++
		inf.log = append(inf.log, "» login "+guess+" : denied")
		if n.alert >= n.alertMax {
			g.kicked()
		}
	}
}

func (g *Game) owned(n *node) {
	n.state = "owned"
	g.trace = math.Min(100, g.trace+3)
	g.logf("+ ROOT on " + n.ip + " (" + n.host + ")")
	g.setMsg("ACCESS GRANTED :: "+n.host, 2000)
	// Each box you crack sharpens the operator. Crossing a rank unlocks a sharper
	// exploit class (SQLi at rank 1) -- the skill is finding & breaching targets.
	old := g.skill
	g.skill++
	if old < 1 && g.skill >= 1 {
		g.logf("  *skill up* rank 1 -- SQL injection unlocked (◳)")
		g.setMsg("SKILL UP! Rank 1: SQL Injection unlocked ◳", 2400)
	}
	if old < 2 && g.skill >= 2 {
		g.logf("  *skill up* rank 2 -- you can hand-roll RCE chains (⚷)")
		g.setMsg("SKILL UP! Rank 2: Remote RCE unlocked ⚷", 2400)
	}
	for _, i := range n.links {
		if g.world.nodes[i].state == "hidden" {
			g.world.nodes[i].state = "discovered"
		}
	}
	// Owning a gateway buys internal trust: every host behind it is a little
	// easier to breach (a foothold inside the perimeter). Rewards pivoting.
	if n.kind == "router" && n.subnet != "" {
		softened := 0
		for _, m := range g.world.nodes {
			if m.subnet == n.subnet && m != n && m.state != "owned" && m.shieldCur > 1 {
				m.shieldCur--
				softened++
			}
		}
		if softened > 0 {
			g.logf("  internal trust: " + n.subnet + ".0/24 shields -1 (" + strconv.Itoa(softened) + " host(s))")
			g.setMsg("FOOTHOLD: inside "+n.subnet+".0/24 — shields dropped", 2200)
		}
	}
	g.inf = nil
	g.screen = scrMap
}

func (g *Game) kicked() {
	n := g.inf.n
	n.shieldCur = n.shield
	g.trace = math.Min(100, g.trace+8)
	g.logf("- connection to " + n.ip + " dropped (alarm)")
	g.setMsg("KICKED OUT -- alarm raised", 1800)
	g.inf = nil
	g.screen = scrMap
}

func (g *Game) win() {
	g.screen = scrWin
	g.connected = false
	bonus := int(math.Round((100 - g.trace) * 10))
	g.score = g.credits + bonus
	if g.score > g.high {
		g.high = g.score
		saveHigh(g.high)
	}
}

func (g *Game) lose() {
	g.screen = scrLose
	g.score = g.credits
	if g.score > g.high {
		g.high = g.score
		saveHigh(g.high)
	}
}

// --- main loop --------------------------------------------------------------

// RunOptions configures a terminal session. The zero value plays at the classic
// 80x25. Set Width/Height to start larger, and Resize to follow live terminal
// resizes (it is polled on the render tick).
type RunOptions struct {
	Width, Height int
	Resize        func() (int, int)
	Diff          Difficulty
}

func enterAlt(out io.Writer) { io.WriteString(out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H") }
func leaveAlt(out io.Writer) { io.WriteString(out, "\x1b[0m\x1b[?25h\x1b[?1049l") }

// Run plays DARKCIDR in the terminal, drawing to out and reading raw input
// bytes from keys (the caller is responsible for raw mode). It returns when the
// player quits (q / ESC on the title screen) or the context is cancelled.
func Run(ctx context.Context, out io.Writer, keys <-chan byte, seed *Seed, seedNum uint32, opt RunOptions) error {
	g := newGameState(seed, seedNum, opt.Width, opt.Height, opt.Diff)
	enterAlt(out)
	defer leaveAlt(out)
	return g.runFrames(ctx, out, keys, opt)
}

// RunEmbedded plays the game inside an alternate screen the CALLER already owns
// (e.g. the scanner shell dropping into the game with G). Unlike Run it does NOT
// enter or leave the alternate screen itself, so when the player quits the game
// control returns cleanly to the host UI -- which is left to repaint. The world
// is built from the supplied seed (typically NewSeed of a live/loaded scan).
func RunEmbedded(ctx context.Context, out io.Writer, keys <-chan byte, seed *Seed, seedNum uint32, opt RunOptions) error {
	g := newGameState(seed, seedNum, opt.Width, opt.Height, opt.Diff)
	io.WriteString(out, "\x1b[2J") // clear into the host's existing buffer
	return g.runFrames(ctx, out, keys, opt)
}

// runFrames is the render/update/input loop, shared by Run and RunReal. The
// caller owns the alternate screen.
func (g *Game) runFrames(ctx context.Context, out io.Writer, keys <-chan byte, opt RunOptions) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	g.lastTs = time.Now()
	g.draw()
	g.scr.Flush(out)

	for {
		select {
		case <-ctx.Done():
			return nil
		case b, ok := <-keys:
			if !ok {
				return nil
			}
			if g.feed(b) {
				return nil
			}
		case <-ticker.C:
			// A lone ESC (not the start of an arrow sequence) acts after a beat.
			if g.escState == 1 && time.Since(g.escTime) > 80*time.Millisecond {
				g.escState = 0
				if g.onKey(keyMsg{kind: kEsc}) {
					return nil
				}
			}
			if opt.Resize != nil {
				if w, h := opt.Resize(); g.setSize(w, h) {
					io.WriteString(out, "\x1b[2J") // new buffer repaints fully
				}
			}
			g.update()
			g.frame++
			g.draw()
			g.scr.Flush(out)
		}
	}
}

// Per-port state in a HostScan row (the caller maps the engine's verdicts onto
// these for the recon pips).
const (
	PortPending uint8 = iota
	PortClosed
	PortOpen
	PortBanner
)

// HostScan is one target IP's port-scan progress for the live recon view.
type HostScan struct {
	IP     string
	Ports  []uint8 // per-port state, in the scan's port order
	Open   int
	Banner bool
}

// ReconSource is a live network scan feeding the pre-game RECON screen. The
// game package stays decoupled from the scanner: the caller adapts its engine
// to this interface.
type ReconSource interface {
	Dialed() int              // probes sent so far
	Total() int               // probes planned (for the global bar)
	Found() []SeedService     // services discovered so far
	Hosts(max int) []HostScan // per-target progress rows (open hosts first)
	Done() bool               // scan finished
	Stop()                    // stop scanning (called when recon ends)
}

// RunReal scans the live network (via src) on an in-game RECON screen, then
// builds the world from what it finds and plays it. ENTER jacks in early once
// services are found; ESC/Q aborts.
func RunReal(ctx context.Context, out io.Writer, keys <-chan byte, src ReconSource, mask string, seedNum uint32, opt RunOptions) error {
	enterAlt(out)
	defer leaveAlt(out)

	g := &Game{seed: &Seed{Mask: mask}, diff: opt.Diff.normalized()}
	g.setSize(opt.Width, opt.Height)
	g.high = loadHigh()

	seed, abort := g.reconPhase(ctx, out, keys, src, mask, opt)
	if abort || seed == nil {
		return nil
	}
	g.seed = seed
	g.newGame(seedNum)
	g.screen = scrMap // straight into the run -- the recon was the intro
	g.setMsg("RECON done — explore, breach, grab the MAINFRAME ⚑, then go DARK.  ?=help", 5000)
	return g.runFrames(ctx, out, keys, opt)
}

// reconPhase draws the live scan until the player jacks in (ENTER, once any
// service is found), the scan finishes, or the player aborts (ESC/Q). It returns
// the world seed and whether the player aborted.
func (g *Game) reconPhase(ctx context.Context, out io.Writer, keys <-chan byte, src ReconSource, mask string, opt RunOptions) (*Seed, bool) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			src.Stop()
			return nil, true
		case b, ok := <-keys:
			if !ok {
				src.Stop()
				return nil, true
			}
			switch b {
			case 'q', 'Q', 0x1b, 3:
				src.Stop()
				return nil, true
			case '\r', '\n':
				if f := src.Found(); len(f) > 0 {
					src.Stop()
					return NewSeed(mask, f), false
				}
			}
		case <-ticker.C:
			if opt.Resize != nil {
				if w, h := opt.Resize(); g.setSize(w, h) {
					io.WriteString(out, "\x1b[2J")
				}
			}
			g.frame++
			if src.Done() {
				if f := src.Found(); len(f) > 0 {
					src.Stop()
					return NewSeed(mask, f), false
				}
			}
			g.drawRecon(mask, src)
			g.scr.Flush(out)
		}
	}
}

// pip maps a per-port state to its bar cell + colour.
func pip(st uint8) (rune, int) {
	switch st {
	case PortOpen:
		return '█', lgreen
	case PortBanner:
		return '█', lcyan
	case PortClosed:
		return '▒', dgray
	default:
		return '░', dgray
	}
}

func (g *Game) drawRecon(mask string, src ReconSource) {
	blink := (g.frame>>3)&1 == 0
	dialed, total, done := src.Dialed(), src.Total(), src.Done()
	found := src.Found()

	g.scr.Clear(dos.Attr(lgray, black))
	for y := 0; y < g.h; y++ {
		ch := ' '
		if y%2 == 1 {
			ch = '░'
		}
		g.fill(0, y, g.w, 1, ch, dgray, black)
	}

	boxW := 72
	if boxW > g.w-4 {
		boxW = g.w - 4
	}
	bx := (g.w - boxW) / 2
	top := (g.h - 20) / 2
	if top < 0 {
		top = 0
	}
	sp := string(g.spinner())
	g.center(top, lgreen, black, sp+"  D A R K C I D R   ::   W A R - D I A L  "+sp)
	g.center(top+1, dgray, black, "dialing "+mask)

	// Global progress bar (sub-cell smooth).
	frac := 0.0
	if total > 0 {
		frac = float64(dialed) / float64(total)
	}
	g.sbar(bx, top+3, boxW, frac, lcyan, dgray, black)
	pct := 0
	if total > 0 {
		pct = int(frac * 100)
	}
	g.p(bx, top+4, dgray, black, dos.Pad(
		"probed "+strconv.Itoa(dialed)+"/"+strconv.Itoa(total)+"  ·  "+
			strconv.Itoa(pct)+"%  ·  hosts up "+strconv.Itoa(countHosts(found))+
			"  ·  services "+strconv.Itoa(len(found)), boxW))

	// Targets box: a port-scan bar per IP.
	by := top + 6
	boxH := g.h - by - 3
	if boxH < 5 {
		boxH = 5
	}
	g.box(bx, by, boxW, boxH, lcyan, black, true)
	g.fill(bx+1, by+1, boxW-2, boxH-2, ' ', lgray, black) // clear the dotted bg inside
	g.ttl(bx, by, boxW, yellow, black, "targets")
	rows := boxH - 2
	svcByIP := map[string][]string{}
	for _, s := range found {
		svcByIP[s.IP] = append(svcByIP[s.IP], s.Svc)
	}
	hosts := src.Hosts(rows)
	barX := bx + 2 + 16
	for i, hs := range hosts {
		if i >= rows {
			break
		}
		y := by + 1 + i
		ipc := dgray
		if hs.Open > 0 {
			ipc = white
		}
		g.p(bx+2, y, ipc, black, dos.Pad(hs.IP, 15))
		w := len(hs.Ports)
		if max := boxW - 20 - 10; w > max {
			w = max
		}
		for j := 0; j < w; j++ {
			ch, c := pip(hs.Ports[j])
			g.st(barX+j, y, ch, c, black)
		}
		if hs.Open > 0 {
			tag := " ▸" + strconv.Itoa(hs.Open)
			if svcs := svcByIP[hs.IP]; len(svcs) > 0 {
				tag += " " + svcs[0]
				if len(svcs) > 1 {
					tag += "+" + strconv.Itoa(len(svcs)-1)
				}
			}
			g.p(barX+w+1, y, lgreen, black, tag)
		}
	}
	if len(hosts) == 0 {
		msg := sp + " dialing the wire ..."
		if done {
			msg = "no live hosts found on " + mask
		}
		g.center(by+boxH/2, dgray, black, msg)
	}

	foot := " ESC: abort"
	if len(found) > 0 {
		if blink {
			g.center(g.h-2, lgreen, black, "▸▸▸  ENTER to JACK IN  ◂◂◂")
		}
		foot = " ENTER: jack in    ESC: abort"
	} else if done {
		foot = " ESC: quit    (--sim plays the sample world)"
	}
	g.p(0, g.h-1, black, lgray, dos.Pad(foot, g.w))
}

func countHosts(found []SeedService) int {
	seen := map[string]bool{}
	for _, s := range found {
		seen[s.IP] = true
	}
	return len(seen)
}

func (g *Game) update() {
	now := time.Now()
	dt := 0.0
	if !g.lastTs.IsZero() {
		dt = now.Sub(g.lastTs).Seconds()
	}
	g.lastTs = now
	if g.screen != scrMap && g.screen != scrInf {
		return
	}
	if g.connected {
		rate := 1.6 + 0.9*float64(g.ownedDepth())
		if g.objectiveTaken {
			rate += 3.5 // alarms blaring once the crown jewels move
		}
		rate *= g.diff.normalized().traceMul
		g.trace = math.Min(100, g.trace+rate*dt)
		if g.heatSpike > 0 {
			g.heatSpike -= dt * 6
		}
		// Home stretch: when the trace is on your doorstep, they start cutting
		// relays -- stay live too long and you lose your deepest foothold. Get out.
		if g.trace >= 88 && g.screen == scrMap && now.After(g.nextEvent) {
			g.dropPivot()
			g.nextEvent = now.Add(4 * time.Second)
		}
		if g.trace >= 100 {
			g.lose()
			return
		}
		// The wire is alive: while you're connected on the map, things happen.
		if g.screen == scrMap && now.After(g.nextEvent) {
			g.fireEvent()
			g.nextEvent = now.Add(time.Duration(13+g.eventSeq%7) * time.Second)
		}
	} else {
		g.trace = math.Max(0, g.trace-22*dt)
	}
}

// securityTips rotate as flavor on quiet events -- each one is true.
var securityTips = []string{
	"TIP: going DARK (disconnect) is the real-world 'log off' -- it breaks the trace.",
	"TIP: defenders watch failed logins. Every miss raises the ALARM, like a real lockout.",
	"TIP: honeypots have no business value -- any traffic to them is automatically suspicious.",
	"TIP: pivoting = lateral movement: own one host, use it to reach the next.",
	"TIP: looted creds work elsewhere because people reuse passwords. So do you.",
	"TIP: the deeper you sit, the faster the trace -- minimize dwell time on a live link.",
	"TIP: a 0-day is unpatched and unknown; that's why it never misses (and is precious).",
	"TIP: default credentials still pop more boxes than any fancy exploit. Change yours.",
}

// fireEvent applies a surprising, bounded event -- and teaches something true.
// Effects never block the path to the objective.
func (g *Game) fireEvent() {
	g.eventSeq++
	r := mulberry32(g.seedNum ^ uint32(g.eventSeq)*2654435761)
	// pick reaches for a random node matching want, using r.
	pick := func(want func(*node) bool) *node {
		var c []*node
		for _, n := range g.world.nodes {
			if want(n) {
				c = append(c, n)
			}
		}
		if len(c) == 0 {
			return nil
		}
		return c[int(r()*float64(len(c)))%len(c)]
	}
	host := func(n *node) bool {
		return n.kind != "home" && n.kind != "uplink" && n.kind != "router"
	}
	switch roll := r(); {
	case roll < 0.20: // insider credential leak
		if n := pick(func(n *node) bool { return host(n) && n.state != "owned" && n.secret != "" && !g.knownPw[n.id] }); n != nil {
			g.knownPw[n.id] = true
			g.logf("~ chatter: creds for " + n.host + " leaked on a forum")
			g.setMsg("INSIDER TIP: creds leaked for "+n.host+" (⚷). People reuse passwords.", 2600)
			return
		}
	case roll < 0.38: // blue team hardens a box
		if n := pick(func(n *node) bool { return host(n) && n.state != "owned" && !n.objective && n.shieldCur > 0 }); n != nil {
			n.shield++
			n.shieldCur++
			g.logf("- blue team hardened " + n.host + " (+1 shield)")
			g.setMsg("SOC HARDENED "+n.host+" (+1 shield). Defenders patch too.", 2400)
			return
		}
	case roll < 0.54: // maintenance reboot clears an alarm
		if n := pick(func(n *node) bool { return host(n) && n.state == "discovered" && n.alert > 0 }); n != nil {
			n.alert = 0
			g.logf("~ " + n.host + " rebooted -- alarm state reset")
			g.setMsg("MAINTENANCE: "+n.host+" rebooted, alarm cleared.", 2200)
			return
		}
	case roll < 0.70: // exploit drops onto the scene
		tool := "bof"
		switch {
		case r() < 0.25:
			tool = "zero"
		case r() < 0.5:
			tool = "rce"
		}
		g.inv[tool]++
		g.logf("+ exploit acquired: " + tools[tool].name)
		g.setMsg("EXPLOIT DROP: +1 "+tools[tool].name+". Unpatched bugs are gold.", 2600)
		return
	case roll < 0.84: // packet storm -- the net gets noisy, trace climbs
		g.trace = math.Min(100, g.trace+6)
		g.logf("! packet storm -- monitoring spikes (trace +6)")
		g.setMsg("PACKET STORM: trace +6. Noisy networks help the hunters.", 2200)
		return
	}
	// Quiet beat: a true security tip.
	g.setMsg(securityTips[g.eventSeq%len(securityTips)], 3000)
}

// --- input ------------------------------------------------------------------

type keyKind int

const (
	kRune keyKind = iota
	kEnter
	kEsc
	kUp
	kDown
	kLeft
	kRight
	kSpace
)

type keyMsg struct {
	kind keyKind
	r    rune
}

func translate(b byte) keyMsg {
	switch b {
	case '\r', '\n':
		return keyMsg{kind: kEnter}
	case ' ':
		return keyMsg{kind: kSpace}
	case 0x1b:
		return keyMsg{kind: kEsc}
	default:
		return keyMsg{kind: kRune, r: rune(b)}
	}
}

// feed pushes one raw byte through the escape parser; returns true to quit.
func (g *Game) feed(b byte) bool {
	switch g.escState {
	case 2: // collecting a CSI / SS3 sequence
		g.csiBuf = append(g.csiBuf, b)
		if b >= 0x40 && b <= 0x7e {
			g.dispatchCSI(g.csiBuf)
			g.escState = 0
		} else if len(g.csiBuf) > 32 {
			g.escState = 0
		}
		return false
	case 1: // saw ESC; sequence or lone ESC?
		if b == '[' || b == 'O' {
			g.escState = 2
			g.csiBuf = g.csiBuf[:0]
			return false
		}
		g.escState = 0
		if g.onKey(keyMsg{kind: kEsc}) {
			return true
		}
		return g.onKey(translate(b))
	default:
		if b == 0x1b {
			g.escState = 1
			g.escTime = time.Now()
			return false
		}
		if b == 3 { // Ctrl-C
			return true
		}
		return g.onKey(translate(b))
	}
}

func (g *Game) dispatchCSI(buf []byte) {
	if len(buf) == 0 {
		return
	}
	switch buf[len(buf)-1] {
	case 'A':
		g.onKey(keyMsg{kind: kUp})
	case 'B':
		g.onKey(keyMsg{kind: kDown})
	case 'C':
		g.onKey(keyMsg{kind: kRight})
	case 'D':
		g.onKey(keyMsg{kind: kLeft})
	}
}

func low(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// onKey applies one logical key; returns true to quit the game.
func (g *Game) onKey(k keyMsg) bool {
	// 'q' quits from anywhere (terminal convenience; not in the web build).
	if k.kind == kRune && low(k.r) == 'q' {
		return true
	}
	switch g.screen {
	case scrTitle:
		switch {
		case k.kind == kEnter:
			g.newGame(g.seedNum)
			g.screen = scrMap
		case k.kind == kEsc:
			return true
		case k.kind == kRune && (low(k.r) == '?' || low(k.r) == 'h'):
			g.screen = scrHelp
		}
		return false
	case scrHelp:
		if k.kind == kEnter || k.kind == kEsc || (k.kind == kRune && k.r == '?') {
			g.screen = scrMap
		}
		return false
	case scrWin, scrLose:
		if k.kind == kEnter {
			g.newGame(uint32(time.Now().UnixNano()))
			g.screen = scrMap
		} else if k.kind == kEsc {
			g.newGame(g.seedNum)
			g.screen = scrTitle
		}
		return false
	case scrInf:
		g.infKey(k)
		return false
	}
	g.mapKey(k)
	return false
}

func (g *Game) infKey(k keyMsg) {
	inf, n := g.inf, g.inf.n
	if inf.mode == "pw" {
		list := g.pwList(n)
		switch {
		case k.kind == kUp || (k.kind == kRune && low(k.r) == 'k'):
			inf.pwSel = (inf.pwSel - 1 + len(list)) % len(list)
		case k.kind == kDown || (k.kind == kRune && low(k.r) == 'j'):
			inf.pwSel = (inf.pwSel + 1) % len(list)
		case k.kind == kEnter:
			inf.mode = "tools"
			g.tryPassword(inf.pwSel)
		case k.kind == kEsc:
			inf.mode = "tools"
		}
		return
	}
	switch {
	case k.kind == kEsc:
		g.trace = math.Min(100, g.trace+4)
		g.logf("- aborted " + n.ip)
		g.inf = nil
		g.screen = scrMap
	case k.kind == kUp || (k.kind == kRune && low(k.r) == 'k'):
		m := len(g.toolIds())
		inf.tool = (inf.tool - 1 + m) % m
	case k.kind == kDown || (k.kind == kRune && low(k.r) == 'j'):
		m := len(g.toolIds())
		inf.tool = (inf.tool + 1) % m
	case k.kind == kLeft || (k.kind == kRune && low(k.r) == 'h'):
		m := max(1, len(n.ports))
		inf.vec = (inf.vec - 1 + m) % m
	case k.kind == kRight || (k.kind == kRune && low(k.r) == 'l'):
		m := max(1, len(n.ports))
		inf.vec = (inf.vec + 1) % m
	case k.kind == kEnter || k.kind == kSpace:
		g.infiltrateAttempt()
	case k.kind == kRune && low(k.r) == 'g' && n.hasLogin:
		inf.mode = "pw"
		inf.pwSel = 0
	}
}

func (g *Game) mapKey(k keyMsg) {
	if k.kind == kEsc {
		g.screen = scrTitle
		return
	}
	if k.kind == kRune && k.r == '?' {
		g.screen = scrHelp
		return
	}
	list := g.curList()
	switch {
	case k.kind == kUp || (k.kind == kRune && low(k.r) == 'k'):
		g.sel = max(0, g.sel-1)
	case k.kind == kDown || (k.kind == kRune && low(k.r) == 'j'):
		g.sel = min(len(list)-1, g.sel+1)
	case k.kind == kRune && low(k.r) == 'r':
		g.toggleConnect()
	case k.kind == kRune && low(k.r) == 's':
		if n := g.selNode(); n != nil {
			g.scan(n)
		}
	case k.kind == kRune && low(k.r) == 'i':
		if n := g.selNode(); n != nil {
			g.startInfiltrate(n)
		}
	case k.kind == kRune && low(k.r) == 'l':
		if n := g.selNode(); n != nil {
			g.loot(n)
		}
	case k.kind == kEnter:
		n := g.selNode()
		if n == nil {
			return
		}
		switch n.state {
		case "owned":
			if n.loot > 0 && !n.looted && n.kind != "router" {
				g.loot(n)
			} else {
				g.scan(n)
			}
		case "discovered":
			g.startInfiltrate(n)
		}
	}
}

// --- high score persistence -------------------------------------------------

func highScorePath() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "darkcidr.high")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".darkcidr-high")
	}
	return ""
}
func loadHigh() int {
	p := highScorePath()
	if p == "" {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}
func saveHigh(n int) {
	if p := highScorePath(); p != "" {
		_ = os.WriteFile(p, []byte(strconv.Itoa(n)), 0o644)
	}
}

// --- drawing ----------------------------------------------------------------

func rlen(s string) int { return len([]rune(s)) }

func (g *Game) p(x, y, fg, bg int, s string)           { g.scr.Print(x, y, dos.Attr(fg, bg), s) }
func (g *Game) st(x, y int, ch rune, fg, bg int)       { g.scr.Set(x, y, ch, dos.Attr(fg, bg)) }
func (g *Game) box(x, y, w, h, fg, bg int, d bool)     { g.scr.Box(x, y, w, h, dos.Attr(fg, bg), d) }
func (g *Game) ttl(x, y, w, fg, bg int, t string)      { g.scr.Title(x, y, w, dos.Attr(fg, bg), t) }
func (g *Game) fill(x, y, w, h int, ch rune, f, b int) { g.scr.Fill(x, y, w, h, ch, dos.Attr(f, b)) }
func (g *Game) meter(x, y, w int, frac float64, fg, tr, bg int) {
	g.scr.Meter(x, y, w, frac, fg, tr, bg)
}
func (g *Game) center(y, fg, bg int, s string) { g.p((g.w-rlen(s))/2, y, fg, bg, s) }

// --- slick bits: braille animations + sub-cell-smooth bars -----------------

var brailleSpin = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func (g *Game) spinner() rune { return brailleSpin[(g.frame/2)%len(brailleSpin)] }

// eighths gives sub-cell fill for buttery progress bars.
var eighths = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// sbar draws a smooth horizontal bar: full blocks plus a fractional last cell.
func (g *Game) sbar(x, y, w int, frac float64, fg, track, bg int) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	total := frac * float64(w)
	full := int(total)
	for i := 0; i < w; i++ {
		switch {
		case i < full:
			g.st(x+i, y, '█', fg, bg)
		case i == full:
			if ch := eighths[int((total-float64(full))*8)]; ch != ' ' {
				g.st(x+i, y, ch, fg, bg)
			} else {
				g.st(x+i, y, '░', track, bg)
			}
		default:
			g.st(x+i, y, '░', track, bg)
		}
	}
}

// brailleWave paints an animated carrier waveform in a single row.
func (g *Game) brailleWave(x, y, w, fg, bg int) {
	ramp := []rune{'⠁', '⠉', '⠋', '⠛', '⠟', '⠿', '⡿', '⣿'}
	for i := 0; i < w; i++ {
		v := (math.Sin(float64(i)*0.5-float64(g.frame)*0.4) + 1) / 2
		g.st(x+i, y, ramp[int(v*float64(len(ramp)-1))], fg, bg)
	}
}

func (g *Game) draw() {
	blink := (g.frame>>4)&1 == 0
	g.scr.Clear(dos.Attr(lgray, black))
	switch g.screen {
	case scrTitle:
		g.drawTitle(blink)
	case scrMap:
		g.drawMap(blink)
	case scrInf:
		g.drawInf(blink)
	case scrWin:
		g.drawEnd(blink, true)
	case scrLose:
		g.drawEnd(blink, false)
	case scrHelp:
		g.drawHelp()
	}
}

// Frame returns the current screen as plain text (used by tests).
func (g *Game) Frame() string { return g.scr.Plain() }

var font = map[rune][]string{
	'T': {"█████", "  █  ", "  █  ", "  █  ", "  █  "},
	'O': {"█████", "█   █", "█   █", "█   █", "█████"},
	'N': {"█   █", "██  █", "█ █ █", "█  ██", "█   █"},
	'E': {"█████", "█    ", "███  ", "█    ", "█████"},
	'S': {"█████", "█    ", "█████", "    █", "█████"},
	'R': {"████ ", "█   █", "████ ", "█  █ ", "█   █"},
	'M': {"█   █", "██ ██", "█ █ █", "█   █", "█   █"},
	'U': {"█   █", "█   █", "█   █", "█   █", " ███ "},
	'D': {"████ ", "█   █", "█   █", "█   █", "████ "},
	'A': {" ███ ", "█   █", "█████", "█   █", "█   █"},
	'K': {"█   █", "█  █ ", "███  ", "█  █ ", "█   █"},
	'C': {"█████", "█    ", "█    ", "█    ", "█████"},
	'I': {"█████", "  █  ", "  █  ", "  █  ", "█████"},
	' ': {"  ", "  ", "  ", "  ", "  "},
}

func banner(word string) []string {
	rows := []string{"", "", "", "", ""}
	for _, ch := range word {
		gl := font[ch]
		if gl == nil {
			gl = font[' ']
		}
		for r := 0; r < 5; r++ {
			rows[r] += gl[r] + " "
		}
	}
	return rows
}

func (g *Game) drawTitle(blink bool) {
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			ch := ' '
			if y%2 == 1 {
				ch = '░'
			}
			g.st(x, y, ch, dgray, black)
		}
	}
	// Anchor the splash to the vertical centre so it sits nicely on tall screens.
	top := (g.h - 25) / 2
	if top < 0 {
		top = 0
	}
	logo := banner("DARKCIDR")
	lw := rlen(logo[0])
	lx := (g.w - lw) / 2
	pal := []int{lgreen, lcyan, cyan, lblue, green}
	for r := 0; r < 5; r++ {
		g.p(lx, top+2+r, pal[(int(g.frame/3)+r)%len(pal)], black, logo[r])
	}
	// A braille carrier-wave sweeping under the logo.
	g.brailleWave(lx, top+7, lw, lcyan, black)
	g.center(top+9, yellow, black, "war-dial the whole internet · find the carrier · breach the core")
	g.center(top+11, white, black, "SCAN with nerva  ::  BREACH with brutus tools & password sprays")
	g.center(top+12, white, black, "PIVOT deeper, RANK UP, steal rivals' kits  ::  reach the MAINFRAME, go DARK")
	g.center(top+14, lred, black, "but a TRACE crawls home while you're on the wire --")
	g.center(top+15, lred, black, "let it reach HOME and you're BUSTED. go dark to cool it.")
	mask := g.seed.Mask
	if mask == "" {
		mask = "?"
	}
	source := "synthetic"
	if g.seed.Meta != nil {
		if v, ok := g.seed.Meta["source"].(string); ok && v != "" {
			source = v
		}
	}
	g.center(top+16, dgray, black, "world: "+mask+"   seed: "+source+"   diff: "+g.diff.normalized().Name)
	if blink {
		g.center(top+19, lgreen, black, "> > >   PRESS  ENTER  TO  JACK IN   < < <")
	}
	g.center(top+21, yellow, black, "BEST HAUL  "+dos.Right(strconv.Itoa(g.high), 7)+" cr")
	g.p(0, g.h-1, black, lgray, dos.Pad(" ENTER:start   ?:help   arrows/jk:move   Q:quit", g.w))
}

func nodeIcon(n *node) (rune, int) {
	switch n.state {
	case "owned":
		return '●', lgreen
	case "discovered":
		return '◌', lcyan
	}
	return '?', dgray
}

func (g *Game) drawMap(blink bool) {
	// Responsive layout: the network panel takes all the width left of a fixed
	// 30-col right column; the panels stretch to fill the height, with three
	// fixed rows at the bottom (log, command bar, message).
	rightW := 30
	listW := g.w - rightW
	panelH := g.h - 4 // panels occupy rows 1 .. g.h-4
	statusH := 7      // the Status box at the bottom of the right column
	detailH := panelH - statusH
	listRows := panelH - 2 // selectable rows inside the Network box
	rightX := listW        // left edge of the right column
	logRow, cmdRow, msgRow := g.h-3, g.h-2, g.h-1

	hb := red
	live := "○ DARK"
	if g.connected {
		hb = green
		live = "● LIVE"
	}
	g.p(0, 0, black, hb, dos.Pad(" DARKCIDR  "+live+"   credits:"+strconv.Itoa(g.credits)+
		"   tools:"+strconv.Itoa(len(g.toolIds()))+"   rank:"+strconv.Itoa(g.skill)+
		"   kits:"+strconv.Itoa(g.stolenKits)+"   ["+g.diff.normalized().Name+"]   ESC:title", g.w))

	g.box(0, 1, listW, panelH, lcyan, blue, true)
	g.ttl(0, 1, listW, yellow, blue, "Network")
	list := g.curList()
	if g.sel >= len(list) {
		g.sel = len(list) - 1
	}
	if g.sel < 0 {
		g.sel = 0
	}
	if g.sel < g.scroll {
		g.scroll = g.sel
	}
	if g.sel >= g.scroll+listRows {
		g.scroll = g.sel - listRows + 1
	}
	innerW := listW - 2
	stateX, flagX, pwX := listW-5, listW-3, listW-2
	for i := 0; i < listRows; i++ {
		if i+g.scroll >= len(list) {
			break
		}
		e := list[i+g.scroll]
		n := e.n
		y := 2 + i
		sel := i+g.scroll == g.sel
		bg := blue
		if sel {
			bg = cyan
		}
		ic, icc := nodeIcon(n)
		gl := kindGlyph[n.kind]
		if gl == 0 {
			gl = '■'
		}
		host := n.host
		if host == "" {
			host = n.ip
		}
		idx := i + g.scroll
		pre := ""
		for k := 1; k < e.depth; k++ {
			pre += "│ "
		}
		if e.depth > 0 {
			last := idx+1 >= len(list) || list[idx+1].depth < e.depth
			if last {
				pre += "└─"
			} else {
				pre += "├─"
			}
		}
		glX := 1 + rlen(pre)
		line := pre + string(gl) + " " + host
		fg := lgray
		if sel {
			fg = black
		}
		g.p(1, y, fg, bg, dos.Pad(line, innerW))
		gfg := white
		if sel {
			gfg = black
		}
		g.st(glX, y, gl, gfg, bg) // recolour the glyph in place (no double)
		icf := icc
		if sel {
			icf = black
		}
		g.st(stateX, y, ic, icf, bg)
		switch {
		case n.objective:
			c := lred
			if sel {
				c = black
			}
			g.st(flagX, y, '⚑', c, bg)
		case n.looted:
			c := dgray
			if sel {
				c = black
			}
			g.st(flagX, y, '·', c, bg)
		case n.loot > 0 && n.state == "owned":
			c := yellow
			if sel {
				c = black
			}
			g.st(flagX, y, '$', c, bg)
		}
		if g.knownPw[n.id] {
			c := lmag
			if sel {
				c = black
			}
			g.st(pwX, y, '⚷', c, bg)
		}
	}

	// Node detail (right top).
	n := g.selNode()
	g.box(rightX, 1, rightW, detailH, lgray, black, true)
	g.ttl(rightX, 1, rightW, white, black, "Node")
	if n != nil {
		y := 3
		info := func(l, v string, c int) {
			g.p(rightX+2, y, yellow, black, l)
			g.p(rightX+2+rlen(l), y, c, black, v)
			y++
		}
		info("host : ", n.host, white)
		info("ip   : ", n.ip, white)
		typ := n.kind
		if n.objective {
			typ += "  ⚑OBJECTIVE"
		} else if n.honeypot && n.state == "discovered" {
			typ += "  ⚠trap?" // a deduction reward for reading the detail
		}
		info("type : ", typ, white)
		sc := dgray
		if n.state == "owned" {
			sc = lgreen
		} else if n.state == "discovered" {
			sc = lcyan
		}
		info("state: ", n.state, sc)
		if n.kind != "home" && n.kind != "uplink" {
			shieldStr := strconv.Itoa(n.shieldCur)
			if n.state == "owned" {
				shieldStr = "--"
			}
			info("shield:", shieldStr+"/"+strconv.Itoa(n.shield), white)
			lootStr := strconv.Itoa(n.loot) + " cr"
			if n.looted {
				lootStr += " (taken)"
			}
			info("loot : ", lootStr, white)
		}
		y++
		g.p(rightX+2, y, lcyan, black, "services:")
		y++
		maxSvc := detailH - y // rows left before the box border
		shown := 0
		for _, pt := range n.ports {
			if shown >= maxSvc || shown >= 8 {
				break
			}
			c := lgray
			if isLogin(pt.svc) {
				c = lmag
			}
			g.p(rightX+3, y, c, black, dos.Pad(pt.svc+"/"+strconv.Itoa(pt.port), rightW-6))
			y++
			shown++
		}
		if len(n.ports) == 0 {
			g.p(rightX+3, y, dgray, black, "(none)")
		} else {
			// Surface a scanned banner -- intel the recon actually pulled, and a hint
			// at how soft the box is.
			for _, pt := range n.ports {
				if pt.banner != "" && y < 1+detailH-1 {
					g.p(rightX+2, y, dgray, black, dos.Pad("❧ "+pt.banner, rightW-3))
					y++
					break
				}
			}
		}
		// Edges: where this box can pivot. Shows the live topology -- known
		// neighbours, color-coded by state -- so deeper paths are legible.
		y++
		if y < 1+detailH-1 {
			g.p(rightX+2, y, lcyan, black, "links →")
			y++
			lx := rightX + 3
			for _, li := range n.links {
				if y >= 1+detailH-1 {
					break
				}
				m := g.world.nodes[li]
				if m.state == "hidden" {
					continue
				}
				ic, icc := nodeIcon(m)
				if lx+2 > rightX+rightW-1 {
					lx = rightX + 3
					y++
					if y >= 1+detailH-1 {
						break
					}
				}
				g.st(lx, y, ic, icc, black)
				lx += 2
			}
		}
	}

	// Status + trace (right bottom).
	sy := 1 + detailH
	g.box(rightX, sy, rightW, statusH, lred, black, true)
	g.ttl(rightX, sy, rightW, white, black, "Status")
	g.p(rightX+2, sy+1, yellow, black, "link : ")
	lc := lred
	ltxt := "DARK"
	if g.connected {
		lc = lgreen
		ltxt = "LIVE"
	}
	g.p(rightX+9, sy+1, lc, black, ltxt)
	g.p(rightX+2, sy+2, yellow, black, "depth: ")
	g.p(rightX+9, sy+2, white, black, strconv.Itoa(g.ownedDepth()))
	tc := traceColor(g.trace)
	tlab := lred
	if g.trace > 85 && blink { // flash the warning as the trace closes in
		tlab = white
	}
	g.p(rightX+2, sy+3, tlab, black, "TRACE→HOME")
	g.meter(rightX+2, sy+4, rightW-4, g.trace/100, tc, dgray, black)
	tail := ""
	if g.objectiveTaken {
		tail = "  *OBJ! GO DARK*"
	}
	g.p(rightX+2, sy+5, tc, black, dos.Right(strconv.Itoa(int(g.trace))+"%", 4)+tail)

	last := ""
	if len(g.logLines) > 0 {
		last = g.logLines[len(g.logLines)-1]
	}
	g.p(0, logRow, lgreen, black, dos.Pad("» "+last, g.w))
	act := ""
	if n2 := g.selNode(); n2 != nil {
		switch n2.state {
		case "owned":
			act = "ENTER/S:scan  L:loot"
		case "discovered":
			act = "ENTER/I:infiltrate"
		default:
			act = "?"
		}
	}
	rtxt := "connect"
	if g.connected {
		rtxt = "go dark"
	}
	g.p(0, cmdRow, black, lgray, dos.Pad(" "+act+"   R:"+rtxt+"   arrows:move   ?:help   ESC:title", g.w))
	g.p(0, msgRow, black, black, dos.Pad("", g.w))
	if time.Now().Before(g.msgUntil) && g.msg != "" {
		a := lcyan
		if g.heatSpike > 0 {
			a = yellow
			if blink {
				a = lred
			}
		}
		g.center(msgRow, a, black, g.msg)
	}
}

func traceColor(t float64) int {
	switch {
	case t > 80:
		return lred
	case t > 55:
		return yellow
	}
	return lgreen
}

func (g *Game) drawInf(blink bool) {
	inf := g.inf
	if inf == nil {
		g.screen = scrMap
		return
	}
	n := inf.n

	// The breach UI is a fixed 80x19 panel (under a full-width header, above the
	// command bar); centre it within the larger terminal.
	ox := (g.w - 80) / 2
	if ox < 0 {
		ox = 0
	}
	dy := (g.h - 3 - 19) / 2 // rows 1..19 of the panel, centred between header and bars
	if dy < 0 {
		dy = 0
	}
	P := func(x, y, fg, bg int, s string) { g.p(ox+x, dy+y, fg, bg, s) }
	B := func(x, y, w, h, fg, bg int, d bool) { g.box(ox+x, dy+y, w, h, fg, bg, d) }
	T := func(x, y, w, fg, bg int, t string) { g.ttl(ox+x, dy+y, w, fg, bg, t) }
	M := func(x, y, w int, frac float64, fg, tr, bg int) { g.meter(ox+x, dy+y, w, frac, fg, tr, bg) }

	g.p(0, 0, black, red, dos.Pad(" INFILTRATE  "+n.host+"  ("+n.ip+")   ESC:abort", g.w))

	B(0, 1, 40, 8, lred, black, true)
	T(0, 1, 40, white, black, "Target")
	P(2, 3, yellow, black, "SHIELD")
	M(9, 3, 28, float64(n.shieldCur)/math.Max(1, float64(n.shield)), lcyan, dgray, black)
	P(2, 4, white, black, strconv.Itoa(n.shieldCur)+" / "+strconv.Itoa(n.shield))
	P(2, 5, yellow, black, "ALARM ")
	M(9, 5, 28, float64(n.alert)/math.Max(1, float64(n.alertMax)), lred, dgray, black)
	trap := ""
	if n.honeypot {
		trap = "   ☠ feels like a trap..."
	}
	P(2, 6, white, black, strconv.Itoa(n.alert)+" / "+strconv.Itoa(n.alertMax)+trap)

	B(40, 1, 40, 8, lgray, black, true)
	T(40, 1, 40, white, black, "Vectors")
	vecs := n.ports
	if len(vecs) == 0 {
		vecs = []port{{svc: "*", port: 0}}
	}
	for i, p := range vecs {
		if i >= 5 {
			break
		}
		selv := i == inf.vec
		fg := lgray
		if isLogin(p.svc) {
			fg = lmag
		}
		bg := black
		if selv {
			fg, bg = black, cyan
		}
		mark := " "
		if selv {
			mark = "►"
		}
		P(42, 3+i, fg, bg, dos.Pad(mark+p.svc+"/"+strconv.Itoa(p.port), 36))
	}

	B(0, 9, 40, 10, lgreen, black, true)
	T(0, 9, 40, white, black, "Tools / Exploits")
	ids := g.toolIds()
	svc := g.curVecSvc(n, inf.vec)
	for i, id := range ids {
		if i >= 6 { // panel fits 6 exploit rows; deeper kits scroll off honestly
			break
		}
		t := tools[id]
		selt := i == inf.tool
		ok := applies(t, svc)
		c := dgray
		if ok {
			c = lgreen
		}
		bg := black
		if selt {
			c, bg = black, green
		}
		lim := ""
		if t.limited {
			lim = " x" + strconv.Itoa(g.inv[id])
		}
		mark := " "
		if selt {
			mark = "►"
		}
		P(2, 11+i, c, bg, dos.Pad(mark+string(t.glyph)+" "+t.name+lim, 24))
		oc := yellow
		if selt {
			oc = black
		}
		odds := "n/a"
		if ok {
			odds = strconv.Itoa(int(math.Round(t.odds*100))) + "%"
		}
		P(27, 11+i, oc, bg, odds)
	}
	if n.hasLogin {
		P(2, 17, lmag, black, "[G] brutus password spray")
	}

	B(40, 9, 40, 10, lcyan, black, true)
	T(40, 9, 40, white, black, "Session")
	lg := inf.log
	if len(lg) > 7 {
		lg = lg[len(lg)-7:]
	}
	for i, l := range lg {
		c := lgray
		if strings.Contains(l, "BREACH") || strings.Contains(l, "GRANTED") {
			c = lgreen
		} else if strings.Contains(l, "IDS") || strings.Contains(l, "denied") {
			c = lred
		}
		P(42, 11+i, c, black, dos.Pad(l, 36))
	}

	tc := traceColor(g.trace)
	P(0, 19, lred, black, "TRACE ")
	M(6, 19, 40, g.trace/100, tc, dgray, black)
	P(47, 19, tc, black, strconv.Itoa(int(g.trace))+"%")

	// Educational: what the highlighted tool actually is.
	if inf.mode != "pw" && len(ids) > 0 && dy+20 < g.h-2 {
		ti := inf.tool
		if ti > len(ids)-1 {
			ti = len(ids) - 1
		}
		d := tools[ids[ti]].desc
		P((80-rlen(d))/2, 20, lcyan, black, d)
	}

	if inf.mode == "pw" {
		g.fill(ox+10, dy+8, 60, 11, ' ', white, blue)
		B(10, 8, 60, 11, lcyan, blue, true)
		T(10, 8, 60, yellow, blue, "Guess Password")
		list := g.pwList(n)
		for i, p := range list {
			sel := i == inf.pwSel
			known := g.knownPw[n.id] && p == n.secret
			fg := white
			if known {
				fg = lgreen
			}
			bg := blue
			if sel {
				fg, bg = black, cyan
			}
			mark := "  "
			if sel {
				mark = "► "
			}
			intel := ""
			if known {
				intel = "   ← intel"
			}
			P(14, 10+i, fg, bg, dos.Pad(mark+p+intel, 52))
		}
		P(12, 17, lgray, blue, "ENTER:try   ESC:back   ↑↓:choose")
	}
	g.p(0, g.h-2, black, lgray, dos.Pad(" ↑↓:tool  ←→:vector  ENTER:attack  G:password  ESC:abort", g.w))
	if time.Now().Before(g.msgUntil) && g.msg != "" {
		c := lred
		if blink {
			c = yellow
		}
		g.center(g.h-1, c, black, g.msg)
	}
}

func (g *Game) drawEnd(blink, won bool) {
	hi := lred
	if won {
		hi = lgreen
	}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			ch := ' '
			if y%2 == 1 {
				ch = '▒'
			}
			c := red
			if won {
				c = green
			}
			g.st(x, y, ch, c, black)
		}
	}
	top := (g.h - 25) / 2
	if top < 0 {
		top = 0
	}
	bar := strings.Repeat("█", 40)
	g.center(top+5, hi, black, bar)
	hc := hi
	if blink {
		hc = white
	}
	if won {
		g.center(top+6, hc, black, "   D A T A   E X F I L T R A T E D   ")
	} else {
		g.center(top+6, hc, black, "   C O N N E C T I O N   T R A C E D   ")
	}
	g.center(top+7, hi, black, bar)
	if won {
		g.center(top+9, yellow, black, "You vanished into the noise. Clean job.")
	} else {
		g.center(top+9, yellow, black, "They kicked your door in. BUSTED.")
	}
	g.center(top+12, white, black, "Credits looted   "+strconv.Itoa(g.credits))
	if won {
		g.center(top+13, lcyan, black, "Stealth bonus    "+strconv.Itoa(max(0, int(math.Round((100-g.trace)*10)))))
	}
	g.center(top+15, lgreen, black, "SCORE   "+strconv.Itoa(g.score))
	rname, rglyph, rcol := rankFor(g.score)
	g.center(top+16, rcol, black, "RANK  "+string(rglyph)+" "+rname)
	label := "BEST HAUL  "
	if g.score >= g.high {
		label = "** NEW BEST HAUL **  "
	}
	g.center(top+17, yellow, black, label+strconv.Itoa(g.high))
	if blink {
		g.center(top+20, white, black, "PRESS ENTER TO RUN AGAIN")
	}
	g.p(0, g.h-1, black, lgray, dos.Pad(" ENTER:new run   ESC:title   Q:quit", g.w))
}

// rankFor maps a final score to a hacker handle, its icon, and colour.
func rankFor(score int) (string, rune, int) {
	switch {
	case score >= 1200:
		return "GHOST IN THE WIRE", '★', lmag
	case score >= 800:
		return "ICE BREAKER", '◆', lcyan
	case score >= 450:
		return "PHREAK", '▣', lgreen
	case score >= 150:
		return "SCRIPT KIDDIE", '■', yellow
	default:
		return "LAMER", '·', dgray
	}
}

func (g *Game) drawHelp() {
	g.box(2, 1, g.w-4, g.h-2, lcyan, black, true)
	g.ttl(2, 1, g.w-4, yellow, black, "DARKCIDR -- how to run")
	L := []string{
		"",
		" GOAL  Pivot deeper, exfiltrate the MAINFRAME (⚑), then go DARK to bank it.",
		"       Each box you breach is a foothold to the next. Don't get traced.",
		"",
		" THE TRACE is an IDS/SOC tracing your live link back to HOME. The deeper",
		"       you sit and the longer you stay LIVE, the faster it climbs -- 100%",
		"       means BUSTED. R goes DARK (disconnect): safe & cooling, but it can",
		"       cost your deepest foothold. Honeypots (☠) spike the trace.",
		"",
		" LOOP  S/ENTER  scan an OWNED node to reveal neighbours   (discovery)",
		"       I/ENTER  infiltrate a DISCOVERED node             (exploitation)",
		"       L        loot credits, tools & password intel ⚷  (credential reuse)",
		"       R        toggle LIVE / DARK   (DARK cools trace, may drop a pivot)",
		"",
		" GROW  every box you own raises your RANK and unlocks sharper exploits.",
		"       loot rivals' machines for STOLEN kits (RCE/0-day) keyed to what",
		"       they ran. work fast LIVE, exfil deep, then vanish before busted.",
		"",
		" BREACH pick a vector + tool; ENTER drops SHIELD. Misses raise the ALARM",
		"       (login lockout) -- max it and you're kicked. For logins, G sprays",
		"       passwords (a looted ⚷ reveals the right one).",
		"",
		" It's a toy, but the moves are real: recon, lateral movement, creds, IDS.",
		"",
		"       ENTER: back to the run",
		"",
		" LEGEND ⌂ home  ↑ uplink  # gateway  ■ workstation  ▦ fileserver  ◰ webserver",
		"        ✉ mailserver  ▤ database  ★ datacentre / MAINFRAME",
		"        ● owned  ◌ discovered  ? hidden   ⚑ objective  $ loot  ⚷ creds  ☠ trap",
	}
	// Draw only what fits inside the box (the legend is the first to be cut on a
	// short terminal).
	for i, l := range L {
		if 2+i > g.h-3 {
			break
		}
		c := lgray
		switch {
		case strings.HasPrefix(l, " GOAL"), strings.HasPrefix(l, " THE TRACE"),
			strings.HasPrefix(l, " LOOP"), strings.HasPrefix(l, " BREACH"),
			strings.HasPrefix(l, " LEGEND"):
			c = yellow
		case strings.Contains(l, "moves are real"):
			c = lcyan
		}
		g.p(4, 2+i, c, black, l)
	}
}
