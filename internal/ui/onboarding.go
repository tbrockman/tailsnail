package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theolol/tailsnail/internal/tsnode"
)

// onboardSuccessHold is how long the success beat plays before the menu takes
// over. It exists so the user actually reads which device they came up as.
const onboardSuccessHold = 1600 * time.Millisecond

// onboardState is the onboarding screen's local state.
type onboardState struct {
	// successAt is when the node first reached Running, or zero.
	successAt time.Time
	// openedBrowser records whether the auth URL was handed to the desktop.
	openedBrowser bool
}

// updateOnboarding handles keys on the onboarding screen.
func (m *Model) updateOnboarding(msg tea.KeyMsg) tea.Cmd {
	switch {
	case msg.String() == "q":
		return m.quit()
	case key.Matches(msg, m.keys.Copy) && m.node.AuthURL != "":
		m.onboard.openedBrowser = true
		return tea.Batch(m.openAuthURL(m.node.AuthURL),
			m.setToast(toastInfo, "Opening %s", shortURL(m.node.AuthURL)))
	case key.Matches(msg, m.keys.Retry):
		return m.retryLogin()
	}
	return nil
}

// retryLogin restarts the interactive login, used after a timeout or when a
// stored node key has expired.
func (m *Model) retryLogin() tea.Cmd {
	return func() tea.Msg {
		if err := m.app.Node.Relogin(m.app.Ctx); err != nil {
			m.app.Log.Logf("ui: relogin: %v", err)
		}
		return nil
	}
}

// viewOnboarding renders whichever stage of connecting we are at.
func (m *Model) viewOnboarding() string {
	var body string
	var hints []hint

	switch {
	case !m.onboard.successAt.IsZero():
		body, hints = m.onboardSuccess()
	case m.node.Phase == tsnode.PhaseNeedsLogin && m.node.AuthURL != "":
		body, hints = m.onboardAuthorise()
	case m.node.Phase == tsnode.PhaseNeedsApproval:
		body, hints = m.onboardApproval()
	case m.node.Phase == tsnode.PhaseStopped:
		body, hints = m.onboardLoggedOut()
	case m.node.Phase == tsnode.PhaseFailed || m.node.Err != nil:
		body, hints = m.onboardTrouble()
	default:
		body, hints = m.onboardConnecting()
	}

	return m.chrome("", "", m.center(body, m.bodyHeight()), hints)
}

// onboardConnecting is the quiet path: a node with a stored key comes up in a
// second or two, and this is all the user ever sees.
func (m *Model) onboardConnecting() (string, []hint) {
	spinner := m.style.Accent(m.style.Glyphs.Spin(m.phase(800 * time.Millisecond)))
	msg := "connecting to your tailnet"
	if m.node.Phase == tsnode.PhaseStarting {
		msg = "starting the embedded Tailscale node"
	}
	lines := []string{
		m.logo(),
		"",
		spinner + " " + m.style.DimText(msg+m.style.Glyphs.Ellipsis),
	}
	// Only start explaining the wait once it is actually a wait.
	if elapsed := m.now.Sub(m.node.Since); elapsed > 6*time.Second && !m.node.Since.IsZero() {
		lines = append(lines, "",
			m.style.FaintText(fmt.Sprintf("still trying after %s", duration(elapsed))))
	}
	lines = append(lines, m.healthLines()...)
	return lipgloss.JoinVertical(lipgloss.Center, lines...), []hint{
		{"ctrl+l", "logs"}, {"q", "quit"},
	}
}

// onboardAuthorise is the first-run screen: one browser visit, once, ever.
func (m *Model) onboardAuthorise() (string, []hint) {
	th := m.style.Theme
	spinner := m.style.Accent(m.style.Glyphs.Spin(m.phase(800 * time.Millisecond)))

	explain := []string{
		m.style.Text(th.Fg, "tailsnail joins your tailnet as its own device,"),
		m.style.Text(th.Fg, "so other players can find you directly."),
		"",
		m.style.DimText("Authorise it once. Every launch after this connects"),
		m.style.DimText("straight away with no browser and no prompt."),
	}

	// The URL is the one thing that has to be readable and selectable, so it
	// gets its own panel and is never truncated — it wraps instead.
	urlWidth := min(max(m.width-12, 24), 76)
	url := lipgloss.NewStyle().Width(urlWidth).Render(m.style.Text(th.Accent2, m.node.AuthURL))
	urlBox := m.style.Panel().BorderForeground(th.Accent.TermColor(m.style.Mode)).Render(url)

	opened := m.style.FaintText("if your browser did not open, visit the link above")
	if !m.onboard.openedBrowser {
		opened = m.style.FaintText("opening this in your browser" + m.style.Glyphs.Ellipsis)
	}

	lines := []string{
		m.logo(),
		"",
		lipgloss.JoinVertical(lipgloss.Center, explain...),
		"",
		urlBox,
		"",
		spinner + " " + m.style.DimText("waiting for you to authorise this device"+m.style.Glyphs.Ellipsis),
		opened,
	}
	lines = append(lines, m.healthLines()...)
	return lipgloss.JoinVertical(lipgloss.Center, lines...), []hint{
		hintFor(m.keys.Copy, "open in browser"),
		hintFor(m.keys.Retry, "restart login"),
		{"ctrl+l", "logs"}, {"q", "quit"},
	}
}

