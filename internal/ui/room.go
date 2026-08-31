package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/proto"
)

// roomState is the lobby room's view of the roster.
type roomState struct {
	state proto.LobbyState
	// cursor selects a player, used by the host to kick.
	cursor int
	// changedAt marks the last roster change, so arrivals can animate in.
	changedAt time.Time
}

// reset clears the room for a new session.
func (r *roomState) reset() {
	*r = roomState{}
}

// apply folds in a new roster.
func (r *roomState) apply(st proto.LobbyState) {
	if len(st.Players) != len(r.state.Players) {
		r.changedAt = time.Now()
	}
	r.state = st
	if r.cursor >= len(st.Players) {
		r.cursor = max(len(st.Players)-1, 0)
	}
}

// me returns this peer's own roster entry.
func (r *roomState) me(seat game.PlayerID) (proto.Player, bool) {
	for _, p := range r.state.Players {
		if p.Seat == seat {
			return p, true
		}
	}
	return proto.Player{}, false
}

// updateRoom handles lobby room input.
func (m *Model) updateRoom(msg tea.KeyMsg) tea.Cmd {
	if m.session == nil {
		m.screen = screenBrowser
		return nil
	}
	switch {
	case key.Matches(msg, m.keys.Activity):
		m.openModal(modalActivity)
	case key.Matches(msg, m.keys.Edit):
		return m.editLobbySettings()
	case key.Matches(msg, m.keys.Up):
		m.room.cursor = wrapIndex(m.room.cursor-1, len(m.room.state.Players))
	case key.Matches(msg, m.keys.Down):
		m.room.cursor = wrapIndex(m.room.cursor+1, len(m.room.state.Players))
	case key.Matches(msg, m.keys.Ready), key.Matches(msg, m.keys.Enter):
		me, ok := m.room.me(m.session.Seat())
		if ok {
			m.session.SetReady(!me.Ready)
		}
	case key.Matches(msg, m.keys.Kick):
		return m.kickSelected()
	case key.Matches(msg, m.keys.Back):
		reason := "left the lobby"
		if m.session.IsHost() {
			reason = "the host closed the lobby"
		}
		m.leaveSession(reason)
		m.screen = screenBrowser
	case msg.String() == "q":
		return m.quit()
	}
	return nil
}

// editLobbySettings opens the host form pre-filled with the running lobby, so
// the settings can be adjusted without tearing the room down.
func (m *Model) editLobbySettings() tea.Cmd {
	if m.session == nil || !m.session.IsHost() {
		return m.setToast(toastWarn, "Only the host can change the settings")
	}
	if m.room.state.Phase == proto.PhaseInGame {
		return m.setToast(toastWarn, "Settings cannot change during a match")
	}
	m.form.editFrom(m.room.state)
	m.returnTo = screenRoom
	m.screen = screenHostForm
	return nil
}

// kickSelected removes the highlighted player, host only.
func (m *Model) kickSelected() tea.Cmd {
	if !m.session.IsHost() {
		return m.setToast(toastWarn, "Only the host can remove players")
	}
	if m.room.cursor >= len(m.room.state.Players) {
		return nil
	}
	target := m.room.state.Players[m.room.cursor]
	if target.Seat == m.session.Seat() {
		return m.setToast(toastWarn, "Press esc to close your own lobby")
	}
	m.session.Kick(target.Seat)
	return m.setToast(toastInfo, "Removed %s", target.DisplayName)
}

// viewRoom renders the lobby room.
func (m *Model) viewRoom() string {
	if m.session == nil {
		return m.chrome("lobby", "", m.center(m.style.DimText("leaving"+m.style.Glyphs.Ellipsis), m.bodyHeight()), nil)
	}
	st := m.room.state

	// The countdown owns the screen. Once the match is seconds away the roster
	// is no longer what anyone is looking at, and leaving it up under a
	// swelling digit just makes the moment noisy.
	if st.Phase == proto.PhaseCountdown {
		return m.chrome("", "", m.center(m.countdownBanner(st.Countdown), m.bodyHeight()), nil)
	}

	body := lipgloss.JoinVertical(lipgloss.Center,
		m.rosterPanel(),
		"",
		m.readinessBanner(),
	)
	headline := st.Name + "  " + m.style.Glyphs.Bullet + "  " + m.configSummary(st.Config)
	return m.chrome("lobby", headline, m.center(body, m.bodyHeight()), m.roomHints())
}

