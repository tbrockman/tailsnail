package theme

import "strings"

// Theme is a complete colour scheme. Both built-in themes define every field,
// so a screen never has to reason about which theme is active.
type Theme struct {
	Name string
	// Desc is the one-line description shown on the settings screen.
	Desc string

	Bg      RGB // page background
	Panel   RGB // raised surface
	Fg      RGB // primary text
	Dim     RGB // secondary text
	Faint   RGB // borders, rules, disabled text
	Accent  RGB // focus, selection, the tailsnail brand colour
	Accent2 RGB // secondary highlight
	Ok      RGB
	Warn    RGB
	Err     RGB

	// Arena colours.
	Grid RGB // the empty playfield
	Wall RGB // arena boundary
	Food RGB

	// Players holds one colour per seat. They are chosen to stay separable
	// after downsampling to 16 colours, and each seat also gets its own glyph
	// so hue is never the only signal.
	Players [8]RGB
}

// Neon is the default theme: a dark surface with saturated player colours.
var Neon = Theme{
	Name: "neon",
	Desc: "dark, high-contrast, saturated player colours",

	Bg:      RGB{0x0e, 0x10, 0x18},
	Panel:   RGB{0x18, 0x1c, 0x28},
	Fg:      RGB{0xe6, 0xe9, 0xf2},
	Dim:     RGB{0xae, 0xb6, 0xcc},
	Faint:   RGB{0x6b, 0x75, 0x90},
	Accent:  RGB{0x4a, 0xe0, 0xa8},
	Accent2: RGB{0x7a, 0x9c, 0xff},
	Ok:      RGB{0x5c, 0xd6, 0x7a},
	Warn:    RGB{0xf0, 0xb4, 0x4a},
	Err:     RGB{0xf0, 0x60, 0x70},

	Grid: RGB{0x1a, 0x1e, 0x2c},
	Wall: RGB{0x3e, 0x48, 0x66},
	Food: RGB{0xff, 0x8a, 0x3d},

	Players: [8]RGB{
		{0x45, 0xe0, 0x8a}, // green
		{0x7a, 0x9c, 0xff}, // blue
		{0xff, 0x7a, 0xb8}, // pink
		{0xf5, 0xd0, 0x4a}, // yellow
		{0xb4, 0x8a, 0xff}, // violet
		{0x4a, 0xd8, 0xe8}, // cyan
		{0xff, 0x9a, 0x5a}, // orange
		{0xd8, 0xe4, 0xf0}, // pale
	},
}

// Mono is the second theme: a restrained near-monochrome scheme that leans on
// brightness and glyph shape rather than hue. It is the better choice on a
// light terminal, on a projector, or for anyone who finds Neon busy.
var Mono = Theme{
	Name: "mono",
	Desc: "restrained greys; players separated by shape and brightness",

	Bg:      RGB{0x12, 0x12, 0x14},
	Panel:   RGB{0x1e, 0x1e, 0x22},
	Fg:      RGB{0xec, 0xec, 0xf0},
	Dim:     RGB{0xb4, 0xb4, 0xbe},
	Faint:   RGB{0x78, 0x78, 0x84},
	Accent:  RGB{0xf0, 0xf0, 0xf4},
	Accent2: RGB{0xb8, 0xb8, 0xc4},
	Ok:      RGB{0xd0, 0xd8, 0xd0},
	Warn:    RGB{0xd8, 0xd0, 0xb0},
	Err:     RGB{0xe8, 0xc0, 0xc0},

	Grid: RGB{0x1a, 0x1a, 0x1e},
	Wall: RGB{0x50, 0x50, 0x58},
	Food: RGB{0xff, 0xff, 0xff},

	Players: [8]RGB{
		{0xff, 0xff, 0xff},
		{0xc8, 0xc8, 0xd4},
		{0x9a, 0x9a, 0xa6},
		{0xe4, 0xd8, 0xc0},
		{0xc0, 0xd0, 0xe4},
		{0xd8, 0xc0, 0xd4},
		{0xb0, 0xc4, 0xb0},
		{0x88, 0x88, 0x94},
	},
}

// All returns the selectable themes in display order.
func All() []Theme { return []Theme{Neon, Mono} }

// ByName returns a theme by name, falling back to Neon.
func ByName(name string) Theme {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, t := range All() {
		if t.Name == target {
			return t
		}
	}
	return Neon
}

// Player returns the colour for a palette slot, wrapping if a seat index ever
// exceeds the palette.
func (t Theme) Player(slot int) RGB {
	if slot < 0 {
		slot = 0
	}
	return t.Players[slot%len(t.Players)]
}
