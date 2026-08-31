package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
)

// historyTab selects between the two views on stored match records.
type historyTab int

const (
	tabLeaderboard historyTab = iota
	tabMatches
)

// rowAnchor is where a table's selected row ended up, so a popover can attach
// to it after the table has been composed.
type rowAnchor struct {
	// row is the offset within the table block, and col the column just past
	// the row's text.
	row, col int
	text     string
}

// historyState is the history and leaderboard screen.
type historyState struct {
	tab     historyTab
	cursor  int
	records []proto.AttestedRecord
	board   []store.PlayerStats
	// loadedAt drives the periodic reload, so records arriving by gossip
	// appear without the user having to leave and come back.
	loadedAt time.Time
}

// initHistory loads the store at startup so the menu footer has a count.
func (m *Model) initHistory() { m.history.reload(m.app.Store) }

// reload refreshes both views from the store.
func (h *historyState) reload(st *store.Store) {
	h.records = st.All()
	h.board = st.Leaderboard()
	h.loadedAt = time.Now()
	h.clamp()
}

// clamp keeps the cursor inside the active list.
func (h *historyState) clamp() {
	n := len(h.board)
	if h.tab == tabMatches {
		n = len(h.records)
	}
	if n == 0 {
		h.cursor = 0
		return
	}
	h.cursor = min(max(h.cursor, 0), n-1)
}

// updateHistory handles history screen input.
func (m *Model) updateHistory(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.history.cursor--
		m.history.clamp()
	case key.Matches(msg, m.keys.Down):
		m.history.cursor++
		m.history.clamp()
	case key.Matches(msg, m.keys.Left), key.Matches(msg, m.keys.Right), msg.Type == tea.KeyTab:
		if m.history.tab == tabLeaderboard {
			m.history.tab = tabMatches
		} else {
			m.history.tab = tabLeaderboard
		}
		m.history.cursor = 0
	case key.Matches(msg, m.keys.Refresh):
		m.history.reload(m.app.Store)
		return m.setToast(toastInfo, "Reloaded %d %s",
			len(m.history.records), plural(len(m.history.records), "match", "matches"))
	case key.Matches(msg, m.keys.Back):
		m.screen = m.returnTo
	case msg.String() == "q":
		return m.quit()
	}
	return nil
}

// viewHistory renders the leaderboard or the match list.
func (m *Model) viewHistory() string {
	tabs := m.historyTabs()
	var body string
	var anchor rowAnchor
	if m.history.tab == tabLeaderboard {
		body, anchor = m.leaderboardTable()
	} else {
		body, anchor = m.matchTable()
	}

	subtitle := fmt.Sprintf("%d %s stored", len(m.history.records),
		plural(len(m.history.records), "match", "matches"))
	content := lipgloss.JoinVertical(lipgloss.Left, tabs, "", body)
	frame := m.chrome("history", subtitle, content, []hint{
		{"tab/←→", "switch view"}, {"↑/↓", "move"}, {"R", "reload"},
		{"esc", "back"}, {"q", "quit"},
	})

	// The table is left-aligned at the top of the body, which begins after the
	// two-line header, the tab strip and its blank line.
	const bodyTop = 2 + 2
	return m.withTooltip(frame, tooltip{
		text: anchor.text,
		row:  bodyTop + anchor.row,
		col:  anchor.col + 1,
		// A row's extra detail reads naturally hanging under it, and there is
		// rarely room beside a full-width table anyway.
		prefer: placeBelow,
	})
}

// historyTabs renders the two-tab selector.
func (m *Model) historyTabs() string {
	th := m.style.Theme
	render := func(t historyTab, label string) string {
		if m.history.tab == t {
			return m.style.Text(th.Accent, "["+label+"]")
		}
		return m.style.FaintText(" " + label + " ")
	}
	return "  " + render(tabLeaderboard, "leaderboard") + "  " + render(tabMatches, "matches")
}

