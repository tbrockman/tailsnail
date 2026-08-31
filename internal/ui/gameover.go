package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
)

// gameOverSlide is how long the results dialog takes to settle into place.
const gameOverSlide = 420 * time.Millisecond

// overState is the results screen.
type overState struct {
	state   game.State
	players []proto.Player
	reason  string
	record  *proto.AttestedRecord
	at      time.Time
}

// apply captures the final state of a match.
func (o *overState) apply(ev netplay.MatchOver, lobby proto.LobbyState, now time.Time) {
	players := ev.Players
	if len(players) == 0 {
		players = lobby.Players
	}
	*o = overState{state: ev.State, players: players, reason: ev.Reason, at: now}
}

// playerFor returns a seat's roster entry.
func (o *overState) playerFor(id game.PlayerID) (proto.Player, bool) {
	for _, p := range o.players {
		if p.Seat == id {
			return p, true
		}
	}
	return proto.Player{}, false
}

// updateGameOver handles the results dialog.
func (m *Model) updateGameOver(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Enter), key.Matches(msg, m.keys.Back):
		if m.session != nil {
			m.screen = screenRoom
		} else {
			m.screen = screenBrowser
			m.app.Prober.Refresh()
		}
	case msg.String() == "H":
		m.history.reload(m.app.Store)
		m.screen = screenHistory
		m.returnTo = screenGameOver
	case msg.String() == "q":
		return m.quit()
	}
	return nil
}

// viewGameOver renders the final rankings.
func (m *Model) viewGameOver() string {
	th := m.style.Theme
	g := m.style.Glyphs

	// Rank by placement, which the simulation already assigned.
	snakes := append([]game.Snake(nil), m.over.state.Snakes...)
	sort.Slice(snakes, func(i, j int) bool { return snakes[i].Placement < snakes[j].Placement })

	rows := []string{
		m.style.FaintText(pad("   place", 9) + pad("player", 20) + pad("length", 8) +
			pad("food", 6) + pad("kills", 6) + "survived"),
	}
	for _, sn := range snakes {
		p, _ := m.over.playerFor(sn.ID)
		slot := p.Palette
		name := p.DisplayName
		if name == "" {
			name = fmt.Sprintf("seat %d", sn.ID)
		}
		mine := m.session != nil && sn.ID == m.game.seat

		place := fmt.Sprintf("%d", sn.Placement)
		placeColor := th.Dim
		if sn.Placement == 1 {
			// The winner's row pulses gently rather than sitting still.
			placeColor = th.Accent.Scale(0.85 + 0.3*m.pulse(1400*time.Millisecond))
			place = g.Check + " 1"
		}

		nameText := name
		if mine {
			nameText += " (you)"
		}
		survived := duration(time.Duration(sn.DiedAtTick) * time.Second / time.Duration(max(m.game.cfg.TickRate, 1)))
		if sn.DiedAtTick < 0 {
			survived = m.style.Text(th.Ok, "to the end")
		}

		rows = append(rows, "  "+
			m.style.Text(placeColor, pad(place, 7))+
			m.style.Text(th.Player(slot), g.Head(slot)+" ")+
			m.style.Text(th.Player(slot), pad(truncate(nameText, 18), 19))+
			m.style.DimText(pad(fmt.Sprintf("%d", sn.MaxLength), 8))+
			m.style.DimText(pad(fmt.Sprintf("%d", sn.Score), 6))+
			m.style.DimText(pad(fmt.Sprintf("%d", sn.Kills), 6))+
			m.style.DimText(survived))
	}

	rows = append(rows, "", m.attestationLine())

	title := m.style.Bold("match over")
	if winner, ok := m.winnerName(snakes); ok {
		title = m.style.Bold("match over") + m.style.DimText("  —  ") + m.style.Accent(winner+" wins")
	}

	panel := m.style.Panel().Width(min(max(m.width-8, 50), 76)).Render(
		lipgloss.JoinVertical(lipgloss.Left, append([]string{title, ""}, rows...)...))

	// Slide the dialog up into place, which reads as the results arriving
	// rather than the arena being replaced.
	progress := min(float64(m.now.Sub(m.over.at))/float64(gameOverSlide), 1)
	offset := int((1 - progress) * 4)
	body := strings.Repeat("\n", offset) + panel

	hints := []hint{{"enter", "back to lobby"}, {"H", "history"}, {"q", "quit"}}
	return m.chrome("", "", m.center(body, m.bodyHeight()), hints)
}

// winnerName returns the display name of the first-place finisher.
func (m *Model) winnerName(snakes []game.Snake) (string, bool) {
	for _, sn := range snakes {
		if sn.Placement != 1 {
			continue
		}
		if p, ok := m.over.playerFor(sn.ID); ok && p.DisplayName != "" {
			return p.DisplayName, true
		}
		return fmt.Sprintf("seat %d", sn.ID), true
	}
	return "", false
}

// attestationLine reports whether the result has been signed and stored yet.
func (m *Model) attestationLine() string {
	th := m.style.Theme
	g := m.style.Glyphs
	if m.over.record == nil {
		spinner := m.style.Accent(g.Spin(m.phase(700 * time.Millisecond)))
		return spinner + " " + m.style.DimText("collecting signatures"+g.Ellipsis)
	}
	rec := m.over.record
	if rec.FullyAttested() {
		return m.style.Text(th.Ok, g.Check+" ") +
			m.style.DimText(fmt.Sprintf("signed by all %d players and recorded", len(rec.Signatures)))
	}
	return m.style.Text(th.Warn, g.Bullet+" ") +
		m.style.DimText(fmt.Sprintf("recorded %s — someone left before signing", rec.AttestationSummary()))
}
