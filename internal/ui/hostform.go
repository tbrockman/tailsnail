package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
)

// formField is one configurable row on the host form.
type formField struct {
	label string
	help  string
	// value renders the current setting.
	value func(*Model) string
	// adjust changes it by delta, which is -1 or +1.
	adjust func(*Model, int)
	// text marks the row as a free-text input rather than a stepper.
	text bool
	// hidden hides a row that does not apply to the current mode.
	hidden func(*Model) bool
}

// formState is the host configuration form.
type formState struct {
	cfg    game.Config
	name   textinput.Model
	cursor int
	fields []formField
	// editing is true when the form is adjusting a lobby that already exists
	// rather than describing one to open.
	editing bool
}

// editFrom loads a running lobby's settings so the host can adjust them in
// place. The seat count cannot drop below the people already sitting in it.
func (f *formState) editFrom(st proto.LobbyState) {
	f.cfg = st.Config
	f.name.SetValue(st.Name)
	fitInput(&f.name)
	f.editing = true
	f.cursor = 0
	f.name.Blur()

	people := 0
	for _, p := range st.Players {
		if !p.Bot {
			people++
		}
	}
	if f.cfg.MaxPlayers < people {
		f.cfg.MaxPlayers = people
	}
}

// initForm builds the form, restoring the last hosted configuration so a
// regular group does not re-enter the same settings every session.
func (m *Model) initForm() {
	cfg := game.DefaultConfig()
	if p := m.app.Settings.LastConfig; p != nil {
		cfg.Name = p.Name
		cfg.Width, cfg.Height = p.Width, p.Height
		cfg.TickRate, cfg.TicksPerMove = p.TickRate, p.TicksPerMove
		cfg.MaxPlayers = p.MaxPlayers
		cfg.Bots = p.Bots
		cfg.Wrap = p.Wrap
		if game.Mode(p.Mode).Valid() {
			cfg.Mode = game.Mode(p.Mode)
		}
		if cfg.Validate() != nil {
			// A stored config from an older build may no longer be valid;
			// fall back rather than presenting an unusable form.
			cfg = game.DefaultConfig()
		}
	}
	if cfg.Name == "" || cfg.Name == "tailsnail" {
		cfg.Name = m.app.Ident.DisplayName + "'s game"
	}

	name := textinput.New()
	name.Prompt = ""
	name.CharLimit = 24
	name.SetValue(cfg.Name)
	fitInput(&name)

	m.form = formState{cfg: cfg, name: name}
	m.form.fields = []formField{
		{
			label: "lobby name", help: "shown in other players' browsers",
			text:  true,
			value: func(m *Model) string { return m.form.name.View() },
		},
		{
			label: "arena width", help: fmt.Sprintf("%d to %d cells", game.MinWidth, game.MaxWidth),
			value: func(m *Model) string { return fmt.Sprintf("%d", m.form.cfg.Width) },
			adjust: func(m *Model, d int) {
				m.form.cfg.Width = clampInt(m.form.cfg.Width+d*2, game.MinWidth, game.MaxWidth)
			},
		},
		{
			label: "arena height", help: fmt.Sprintf("%d to %d cells", game.MinHeight, game.MaxHeight),
			value: func(m *Model) string { return fmt.Sprintf("%d", m.form.cfg.Height) },
			adjust: func(m *Model, d int) {
				m.form.cfg.Height = clampInt(m.form.cfg.Height+d*2, game.MinHeight, game.MaxHeight)
			},
		},
		{
			label: "tick rate", help: "simulation ticks per second",
			value: func(m *Model) string { return fmt.Sprintf("%d/s", m.form.cfg.TickRate) },
			adjust: func(m *Model, d int) {
				m.form.cfg.TickRate = clampInt(m.form.cfg.TickRate+d*5, 5, 60)
			},
		},
		{
			label: "snake speed", help: "how fast a snake crosses the arena",
			value: func(m *Model) string {
				cfg := m.form.cfg
				return fmt.Sprintf("%.1f cells/s", float64(cfg.TickRate)/float64(cfg.TicksPerMove))
			},
			// Right means faster. The underlying setting is ticks per move, so
			// larger is slower; presenting that raw made the arrow keys read
			// backwards.
			adjust: func(m *Model, d int) {
				m.form.cfg.TicksPerMove = clampInt(m.form.cfg.TicksPerMove-d, 1, 10)
			},
		},
		{
			label: "max players", help: fmt.Sprintf("%d to %d seats", game.MinPlayers, game.MaxPlayers),
			value: func(m *Model) string { return fmt.Sprintf("%d", m.form.cfg.MaxPlayers) },
			adjust: func(m *Model, d int) {
				m.form.cfg.MaxPlayers = clampInt(m.form.cfg.MaxPlayers+d, game.MinPlayers, game.MaxPlayers)
				m.form.cfg.Bots = min(m.form.cfg.Bots, m.form.cfg.MaxPlayers-1)
			},
		},
		{
			label: "bots", help: "computer players, so a lobby can be played without waiting for anyone",
			value: func(m *Model) string {
				if m.form.cfg.Bots == 0 {
					return "none"
				}
				return fmt.Sprintf("%d of %d seats", m.form.cfg.Bots, m.form.cfg.MaxPlayers)
			},
			adjust: func(m *Model, d int) {
				m.form.cfg.Bots = clampInt(m.form.cfg.Bots+d, 0, m.form.cfg.MaxPlayers-1)
			},
		},
		{
			label: "walls", help: "wrap-around teleports you to the far side",
			value: func(m *Model) string {
				if m.form.cfg.Wrap {
					return "wrap-around"
				}
				return "solid walls"
			},
			adjust: func(m *Model, _ int) { m.form.cfg.Wrap = !m.form.cfg.Wrap },
		},
		{
			label: "mode", help: "classic, or an arena that closes in",
			value: func(m *Model) string {
				if m.form.cfg.Mode == game.ModeShrink {
					return "shrinking arena"
				}
				return "classic"
			},
			adjust: func(m *Model, _ int) {
				if m.form.cfg.Mode == game.ModeClassic {
					m.form.cfg.Mode = game.ModeShrink
				} else {
					m.form.cfg.Mode = game.ModeClassic
				}
			},
		},
		{
			label: "shrink every", help: "moves between the walls closing in",
			hidden: func(m *Model) bool { return m.form.cfg.Mode != game.ModeShrink },
			value:  func(m *Model) string { return fmt.Sprintf("%d moves", m.form.cfg.ShrinkEvery) },
			adjust: func(m *Model, d int) {
				m.form.cfg.ShrinkEvery = clampInt(m.form.cfg.ShrinkEvery+d*5, 5, 200)
			},
		},
		{
			label: "food", help: "pellets on the board at once",
			value: func(m *Model) string { return fmt.Sprintf("%d", m.form.cfg.FoodCount) },
			adjust: func(m *Model, d int) {
				m.form.cfg.FoodCount = clampInt(m.form.cfg.FoodCount+d, 1, 16)
			},
		},
	}
}

