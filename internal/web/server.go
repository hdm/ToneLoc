// Package web serves the ToneLoc UI in a browser. The browser runs ghostty.js
// (the ghostty-web WASM terminal, xterm.js-compatible) and connects back over a
// WebSocket; the server runs one engine + TUI render loop per connection and
// streams the very same ANSI/VT output the local terminal would draw. Keys typed
// in the browser flow back over the socket to the engine. No raw sockets, no
// privileges -- the simulator backend means the demo works for anyone.
package web

import (
	"context"
	_ "embed"
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

	fmt.Printf("ToneLoc/Go web -- open http://%s/ in your browser\n", friendly(addr))
	fmt.Printf("Backend: %s   Mask: %s   Ports: %v\n", job.Backend, job.Mask.Text(), job.Ports)
	return http.ListenAndServe(addr, mux)
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
