package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/theolol/tailsnail/internal/tsnode"
	"github.com/theolol/tailsnail/internal/ui/theme"
)

// Minimum viewport the chrome needs before any screen is drawn. Anything
// smaller gets the resize overlay instead of a corrupted layout.
const (
	minWidth  = 62
	minHeight = 18
)

// hint is one entry in the help bar.
type hint struct {
	keys string
	desc string
}

// hintFor builds a hint from a key binding's own help text, so the bar and the
// bindings can never drift apart.
func hintFor(b key.Binding, desc string) hint {
	h := b.Help()
	if desc == "" {
		desc = h.Desc
	}
	return hint{keys: h.Key, desc: desc}
}

// chrome composes a full screen: a title bar, the body, and a help bar, with
// any active toast floating just above the help.
func (m *Model) chrome(title, subtitle, body string, hints []hint) string {
	header := m.header(title, subtitle)
	help := m.helpBar(hints)

	reserved := lipgloss.Height(header) + lipgloss.Height(help)
	if t := m.toastLine(); t != "" {
		reserved += lipgloss.Height(t)
	}
	bodyHeight := max(m.height-reserved, 1)

	bodyBox := lipgloss.NewStyle().Width(m.width).Height(bodyHeight).Render(body)

	parts := []string{header, bodyBox}
	if t := m.toastLine(); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// header renders the title bar: the wordmark, the screen title, and the node
// identity on the right once it is known.
//
// Everything here is budgeted against the viewport width. The subtitle is the
// elastic part: it is trimmed to whatever space is left, and dropped entirely
// on a narrow window, so the header can never be the thing that overflows.
func (m *Model) header(title, subtitle string) string {
	th := m.style.Theme
	g := m.style.Glyphs

	wordmark := "tailsnail"
	if icon := m.style.Glyphs.Logo; icon != "" {
		wordmark = icon + " " + wordmark
	}
	left := m.style.Text(th.Accent, wordmark)
	used := lipgloss.Width(wordmark)

	if title != "" {
		sep := "  " + g.Vertical + "  "
		left += m.style.FaintText(sep) + m.style.Bold(title)
		used += lipgloss.Width(sep) + lipgloss.Width(title)
	}

	right := m.nodeBadge()
	rightWidth := lipgloss.Width(right)
	if used+rightWidth+2 > m.width {
		right, rightWidth = "", 0
	}

	if subtitle != "" {
		if avail := m.width - used - rightWidth - 4; avail >= 8 {
			trimmed := truncate(subtitle, avail)
			left += m.style.DimText("  " + trimmed)
			used += 2 + lipgloss.Width(trimmed)
		}
	}

	gap := max(m.width-used-rightWidth, 0)
	line := left + strings.Repeat(" ", gap) + right
	rule := m.style.FaintText(strings.Repeat(g.Horizontal, m.width))
	return line + "\n" + rule
}

// nodeBadge shows which tailnet device this instance is, which is the thing a
// user most often wants to confirm at a glance.
func (m *Model) nodeBadge() string {
	if m.node.Phase != tsnode.PhaseRunning {
		return m.style.FaintText(m.node.Phase.String())
	}
	name := m.node.Self.Short()
	if name == "" {
		return ""
	}
	badge := m.style.Text(m.style.Theme.Ok, m.style.Glyphs.Bullet+" ") + m.style.DimText(name)
	if m.app.Settings.ShowNodeID && m.node.Self.IPv4 != "" {
		badge += m.style.FaintText(" " + m.node.Self.IPv4)
	}
	return badge
}

// helpBar renders the keybinding hints along the bottom.
func (m *Model) helpBar(hints []hint) string {
	if len(hints) == 0 {
		return ""
	}
	sep := m.style.FaintText("  " + m.style.Glyphs.Bullet + "  ")
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, m.style.Text(m.style.Theme.Accent2, h.keys)+m.style.DimText(" "+h.desc))
	}
	line := strings.Join(parts, sep)
	// Drop hints from the right until the bar fits rather than wrapping.
	for len(parts) > 1 && lipgloss.Width(line) > m.width {
		parts = parts[:len(parts)-1]
		line = strings.Join(parts, sep)
	}
	rule := m.style.FaintText(strings.Repeat(m.style.Glyphs.Horizontal, m.width))
	return rule + "\n" + lipgloss.NewStyle().Width(m.width).Render(line)
}

