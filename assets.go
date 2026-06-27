// Package darkcidr embeds the data the single binary ships with -- currently the
// DARKCIDR game world seed (game/seed.js). Keeping the embed at the module root
// lets it reach game/ (go:embed cannot traverse "..").
package darkcidr

import _ "embed"

//go:embed game/seed.js
var seedJS []byte

// SeedJS returns the embedded game world seed (window.TONELOC_SEED = {...};),
// from which the terminal game builds its network.
func SeedJS() []byte { return seedJS }