// viewActivityModal draws the lobby's event feed over the current screen.
//
// The feed used to sit permanently beside the roster, where long lines wrapped
// and were then clipped, and where it took space from the thing people are
// actually reading. As a dialog it can be as wide as it needs and is only
// present when someone asks for it.
func (m *Model) viewActivityModal(frame string) string {
	events := m.room.state.Events
	width := min(max(m.width-12, 30), 60)

	visible := max(min(m.height-10, 14), 3)
	end := max(len(events)-m.modalTop, 0)
	start := max(end-visible, 0)
	m.modalTop = min(m.modalTop, max(len(events)-1, 0))

	var rows []string
	if len(events) == 0 {
		rows = append(rows, m.style.FaintText("nothing has happened yet"))
	}
	for _, e := range events[start:end] {
		stamp := m.style.FaintText(e.At.Format("15:04:05") + " ")
		// Wrap rather than clip: a dialog has the room, and a line cut in half
		// is worse than one that runs on.
		text := lipgloss.NewStyle().Width(width - 11).Render(m.style.DimText(e.Text))
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, stamp, text))
	}

	footer := "esc close"
	if len(events) > visible {
		footer = fmt.Sprintf("%d of %d  %s  ↑/↓ scroll  %s  esc close",
			end-start, len(events), m.style.Glyphs.Bullet, m.style.Glyphs.Bullet)
	}
	return m.renderModal(frame, "activity", lipgloss.JoinVertical(lipgloss.Left, rows...), footer, width)
}

// roomHints builds the help bar for the room, which differs for the host.
func (m *Model) roomHints() []hint {
	hints := []hint{{"r", "ready"}, {"a", "activity"}}
	if m.session != nil && m.session.IsHost() {
		hints = append(hints,
			hint{"e", "edit settings"}, hint{"↑/↓", "select"},
			hint{"x", "remove"}, hint{"esc", "close lobby"})
	} else {
		hints = append(hints, hint{"esc", "leave"})
	}
	return append(hints, hint{",", "settings"}, hint{"q", "quit"})
}

// configSummary renders the match settings. The lobby's own name is not
// included: it comes from the lobby state, which is what the host actually
// advertised, rather than from the config it was built with.
func (m *Model) configSummary(cfg game.Config) string {
	walls := "walls"
	if cfg.Wrap {
		walls = "wrap"
	}
	mode := "classic"
	if cfg.Mode == game.ModeShrink {
		mode = "shrinking"
	}
	return fmt.Sprintf("%d×%d  %s  %d ticks/s  %s", cfg.Width, cfg.Height, walls, cfg.TickRate, mode)
}

