// Package theme owns everything about how tailsnail looks: colour depth
// detection and downsampling, the two visual themes, and the glyph sets used
// to draw the arena.
//
// Colour is expressed once, in truecolor RGB, and downsampled at render time
// to whatever the terminal actually supports. That keeps the themes readable
// as data and puts all the fallback logic in one tested place.
package theme

import (
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Mode is the colour depth to render at.
type Mode string

// The supported colour modes. Auto resolves to one of the others at startup.
const (
	ModeAuto      Mode = "auto"
	ModeTrueColor Mode = "truecolor"
	Mode256       Mode = "256"
	Mode16        Mode = "16"
	// ModeNone disables colour entirely. It is what NO_COLOR selects, and the
	// reason every player also gets a distinct glyph rather than only a hue.
	ModeNone Mode = "none"
)

// ParseMode validates a --color value.
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeAuto:
		return ModeAuto, true
	case ModeTrueColor:
		return ModeTrueColor, true
	case Mode256:
		return Mode256, true
	case Mode16:
		return Mode16, true
	case ModeNone:
		return ModeNone, true
	}
	return ModeAuto, false
}

// ModeNames lists the accepted --color values, for help text and errors.
func ModeNames() []string {
	return []string{string(ModeAuto), string(ModeTrueColor), string(Mode256), string(Mode16), string(ModeNone)}
}

// Env is the slice of the environment that terminal detection reads. It is a
// struct so the logic can be tested without touching the real environment.
type Env struct {
	NoColor     string
	ColorTerm   string
	Term        string
	TermProgram string
	Locale      string
}

// EnvFromOS reads the relevant variables from the process environment.
func EnvFromOS() Env {
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LC_CTYPE")
	}
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	return Env{
		NoColor:     os.Getenv("NO_COLOR"),
		ColorTerm:   os.Getenv("COLORTERM"),
		Term:        os.Getenv("TERM"),
		TermProgram: os.Getenv("TERM_PROGRAM"),
		Locale:      locale,
	}
}

// EmojiMode controls whether the interface uses emoji.
type EmojiMode string

// The emoji modes. Auto applies the detection below.
const (
	EmojiAuto EmojiMode = "auto"
	EmojiOn   EmojiMode = "on"
	EmojiOff  EmojiMode = "off"
)

// ParseEmojiMode validates an --emoji value.
func ParseEmojiMode(s string) (EmojiMode, bool) {
	switch EmojiMode(strings.ToLower(strings.TrimSpace(s))) {
	case EmojiAuto:
		return EmojiAuto, true
	case EmojiOn:
		return EmojiOn, true
	case EmojiOff:
		return EmojiOff, true
	}
	return EmojiAuto, false
}

// EmojiModeNames lists the accepted --emoji values.
func EmojiModeNames() []string {
	return []string{string(EmojiAuto), string(EmojiOn), string(EmojiOff)}
}

// emojiTermPrograms are terminal emulators known to render emoji at the width
// they advertise. The list is deliberately conservative: a terminal that
// disagrees with us about how many cells an emoji occupies shifts everything
// after it on the line, which is worse than simply not showing a snail.
var emojiTermPrograms = map[string]bool{
	"iterm.app":      true,
	"apple_terminal": true,
	"wezterm":        true,
	"vscode":         true,
	"ghostty":        true,
	"hyper":          true,
	"warp":           true,
	"rio":            true,
	"tabby":          true,
	"kitty":          true,
	"alacritty":      true,
}

// emojiTerms are TERM values from emulators that set TERM rather than
// TERM_PROGRAM.
var emojiTerms = []string{"kitty", "alacritty", "wezterm", "ghostty", "contour", "foot", "rio"}

