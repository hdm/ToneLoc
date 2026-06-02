// Package web serves the ToneLoc UI in a browser. The browser runs ghostty.js
// (the ghostty-web WASM terminal, xterm.js-compatible) and connects back over a
// WebSocket; the server runs one engine + TUI render loop per connection and
// streams the very same ANSI/VT output the local terminal would draw. Keys typed
// in the browser flow back over the socket to the engine. No raw sockets, no
// privileges -- the simulator backend means the demo works for anyone.
package web

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/hdm/toneloc/internal/engine"
	"github.com/hdm/toneloc/internal/tui"
)

//go:embed static/index.html
var indexHTML []byte

// sessions maps a per-connection id to its live engine, so the /save and /load
// HTTP endpoints can reach the scan a given browser tab is running.
var sessions sync.Map // id -> *engine.Engine

// Serve starts the HTTP server on addr. Every WebSocket client gets a fresh,
// independent scan of the same job.
func Serve(addr string, job engine.Job) error {
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

	fmt.Printf("ToneLoc/Go web -- open http://%s/ in your browser\n", friendly(addr))
	fmt.Printf("Backend: %s   Mask: %s   Ports: %v\n", job.Backend, maskLabel(job), job.Ports)
	return http.ListenAndServe(addr, mux)
}

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

// ServeGame serves the standalone, server-free JavaScript game (the game/
// directory: index.html, toneloc.js, seed.js) as static files. The game needs
// no backend -- this is purely a convenience so you don't have to open the file
// by hand. The same files also run straight from file:// in any browser.
func ServeGame(addr, dir string) error {
	fmt.Printf("ToneLoc/Go THE GAME -- open http://%s/ in your browser\n", friendly(addr))
	fmt.Printf("(static files from %s; no backend, the simulation runs in your browser)\n", dir)
	return http.ListenAndServe(addr, http.FileServer(http.Dir(dir)))
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

	keys := make(chan byte, 64)
	// Reader: browser keystrokes -> engine.
	go func() {
		defer close(keys)
		for {
			var msg []byte
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				cancel()
				return
			}
			for _, b := range msg {
				select {
				case keys <- b:
				default:
				}
			}
		}
	}()

	out := &wsWriter{conn: conn}
	app := tui.New(eng, out)
	if err := app.Run(ctx, keys); err != nil && err != io.EOF {
		log.Printf("session ended: %v", err)
	}
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
