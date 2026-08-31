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

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/netplay"
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
	name.Width = 24

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
			label: "snake speed", help: "ticks between moves — lower is faster",
			value: func(m *Model) string {
				cfg := m.form.cfg
				cells := float64(cfg.TickRate) / float64(cfg.TicksPerMove)
				return fmt.Sprintf("%d (%.1f cells/s)", cfg.TicksPerMove, cells)
			},
			adjust: func(m *Model, d int) {
				m.form.cfg.TicksPerMove = clampInt(m.form.cfg.TicksPerMove+d, 1, 10)
			},
		},
		{
			label: "max players", help: fmt.Sprintf("%d to %d seats", game.MinPlayers, game.MaxPlayers),
			value: func(m *Model) string { return fmt.Sprintf("%d", m.form.cfg.MaxPlayers) },
			adjust: func(m *Model, d int) {
				m.form.cfg.MaxPlayers = clampInt(m.form.cfg.MaxPlayers+d, game.MinPlayers, game.MaxPlayers)
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
		return m.startHosting()
	}

	if field.text {
		var cmd tea.Cmd
		m.form.name, cmd = m.form.name.Update(msg)
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
		MaxPlayers: cfg.MaxPlayers, Wrap: cfg.Wrap, Mode: string(cfg.Mode),
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

	rows := make([]string, 0, len(m.form.fields)+4)
	for _, idx := range m.visibleFields() {
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
				value = m.style.FaintText("‹ ") + value + m.style.FaintText(" ›")
				if g.ASCII {
					value = m.style.FaintText("< ") + m.style.Text(th.Fg, f.value(m)) + m.style.FaintText(" >")
				}
			}
		}
		rows = append(rows, marker+label+value)
		if selected && f.help != "" {
			rows = append(rows, m.style.FaintText("     "+f.help))
		}
	}

	rows = append(rows, "", m.style.FaintText(strings.Repeat(g.Horizontal, 44)), m.sizeAdvice())

	panel := m.style.Panel().Width(min(max(m.width-8, 40), 60)).Render(
		lipgloss.JoinVertical(lipgloss.Left, rows...))

	body := lipgloss.JoinVertical(lipgloss.Center,
		panel,
		"",
		m.style.Text(th.Accent, "press enter to open the lobby"),
	)
	return m.chrome("host a game", "", m.center(body, m.bodyHeight()), []hint{
		{"↑/↓", "field"}, {"←/→", "change"}, {"enter", "host"}, {"esc", "back"},
	})
}

// sizeAdvice tells the user up front whether their terminal can display the
// arena they are configuring, rather than letting them find out at kickoff.
func (m *Model) sizeAdvice() string {
	th := m.style.Theme
	needW := m.form.cfg.Width + 4
	needH := m.form.cfg.Height + 10
	fits := m.width >= needW && m.height >= needH

	text := fmt.Sprintf("needs a %d×%d terminal; yours is %d×%d", needW, needH, m.width, m.height)
	if fits {
		return m.style.Text(th.Ok, m.style.Glyphs.Check+" ") + m.style.FaintText(text)
	}
	return m.style.Text(th.Warn, m.style.Glyphs.Bullet+" ") + m.style.Text(th.Warn, text)
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int { return min(max(v, lo), hi) }
