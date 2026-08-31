package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/theolol/tailsnail/internal/discovery"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
)

// browserState holds the lobby browser's view of discovery.
type browserState struct {
	snapshot discovery.Snapshot
	cursor   int
	// joining is the peer a join is in flight for, so its row can show that
	// something is happening. It is cleared as soon as the attempt settles,
	// either way — a stale flag left a lobby reading "joining" forever.
	joining string
}

// rows returns the peers to display: lobbies first, then peers that are
// running tailsnail but not hosting.
func (b *browserState) rows() []discovery.Peer { return b.snapshot.Peers }

// clampCursor keeps the selection inside the current list.
func (b *browserState) clampCursor() {
	n := len(b.rows())
	if n == 0 {
		b.cursor = 0
		return
	}
	b.cursor = min(max(b.cursor, 0), n-1)
}

// selected returns the highlighted peer.
func (b *browserState) selected() (discovery.Peer, bool) {
	rows := b.rows()
	if b.cursor < 0 || b.cursor >= len(rows) {
		return discovery.Peer{}, false
	}
	return rows[b.cursor], true
}

// updateBrowser handles lobby browser input.
func (m *Model) updateBrowser(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.browser.cursor--
		m.browser.clampCursor()
	case key.Matches(msg, m.keys.Down):
		m.browser.cursor++
		m.browser.clampCursor()
	case key.Matches(msg, m.keys.Refresh):
		m.app.Prober.Refresh()
		return m.setToast(toastInfo, "Scanning the tailnet%s", m.style.Glyphs.Ellipsis)
	case key.Matches(msg, m.keys.Enter):
		return m.joinSelected()
	case key.Matches(msg, m.keys.Back):
		m.screen = screenMenu
	case msg.String() == "q":
		return m.quit()
	case msg.String() == "h":
		m.screen = screenHostForm
		m.returnTo = screenBrowser
	}
	return nil
}

// joinSelected dials the highlighted lobby.
func (m *Model) joinSelected() tea.Cmd {
	peer, ok := m.browser.selected()
	if !ok {
		return m.setToast(toastWarn, "No lobby selected")
	}
	if peer.Advert == nil {
		return m.setToast(toastWarn, "%s is not hosting a game", peer.DisplayName)
	}
	if !peer.Advert.Joinable() {
		reason := "that lobby is full"
		if peer.Advert.Phase != proto.PhaseOpen {
			reason = "that game is already under way"
		}
		return m.setToast(toastWarn, "%s", capitalise(reason))
	}
	addr, err := discovery.DialAddr(peer)
	if err != nil {
		return m.setToast(toastErr, "%v", err)
	}

	m.browser.joining = peer.NodeID
	lobbyID := peer.Advert.LobbyID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.app.Ctx, 12*time.Second)
		defer cancel()
		client, err := netplay.Join(ctx, m.app.Server.Dial, netplay.JoinOptions{
			Addr:     addr,
			LobbyID:  lobbyID,
			Identity: m.app.Ident,
			Store:    m.app.Store,
			Log:      m.app.Log,
			Hostname: m.node.Self.Short(),
		})
		if err != nil {
			return sessionReadyMsg{err: err}
		}
		return sessionReadyMsg{session: client}
	}
}

// viewBrowser renders the lobby list.
func (m *Model) viewBrowser() string {
	g := m.style.Glyphs
	rows := m.browser.rows()

	var body string
	var anchor rowAnchor
	switch {
	case len(rows) == 0 && m.browser.snapshot.At.IsZero():
		body = m.emptyBrowser("looking for peers"+g.Ellipsis, "")
	case len(rows) == 0:
		body = m.emptyBrowser(
			"no tailsnail peers found",
			fmt.Sprintf("checked %d online %s on your tailnet",
				m.browser.snapshot.Candidates, plural(m.browser.snapshot.Candidates, "device", "devices")))
	default:
		body, anchor = m.browserTable(rows)
	}

	frame := m.chrome("lobbies", m.browserSubtitle(), body, []hint{
		{"↑/↓", "move"}, {"enter", "join"}, {"R", "refresh"},
		{"h", "host"}, {"esc", "back"}, {"q", "quit"},
	})
	// The table is left-aligned directly under the two-line header.
	const bodyTop = 2
	return m.withTooltip(frame, tooltip{
		text: anchor.text,
		row:  bodyTop + anchor.row,
		// The anchor is already the column the row's content starts in, so it
		// is used as-is: the notch lands under the row's first character.
		col:    anchor.col,
		prefer: placeBelow,
	})
}

// browserSubtitle summarises the last sweep.
func (m *Model) browserSubtitle() string {
	s := m.browser.snapshot
	if s.Err != nil {
		return m.style.Text(m.style.Theme.Err, "cannot read the tailnet: "+s.Err.Error())
	}
	if s.Scanning {
		return m.style.Glyphs.Spin(m.phase(700*time.Millisecond)) + " scanning"
	}
	lobbies := len(s.Lobbies())
	return fmt.Sprintf("%d %s  %s  %d %s online",
		lobbies, plural(lobbies, "lobby", "lobbies"), m.style.Glyphs.Bullet,
		s.Candidates, plural(s.Candidates, "device", "devices"))
}

