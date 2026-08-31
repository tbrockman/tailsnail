package theme

import (
	"math"

	"github.com/charmbracelet/lipgloss"
)

// Style bundles a theme, a resolved colour mode and a glyph set, and is the
// single object every screen renders through.
type Style struct {
	Theme  Theme
	Mode   Mode
	Glyphs Glyphs
}

// NewStyle builds a renderer and installs the colour profile globally so that
// lipgloss's own styling degrades in step with ours.
func NewStyle(t Theme, m Mode, ascii, emoji bool) *Style {
	m.Apply()
	return &Style{Theme: t, Mode: m, Glyphs: Set(ascii, emoji)}
}

// Colored reports whether any colour will be emitted at all. Screens use it to
// add textual markers where colour would otherwise have carried the meaning.
func (s *Style) Colored() bool { return s.Mode != ModeNone }

// Fg returns a style with the given foreground colour.
func (s *Style) Fg(c RGB) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c.TermColor(s.Mode))
}

// Bg returns a style with the given background colour.
func (s *Style) Bg(c RGB) lipgloss.Style {
	return lipgloss.NewStyle().Background(c.TermColor(s.Mode))
}

// Text renders a string in a colour.
func (s *Style) Text(c RGB, str string) string { return s.Fg(c).Render(str) }

// Accent renders a string in the theme's accent colour.
func (s *Style) Accent(str string) string { return s.Text(s.Theme.Accent, str) }

// Dim renders secondary text.
func (s *Style) DimText(str string) string { return s.Text(s.Theme.Dim, str) }

// Faint renders tertiary text such as rules and disabled entries.
func (s *Style) FaintText(str string) string { return s.Text(s.Theme.Faint, str) }

// Bold renders emphasised text in the primary foreground colour.
func (s *Style) Bold(str string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(s.Theme.Fg.TermColor(s.Mode)).Render(str)
}

// PlayerText renders a string in a seat's colour.
func (s *Style) PlayerText(slot int, str string) string {
	return s.Text(s.Theme.Player(slot), str)
}

// Panel returns a bordered container style used for cards and dialogs.
func (s *Style) Panel() lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if s.Glyphs.ASCII {
		border = lipgloss.Border{
			Top: "-", Bottom: "-", Left: "|", Right: "|",
			TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		}
	}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(s.Theme.Faint.TermColor(s.Mode)).
		Padding(0, 1)
}

// Reset ends any active colour. It is emitted directly rather than through
// lipgloss on the arena's hot path.
const Reset = "\x1b[0m"

// SGR returns the escape sequence that sets the foreground to c under the
// current colour mode, or the empty string when colour is off.
//
// The arena redraws every cell many times a second, and going through lipgloss
// for each one allocates a style and re-measures the string. Emitting the
// escape directly, and only when the colour actually changes from the previous
// cell, keeps a full frame down to a handful of allocations.
func (s *Style) SGR(c RGB) string {
	switch s.Mode {
	case ModeTrueColor:
		return "\x1b[38;2;" + itoa(int(c.R)) + ";" + itoa(int(c.G)) + ";" + itoa(int(c.B)) + "m"
	case Mode256:
		return "\x1b[38;5;" + itoa(int(c.ANSI256())) + "m"
	case Mode16:
		idx := c.ANSI16()
		if idx < 8 {
			return "\x1b[" + itoa(30+int(idx)) + "m"
		}
		return "\x1b[" + itoa(90+int(idx)-8) + "m"
	default:
		return ""
	}
}

// TailColor returns the colour of body segment i of an n-segment snake.
//
// Two effects combine. A static gradient dims each segment towards the tail so
// the direction of travel is obvious even in a still frame. On top of that a
// travelling brightness wave, driven by phase, makes the tail visibly flow —
// which is what tells a player at a glance that a snake is alive and moving
// rather than a wall or a wreck.
func (t Theme) TailColor(slot, i, n int, phase float64) RGB {
	base := t.Player(slot)
	if n <= 1 {
		return base
	}
	pos := float64(i) / float64(n-1) // 0 at the head, 1 at the tail

	// Static falloff: the tail settles towards the board rather than to black,
	// so it stays visible without competing with the head.
	col := base.Lerp(base.Mix(t.Grid), pos*0.85)

	// Travelling wave. The 1.6 factor puts a little over one and a half cycles
	// along a typical body, which reads as flow rather than strobing.
	wave := math.Sin(2 * math.Pi * (phase - pos*1.6)) // -1..1
	col = col.Scale(1 + 0.16*wave)
	return col
}

// HeadColor returns the head colour, brightened slightly on the beat so the
// head always leads the tail visually.
func (t Theme) HeadColor(slot int, phase float64) RGB {
	base := t.Player(slot)
	wave := math.Sin(2 * math.Pi * phase)
	return base.Scale(1.05 + 0.08*wave)
}

// FoodColor pulses the pellet between its base colour and a brighter tint,
// paired with the glyph pulse so the two beat together.
func (t Theme) FoodColor(phase float64) RGB {
	wave := 0.5 + 0.5*math.Sin(2*math.Pi*phase)
	return t.Food.Scale(0.82 + 0.32*wave)
}

// DeathColor fades a death flash from white-hot to the board colour as
// progress runs 0 to 1.
func (t Theme) DeathColor(slot int, progress float64) RGB {
	hot := RGB{0xff, 0xff, 0xff}.Mix(t.Player(slot))
	return hot.Lerp(t.Grid, clamp01(progress))
}
