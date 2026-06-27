//go:build !unix

package main

import "github.com/hdm/toneloc/internal/tui"

// watchTerminalResize is a no-op where SIGWINCH is unavailable (e.g. Windows);
// the initial size is still applied at startup.
func watchTerminalResize(fd int, app *tui.App) {}