// EmojiSupported reports whether emoji are likely to render correctly.
//
// There is no capability query for this, so it is a heuristic: a UTF-8 locale
// is required, and the terminal has to be one known to handle emoji width.
// Anything unrecognised is assumed not to, because the cost of guessing wrong
// is a sheared line rather than a missing decoration.
func EmojiSupported(env Env) bool {
	locale := strings.ToLower(env.Locale)
	if !strings.Contains(locale, "utf-8") && !strings.Contains(locale, "utf8") {
		return false
	}
	term := strings.ToLower(env.Term)
	if term == "" || term == "dumb" || term == "linux" || strings.HasPrefix(term, "vt") {
		return false
	}
	if emojiTermPrograms[strings.ToLower(env.TermProgram)] {
		return true
	}
	for _, t := range emojiTerms {
		if strings.Contains(term, t) {
			return true
		}
	}
	return false
}

// ResolveEmoji turns a requested emoji mode into a decision.
func ResolveEmoji(requested EmojiMode, env Env) bool {
	switch requested {
	case EmojiOn:
		return true
	case EmojiOff:
		return false
	default:
		return EmojiSupported(env)
	}
}

// Resolve turns a requested mode into a concrete one.
//
// NO_COLOR always wins, whatever was asked for: the convention is that its
// mere presence disables colour, and honouring it is cheap accessibility.
func Resolve(requested Mode, env Env) Mode {
	if env.NoColor != "" {
		return ModeNone
	}
	if requested != ModeAuto {
		return requested
	}
	ct := strings.ToLower(env.ColorTerm)
	if strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit") {
		return ModeTrueColor
	}
	term := strings.ToLower(env.Term)
	switch {
	case term == "" || term == "dumb":
		return ModeNone
	case strings.Contains(term, "256color"), strings.Contains(term, "direct"):
		return Mode256
	case strings.Contains(term, "truecolor"):
		return ModeTrueColor
	}
	return Mode16
}

// Profile maps a mode onto the termenv profile. Applying it to lipgloss makes
// any styling that does not go through RGB.TermColor — a border, a help bar —
// degrade in exactly the same way as the arena does.
func (m Mode) Profile() termenv.Profile {
	switch m {
	case ModeTrueColor:
		return termenv.TrueColor
	case Mode256:
		return termenv.ANSI256
	case Mode16:
		return termenv.ANSI
	default:
		return termenv.Ascii
	}
}

// Apply installs the mode as lipgloss's global colour profile.
func (m Mode) Apply() { lipgloss.SetColorProfile(m.Profile()) }

// RGB is a 24-bit colour.
type RGB struct {
	R, G, B uint8
}

// Hex renders the colour as #rrggbb.
func (c RGB) Hex() string {
	const digits = "0123456789abcdef"
	out := []byte("#000000")
	for i, v := range [3]uint8{c.R, c.G, c.B} {
		out[1+i*2] = digits[v>>4]
		out[2+i*2] = digits[v&0x0f]
	}
	return string(out)
}

// Lerp blends towards other by t in [0,1].
func (c RGB) Lerp(other RGB, t float64) RGB {
	t = clamp01(t)
	return RGB{
		R: lerpByte(c.R, other.R, t),
		G: lerpByte(c.G, other.G, t),
		B: lerpByte(c.B, other.B, t),
	}
}

// Scale multiplies brightness, clamping at full intensity. Values above 1
// brighten, below 1 dim.
func (c RGB) Scale(f float64) RGB {
	return RGB{R: scaleByte(c.R, f), G: scaleByte(c.G, f), B: scaleByte(c.B, f)}
}

// Mix averages two colours evenly.
func (c RGB) Mix(other RGB) RGB { return c.Lerp(other, 0.5) }

// Luminance returns perceived brightness in [0,1], used to keep text legible
// against generated backgrounds.
func (c RGB) Luminance() float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}

// TermColor converts to the lipgloss colour for the given mode. ModeNone
// returns the empty colour, which lipgloss renders as no escape at all.
func (c RGB) TermColor(m Mode) lipgloss.TerminalColor {
	switch m {
	case ModeTrueColor:
		return lipgloss.Color(c.Hex())
	case Mode256:
		return lipgloss.Color(itoa(int(c.ANSI256())))
	case Mode16:
		return lipgloss.Color(itoa(int(c.ANSI16())))
	default:
		return lipgloss.NoColor{}
	}
}