// leaderboardTable renders aggregate standings.
func (m *Model) leaderboardTable() (string, rowAnchor) {
	th := m.style.Theme
	g := m.style.Glyphs

	if len(m.history.board) == 0 {
		return m.center(lipgloss.JoinVertical(lipgloss.Center,
			m.style.DimText("no matches recorded yet"),
			"",
			m.style.FaintText("play a game, or let tailsnail sync history"),
			m.style.FaintText("from a peer that already has some"),
		), m.bodyHeight()-3), rowAnchor{}
	}

	rows := []string{m.style.FaintText("  " + pad("#", 4) + pad("player", 22) +
		pad("wins", 7) + pad("played", 8) + pad("rate", 7) + pad("kills", 7) + "best")}
	var anchor rowAnchor

	visible := max(m.bodyHeight()-5, 1)
	start := 0
	if m.history.cursor >= visible {
		start = m.history.cursor - visible + 1
	}
	end := min(start+visible, len(m.history.board))

	for i := start; i < end; i++ {
		ps := m.history.board[i]
		marker := "  "
		nameColor := th.Fg
		if i == m.history.cursor {
			marker = m.style.Accent(g.Arrow + " ")
		}
		if ps.PubKey == m.app.Ident.PubKey() {
			nameColor = th.Accent
		}
		name := ps.DisplayName
		if ps.PubKey == m.app.Ident.PubKey() {
			name += " (you)"
		}
		row := marker +
			m.style.FaintText(pad(fmt.Sprintf("%d", i+1), 4)) +
			m.style.Text(nameColor, pad(truncate(name, 21), 22)) +
			m.style.Text(th.Ok, pad(fmt.Sprintf("%d", ps.Wins), 7)) +
			m.style.DimText(pad(fmt.Sprintf("%d", ps.Matches), 8)) +
			m.style.DimText(pad(fmt.Sprintf("%.0f%%", ps.WinRate()*100), 7)) +
			m.style.DimText(pad(fmt.Sprintf("%d", ps.Kills), 7)) +
			m.style.DimText(fmt.Sprintf("%d", ps.BestLength))
		if i == m.history.cursor {
			detail := ps.Login
			if detail != "" {
				detail += "  " + g.Bullet + "  "
			}
			anchor = rowAnchor{
				row:  len(rows),
				col:  ansi.StringWidth(row),
				text: detail + "key " + proto.ShortKey(ps.PubKey),
			}
		}
		rows = append(rows, row)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...), anchor
}

// matchTable renders the recent-match list with attestation status.
func (m *Model) matchTable() (string, rowAnchor) {
	th := m.style.Theme
	g := m.style.Glyphs

	if len(m.history.records) == 0 {
		return m.center(m.style.DimText("no matches recorded yet"), m.bodyHeight()-3), rowAnchor{}
	}

	rows := []string{m.style.FaintText("  " + pad("when", 16) + pad("lobby", 20) +
		pad("winner", 18) + pad("players", 9) + "attestation")}
	var anchor rowAnchor

	visible := max(m.bodyHeight()-5, 1)
	start := 0
	if m.history.cursor >= visible {
		start = m.history.cursor - visible + 1
	}
	end := min(start+visible, len(m.history.records))

	for i := start; i < end; i++ {
		rec := m.history.records[i]
		marker := "  "
		if i == m.history.cursor {
			marker = m.style.Accent(g.Arrow + " ")
		}

		winner := "—"
		if key, ok := rec.Result.Winner(); ok {
			if p, ok := rec.Result.Participant(key); ok {
				winner = p.DisplayName
			}
		}
		status := m.style.Text(th.Ok, g.Check+" "+rec.AttestationSummary())
		if !rec.FullyAttested() {
			status = m.style.Text(th.Warn, g.Bullet+" "+rec.AttestationSummary())
		}

		row := marker +
			m.style.DimText(pad(relativeTime(rec.Result.Ended(), m.now), 16)) +
			m.style.Text(th.Fg, pad(truncate(rec.Result.LobbyName, 19), 20)) +
			m.style.Text(th.Accent, pad(truncate(winner, 17), 18)) +
			m.style.DimText(pad(fmt.Sprintf("%d", len(rec.Result.Participants)), 9)) +
			status
		if i == m.history.cursor {
			anchor = rowAnchor{row: len(rows), col: ansi.StringWidth(row), text: m.matchDetail(rec)}
		}
		rows = append(rows, row)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...), anchor
}

// matchDetail is the expanded line under the highlighted match.
func (m *Model) matchDetail(rec proto.AttestedRecord) string {
	cfg := rec.Result.Config
	length := rec.Result.Ended().Sub(rec.Result.Started())

	names := make([]string, 0, len(rec.Result.Placements))
	for _, pl := range rec.Result.Placements {
		if p, ok := rec.Result.Participant(pl.PubKey); ok {
			names = append(names, fmt.Sprintf("%d. %s", pl.Place, p.DisplayName))
		}
	}
	return fmt.Sprintf("%d×%d %s  %s  %s\n%s",
		cfg.Width, cfg.Height, cfg.Mode, m.style.Glyphs.Bullet,
		duration(length), strings.Join(names, "  "))
}

// historyRefreshInterval bounds how often the history screen re-reads the
// store while it is open, so gossip arriving in the background shows up.
const historyRefreshInterval = 5 * time.Second
