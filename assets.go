// Package toneloc embeds the assets the single binary ships with -- currently
// the standalone JavaScript game in game/. Keeping the embed at the module root
// lets it reach game/ (go:embed cannot traverse "..").
package toneloc

import (
	"embed"
	"io/fs"
)

//go:embed all:game
var gameAssets embed.FS

// GameFS returns the embedded standalone game (index.html, netrun.js,
// toneloc.js, seed.js, ...) as a filesystem rooted at the game directory.
func GameFS() (fs.FS, error) { return fs.Sub(gameAssets, "game") }
