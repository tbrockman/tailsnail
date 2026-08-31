package theme

// Glyphs is the character set used to draw the arena and chrome. Two sets
// exist: a Unicode one using box-drawing and geometric shapes, and a pure
// ASCII one selected by --ascii for terminals and fonts that render the
// geometric block inconsistently.
//
// No emoji appear in either set. Emoji width is unreliable across terminals,
// and a mis-measured cell corrupts every column after it.
type Glyphs struct {
	ASCII bool

	// Logo is the application icon: the snail emoji where the terminal is known
	// to render it at the width it reports, and a plain glyph everywhere else.
	// It is never empty, so the header keeps its shape either way.
	Logo string

	// Heads holds one distinctive glyph per palette slot. Shape carries the
	// player's identity on its own, so the game stays playable with NO_COLOR
	// set or on a 16-colour terminal where two players may land on similar hues.
	Heads [8]string
	// Body is a full body segment; Tail is the last one or two segments, drawn
	// lighter so the direction of travel reads at a glance.
	Body string
	Tail string
	// Dead marks a snake's final cell during the death flash.
	Dead string
	// Food cycles through FoodPulse for the pellet's breathing animation.
	FoodPulse []string
	// Empty is the unoccupied playfield cell.
	Empty string
	// Spark is the particle drawn briefly where a snake died.
	Spark []string

	// Box drawing for panels and the arena frame.
	TopLeft, TopRight, BottomLeft, BottomRight string
	Horizontal, Vertical                       string

	// Chrome.
	Bullet   string
	Check    string
	Cross    string
	Arrow    string
	Ellipsis string
	// PointLeft, PointRight and PointUp tie a popover to the text it
	// describes, whichever side there was room on.
	PointLeft, PointRight, PointUp string
	// Spinner frames for connecting and scanning states.
	Spinner []string
	// Meter fills a progress or countdown bar.
	MeterFull, MeterEmpty string
}

// Unicode is the default glyph set.
var Unicode = Glyphs{
	Heads: [8]string{"●", "◆", "■", "▲", "★", "▼", "◉", "✚"},
	Body:  "█",
	Tail:  "▓",
	Dead:  "▒",
	// A pellet that grows and shrinks: empty ring, ring, heavy ring, disc.
	FoodPulse: []string{"◌", "○", "◎", "●"},
	Empty:     " ",
	Spark:     []string{"✹", "✵", "✲", "·"},

	TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯",
	Horizontal: "─", Vertical: "│",

	Bullet: "•", Check: "✓", Cross: "✗", Arrow: "›", Ellipsis: "…",
	PointLeft: "◂", PointRight: "▸", PointUp: "▴",
	// A shell-like spiral stands in for the snail where emoji cannot be
	// trusted to measure correctly.
	Logo:      "◎",
	Spinner:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	MeterFull: "█", MeterEmpty: "░",
}

// ASCIIGlyphs is the fallback set selected by --ascii. Every glyph is a plain
// 7-bit character, so it renders identically everywhere.
var ASCIIGlyphs = Glyphs{
	ASCII:     true,
	Heads:     [8]string{"@", "%", "#", "^", "*", "v", "O", "+"},
	Body:      "o",
	Tail:      ".",
	Dead:      "x",
	FoodPulse: []string{".", "o", "O", "0"},
	Empty:     " ",
	Spark:     []string{"*", "+", ".", " "},

	TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	Horizontal: "-", Vertical: "|",

	Bullet: "*", Check: "x", Cross: "-", Arrow: ">", Ellipsis: "...",
	PointLeft: "<", PointRight: ">", PointUp: "^",
	Logo:      "@",
	Spinner:   []string{"|", "/", "-", "\\"},
	MeterFull: "#", MeterEmpty: ".",
}

// SnailIcon is the application's emoji icon, used where emoji are supported.
const SnailIcon = "🐌"

// Set returns the glyph set for the requested modes. ASCII mode always wins
// over emoji: someone asking for plain ASCII does not want a snail either.
//
// Emoji is not a preference the user is asked about — it is either going to
// render correctly or it is not — so this is purely a capability decision, and
// there is always an icon of some kind.
func Set(ascii, emoji bool) Glyphs {
	if ascii {
		return ASCIIGlyphs
	}
	g := Unicode
	if emoji {
		g.Logo = SnailIcon
	}
	return g
}

// Head returns the head glyph for a palette slot.
func (g Glyphs) Head(slot int) string {
	if slot < 0 {
		slot = 0
	}
	return g.Heads[slot%len(g.Heads)]
}

// Food returns the pellet glyph for an animation phase in [0,1).
func (g Glyphs) Food(phase float64) string {
	return pick(g.FoodPulse, phase)
}

// Spin returns the spinner frame for an animation phase in [0,1).
func (g Glyphs) Spin(phase float64) string {
	return pick(g.Spinner, phase)
}

// Ember returns the death-particle glyph for a fade progress in [0,1], where 0
// is the instant of death and 1 is fully faded.
func (g Glyphs) Ember(progress float64) string {
	return pick(g.Spark, clamp01(progress)*0.999)
}

// pick indexes a frame list by a phase in [0,1).
func pick(frames []string, phase float64) string {
	if len(frames) == 0 {
		return " "
	}
	phase -= float64(int(phase)) // keep only the fractional part
	if phase < 0 {
		phase += 1
	}
	i := int(phase * float64(len(frames)))
	if i >= len(frames) {
		i = len(frames) - 1
	}
	return frames[i]
}