// visibleFields returns the rows that apply to the current configuration.
func (m *Model) visibleFields() []int {
	out := make([]int, 0, len(m.form.fields))
	for i, f := range m.form.fields {
		if f.hidden == nil || !f.hidden(m) {
			out = append(out, i)
		}
	}
	return out
}

// updateForm handles host form input.
func (m *Model) updateForm(msg tea.KeyMsg) tea.Cmd {
	visible := m.visibleFields()
	// Track the cursor by position within the visible rows so hiding a row
	// cannot strand the selection on it.
	pos := 0
	for i, idx := range visible {
		if idx == m.form.cursor {
			pos = i
		}
	}
	field := m.form.fields[m.form.cursor]

	switch {
	case key.Matches(msg, m.keys.Back):
		m.form.editing = false
		m.screen = m.returnTo
		return nil
	case key.Matches(msg, m.keys.Up):
		m.form.cursor = visible[wrapIndex(pos-1, len(visible))]
		m.syncNameFocus()
		return nil
	case key.Matches(msg, m.keys.Down), msg.Type == tea.KeyTab:
		m.form.cursor = visible[wrapIndex(pos+1, len(visible))]
		m.syncNameFocus()
		return nil
	case key.Matches(msg, m.keys.Left) && !field.text:
		if field.adjust != nil {
			field.adjust(m, -1)
		}
		return nil
	case key.Matches(msg, m.keys.Right) && !field.text:
		if field.adjust != nil {
			field.adjust(m, +1)
		}
		return nil
	case msg.Type == tea.KeyEnter:
		return m.submitForm()
	}

	if field.text {
		var cmd tea.Cmd
		m.form.name, cmd = m.form.name.Update(msg)
		fitInput(&m.form.name)
		return cmd
	}
	if msg.String() == "q" {
		return m.quit()
	}
	return nil
}

