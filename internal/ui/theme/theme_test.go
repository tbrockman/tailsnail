package theme

import (
	"math"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, name := range ModeNames() {
		if _, ok := ParseMode(name); !ok {
			t.Errorf("ParseMode(%q) rejected a documented mode", name)
		}
	}
	if got, ok := ParseMode("TrueColor"); !ok || got != ModeTrueColor {
		t.Errorf("ParseMode is case sensitive: got %q ok=%v", got, ok)
	}
	if got, ok := ParseMode("  256 "); !ok || got != Mode256 {
		t.Errorf("ParseMode(%q) = %q, %v", "  256 ", got, ok)
	}
	if _, ok := ParseMode("magenta"); ok {
		t.Error("ParseMode accepted a nonsense mode")
	}
}

func TestResolveHonoursNoColorAboveEverything(t *testing.T) {
	env := Env{NoColor: "1", ColorTerm: "truecolor", Term: "xterm-256color"}
	for _, requested := range []Mode{ModeAuto, ModeTrueColor, Mode256, Mode16} {
		if got := Resolve(requested, env); got != ModeNone {
			t.Errorf("Resolve(%q) with NO_COLOR set = %q, want %q", requested, got, ModeNone)
		}
	}
}

func TestResolveExplicitModeWins(t *testing.T) {
	env := Env{Term: "dumb"}
	if got := Resolve(ModeTrueColor, env); got != ModeTrueColor {
		t.Errorf("an explicit --color was overridden: got %q", got)
	}
}