// onboardApproval covers a tailnet that gates new devices behind an admin.
// There is nothing the player can do here, so the screen says so plainly
// rather than spinning forever with no explanation.
func (m *Model) onboardApproval() (string, []hint) {
	th := m.style.Theme
	spinner := m.style.Text(th.Warn, m.style.Glyphs.Spin(m.phase(900*time.Millisecond)))
	name := m.node.Self.Hostname
	if name == "" {
		name = "this device"
	}
	lines := []string{
		m.logo(),
		"",
		m.style.Text(th.Warn, m.style.Glyphs.Bullet+" this tailnet requires new devices to be approved"),
		"",
		m.style.Text(th.Fg, "An admin needs to approve "+m.style.Bold(name)),
		m.style.Text(th.Fg, "in the Tailscale admin console."),
		"",
		m.style.DimText("Once they do, tailsnail continues on its own."),
		"",
		spinner + " " + m.style.DimText("waiting for approval"+m.style.Glyphs.Ellipsis),
	}
	lines = append(lines, m.healthLines()...)
	return lipgloss.JoinVertical(lipgloss.Center, lines...), []hint{
		{"ctrl+l", "logs"}, {"q", "quit"},
	}
}

// onboardLoggedOut covers an expired or revoked node key: the same flow as a
// first run, framed so the user knows why they are seeing it again.
func (m *Model) onboardLoggedOut() (string, []hint) {
	th := m.style.Theme
	lines := []string{
		m.logo(),
		"",
		m.style.Text(th.Warn, m.style.Glyphs.Bullet+" this device is logged out"),
		"",
		m.style.DimText("Its Tailscale key expired or was revoked."),
		m.style.DimText("Signing in again puts it back on your tailnet."),
		"",
		m.style.Text(th.Accent, "press ctrl+r to sign in again"),
	}
	lines = append(lines, m.healthLines()...)
	return lipgloss.JoinVertical(lipgloss.Center, lines...), []hint{
		hintFor(m.keys.Retry, "sign in"), {"ctrl+l", "logs"}, {"q", "quit"},
	}
}

// onboardTrouble covers everything else that can go wrong, most often no
// network at all.
func (m *Model) onboardTrouble() (string, []hint) {
	th := m.style.Theme
	detail := "the embedded Tailscale node could not start"
	if m.node.Err != nil {
		detail = m.node.Err.Error()
	}
	lines := []string{
		m.logo(),
		"",
		m.style.Text(th.Err, m.style.Glyphs.Cross+" cannot reach your tailnet"),
		"",
		lipgloss.NewStyle().Width(min(max(m.width-16, 24), 64)).Align(lipgloss.Center).
			Render(m.style.DimText(detail)),
		"",
		m.style.FaintText("tailsnail keeps retrying in the background"),
	}
	lines = append(lines, m.healthLines()...)
	return lipgloss.JoinVertical(lipgloss.Center, lines...), []hint{
		hintFor(m.keys.Retry, "retry"), {"ctrl+l", "logs"}, {"q", "quit"},
	}
}

// onboardSuccess is the brief beat between connecting and the menu.
func (m *Model) onboardSuccess() (string, []hint) {
	th := m.style.Theme
	self := m.node.Self

	// The check mark sweeps in over the hold, so the screen reads as an
	// arrival rather than a flash.
	progress := min(float64(m.now.Sub(m.onboard.successAt))/float64(onboardSuccessHold), 1)
	mark := m.style.Text(th.Ok.Scale(0.6+0.6*progress), m.style.Glyphs.Check)

	rows := [][2]string{
		{"device", self.Short()},
		{"address", self.IPv4},
	}
	if self.Login != "" {
		rows = append(rows, [2]string{"account", self.Login})
	}
	if self.Tailnet != "" {
		rows = append(rows, [2]string{"tailnet", self.Tailnet})
	}

	var detail []string
	for _, r := range rows {
		if r[1] == "" {
			continue
		}
		detail = append(detail, m.style.DimText(pad(r[0], 8))+m.style.Text(th.Fg, r[1]))
	}

	lines := []string{
		m.logo(),
		"",
		mark + " " + m.style.Text(th.Ok, "you're on the tailnet"),
		"",
		lipgloss.JoinVertical(lipgloss.Left, detail...),
	}
	return lipgloss.JoinVertical(lipgloss.Center, lines...), nil
}

// healthLines surfaces backend health warnings, which are usually the real
// explanation for a connection that will not complete.
func (m *Model) healthLines() []string {
	if len(m.node.Health) == 0 {
		return nil
	}
	out := []string{""}
	for _, w := range m.node.Health {
		out = append(out, m.style.Text(m.style.Theme.Warn, m.style.Glyphs.Bullet+" "+truncate(w, max(m.width-8, 20))))
	}
	return out
}

// logo renders the wordmark: a live snake drawn with the same glyphs and the
// same shimmer the arena uses, so the brand and the game are the same thing.
func (m *Model) logo() string {
	g := m.style.Glyphs
	th := m.style.Theme
	phase := m.phase(2200 * time.Millisecond)

	const segments = 11
	var b strings.Builder
	b.WriteString(m.style.Text(th.HeadColor(0, phase), g.Head(0)))
	for i := 1; i < segments; i++ {
		glyph := g.Body
		if i > segments-3 {
			glyph = g.Tail
		}
		b.WriteString(m.style.Text(th.TailColor(0, i, segments, phase), glyph))
	}
	snake := b.String()

	word := m.style.Text(th.Fg, "t a i l s n a i l")
	tag := m.style.FaintText("peer-to-peer snake over your tailnet")
	return lipgloss.JoinVertical(lipgloss.Center, snake, "", word, tag)
}

// shortURL trims a URL for a one-line notice.
func shortURL(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		return u[:i]
	}
	return u
}