// syncNameFocus focuses the text input only while its row is selected, so the
// caret does not blink on a row the user is not editing.
func (m *Model) syncNameFocus() {
	if m.form.fields[m.form.cursor].text {
		m.form.name.Focus()
	} else {
		m.form.name.Blur()
	}
}

// submitForm applies the form: opening a new lobby, or updating a running one.
func (m *Model) submitForm() tea.Cmd {
	if m.form.editing {
		return m.applyLobbyEdits()
	}
	return m.startHosting()
}

// applyLobbyEdits pushes the edited settings to the lobby the host is running.
func (m *Model) applyLobbyEdits() tea.Cmd {
	cfg := m.form.cfg
	name := strings.TrimSpace(m.form.name.Value())
	if err := cfg.Validate(); err != nil {
		return m.setToast(toastErr, "%v", err)
	}
	if m.session == nil || !m.session.IsHost() {
		m.form.editing = false
		m.screen = screenMenu
		return m.setToast(toastWarn, "That lobby is no longer yours")
	}
	m.session.Reconfigure(name, cfg)
	m.form.editing = false
	m.screen = screenRoom
	return m.setToast(toastOk, "Settings updated")
}

// startHosting validates the form and opens the lobby.
func (m *Model) startHosting() tea.Cmd {
	cfg := m.form.cfg
	cfg.Name = strings.TrimSpace(m.form.name.Value())
	if cfg.Name == "" {
		cfg.Name = m.app.Ident.DisplayName + "'s game"
	}
	if err := cfg.Validate(); err != nil {
		return m.setToast(toastErr, "%v", err)
	}

	// Remember the choices for next time.
	m.app.Settings.LastConfig = &store.HostPrefs{
		Name: cfg.Name, Width: cfg.Width, Height: cfg.Height,
		TickRate: cfg.TickRate, TicksPerMove: cfg.TicksPerMove,
		MaxPlayers: cfg.MaxPlayers, Bots: cfg.Bots, Wrap: cfg.Wrap, Mode: string(cfg.Mode),
	}
	if err := store.SaveSettings(m.app.StateDir, m.app.Settings); err != nil {
		m.app.Log.Logf("ui: saving host preferences: %v", err)
	}

	name := cfg.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.app.Ctx, 10*time.Second)
		defer cancel()
		host, err := m.app.Server.Host(ctx, netplay.HostOptions{Name: name, Config: cfg})
		if err != nil {
			return sessionReadyMsg{err: err, hosting: true}
		}
		return sessionReadyMsg{session: host, hosting: true}
	}
}

