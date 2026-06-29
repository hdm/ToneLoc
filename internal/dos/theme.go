package dos

// theme.go adds a thin truecolor layer on top of the 16-colour VGA palette: RGB
// colour tokens, linear interpolation, multi-stop ramps, and a few named
// "elevated retro" accents. None of it changes the default look -- existing code
// keeps using the palette constants -- but it lets the map and host views paint
// smooth heatmaps and neon gradients with SetRGB.

// PaletteRGB returns the 24-bit colour token for a VGA palette index, so palette
// colours can be blended with truecolor ones.
func PaletteRGB(idx int) uint32 {
	p := dosRGB[idx&0x0F]
	return RGB(p[0], p[1], p[2])
}

// Lerp linearly interpolates between two colour tokens (t clamped to [0,1]).
func Lerp(a, b uint32, t float64) uint32 {
	if t <= 0 {
		return a | rgbSet
	}
	if t >= 1 {
		return b | rgbSet
	}
	ar, ag, ab := RGBval(a)
	br, bg, bb := RGBval(b)
	return RGB(
		lerpByte(ar, br, t),
		lerpByte(ag, bg, t),
		lerpByte(ab, bb, t),
	)
}

func lerpByte(a, b byte, t float64) byte {
	return byte(float64(a) + (float64(b)-float64(a))*t + 0.5)
}

// Ramp samples a multi-stop gradient at t in [0,1]. With one stop it returns it;
// with none it returns black.
func Ramp(stops []uint32, t float64) uint32 {
	switch len(stops) {
	case 0:
		return RGB(0, 0, 0)
	case 1:
		return stops[0] | rgbSet
	}
	if t <= 0 {
		return stops[0] | rgbSet
	}
	if t >= 1 {
		return stops[len(stops)-1] | rgbSet
	}
	seg := t * float64(len(stops)-1)
	i := int(seg)
	if i >= len(stops)-1 {
		i = len(stops) - 2
	}
	return Lerp(stops[i], stops[i+1], seg-float64(i))
}

// Dim scales a colour's brightness by f (0 = black, 1 = unchanged).
func Dim(token uint32, f float64) uint32 {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	r, g, b := RGBval(token)
	return RGB(byte(float64(r)*f), byte(float64(g)*f), byte(float64(b)*f))
}

// Mix blends two colours 50/50 (handy for cursor highlights).
func Mix(a, b uint32) uint32 { return Lerp(a, b, 0.5) }

// "Elevated retro" accent tokens -- neon takes on the CGA hues, used for
// gradients, headers, and the map heat ramp.
var (
	NeonGreen   = RGB(0x39, 0xFF, 0x14)
	NeonCyan    = RGB(0x00, 0xF0, 0xFF)
	NeonMagenta = RGB(0xFF, 0x2B, 0xD6)
	NeonAmber   = RGB(0xFF, 0xB0, 0x00)
	NeonRed     = RGB(0xFF, 0x3B, 0x3B)
	DeepBlue    = RGB(0x08, 0x12, 0x2B)
	Phosphor    = RGB(0x9C, 0xFF, 0xC4) // soft CRT green-white
	Ink         = RGB(0x05, 0x07, 0x0C) // near-black backdrop
	Ash         = RGB(0x1a, 0x1e, 0x26) // dim grid backdrop
)

// HeatRamp maps a 0..1 "open density" to a dark->green->cyan->white phosphor
// ramp, for the map's saturation heatmap.
var HeatRamp = []uint32{
	RGB(0x0a, 0x16, 0x12),
	RGB(0x0f, 0x5a, 0x32),
	RGB(0x1d, 0x9e, 0x55),
	RGB(0x39, 0xff, 0x6e),
	RGB(0x9c, 0xff, 0xe0),
}

// Heat samples HeatRamp at t in [0,1].
func Heat(t float64) uint32 { return Ramp(HeatRamp, t) }
