package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// menuItem is one entry on the main menu.
type menuItem struct {
	title  string
	desc   string
	action func(*Model) tea.Cmd
}

// menuState holds the main menu's selection.
type menuState struct {
	cursor int
	items  []menuItem
}

// initMenu builds the menu once at startup.
func (m *Model) initMenu() {
	m.menu.items = []menuItem{
		{
			title: "host a game",
			desc:  "configure an arena and let peers join",
			action: func(m *Model) tea.Cmd {
				m.screen = screenHostForm
				m.returnTo = screenMenu
				return nil
			},
		},
		{
			title: "find a game",
			desc:  "browse lobbies on your tailnet",
			action: func(m *Model) tea.Cmd {
				m.screen = screenBrowser
				m.returnTo = screenMenu
				return func() tea.Msg { return browserOpenedMsg{} }
			},
		},
		{
			title: "history",
			desc:  "past matches and the leaderboard",
			action: func(m *Model) tea.Cmd {
				m.history.reload(m.app.Store)
				m.screen = screenHistory
				m.returnTo = screenMenu
				return nil
			},
		},
		{
			title: "settings",
			desc:  "theme, glyphs, display name",
			// Everything routes through openSettings so the discard snapshot
			// is always taken; entering the screen any other way left it with
			// an empty name to "restore" on escape.
			action: func(m *Model) tea.Cmd {
				m.openSettings()
				return nil
			},
		},
		{
			title:  "quit",
			desc:   "leave any lobby and exit",
			action: func(m *Model) tea.Cmd { return m.quit() },
		},
	}
}

// updateMenu handles menu navigation.
func (m *Model) updateMenu(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.menu.cursor = wrapIndex(m.menu.cursor-1, len(m.menu.items))
	case key.Matches(msg, m.keys.Down):
		m.menu.cursor = wrapIndex(m.menu.cursor+1, len(m.menu.items))
	case key.Matches(msg, m.keys.Enter):
		return m.menu.items[m.menu.cursor].action(m)
	case msg.String() == "q":
		return m.quit()
	}
	// Number keys jump straight to an entry.
	if n := int(msg.String()[0]) - '1'; len(msg.String()) == 1 && n >= 0 && n < len(m.menu.items) {
		m.menu.cursor = n
		return m.menu.items[n].action(m)
	}
	return nil
}

// viewMenu renders the main menu.
func (m *Model) viewMenu() string {
	th := m.style.Theme
	g := m.style.Glyphs

	rows := make([]string, 0, len(m.menu.items))
	selectedRow, selectedWidth := 0, 0
	for i, item := range m.menu.items {
		marker := "  "
		title := m.style.Text(th.Dim, item.title)
		if i == m.menu.cursor {
			marker = m.style.Accent(g.Arrow + " ")
			title = m.style.Text(th.Accent, item.title)
		}
		row := marker + m.style.FaintText(fmt.Sprintf("%d ", i+1)) + title
		if i == m.menu.cursor {
			// The panel's top border sits above the first row.
			selectedRow = 1 + len(rows)
			selectedWidth = ansi.StringWidth(row)
		}
		rows = append(rows, row)
	}

	inner := min(max(naturalWidth(rows), 22), max(m.width-8, 20))
	panel := m.style.Panel().Width(inner + 2).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))

	logo := m.logo()
	footer := m.style.FaintText(m.storeSummary())
	body := lipgloss.JoinVertical(lipgloss.Center, logo, "", panel, "", footer)

	frame, top, left := m.place(body, m.bodyHeight())
	panelLeft := left + (lipgloss.Width(body)-lipgloss.Width(panel))/2
	panelTop := top + lipgloss.Height(logo) + 1
	frame = m.withTooltip(frame, tooltip{
		text: m.menu.items[m.menu.cursor].desc,
		row:  panelTop + selectedRow,
		col:  panelLeft + 2 + selectedWidth + 1,
	})

	return m.chrome("", "", frame, []hint{
		{"↑/↓", "move"}, {"enter", "select"}, {"1-5", "jump"},
		{"ctrl+l", "logs"}, {"q", "quit"},
	})
}

// storeSummary is the one-line footer showing how much history this peer has
// accumulated, which is the visible payoff of gossip.
func (m *Model) storeSummary() string {
	n := m.app.Store.Count()
	if n == 0 {
		return "no matches recorded yet"
	}
	peers := len(m.browser.snapshot.Peers)
	summary := fmt.Sprintf("%d %s recorded", n, plural(n, "match", "matches"))
	if peers > 0 {
		summary += fmt.Sprintf("  %s  %d %s on the tailnet",
			m.style.Glyphs.Bullet, peers, plural(peers, "peer", "peers"))
	}
	return summary
}

// wrapIndex wraps an index into [0,n).
func wrapIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i % n) + n) % n
}

// pulse returns a 0..1 triangle wave for the given cycle, used where a sine is
// more motion than a control needs.
func (m *Model) pulse(cycle time.Duration) float64 {
	p := m.phase(cycle)
	if p < 0.5 {
		return p * 2
	}
	return (1 - p) * 2
}
