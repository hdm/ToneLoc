// Package web serves the ToneLoc UI in a browser. The browser runs ghostty.js
// (the ghostty-web WASM terminal, xterm.js-compatible) and connects back over a
// WebSocket; the server runs one engine + TUI render loop per connection and
// streams the very same ANSI/VT output the local terminal would draw. Keys typed
// in the browser flow back over the socket to the engine. No raw sockets, no
// privileges -- the simulator backend means the demo works for anyone.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/websocket"

	"github.com/hdm/toneloc/internal/engine"
	"github.com/hdm/toneloc/internal/tui"
)

//go:embed static/index.html
var indexHTML []byte

// sessions maps a per-connection id to its live engine, so the /save and /load
// HTTP endpoints can reach the scan a given browser tab is running.
var sessions sync.Map // id -> *engine.Engine

// Options controls how the HTTP(S) server listens.
//
//   - plain HTTP:   {Addr: ":8080"}
//   - static certs: {Addr: ":443", CertFile: "...", KeyFile: "..."}
//   - ACME (Let's Encrypt): {TLS: true, Domain: "scan.example.com"}
type Options struct {
	Addr     string
	TLS      bool   // enable HTTPS (ACME unless CertFile/KeyFile are set)
	Domain   string // ACME domain to issue a certificate for
	CertFile string // static certificate (overrides ACME)
	KeyFile  string // static key (overrides ACME)
}

// Serve starts the ghostty.js web UI. Every WebSocket client gets a fresh,
// independent scan of the same job.
func Serve(opts Options, job engine.Job) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		serveSession(conn, job)
	}))
	mux.HandleFunc("/save", handleSave)
	mux.HandleFunc("/load", handleLoad)

	fmt.Printf("Backend: %s   Mask: %s   Ports: %v\n", job.Backend, maskLabel(job), job.Ports)
	return listen(opts, mux, "web UI")
}

// ServeGameFS serves the standalone JS game from an embedded filesystem, so the
// single binary needs no game/ directory on disk.
func ServeGameFS(opts Options, fsys fs.FS) error {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(fsys))
	fmt.Println("(serving the standalone JS game from embedded assets; no backend)")
	return listen(opts, mux, "game")
}

// listen binds the server per Options: ACME-issued HTTPS for a domain, static
// cert/key, or plain HTTP.
func listen(opts Options, mux http.Handler, label string) error {
	scheme := "http"
	addr := opts.Addr

	switch {
	case opts.CertFile != "" && opts.KeyFile != "":
		if addr == "" {
			addr = ":443"
		}
		scheme = "https"
		fmt.Printf("ToneLoc/Go %s -- %s://%s/  (cert %s)\n", label, scheme, friendly(addr), opts.CertFile)
		srv := &http.Server{Addr: addr, Handler: mux}
		return srv.ListenAndServeTLS(opts.CertFile, opts.KeyFile)

	case opts.TLS && opts.Domain != "":
		if addr == "" {
			addr = ":443"
		}
		scheme = "https"
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(opts.Domain),
			Cache:      autocert.DirCache("toneloc-acme"),
		}
		// ACME HTTP-01 challenges + redirect to HTTPS on :80.
		go func() { _ = http.ListenAndServe(":80", m.HTTPHandler(nil)) }()
		fmt.Printf("ToneLoc/Go %s -- %s://%s/  (ACME cert for %s; needs :80 and :443 reachable)\n",
			label, scheme, opts.Domain, opts.Domain)
		srv := &http.Server{Addr: addr, Handler: mux, TLSConfig: m.TLSConfig()}
		return srv.ListenAndServeTLS("", "")

	default:
		if addr == "" {
			addr = ":8080"
		}
		fmt.Printf("ToneLoc/Go %s -- open http://%s/ in your browser\n", label, friendly(addr))
		return http.ListenAndServe(addr, mux)
	}
}

var _ = tls.VersionTLS12

func maskLabel(job engine.Job) string {
	if job.Mask != nil {
		return job.Mask.Text()
	}
	if len(job.Masks) > 0 {
		return fmt.Sprintf("%s (+%d more)", job.Masks[0].Text(), len(job.Masks)-1)
	}
	return "?"
}

// handleSave streams the session's current .DAT state as a download.
func handleSave(w http.ResponseWriter, r *http.Request) {
	v, ok := sessions.Load(r.URL.Query().Get("s"))
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	name, blob := v.(*engine.Engine).ExportDat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Write(blob)
}

// handleLoad merges an uploaded .DAT into the session's running scan.
func handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a .DAT body", http.StatusMethodNotAllowed)
		return
	}
	v, ok := sessions.Load(r.URL.Query().Get("s"))
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	n, err := v.(*engine.Engine).ImportDat(data)
	if err != nil {
		http.Error(w, "bad data file: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"merged":%d}`, n)
}

func newSessionID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func serveSession(conn *websocket.Conn, job engine.Job) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng, err := engine.New(ctx, job)
	if err != nil {
		websocket.Message.Send(conn, "\x1b[2J\x1b[Herror: "+err.Error()+"\r\n")
		return
	}

	// Register the session so /save and /load can find this engine, and tell the
	// browser its id via a private OSC cue (stripped client-side).
	id := newSessionID()
	sessions.Store(id, eng)
	defer sessions.Delete(id)
	websocket.Message.Send(conn, "\x1b]1338;"+id+"\x07")

	go eng.Run(ctx)

	out := &wsWriter{conn: conn}
	app := tui.New(eng, out)

	keys := make(chan byte, 64)
	// Reader: browser keystrokes -> engine. Resize control messages
	// (ESC ]1339;COLS;ROWS BEL) are intercepted and applied to the app so the
	// UI fills the browser terminal rather than a fixed 80x25.
	go func() {
		defer close(keys)
		for {
			var msg []byte
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				cancel()
				return
			}
			for _, b := range parseResize(msg, app) {
				select {
				case keys <- b:
				default:
				}
			}
		}
	}()

	if err := app.Run(ctx, keys); err != nil && err != io.EOF {
		log.Printf("session ended: %v", err)
	}
}

// resizeRE matches the browser's resize control sequence: ESC ]1339;COLS;ROWS BEL.
var resizeRE = regexp.MustCompile("\x1b\\]1339;(\\d+);(\\d+)\x07")

// parseResize applies any resize control sequences in msg to the app and
// returns the remaining bytes (keystrokes).
func parseResize(msg []byte, app *tui.App) []byte {
	if !bytes.Contains(msg, []byte("\x1b]1339;")) {
		return msg
	}
	for _, m := range resizeRE.FindAllSubmatch(msg, -1) {
		w, _ := strconv.Atoi(string(m[1]))
		h, _ := strconv.Atoi(string(m[2]))
		if w > 0 && h > 0 {
			app.Resize(w, h)
		}
	}
	return resizeRE.ReplaceAll(msg, nil)
}

// wsWriter adapts a websocket connection to io.Writer, sending each render as a
// single text frame so ghostty.js can write() it straight into the terminal.
type wsWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := websocket.Message.Send(w.conn, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func friendly(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "localhost" + addr
	}
	return addr
}
