package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
			action: func(m *Model) tea.Cmd {
				m.screen = screenSettings
				m.returnTo = screenMenu
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

	rows := make([]string, 0, len(m.menu.items)*2)
	for i, item := range m.menu.items {
		selected := i == m.menu.cursor
		marker := "  "
		title := m.style.Text(th.Dim, item.title)
		if selected {
			// The caret breathes so the selection is obvious without colour.
			marker = m.style.Accent(g.Arrow + " ")
			title = m.style.Text(th.Accent, item.title)
		}
		number := m.style.FaintText(fmt.Sprintf("%d ", i+1))
		rows = append(rows, marker+number+title)
		if selected {
			rows = append(rows, m.style.FaintText("     "+item.desc))
		} else {
			rows = append(rows, "")
		}
	}

	panel := m.style.Panel().Width(min(max(m.width-8, 30), 52)).Render(
		lipgloss.JoinVertical(lipgloss.Left, rows...))

	body := lipgloss.JoinVertical(lipgloss.Center,
		m.logo(),
		"",
		panel,
		"",
		m.style.FaintText(m.storeSummary()),
	)
	return m.chrome("", "", m.center(body, m.bodyHeight()), []hint{
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
