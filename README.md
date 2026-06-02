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

ToneLoc/Go is built against its sibling scanner module via a `replace`
directive, so check the two repos out **side by side**:

```sh
git clone https://github.com/hdm/zmap-go
git clone https://github.com/hdm/toneloc ToneLoc
cd ToneLoc
go build -o toneloc ./cmd/toneloc     # or: make
```

Requires Go 1.25+.

## Usage

```text
toneloc <DataFile|Mask> [/M:mask] [/R:lo-hi] [/X:exmask] [/p:ports] [flags]
```

**Masks** are the IP equivalent of the old `555-1XXX` phone masks — each `X` is
a wildcard octet, and the space is dialed in random, non-repeating order:

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
* **`zmap`** — drives the **real zmap scanner** built from the sibling
  `zmap-go` module (`tcp_synscan`). This is a raw-packet stateless mass scan,
  so it needs `root`/`cap_net_raw` and a live network; if that's unavailable
  ToneLoc transparently falls back to the simulator.

In every mode the **target ordering itself comes from zmap-go** — ToneLoc/Go
walks the `(address, port)` space with zmap-go's cyclic multiplicative-group
iterator, the exact machinery the real scanner uses to permute the IPv4 space.
It's a remarkably good fit for the original program's "never dial the same
random number twice" promise.

### Keys while dialing

```
ESC quit   SPACE abort   P pause   R redial   S speaker   X +5s wait
N/C/F/G/V/Y annotate the current number
M or TAB   cycle views: Dialer -> ToneMap -> Hall of Fame   (F jumps to Hall of Fame)
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

The browser loads [`ghostty-web`](https://github.com/coder/ghostty-web) — the
xterm.js-compatible WASM build of Ghostty's VT100 emulator — from a CDN and
opens a WebSocket back to the server. The server runs the **same** TUI render
loop per connection and streams the identical ANSI/VT output a local terminal
would draw; keystrokes flow back over the socket. The page goes full retro: a
matrix hex-rain backdrop, a CRT power-on animation, VGA scanlines, chromatic
aberration with periodic glitch tearing and TV static, a faux-CRT bezel, and a
fake BIOS/POST boot sequence before the carrier connects. It also synthesizes
**modem audio** with WebAudio — dial tones, busy signals, and the unmistakable
handshake screech on a carrier (toggle with **S**). (If `ghostty-web` can't be
reached, the page falls back to `xterm.js` automatically.)

## ToneLoc: THE GAME (standalone, no server)

There's also a complete **server-free JavaScript game** in [`game/`](game/) — the
whole ToneLoc frontend (DOS renderer, dialer, ToneMap, Hall of Fame, CRT/glitch
shell, modem sounds) reimplemented in the browser, played as an arcade hacker
game:

> **War-dial the net before the Feds trace you.** Find carriers and tones to
> score and clear each level's quota while a **TRACE** meter climbs. Honeypots
> spike it. Hit 100% and you're **BUSTED**. Burn an **evade** (C) to clear your
> logs; **boost** (B) to dial faster at the cost of more heat.

The game world is **seeded by real-zmap-format data**: `game/seed.js` is
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

Controls: **ENTER** start · **M** cycle views · **F** hall of fame · **C**
clear logs (evade) · **B** boost · **P** pause · **S** sound · **ESC** title.
High score is kept in `localStorage`.

## How it fits together

```
cmd/toneloc            CLI: ToneLoc-style /M /R /X /p args, raw-tty input, --web
internal/engine        scan engine: IP masks, zmap-go iterator, response
                       classification, the sim/connect/zmap backends, live
                       stats, the ToneMap grid, and .DAT persistence
internal/dos           an 80x25, 16-colour MS-DOS text-mode renderer
                       (CP437 box-drawing, blink, a diffing ANSI flusher, SVG)
internal/tui           the 3-window dialer + the ToneMap view, keyboard and
                       mouse handling (arrow/SGR-mouse escape parsing)
internal/web           HTTP + WebSocket bridge to ghostty.js; static game server
cmd/toneloc-seed       builds game/seed.js from zmap-go's iterator or a zmap CSV
game/                  the standalone, server-free JavaScript arcade game
```

## Legal / ethical note

Port scanning networks you do not own or have explicit permission to test may
be illegal. The default `sim` backend touches no network at all. Use the
`connect` and `zmap` backends only against systems you are authorised to scan.

---

*ToneLoc rocks, and if you don't read the docs, you're a LAMER.*