func TestResolveAutoDetection(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want Mode
	}{
		{"colorterm truecolor", Env{ColorTerm: "truecolor", Term: "xterm"}, ModeTrueColor},
		{"colorterm 24bit", Env{ColorTerm: "24bit", Term: "xterm"}, ModeTrueColor},
		{"term 256color", Env{Term: "xterm-256color"}, Mode256},
		{"term screen-256color", Env{Term: "screen-256color"}, Mode256},
		{"plain xterm", Env{Term: "xterm"}, Mode16},
		{"linux console", Env{Term: "linux"}, Mode16},
		{"dumb terminal", Env{Term: "dumb"}, ModeNone},
		{"no term at all", Env{}, ModeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(ModeAuto, tc.env); got != tc.want {
				t.Errorf("Resolve(auto, %+v) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestHex(t *testing.T) {
	cases := []struct {
		c    RGB
		want string
	}{
		{RGB{0, 0, 0}, "#000000"},
		{RGB{255, 255, 255}, "#ffffff"},
		{RGB{0x4a, 0xe0, 0xa8}, "#4ae0a8"},
		{RGB{1, 2, 3}, "#010203"},
	}
	for _, tc := range cases {
		if got := tc.c.Hex(); got != tc.want {
			t.Errorf("RGB%+v.Hex() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestLerpEndpoints(t *testing.T) {
	a, b := RGB{0, 0, 0}, RGB{255, 128, 64}
	if got := a.Lerp(b, 0); got != a {
		t.Errorf("Lerp(0) = %+v, want the start colour", got)
	}
	if got := a.Lerp(b, 1); got != b {
		t.Errorf("Lerp(1) = %+v, want the end colour", got)
	}
	if got := a.Lerp(b, 0.5); got != (RGB{128, 64, 32}) {
		t.Errorf("Lerp(0.5) = %+v", got)
	}
	// Out-of-range t must clamp rather than overshoot into wrapped bytes.
	if got := a.Lerp(b, 5); got != b {
		t.Errorf("Lerp(5) = %+v, want a clamp to the end colour", got)
	}
	if got := a.Lerp(b, -5); got != a {
		t.Errorf("Lerp(-5) = %+v, want a clamp to the start colour", got)
	}
}

func TestScaleClamps(t *testing.T) {
	c := RGB{200, 100, 50}
	if got := c.Scale(10); got != (RGB{255, 255, 255}) {
		t.Errorf("Scale(10) = %+v, want a clamp to white rather than a wrap", got)
	}
	if got := c.Scale(0); got != (RGB{0, 0, 0}) {
		t.Errorf("Scale(0) = %+v", got)
	}
	if got := c.Scale(-1); got != (RGB{0, 0, 0}) {
		t.Errorf("Scale(-1) = %+v, want a clamp to black", got)
	}
}

func TestANSI256RoundTripsExactCubeColours(t *testing.T) {
	// A colour that is exactly on the cube must map to its own index.
	for _, level := range cubeLevels {
		c := RGB{level, level, level}
		idx := c.ANSI256()
		if idx < 16 {
			t.Errorf("RGB%+v mapped to a basic colour index %d", c, idx)
		}
	}
	// Pure red is cube index 16 + 36*5 = 196.
	if got := (RGB{255, 0, 0}).ANSI256(); got != 196 {
		t.Errorf("pure red -> %d, want 196", got)
	}
	if got := (RGB{0, 255, 0}).ANSI256(); got != 46 {
		t.Errorf("pure green -> %d, want 46", got)
	}
	if got := (RGB{0, 0, 255}).ANSI256(); got != 21 {
		t.Errorf("pure blue -> %d, want 21", got)
	}
}

func TestANSI256UsesTheGreyRampForGreys(t *testing.T) {
	// Mid grey is closer to the 24-step ramp than to the coarse colour cube.
	got := (RGB{0x77, 0x77, 0x77}).ANSI256()
	if got < 232 || got > 255 {
		t.Errorf("mid grey -> %d, want an index in the 232..255 grey ramp", got)
	}
}

func TestANSI256StaysInRange(t *testing.T) {
	for r := 0; r < 256; r += 17 {
		for g := 0; g < 256; g += 17 {
			for b := 0; b < 256; b += 17 {
				got := (RGB{uint8(r), uint8(g), uint8(b)}).ANSI256()
				if got < 16 {
					t.Fatalf("RGB{%d %d %d} -> %d, below the 16-colour boundary", r, g, b, got)
				}
			}
		}
	}
}

func TestANSI16PicksSensibleBasics(t *testing.T) {
	// A saturated colour must land on one of its own hue's two variants. Which
	// of the pair wins is a matter of distance — pure #ff0000 really is nearer
	// to dark red than to the washed-out bright red — so the assertion is on
	// the hue family, not on a specific index.
	cases := []struct {
		name string
		c    RGB
		want []uint8
	}{
		{"black", RGB{0, 0, 0}, []uint8{0}},
		{"white", RGB{255, 255, 255}, []uint8{15}},
		{"red", RGB{255, 0, 0}, []uint8{1, 9}},
		{"green", RGB{0, 255, 0}, []uint8{2, 10}},
		{"blue", RGB{0, 0, 255}, []uint8{4, 12}},
		{"pale blue", RGB{160, 160, 255}, []uint8{12, 15, 7}},
	}
	for _, tc := range cases {
		got := tc.c.ANSI16()
		if !containsByte(tc.want, got) {
			t.Errorf("%s: RGB%+v.ANSI16() = %d, want one of %v", tc.name, tc.c, got, tc.want)
		}
	}
	for r := 0; r < 256; r += 51 {
		for g := 0; g < 256; g += 51 {
			for b := 0; b < 256; b += 51 {
				if got := (RGB{uint8(r), uint8(g), uint8(b)}).ANSI16(); got > 15 {
					t.Fatalf("ANSI16 returned %d, outside 0..15", got)
				}
			}
		}
	}
}

func TestTermColorEmitsNothingWithoutColor(t *testing.T) {
	c := RGB{0x4a, 0xe0, 0xa8}
	if _, ok := c.TermColor(ModeNone).(interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}); !ok {
		// lipgloss.NoColor still satisfies color.Color; the point is only that
		// it is not a concrete colour value.
	}
	s := NewStyle(Neon, ModeNone, false, false)
	rendered := s.Text(c, "hello")
	if strings.ContainsRune(rendered, 0x1b) {
		t.Errorf("ModeNone emitted an escape sequence: %q", rendered)
	}
	if rendered != "hello" {
		t.Errorf("ModeNone rendered %q, want the plain string", rendered)
	}
}

func TestThemesDefineEveryPlayerColour(t *testing.T) {
	for _, th := range All() {
		if th.Name == "" || th.Desc == "" {
			t.Errorf("theme %+v is missing a name or description", th)
		}
		zero := RGB{}
		for i, c := range th.Players {
			if c == zero {
				t.Errorf("theme %q leaves player slot %d unset", th.Name, i)
			}
		}
		if th.Fg == zero || th.Bg == zero || th.Accent == zero {
			t.Errorf("theme %q leaves a core colour unset", th.Name)
		}
	}
}

func containsByte(haystack []uint8, needle uint8) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func TestNeonStaysMostlySeparableAt16Colours(t *testing.T) {
	// Sixteen colours cannot hold eight distinct hues that are all legible on
	// a dark background, so a little collapsing is expected. What matters is
	// that it stays close to distinct — and that glyph shape, which is tested
	// separately, carries identity regardless.
	seen := map[uint8]bool{}
	for _, c := range Neon.Players {
		seen[c.ANSI16()] = true
	}
	if len(seen) < 7 {
		t.Errorf("neon collapses to %d distinct 16-colour indices, want at least 7", len(seen))
	}
	// None of them may land on a colour too dark to see on the board.
	for i, c := range Neon.Players {
		if idx := c.ANSI16(); idx == 0 || idx == 8 {
			t.Errorf("seat %d downsamples to index %d, which is black on a dark board", i, idx)
		}
	}
}

func TestMonoDeliberatelyCollapsesToGreys(t *testing.T) {
	// Mono is meant to lean on shape rather than hue, so it is expected to
	// flatten at low colour depth. The test records that intent so a future
	// change to the palette has to be deliberate.
	seen := map[uint8]bool{}
	for _, c := range Mono.Players {
		seen[c.ANSI16()] = true
	}
	if len(seen) > 3 {
		t.Errorf("mono resolved to %d distinct 16-colour indices; it is meant to be near-monochrome", len(seen))
	}
}

func TestPlayerColoursAreDistinguishable(t *testing.T) {
	// Players must remain separable after downsampling to 256 colours; a theme
	// that collapses two seats onto one index would make them unreadable on a
	// common terminal.
	for _, th := range All() {
		seen := map[uint8]int{}
		for i, c := range th.Players {
			idx := c.ANSI256()
			if prev, dup := seen[idx]; dup {
				t.Errorf("theme %q: seats %d and %d both downsample to 256-colour index %d", th.Name, prev, i, idx)
			}
			seen[idx] = i
		}
	}
}

func TestPlayerColoursAreLegibleOnTheBoard(t *testing.T) {
	// Every player colour must be clearly brighter than the grid it is drawn
	// on, or a snake vanishes into the background.
	for _, th := range All() {
		grid := th.Grid.Luminance()
		for i, c := range th.Players {
			if c.Luminance() < grid+0.2 {
				t.Errorf("theme %q seat %d (luminance %.2f) is too close to the grid (%.2f)",
					th.Name, i, c.Luminance(), grid)
			}
		}
	}
}

func TestPlayerWrapsBeyondThePalette(t *testing.T) {
	th := Neon
	if got, want := th.Player(8), th.Player(0); got != want {
		t.Errorf("Player(8) = %+v, want it to wrap to %+v", got, want)
	}
	if got, want := th.Player(-3), th.Player(0); got != want {
		t.Errorf("Player(-3) = %+v, want %+v", got, want)
	}
}

func TestByName(t *testing.T) {
	if got := ByName("mono"); got.Name != "mono" {
		t.Errorf("ByName(mono) = %q", got.Name)
	}
	if got := ByName("MONO"); got.Name != "mono" {
		t.Errorf("ByName is case sensitive: got %q", got.Name)
	}
	if got := ByName("nonexistent"); got.Name != Neon.Name {
		t.Errorf("ByName fallback = %q, want %q", got.Name, Neon.Name)
	}
}

func TestTailGradientRunsHeadToTail(t *testing.T) {
	th := Neon
	const n = 12
	head := th.TailColor(0, 0, n, 0)
	tail := th.TailColor(0, n-1, n, 0)
	if tail.Luminance() >= head.Luminance() {
		t.Fatalf("tail (%.3f) is not dimmer than the head (%.3f)", tail.Luminance(), head.Luminance())
	}
}

func TestTailShimmerActuallyMoves(t *testing.T) {
	th := Neon
	const n = 12
	// The same segment must change between animation frames, or the tail is
	// static and the shimmer is not doing anything.
	a := th.TailColor(0, 4, n, 0.0)
	b := th.TailColor(0, 4, n, 0.25)
	c := th.TailColor(0, 4, n, 0.5)
	if a == b && b == c {
		t.Fatal("tail colour is identical across animation phases")
	}
}

func TestTailShimmerIsPeriodic(t *testing.T) {
	th := Neon
	if got, want := th.TailColor(0, 3, 10, 0.0), th.TailColor(0, 3, 10, 1.0); got != want {
		t.Errorf("phase 0 gives %+v but phase 1 gives %+v; the wave should be periodic", got, want)
	}
}

func TestSingleSegmentSnakeUsesTheBaseColour(t *testing.T) {
	th := Neon
	if got, want := th.TailColor(2, 0, 1, 0.3), th.Player(2); got != want {
		t.Errorf("TailColor for a one-segment snake = %+v, want the base colour %+v", got, want)
	}
}

func TestFoodPulseVaries(t *testing.T) {
	th := Neon
	low := th.FoodColor(0.75) // trough of the sine
	high := th.FoodColor(0.25)
	if high.Luminance() <= low.Luminance() {
		t.Errorf("food does not brighten across its pulse: %.3f then %.3f", low.Luminance(), high.Luminance())
	}
}

func TestDeathFlashFadesToTheBoard(t *testing.T) {
	th := Neon
	start := th.DeathColor(0, 0)
	end := th.DeathColor(0, 1)
	if end != th.Grid {
		t.Errorf("death flash ends at %+v, want the grid colour %+v", end, th.Grid)
	}
	if start.Luminance() <= end.Luminance() {
		t.Error("the death flash should start bright and fade")
	}
}

func TestGlyphSetsAreComplete(t *testing.T) {
	for _, g := range []Glyphs{Unicode, ASCIIGlyphs} {
		name := "unicode"
		if g.ASCII {
			name = "ascii"
		}
		seen := map[string]int{}
		for i, h := range g.Heads {
			if h == "" {
				t.Errorf("%s: head %d is empty", name, i)
			}
			if prev, dup := seen[h]; dup {
				t.Errorf("%s: seats %d and %d share the glyph %q", name, prev, i, h)
			}
			seen[h] = i
		}
		if len(g.FoodPulse) < 2 || len(g.Spinner) < 2 || len(g.Spark) < 2 {
			t.Errorf("%s: an animation frame list is too short to animate", name)
		}
		for _, s := range []string{g.Body, g.Tail, g.Dead, g.Empty, g.Horizontal, g.Vertical, g.Bullet} {
			if s == "" {
				t.Errorf("%s: a required glyph is empty", name)
			}
		}
	}
}

func TestASCIIGlyphsAreSevenBit(t *testing.T) {
	g := ASCIIGlyphs
	all := append([]string{g.Body, g.Tail, g.Dead, g.Empty, g.Bullet, g.Check, g.Cross,
		g.Arrow, g.Ellipsis, g.MeterFull, g.MeterEmpty,
		g.TopLeft, g.TopRight, g.BottomLeft, g.BottomRight, g.Horizontal, g.Vertical},
		g.Heads[:]...)
	all = append(all, g.FoodPulse...)
	all = append(all, g.Spinner...)
	all = append(all, g.Spark...)
	for _, s := range all {
		for _, r := range s {
			if r > 127 {
				t.Errorf("ASCII glyph set contains a non-ASCII rune %q in %q", r, s)
			}
		}
	}
}

func TestUnicodeGlyphsAreSingleRunes(t *testing.T) {
	// A multi-rune glyph would occupy an unpredictable number of cells and
	// shear every column after it.
	g := Unicode
	all := append([]string{g.Body, g.Tail, g.Dead, g.Empty, g.Bullet,
		g.MeterFull, g.MeterEmpty, g.Horizontal, g.Vertical}, g.Heads[:]...)
	all = append(all, g.FoodPulse...)
	all = append(all, g.Spinner...)
	for _, s := range all {
		if n := len([]rune(s)); n != 1 {
			t.Errorf("glyph %q is %d runes, want exactly 1", s, n)
		}
	}
}

func TestSetSelectsTheRequestedGlyphs(t *testing.T) {
	if !Set(true, false).ASCII {
		t.Error("Set(ascii) did not return the ASCII glyphs")
	}
	if Set(false, false).ASCII {
		t.Error("Set(unicode) did not return the Unicode glyphs")
	}
	if got := Set(false, true).Logo; got != SnailIcon {
		t.Errorf("Logo = %q, want the snail", got)
	}
	// There is always an icon of some kind, so the header keeps its shape;
	// only which glyph is used changes.
	if got := Set(false, false).Logo; got == "" || got == SnailIcon {
		t.Errorf("Logo = %q, want a non-emoji stand-in", got)
	}
	// Asking for plain ASCII rules out emoji too.
	got := Set(true, true).Logo
	if got == "" || got == SnailIcon {
		t.Errorf("Logo = %q, want an ASCII stand-in", got)
	}
	for _, r := range got {
		if r > 127 {
			t.Errorf("the ASCII icon %q is not ASCII", got)
		}
	}
}

func TestEveryGlyphSetHasAnIcon(t *testing.T) {
	// The icon is a capability decision, not a preference, so there is no
	// configuration in which it is simply absent.
	for _, g := range []Glyphs{Unicode, ASCIIGlyphs, Set(false, true)} {
		if g.Logo == "" {
			t.Error("a glyph set has no icon")
		}
		if n := len([]rune(g.Logo)); n != 1 {
			t.Errorf("icon %q is %d runes; a single codepoint is what measures reliably", g.Logo, n)
		}
	}
}

func TestEmojiDetection(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want bool
	}{
		{"iterm with utf-8", Env{Locale: "en_GB.UTF-8", Term: "xterm-256color", TermProgram: "iTerm.app"}, true},
		{"apple terminal", Env{Locale: "en_US.UTF-8", Term: "xterm-256color", TermProgram: "Apple_Terminal"}, true},
		{"kitty by term", Env{Locale: "en_US.UTF-8", Term: "xterm-kitty"}, true},
		{"ghostty by term", Env{Locale: "C.utf8", Term: "xterm-ghostty"}, true},
		{"no utf-8 locale", Env{Locale: "C", Term: "xterm-256color", TermProgram: "iTerm.app"}, false},
		{"no locale at all", Env{Term: "xterm-256color", TermProgram: "iTerm.app"}, false},
		{"unknown terminal", Env{Locale: "en_US.UTF-8", Term: "xterm-256color"}, false},
		{"linux console", Env{Locale: "en_US.UTF-8", Term: "linux"}, false},
		{"dumb", Env{Locale: "en_US.UTF-8", Term: "dumb", TermProgram: "iTerm.app"}, false},
		{"no term", Env{Locale: "en_US.UTF-8"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EmojiSupported(tc.env); got != tc.want {
				t.Errorf("EmojiSupported(%+v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestResolveEmojiHonoursAnExplicitChoice(t *testing.T) {
	bare := Env{Locale: "C", Term: "dumb"}
	rich := Env{Locale: "en_US.UTF-8", Term: "xterm-kitty"}

	if !ResolveEmoji(EmojiOn, bare) {
		t.Error("--emoji=on was overridden by detection")
	}
	if ResolveEmoji(EmojiOff, rich) {
		t.Error("--emoji=off was overridden by detection")
	}
	if ResolveEmoji(EmojiAuto, bare) {
		t.Error("auto enabled emoji on a bare terminal")
	}
	if !ResolveEmoji(EmojiAuto, rich) {
		t.Error("auto disabled emoji on a capable terminal")
	}
}

func TestParseEmojiMode(t *testing.T) {
	for _, name := range EmojiModeNames() {
		if _, ok := ParseEmojiMode(name); !ok {
			t.Errorf("ParseEmojiMode(%q) rejected a documented mode", name)
		}
	}
	if got, ok := ParseEmojiMode(" ON "); !ok || got != EmojiOn {
		t.Errorf("ParseEmojiMode = %q, %v", got, ok)
	}
	if _, ok := ParseEmojiMode("snails"); ok {
		t.Error("ParseEmojiMode accepted a nonsense mode")
	}
}

func TestSnailIconIsASingleGrapheme(t *testing.T) {
	// A multi-codepoint emoji sequence would be measured inconsistently across
	// terminals, so the icon is deliberately a plain single-rune emoji.
	if n := len([]rune(SnailIcon)); n != 1 {
		t.Fatalf("the icon is %d runes; a single codepoint is what measures reliably", n)
	}
}

func TestSecondaryTextIsLegibleAgainstTheBackground(t *testing.T) {
	// Dim and faint text are used for descriptions, rules and hints. If they
	// sit too close to the background they simply are not readable, which is
	// what the first pass at these palettes got wrong.
	for _, th := range All() {
		bg := th.Bg.Luminance()
		if gap := th.Dim.Luminance() - bg; gap < 0.45 {
			t.Errorf("theme %q: dim text is only %.2f above the background", th.Name, gap)
		}
		if gap := th.Faint.Luminance() - bg; gap < 0.25 {
			t.Errorf("theme %q: faint text is only %.2f above the background", th.Name, gap)
		}
		// Faint must still read as quieter than dim, or the hierarchy is lost.
		if th.Faint.Luminance() >= th.Dim.Luminance() {
			t.Errorf("theme %q: faint text is not quieter than dim text", th.Name)
		}
	}
}

func TestFoodGlyphCyclesThroughItsFrames(t *testing.T) {
	g := Unicode
	seen := map[string]bool{}
	for i := range 40 {
		seen[g.Food(float64(i)/40)] = true
	}
	if len(seen) != len(g.FoodPulse) {
		t.Errorf("food animation used %d of %d frames", len(seen), len(g.FoodPulse))
	}
}

func TestFrameSelectionHandlesEdgePhases(t *testing.T) {
	g := Unicode
	for _, phase := range []float64{0, 0.999999, 1, 1.5, -0.25, math.Inf(1) * 0} {
		if got := g.Spin(phase); got == "" {
			t.Errorf("Spin(%v) returned an empty frame", phase)
		}
	}
	if got := g.Food(1.0); got != g.FoodPulse[0] {
		t.Errorf("phase 1.0 gave %q, want it to wrap to the first frame %q", got, g.FoodPulse[0])
	}
	if got := g.Ember(1); got != g.Spark[len(g.Spark)-1] {
		t.Errorf("a fully faded ember = %q, want the last frame", got)
	}
	empty := Glyphs{}
	if got := empty.Spin(0.5); got != " " {
		t.Errorf("an empty frame list returned %q, want a space", got)
	}
}

func TestHeadWrapsBeyondThePalette(t *testing.T) {
	g := Unicode
	if got, want := g.Head(8), g.Head(0); got != want {
		t.Errorf("Head(8) = %q, want it to wrap to %q", got, want)
	}
	if got, want := g.Head(-1), g.Head(0); got != want {
		t.Errorf("Head(-1) = %q, want %q", got, want)
	}
}