// rosterPanel renders the seated players.
func (m *Model) rosterPanel() string {
	th := m.style.Theme
	g := m.style.Glyphs
	st := m.room.state

	rows := []string{m.style.FaintText(pad("  player", 26) + pad("device", 18) + "ready")}
	for i, p := range st.Players {
		selected := m.session != nil && m.session.IsHost() && i == m.room.cursor
		marker := "  "
		if selected {
			marker = m.style.Accent(g.Arrow + " ")
		}

		// The head glyph is the player's identity everywhere: here, in the
		// arena, and on the results screen.
		glyph := m.style.Text(th.HeadColor(p.Palette, m.phase(1600*time.Millisecond)), g.Head(p.Palette))

		name := p.DisplayName
		if m.session != nil && p.Seat == m.session.Seat() {
			name += " (you)"
		}
		nameCell := m.style.Text(th.Player(p.Palette), pad(truncate(name, 20), 21))

		device := p.Node
		if device == "" {
			device = proto.ShortKey(p.PubKey)
		}
		if p.Host {
			device = m.style.Text(th.Accent2, "host") + m.style.DimText(" "+truncate(device, 12))
		} else {
			device = m.style.DimText(truncate(device, 17))
		}

		ready := m.style.FaintText(g.Cross + " waiting")
		if p.Ready {
			ready = m.style.Text(th.Ok, g.Check+" ready")
		}
		if !p.Connected {
			ready = m.style.Text(th.Warn, g.Bullet+" offline")
		}

		rows = append(rows, marker+glyph+" "+nameCell+pad(device, 18)+ready)
		if p.Login != "" && selected {
			rows = append(rows, m.style.FaintText("     "+p.Login))
		}
	}

	for i := len(st.Players); i < st.Config.MaxPlayers; i++ {
		rows = append(rows, m.style.FaintText("  "+g.Bullet+" empty seat"))
	}

	title := fmt.Sprintf("%d/%d seats", len(st.Players), st.Config.MaxPlayers)
	return m.style.Panel().Width(min(max(m.width-8, 40), 56)).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			append([]string{m.style.Bold(title), ""}, rows...)...))
}

// readinessBanner tells the group what is holding the match up.
func (m *Model) readinessBanner() string {
	st := m.room.state
	th := m.style.Theme

	waiting := 0
	for _, p := range st.Players {
		if !p.Ready {
			waiting++
		}
	}
	if len(st.Players) == 0 {
		return m.style.FaintText("opening the lobby" + m.style.Glyphs.Ellipsis)
	}
	// A lobby with only its host is waiting for company, not for a decision.
	people := 0
	for _, p := range st.Players {
		if !p.Bot {
			people++
		}
	}
	if people == 1 && len(st.Players) == 1 {
		color := th.Dim.Lerp(th.Accent, m.pulse(2*time.Second))
		return m.style.Text(color, "waiting for players — press r to start solo, or add bots with e")
	}
	if waiting == 0 {
		return m.style.Text(th.Ok, "everyone is ready")
	}
	// Breathe the prompt so an idle lobby still looks alive.
	color := th.Dim.Lerp(th.Accent, m.pulse(2*time.Second))
	if me, ok := m.room.me(m.session.Seat()); ok && !me.Ready {
		return m.style.Text(color, "press r when you're ready")
	}
	return m.style.Text(th.Dim, fmt.Sprintf("waiting on %d %s", waiting, plural(waiting, "player", "players")))
}

// countdownBanner renders the animated 3-2-1 before kickoff.
func (m *Model) countdownBanner(n int) string {
	if n <= 0 {
		return m.style.Text(m.style.Theme.Accent, "go!")
	}
	th := m.style.Theme
	// Each digit swells as its second begins and settles as it ends.
	beat := m.pulse(time.Second)
	color := th.Accent.Scale(0.75 + 0.45*beat)

	digit := bigDigit(n, m.style.Glyphs.ASCII)
	lines := make([]string, 0, len(digit)+2)
	for _, l := range digit {
		lines = append(lines, m.style.Text(color, l))
	}
	lines = append(lines, "", m.style.DimText("starting"+m.style.Glyphs.Ellipsis))
	return lipgloss.JoinVertical(lipgloss.Center, lines...)
}

// bigDigit returns a five-line rendering of 1, 2 or 3 for the countdown.
func bigDigit(n int, ascii bool) []string {
	block, light := "█", "▀"
	if ascii {
		block, light = "#", "="
	}
	r := strings.NewReplacer("#", block, "=", light)
	var art []string
	switch n {
	case 3:
		art = []string{"#####", "   ##", " ####", "   ##", "#####"}
	case 2:
		art = []string{"#####", "   ##", "#####", "##   ", "#####"}
	case 1:
		art = []string{"  ##  ", "####  ", "  ##  ", "  ##  ", "######"}
	default:
		art = []string{"#####", "##  #", "##  #", "##  #", "#####"}
	}
	out := make([]string, len(art))
	for i, l := range art {
		out[i] = r.Replace(l)
	}
	return out
}
