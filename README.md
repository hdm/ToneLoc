# ToneLoc/Go

> *"ToneLoc is short for Tone Locator, and is a bit of a wild thing. What it
> does is simple: it dials numbers, looking for some kind of tone."*
> — Minor Threat & Mucho Maas, 1994

**ToneLoc/Go** is a loving port of the legendary MS-DOS war-dialer to Go — but
instead of dialing **telephone numbers** looking for carriers and tones, it
"dials" **IPv4 addresses and ports** looking for open services, using the
[`zmap-go`](https://github.com/hdm/zmap-go) scanner under the hood. The screen
keeps the original's 90s DOS look: the **Activity Log**, **Modem**, and
**Statistics** windows, the progress meter, the blinking status line — all of
it. And there's a **web version** that renders in your browser through
[`ghostty.js`](https://github.com/coder/ghostty-web).

The original C source (TONELOC.C, the CXL windowing library, the FOSSIL serial
drivers, the sample `.DAT` files) is preserved untouched in this repo as a
historical artifact. `README.txt` is the original 1994 user manual.

```
╔══════════════► Activity Log ◄══════════════╗╔═══════════► Modem ◄════════════╗
║ 08:24:18 ToneLoc started on 02-Jun-26       ║║ SSH-2.0-OpenSSH_9.6            ║
║ 08:24:18 Mask used:   204.13.7.X            ║║ 220 FTP ready                 ║
║ 08:24:18 Ports:       23,80,443             ║║                               ║
║ 08:24:19 204.13.7.43:80   - No Dialtone #1  ║╚════════════════════════════════╝
║ 08:24:20 204.13.7.74:80   - Timeout (6)     ║╔═════════► Statistics ◄═════════╗
║ 08:24:21 204.13.7.18:23   - * CARRIER *     ║║  Started : 08:24:18            ║
║ 08:24:22 204.13.7.91:443  - ** TONE **      ║║  MaxDials: 768   Dialed: 142   ║
║ ...                                         ║║  Dials/Hr: 16559  ETA: 00:02   ║
╚═════════════════════════════════════════════╝╟────────────►Found◄────────────╢
Dialing: 204.13.7.113:443 ████████████▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒
```

## From phone lines to TCP/IP

Every concept in the original maps cleanly onto network scanning:

| ToneLoc (1994)            | ToneLoc/Go                                   |
|---------------------------|----------------------------------------------|
| Phone mask `555-1XXX`     | IP mask `192.168.1.X` or CIDR `10.0.0.0/24`  |
| Random, non-repeating dial| zmap-go's cyclic-group iterator              |
| **Carrier** (a modem!)    | TCP **SYN/ACK** — the port is open            |
| **Tone**                  | open port that volunteers a **banner**        |
| **Busy**                  | TCP **RST** — connection refused              |
| **No Dialtone**           | ICMP unreachable / no route to host           |
| **Ringout** / **Timeout** | deadline reached / silence                    |
| The modem (COM port)      | the scan backend (`sim`, `connect`, `zmap`)   |

## Install / build

ToneLoc/Go imports [`zmap-go`](https://github.com/hdm/zmap-go) as a normal Go
module, so a single command installs it:

```sh
go install github.com/hdm/toneloc/cmd/toneloc@latest
# or, from a clone:  go build -o toneloc ./cmd/toneloc   (or: make)
```

Requires Go 1.26+ (brutus needs it). nerva, brutus and zmap-go are all imported as Go modules, so this is a single self-contained binary.

## Usage

```text
toneloc <DataFile|Mask> [/M:mask] [/R:lo-hi] [/X:exmask] [/p:ports] [flags]
```

**With no arguments**, ToneLoc auto-detects every local network this machine is
attached to and sweeps them with a real TCP connect scan, hitting the most
common ports first (80, 443, 22, 135, 445, 3389, ...):

```sh
toneloc            # scan all local networks, common ports first
```

**Masks** are the IP equivalent of the old `555-1XXX` phone masks — each `X` is
a wildcard octet, and the space is dialed in random, non-repeating order. The
scan is swept one network at a time, one port at a time (common ports first):

```sh
toneloc 192.168.1.X /p:22,80,443        # dial .0-.255 on three ports
toneloc 10.0.X.X --connect --wait 2s    # a /16, real TCP connect scan
toneloc 198.51.100.0/24 --zmap          # real zmap SYN scan (needs root)
toneloc CORP /M:172.16.0.X /R:1-50      # bound the last octet to .1-.50
toneloc 203.0.113.X --web :9000         # serve the UI in a browser
```

### Scan backends (`--backend`)

* **`sim`** *(default)* — a self-contained simulator. No privileges, no
  network, no external binary. This is what powers the web demo and lets the
  full DOS experience run for anyone, anywhere.
* **`connect`** — a real TCP `connect()` scan via the Go runtime. Needs no
  special privileges. Reads banners (→ tones).
* **`zmap`** — drives the **real zmap scanner** from the `zmap-go` module
  (`tcp_synscan`). This is a raw-packet stateless mass scan, so it needs
  `root`/`cap_net_raw` and a live network; if that's unavailable ToneLoc
  transparently falls back to the simulator.

### The recon pipeline (nerva + brutus)

Open TCP ports are only the start. ToneLoc runs a four-tool pipeline:

| Tool | Role |
|------|------|
| **connect / zmap** | find open **TCP** ports |
| **[nerva](https://github.com/praetorian-inc/nerva)** | find open **UDP** ports, and **fingerprint** the application/banner on every service |
| **[brutus](https://github.com/praetorian-inc/brutus)** | test **common credentials** against any service whose protocol it supports |

nerva and brutus are **compiled in as Go libraries** — ToneLoc is a single
self-contained binary, no external tools to install. In `sim` mode (and the
game) it uses built-in simulators instead, so the whole flow still works with
no network.

Every discovered service is summarized in the **Services** view (cycle with
**M**). Select one and press **ENTER** for full detail; press **B** to launch
**brutus** against it in the background — its progress shows live (`[bruting N]`)
and a valid credential marks the service **`** PWNED **`**.

### Sessions (`--restore`)

Every scan writes a resumable **session log** (`toneloc-<id>.session`) capturing
the dialed targets, discovered services, nerva fingerprints, and brutus
results. The id is shown at startup; resume anytime with:

```sh
toneloc --restore <id>
```

### Default behaviour

Run **`toneloc`** with no arguments and it immediately enters full-screen
terminal mode and sweeps **every local network** this machine is on — `zmap` if
it can open raw sockets, otherwise `connect` — with **nerva** discovering UDP
services and fingerprinting as it goes, common ports first.

In every mode the **target ordering itself comes from zmap-go** — ToneLoc/Go
walks the `(address, port)` space with zmap-go's cyclic multiplicative-group
iterator, the exact machinery the real scanner uses to permute the IPv4 space.
It's a remarkably good fit for the original program's "never dial the same
random number twice" promise.

### Keys while dialing

```
ESC quit   SPACE abort   P pause   R redial   S speaker   X +5s wait
N/C/F/G/V/Y annotate the current number
M or TAB   cycle views: Dialer -> ToneMap -> Hall of Fame -> Services
in Services:  j/k or arrows select   ENTER detail   B run brutus
```

### Hall of Fame

Cycle to the **Hall of Fame** (M, or press **F**) for the trophy case: every
carrier and tone the scan has bagged, with the banner each one coughed up,
scrollable with **j/k** or the arrow keys.

### ToneMap

Press **M** (or **Tab**) to flip from the dialer to the **ToneMap** — a homage to
the original `TONEMAP.EXE`. It draws the entire scan space as a dense grid, one
cell per address (auto-downsampled for big ranges), coloured by the most
interesting verdict found there:

```
█ Carrier   █ Tone   ▓ Busy   ▓ Voice   ▒ No Dialtone   ▒ Ringout   ░ Timeout   · Undialed
```

A large arrow cursor follows your **mouse** (or the **arrow keys** / **hjkl**),
and the address and verdict under it are shown at the bottom — mouse reporting
works the same in a real terminal and in the ghostty.js web view.

### Resuming scans (.DAT files)

Like the original, results are written to a `<DataFile>.DAT` file and reloaded
on the next run: already-dialed targets are skipped and the stats/ToneMap show
the prior history, so you can stop and pick a scan back up. It autosaves every
15 seconds and on exit.

## The web version (ghostty.js)

```sh
toneloc 203.0.113.X --web :8080
# → open http://localhost:8080/
```

### HTTPS / TLS

For a publicly reachable instance, serve over HTTPS. With `--tls --domain` the
server gets a free **Let's Encrypt (ACME)** certificate automatically — it
listens on `:443` and answers HTTP-01 challenges on `:80` (both must be
reachable from the internet):

```sh
toneloc 203.0.113.X --web --tls --domain scan.example.com
```

Or bring your own certificate instead of ACME:

```sh
toneloc 203.0.113.X --web --tls-cert fullchain.pem --tls-key privkey.pem
```

Both work for the game too (`toneloc --game --tls --domain play.example.com`).
The web UI and the game are served from assets **embedded in the binary**, so
there are no files to deploy alongside it.

The browser loads [`ghostty-web`](https://github.com/coder/ghostty-web) — the
xterm.js-compatible WASM build of Ghostty's VT100 emulator — from a CDN and
opens a WebSocket back to the server. The server runs the **same** TUI render
loop per connection and streams the identical ANSI/VT output a local terminal
would draw; keystrokes flow back over the socket. The page goes full retro: a
matrix hex-rain backdrop, a CRT power-on animation, VGA scanlines, chromatic
aberration with periodic glitch tearing and TV static, a faux-CRT bezel, and a
fake BIOS/POST boot sequence before the carrier connects. It also synthesizes
**modem audio** with WebAudio — dial tones, busy signals, and the unmistakable
handshake screech on a carrier (toggle with **S**). The page scales the monitor
to fill the window (⛶ for true fullscreen), and the **save** / **load** buttons
let you download the current scan state (`.DAT`) and upload a previous one to
resume — merged straight into the running scan. (If `ghostty-web` can't be
reached, the page falls back to `xterm.js` automatically.)

## Games (standalone, no server)

The [`game/`](game/) directory holds **server-free JavaScript games** — the
whole ToneLoc frontend (an 80×25 DOS renderer drawn to `<canvas>`, the CRT/glitch
shell, modem sounds) reimplemented in the browser. No backend, no network: they
run straight from `file://`.

### NETRUNNER — `game/index.html`

A network-infiltration roguelike:

> Start at **HOME**. **Scan** to find nodes; **breach** them with a turn-based
> exploit fight (pick tools, guess passwords); use each foothold to **pivot**
> deeper for **loot** and intel. But the moment you're live on the wire a
> **TRACE** crawls back along your connection toward HOME — let it reach your
> home node while you're connected and you're **BUSTED**. Grab the **MAINFRAME**
> and go **DARK** to win. Deeper footholds and longer sessions trace faster;
> honeypots (☠) spike it; disconnect (R) to cool down and bank your loot.

Loop: **S** scan an owned node · **I** infiltrate a discovered one · **L** loot ·
**R** go dark / reconnect · arrows/**jk** move · **?** help. In a breach: pick a
vector and a tool, **ENTER** to attack and drop the target's SHIELD without
maxing its ALARM; **G** to guess the password (looted intel ⚷ reveals it).

### Arcade dialer — `game/arcade.html`

The original arcade take: war-dial blocks of the net for carriers/tones to
score and clear a quota before a TRACE meter hits 100%. **M** cycles
Dialer/ToneMap/Hall of Fame, **C** clears logs, **B** boosts.

### The seeded world

Both games are **seeded by real-zmap-format data**: `game/seed.js` is
produced by the `toneloc-seed` tool, which walks the address space with
zmap-go's cyclic iterator (the same permutation the real scanner uses) — or
ingests an actual `zmap` scan:

```sh
go build -o toneloc-seed ./cmd/toneloc-seed

# synthesize a world from zmap-go's iterator (reproducible):
./toneloc-seed -mask 10.37.0.0/16 > game/seed.js

# ...or seed it from a REAL zmap scan:
zmap -p 22,23,80,443 -O csv -f saddr,sport,classification,success 198.51.100.0/24 \
    | ./toneloc-seed -csv -mask 198.51.100.0/24 > game/seed.js
```

Play it with **zero dependencies** — just open the file:

```sh
# double-click game/index.html, or:
xdg-open game/index.html            # / open on macOS
# or serve it (also works) :
./toneloc --game :8090              # → http://localhost:8090/
python3 -m http.server -d game 8090 # any static server works too
```

High scores are kept in `localStorage`.

## How it fits together

```
cmd/toneloc            CLI: ToneLoc-style /M /R /X /p args, raw-tty input, --web
internal/engine        scan engine: IP masks, zmap-go iterator, response
                       classification, the sim/connect/zmap backends, the
                       nerva+brutus recon pipeline (tools.go/nerva.go/brutus.go/
                       recon.go), services registry, sessions (session.go),
                       live stats, the ToneMap grid, and .DAT persistence
internal/dos           an 80x25, 16-colour MS-DOS text-mode renderer
                       (CP437 box-drawing, blink, a diffing ANSI flusher, SVG)
internal/tui           the 3-window dialer + the ToneMap view, keyboard and
                       mouse handling (arrow/SGR-mouse escape parsing)
internal/web           HTTP + WebSocket bridge to ghostty.js; static game server
cmd/toneloc-seed       builds game/seed.js from zmap-go's iterator or a zmap CSV
game/                  standalone JS games: NETRUNNER (index.html) + arcade dialer
                       (arcade.html), sharing the canvas DOS renderer and seed.js
```

## Legal / ethical note

Port scanning networks you do not own or have explicit permission to test may
be illegal. The default `sim` backend touches no network at all. Use the
`connect` and `zmap` backends only against systems you are authorised to scan.

---

*ToneLoc rocks, and if you don't read the docs, you're a LAMER.*