// toastLine renders the active transient notice, if any.
func (m *Model) toastLine() string {
	if !m.toast.active(m.now) {
		return ""
	}
	th := m.style.Theme
	color := th.Dim
	marker := m.style.Glyphs.Bullet
	switch m.toast.kind {
	case toastOk:
		color, marker = th.Ok, m.style.Glyphs.Check
	case toastWarn:
		color = th.Warn
	case toastErr:
		color, marker = th.Err, m.style.Glyphs.Cross
	}
	// Fade the notice out over its last second so it does not simply vanish.
	remaining := toastDuration - m.now.Sub(m.toast.at)
	if remaining < time.Second {
		t := 1 - float64(remaining)/float64(time.Second)
		color = color.Lerp(th.Bg, t)
	}
	text := marker + " " + m.toast.text
	return lipgloss.NewStyle().Width(m.width).Render(m.style.Text(color, truncate(text, m.width)))
}

// center places content in the middle of the available body area.
func (m *Model) center(body string, height int) string {
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, body)
}

// bodyHeight is the space a screen's body has, given its chrome.
func (m *Model) bodyHeight() int {
	// Two lines of header, two of help, one optional toast.
	reserved := 4
	if m.toast.active(m.now) {
		reserved++
	}
	return max(m.height-reserved, 1)
}

// resizeOverlay returns a full-screen "make the window bigger" view when the
// terminal cannot hold the current screen.
//
// The overlay has to fit windows far smaller than any real screen, including
// ones only a few cells across — an overlay that overflows would produce
// exactly the sheared output it exists to prevent. It therefore picks the
// richest of three forms that actually fits, and clips as a last resort.
func (m *Model) resizeOverlay() (string, bool) {
	needW, needH := m.requiredSize()
	if m.width >= needW && m.height >= needH {
		return "", false
	}
	w, h := max(m.width, 1), max(m.height, 1)
	th := m.style.Theme
	g := m.style.Glyphs

	// Each dimension is coloured by whether it is the one falling short.
	dim := func(have, need int) string {
		s := fmt.Sprintf("%d", have)
		if have < need {
			return m.style.Text(th.Err, s)
		}
		return m.style.Text(th.Ok, s)
	}

	full := []string{
		m.style.Text(th.Warn, g.Bullet+" this window is too small"),
		"",
		m.style.DimText("current  ") + dim(m.width, needW) + m.style.DimText(" × ") + dim(m.height, needH),
		m.style.DimText("needed   ") + m.style.Text(th.Fg, fmt.Sprintf("%d × %d", needW, needH)),
		"",
		m.style.FaintText("resize the terminal, or press q to quit"),
	}
	compact := []string{
		m.style.Text(th.Warn, "window too small"),
		dim(m.width, needW) + m.style.DimText("×") + dim(m.height, needH) +
			m.style.FaintText(" → ") + m.style.Text(th.Fg, fmt.Sprintf("%d×%d", needW, needH)),
	}

	// fits reports whether a block renders inside the window, allowing for the
	// chrome a container would add around it.
	fits := func(lines []string, padW, padH int) bool {
		if len(lines)+padH > h {
			return false
		}
		for _, l := range lines {
			if lipgloss.Width(l)+padW > w {
				return false
			}
		}
		return true
	}

	var body string
	switch {
	case fits(full, 4, 2): // rounded border plus one cell of padding each side
		body = m.style.Panel().Render(lipgloss.JoinVertical(lipgloss.Center, full...))
	case fits(full, 0, 0):
		body = lipgloss.JoinVertical(lipgloss.Center, full...)
	case fits(compact, 0, 0):
		body = lipgloss.JoinVertical(lipgloss.Center, compact...)
	default:
		// Nothing but the target size, trimmed to whatever is left. The text
		// is truncated before colouring so a cut can never land inside an
		// escape sequence.
		body = m.style.Text(th.Warn, truncate(fmt.Sprintf("%d×%d", needW, needH), w))
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body), true
}