// emptyBrowser renders the no-peers state with something to look at.
func (m *Model) emptyBrowser(title, detail string) string {
	spinner := m.style.Accent(m.style.Glyphs.Spin(m.phase(800 * time.Millisecond)))
	lines := []string{
		spinner + " " + m.style.DimText(title),
	}
	if detail != "" {
		lines = append(lines, "", m.style.FaintText(detail))
	}
	lines = append(lines, "",
		m.style.FaintText("peers appear here as soon as they open a lobby"),
		m.style.FaintText("press h to host one yourself"))
	return m.center(lipgloss.JoinVertical(lipgloss.Center, lines...), m.bodyHeight())
}

// browserTable renders the peer list.
func (m *Model) browserTable(rows []discovery.Peer) (string, rowAnchor) {
	th := m.style.Theme
	g := m.style.Glyphs

	head := m.style.FaintText("  " +
		pad("lobby", 20) + pad("host", 20) + pad("arena", 16) + pad("seats", 8) + "state")
	// The rule spans the table, not an arbitrary width, so it lines up with
	// the columns above and below it.
	out := []string{head, m.style.FaintText(strings.Repeat(g.Horizontal, ansi.StringWidth(head)))}
	var anchor rowAnchor

	visible := max(m.bodyHeight()-3, 1)
	start := 0
	if m.browser.cursor >= visible {
		start = m.browser.cursor - visible + 1
	}
	end := min(start+visible, len(rows))

	for i := start; i < end; i++ {
		p := rows[i]
		selected := i == m.browser.cursor
		marker := "  "
		if selected {
			marker = m.style.Accent(g.Arrow + " ")
		}

		name, arena, seats, state := "—", "—", "—", m.style.FaintText("idle")
		nameColor := th.Faint
		if a := p.Advert; a != nil {
			name = a.Name
			nameColor = th.Fg
			arena = fmt.Sprintf("%d×%d %s", a.Config.Width, a.Config.Height, a.Config.Mode)
			seats = fmt.Sprintf("%d/%d", a.Taken, a.Seats)
			switch {
			case a.Phase == proto.PhaseInGame:
				state = m.style.Text(th.Warn, "in game")
			case a.Taken >= a.Seats:
				state = m.style.Text(th.Warn, "full")
			default:
				state = m.style.Text(th.Ok, "open")
			}
		}
		// The empty string is the sentinel for "no join in flight", so it must
		// not be allowed to match a peer whose node ID came through empty —
		// that peer would read as "joining" forever.
		if m.browser.joining != "" && m.browser.joining == p.NodeID {
			state = m.style.Accent(g.Spin(m.phase(600*time.Millisecond)) + " joining")
		}

		host := p.DisplayName
		if p.Short != "" && p.Short != host {
			host = fmt.Sprintf("%s (%s)", host, p.Short)
		}

		line := marker +
			m.style.Text(nameColor, pad(truncate(name, 19), 20)) +
			m.style.DimText(pad(truncate(host, 19), 20)) +
			m.style.DimText(pad(arena, 16)) +
			m.style.DimText(pad(seats, 8)) +
			state
		if selected {
			// Anchored at the start of the row, not its end: the detail
			// hangs underneath aligned with the row it belongs to.
			anchor = rowAnchor{row: len(out), col: rowContentColumn, text: m.peerDetail(p)}
		}
		out = append(out, line)
	}
	return lipgloss.NewStyle().Width(m.width).Render(lipgloss.JoinVertical(lipgloss.Left, out...)), anchor
}

// peerDetail is the extra information shown for the highlighted peer.
//
// It is laid out as lines rather than one bullet-separated run, because a run
// that wraps inside a popover breaks in the middle of a bullet and reads as
// though a value has gone missing.
func (m *Model) peerDetail(p discovery.Peer) string {
	var lines []string
	if p.DNSName != "" {
		lines = append(lines, p.DNSName)
	}
	if p.Login != "" {
		lines = append(lines, p.Login)
	}

	var stats []string
	if p.Addr.IsValid() {
		stats = append(stats, p.Addr.String())
	}
	if p.RTT > 0 {
		stats = append(stats, fmt.Sprintf("%dms", p.RTT.Milliseconds()))
	}
	if p.AppVersion != "" {
		stats = append(stats, "v"+p.AppVersion)
	}
	if len(stats) > 0 {
		lines = append(lines, strings.Join(stats, "  "+m.style.Glyphs.Bullet+"  "))
	}

	if a := p.Advert; a != nil {
		wrap := "walled"
		if a.Config.Wrap {
			wrap = "wrap-around"
		}
		lines = append(lines, fmt.Sprintf("%d ticks/s, %s", a.Config.TickRate, wrap))
	}
	return strings.Join(lines, "\n")
}
