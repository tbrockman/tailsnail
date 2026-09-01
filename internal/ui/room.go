package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tbrockman/tailsnail/internal/game"
	"github.com/tbrockman/tailsnail/internal/proto"
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

	// The countdown is normally played out on the arena, which is already on
	// screen by then. This only shows if the roster is somehow still in front.
	if st.Phase == proto.PhaseCountdown {
		return m.chrome(st.Name, fmt.Sprintf("starting in %d", max(st.Countdown, 0)),
			m.center(m.countdownBlock(st.Countdown, false), m.bodyHeight()), nil)
	}

	parts := []string{m.rosterPanel(), "", m.readinessBanner()}
	if note := m.boardFitNote(st.Config, st.Config.MaxPlayers); note != "" {
		parts = append(parts, "", note)
	}
	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	headline := st.Name + "  " + m.style.Glyphs.Bullet + "  " + m.configSummary(st.Config)
	return m.chrome("lobby", headline, m.center(body, m.bodyHeight()), m.roomHints())
}

// activityStampWidth is the width of the timestamp column, including its
// trailing space.
const activityStampWidth = 9

// activityLines renders the whole feed into display lines, wrapped at the
// given text width and padded so every line is the same width.
//
// Paging works over rendered lines rather than entries, because an entry that
// wraps to three lines would otherwise make one keypress jump three.
func (m *Model) activityLines(textWidth int) []string {
	events := m.room.state.Events
	if len(events) == 0 {
		return []string{pad(m.style.FaintText("nothing has happened yet"), activityStampWidth+textWidth)}
	}

	var out []string
	for _, e := range events {
		stamp := m.style.FaintText(e.At.Format("15:04:05") + " ")
		wrapped := lipgloss.NewStyle().Width(textWidth).Render(m.style.DimText(e.Text))
		for i, line := range strings.Split(wrapped, "\n") {
			prefix := stamp
			if i > 0 {
				// Continuation lines sit under the text, not the timestamp.
				prefix = strings.Repeat(" ", activityStampWidth)
			}
			out = append(out, pad(prefix+line, activityStampWidth+textWidth))
		}
	}
	return out
}

// activityTextWidth is the wrapping width, taken from the longest entry so the
// dialog hugs its contents and does not change width as it is scrolled.
func (m *Model) activityTextWidth() int {
	longest := 0
	for _, e := range m.room.state.Events {
		longest = max(longest, ansi.StringWidth(e.Text))
	}
	cap := min(max(m.width-28, 24), 56)
	return min(max(longest, 24), cap)
}

// activityCapacity is how many lines the dialog shows. It is fixed for a given
// window size, so scrolling never changes the dialog's height.
func (m *Model) activityCapacity() int {
	return min(max(m.height-12, 4), 14)
}

// viewActivityModal draws the lobby's event feed over the current screen.
//
// The feed used to sit permanently beside the roster, where long lines wrapped
// and were then clipped, and where it took space from the thing people are
// actually reading. As a dialog it can be as wide as it needs and is only
// present when someone asks for it.
func (m *Model) viewActivityModal(frame string) string {
	textWidth := m.activityTextWidth()
	lines := m.activityLines(textWidth)
	capacity := m.activityCapacity()

	// modalTop counts back from the newest line.
	limit := max(len(lines)-capacity, 0)
	m.modalTop = min(max(m.modalTop, 0), limit)

	end := len(lines) - m.modalTop
	start := max(end-capacity, 0)
	window := append([]string(nil), lines[start:end]...)
	// Hold the height steady even when there is not enough to fill it.
	blank := strings.Repeat(" ", activityStampWidth+textWidth)
	for len(window) < capacity {
		window = append(window, blank)
	}

	footer := "esc close"
	if limit > 0 {
		footer = fmt.Sprintf("%d–%d of %d  %s  ↑/↓ scroll  %s  esc close",
			start+1, end, len(lines), m.style.Glyphs.Bullet, m.style.Glyphs.Bullet)
	}
	return m.renderModal(frame, "activity", strings.Join(window, "\n"), footer)
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

// boardFitNote warns, before the match starts, that this terminal will not
// show the whole board.
//
// Finding that out at kickoff is too late: by then the lobby has started and
// the only options are to play half-blind or walk out. Said here, a player can
// ask the host for a smaller arena, or make room, before readying up.
func (m *Model) boardFitNote(cfg game.Config, players int) string {
	needW, needH := arenaViewport(cfg, players)
	if m.width >= needW && m.height >= needH {
		return ""
	}
	// Trimmed rather than wrapped: a second line here would push the roster up
	// and, on a short terminal, off the screen entirely.
	note := fmt.Sprintf("part of this board will be off screen — %d×%d shows all of it", needW, needH)
	return m.style.Text(m.style.Theme.Warn, m.style.Glyphs.Bullet+" ") +
		m.style.DimText(truncate(note, max(m.width-6, 20)))
}
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