// requiredSize is the viewport the current screen needs.
func (m *Model) requiredSize() (int, int) {
	w, h := minWidth, minHeight
	// Only the arena itself needs room for the grid; the results dialog is
	// panel-sized and must not inherit that requirement.
	if m.screen == screenGame {
		aw, ah := m.game.arenaCells()
		if aw > 0 {
			w = max(w, aw+4)
			// The arena and its frame, the scoreboard — whose height depends on
			// how many players fit across this width — the two-line header, the
			// two-line help bar, and one line held back for a toast.
			chrome := hudRowsAt(len(m.game.players), w) + 7
			h = max(h, ah+chrome)
		}
	}
	return w, h
}

// --- log overlay ----------------------------------------------------------

// updateLogOverlay handles input while the debug log is up.
func (m *Model) updateLogOverlay(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Log), key.Matches(msg, m.keys.Back), msg.String() == "q":
		m.showLog = false
	case key.Matches(msg, m.keys.Up):
		m.logTop++
	case key.Matches(msg, m.keys.Down):
		m.logTop = max(m.logTop-1, 0)
	case msg.String() == "ctrl+c":
		return m.quit()
	case msg.String() == "g":
		m.logTop = m.app.Log.Len()
	case msg.String() == "G":
		m.logTop = 0
	}
	return nil
}

// viewLogOverlay renders the captured tsnet and application log.
//
// Log lines are sanitised on capture, so nothing here can move the cursor or
// leave a colour set — which is the whole reason the ring exists.
func (m *Model) viewLogOverlay() string {
	lines := m.app.Log.Lines()
	visible := max(m.bodyHeight()-2, 1)

	// logTop counts backwards from the newest line, so the view stays pinned
	// to the tail as new output arrives.
	end := max(len(lines)-m.logTop, 0)
	start := max(end-visible, 0)
	if end > len(lines) {
		end = len(lines)
	}
	m.logTop = min(m.logTop, max(len(lines)-1, 0))

	var b strings.Builder
	for _, l := range lines[start:end] {
		stamp := m.style.FaintText(l.At.Format("15:04:05.000") + " ")
		b.WriteString(stamp + m.style.DimText(truncate(l.Text, max(m.width-14, 10))) + "\n")
	}
	if len(lines) == 0 {
		b.WriteString(m.style.FaintText("nothing logged yet"))
	}

	subtitle := fmt.Sprintf("%d lines buffered", len(lines))
	if total := m.app.Log.Total(); total > len(lines) {
		subtitle = fmt.Sprintf("%d of %d lines (older output has scrolled out)", len(lines), total)
	}
	return m.chrome("debug log", subtitle, b.String(), []hint{
		{"↑/↓", "scroll"}, {"g/G", "top/bottom"}, {"ctrl+l / esc", "close"},
	})
}

// --- small helpers --------------------------------------------------------

// sprintf formats only when there are arguments, so a literal string
// containing a percent sign passes through untouched.
func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// truncate shortens s to at most width display cells, adding an ellipsis.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// truncateLeft shortens s from the left, keeping the tail. Filesystem paths
// carry their meaning at the end, so trimming the front is what preserves it.
func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

// truncateStyled shortens a string that already carries colour, counting
// display cells and leaving escape sequences intact. Slicing a styled string
// by rune would cut an escape in half and leave the rest of the frame the
// wrong colour.
func truncateStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// pad right-pads s to width display cells.
func pad(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// capitalise upper-cases the first rune, for turning protocol reasons into
// sentences.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// plural returns the singular or plural form for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// relativeTime renders a timestamp the way a person would say it.
func relativeTime(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d.Minutes())
		return fmt.Sprintf("%d %s ago", n, plural(n, "minute", "minutes"))
	case d < 24*time.Hour:
		n := int(d.Hours())
		return fmt.Sprintf("%d %s ago", n, plural(n, "hour", "hours"))
	case d < 30*24*time.Hour:
		n := int(d.Hours() / 24)
		return fmt.Sprintf("%d %s ago", n, plural(n, "day", "days"))
	default:
		return t.Local().Format("2 Jan 2006")
	}
}

// duration renders a match length compactly.
func duration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// meter renders a horizontal bar of the given width filled to fraction t.
func (m *Model) meter(width int, t float64, full, empty theme.RGB) string {
	if width <= 0 {
		return ""
	}
	t = min(max(t, 0), 1)
	n := int(t*float64(width) + 0.5)
	g := m.style.Glyphs
	return m.style.Text(full, strings.Repeat(g.MeterFull, n)) +
		m.style.Text(empty, strings.Repeat(g.MeterEmpty, width-n))
}