// ANSI256 downsamples to an xterm-256 index, choosing between the 6×6×6 colour
// cube and the 24-step grey ramp by whichever is closer.
func (c RGB) ANSI256() uint8 {
	cubeIdx, cubeRGB := nearestCube(c)
	greyIdx, greyRGB := nearestGrey(c)
	if dist2(c, cubeRGB) <= dist2(c, greyRGB) {
		return cubeIdx
	}
	return greyIdx
}

// cubeLevels are the six channel values the xterm colour cube uses.
var cubeLevels = [6]uint8{0, 95, 135, 175, 215, 255}

// nearestCube returns the closest colour-cube index and the colour it renders.
func nearestCube(c RGB) (uint8, RGB) {
	r := nearestLevel(c.R)
	g := nearestLevel(c.G)
	b := nearestLevel(c.B)
	idx := 16 + 36*r + 6*g + b
	return uint8(idx), RGB{cubeLevels[r], cubeLevels[g], cubeLevels[b]}
}

// nearestLevel finds the closest cube level for one channel.
func nearestLevel(v uint8) int {
	best, bestDist := 0, math.MaxInt32
	for i, level := range cubeLevels {
		d := int(v) - int(level)
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// nearestGrey returns the closest greyscale-ramp index and its colour.
func nearestGrey(c RGB) (uint8, RGB) {
	avg := (int(c.R) + int(c.G) + int(c.B)) / 3
	// The ramp runs 232..255 at values 8, 18, ... 238.
	step := (avg - 8 + 5) / 10
	if step < 0 {
		step = 0
	}
	if step > 23 {
		step = 23
	}
	v := uint8(8 + step*10)
	return uint8(232 + step), RGB{v, v, v}
}

// ansi16 are the standard sixteen terminal colours as most emulators render
// them. The palette is only used to pick the nearest index.
var ansi16 = [16]RGB{
	{0, 0, 0}, {170, 0, 0}, {0, 170, 0}, {170, 85, 0},
	{0, 0, 170}, {170, 0, 170}, {0, 170, 170}, {170, 170, 170},
	{85, 85, 85}, {255, 85, 85}, {85, 255, 85}, {255, 255, 85},
	{85, 85, 255}, {255, 85, 255}, {85, 255, 255}, {255, 255, 255},
}

// ANSI16 downsamples to the nearest of the sixteen basic colours.
func (c RGB) ANSI16() uint8 {
	best, bestDist := 0, math.MaxFloat64
	for i, cand := range ansi16 {
		if d := dist2(c, cand); d < bestDist {
			best, bestDist = i, d
		}
	}
	return uint8(best)
}

// dist2 is the squared Euclidean distance between two colours, weighted to
// approximate perceptual distance without the cost of a full colour space
// conversion.
func dist2(a, b RGB) float64 {
	dr := float64(a.R) - float64(b.R)
	dg := float64(a.G) - float64(b.G)
	db := float64(a.B) - float64(b.B)
	return 2*dr*dr + 4*dg*dg + 3*db*db
}

// clamp01 bounds t to [0,1]. A NaN compares false against every bound, so it
// would otherwise slip through both and poison the arithmetic downstream —
// eventually reaching a float-to-int conversion, which is where it bites.
func clamp01(t float64) float64 {
	if math.IsNaN(t) || t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

func lerpByte(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

func scaleByte(v uint8, f float64) uint8 {
	out := math.Round(float64(v) * f)
	// Same reasoning as clamp01: NaN passes both bounds, and uint8(NaN) is
	// implementation-defined.
	if math.IsNaN(out) || out < 0 {
		return 0
	}
	if out > 255 {
		return 255
	}
	return uint8(out)
}

// itoa avoids pulling strconv into the hot render path for small values.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
