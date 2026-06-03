//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/hdm/toneloc/internal/tui"
)

// watchTerminalResize follows SIGWINCH and pushes the new terminal size to the
// app so the UI fills the window as it grows/shrinks.
func watchTerminalResize(fd int, app *tui.App) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	for range ch {
		if w, h, err := term.GetSize(fd); err == nil {
			app.Resize(w, h)
		}
	}
}