// viewForm renders the host configuration form.
func (m *Model) viewForm() string {
	th := m.style.Theme
	g := m.style.Glyphs

	visible := m.visibleFields()
	fieldRows := make([]string, 0, len(visible))
	// The panel's own top border sits above the first row.
	const panelHeaderRows = 1
	selectedRow := panelHeaderRows
	selectedWidth := 0
	selectedHelp := ""

	for _, idx := range visible {
		f := m.form.fields[idx]
		selected := idx == m.form.cursor

		marker := "  "
		label := m.style.DimText(pad(f.label, 14))
		value := m.style.Text(th.Fg, f.value(m))
		if selected {
			marker = m.style.Accent(g.Arrow + " ")
			label = m.style.Text(th.Accent, pad(f.label, 14))
			if !f.text {
				// Show the adjustment affordance only where it applies.
				left, right := "‹ ", " ›"
				if g.ASCII {
					left, right = "< ", " >"
				}
				value = m.style.FaintText(left) + m.style.Text(th.Fg, f.value(m)) + m.style.FaintText(right)
			}
		}
		row := marker + label + value
		if selected {
			selectedRow = panelHeaderRows + len(fieldRows)
			selectedWidth = ansi.StringWidth(row)
			selectedHelp = f.help
		}
		fieldRows = append(fieldRows, row)
	}

	advice := m.sizeAdvice()
	// The panel hugs its contents rather than reserving a fixed width.
	inner := min(max(naturalWidth(append(append([]string{}, fieldRows...), advice)), 34), max(m.width-8, 24))
	for i := range fieldRows {
		fieldRows[i] = truncateStyled(fieldRows[i], inner)
	}
	selectedWidth = min(selectedWidth, inner)

	rows := append(fieldRows,
		"", m.style.FaintText(strings.Repeat(g.Horizontal, inner)), truncateStyled(advice, inner))
	// lipgloss counts padding inside Width, so a panel holding `inner` cells
	// of content has to be built two wider than that.
	panel := m.style.Panel().Width(inner + 2).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))

	action := "press enter to open the lobby"
	if m.form.editing {
		action = "press enter to apply — everyone will need to ready up again"
	}
	body := lipgloss.JoinVertical(lipgloss.Center, panel, "", m.style.Text(th.Accent, action))

	frame, top, left := m.place(body, m.bodyHeight())
	// The panel is centred within the body, which may be wider if the action
	// line is longer. Its content begins one border and one padding cell in.
	panelLeft := left + (lipgloss.Width(body)-lipgloss.Width(panel))/2
	frame = m.withTooltip(frame, tooltip{
		text: selectedHelp,
		row:  top + selectedRow,
		col:  panelLeft + 2 + selectedWidth + 1,
	})

	title, back := "host a game", "back"
	if m.form.editing {
		title, back = "lobby settings", "cancel"
	}
	return m.chrome(title, "", frame, []hint{
		{"↑/↓", "field"}, {"←/→", "change"}, {"enter", "apply"}, {"esc", back},
	})
}

// sizeAdvice tells the user up front whether their terminal can display the
// arena they are configuring, rather than letting them find out at kickoff.
func (m *Model) sizeAdvice() string {
	th := m.style.Theme
	// Quote the same figure the game screen will demand, sized for a full
	// lobby since that is when the scoreboard is tallest.
	needW, needH := arenaViewport(m.form.cfg, m.form.cfg.MaxPlayers)
	fits := m.width >= needW && m.height >= needH

	text := fmt.Sprintf("needs a %d×%d terminal; yours is %d×%d", needW, needH, m.width, m.height)
	if fits {
		return m.style.Text(th.Ok, m.style.Glyphs.Check+" ") + m.style.FaintText(text)
	}
	return m.style.Text(th.Warn, m.style.Glyphs.Bullet+" ") + m.style.Text(th.Warn, text)
}

// fitInput sizes a text field to its own contents.
//
// A text input pads its value out to its configured width, so a popover
// anchored after the field attached to the end of an empty box rather than to
// what had been typed, leaving a gap that closed as the user filled the field.
// Sizing the input to its value puts the two together.
func fitInput(ti *textinput.Model) {
	ti.Width = max(len([]rune(ti.Value())), 1)
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int { return min(max(v, lo), hi) }
