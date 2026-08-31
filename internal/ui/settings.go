package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/ui/theme"
)

// settingField is one row on the settings screen.
type settingField struct {
	label  string
	help   string
	value  func(*Model) string
	adjust func(*Model, int)
	text   bool
	// action runs on enter instead of adjusting a value.
	action func(*Model) tea.Cmd
}

// settingsState is the settings screen.
type settingsState struct {
	cursor int
	name   textinput.Model
	fields []settingField
	// dirty marks unsaved changes so the screen can say so.
	dirty bool
}

// initSettings builds the settings rows.
func (m *Model) initSettings() {
	name := textinput.New()
	name.Prompt = ""
	name.CharLimit = 24
	name.Width = 24
	name.SetValue(m.app.Ident.DisplayName)

	m.settings = settingsState{name: name}
	m.settings.fields = []settingField{
		{
			label: "display name", help: "how other players see you; your key stays the same",
			text:  true,
			value: func(m *Model) string { return m.settings.name.View() },
		},
		{
			label: "theme", help: themeHelp(),
			value: func(m *Model) string { return m.style.Theme.Name + " — " + m.style.Theme.Desc },
			adjust: func(m *Model, d int) {
				all := theme.All()
				idx := 0
				for i, t := range all {
					if t.Name == m.style.Theme.Name {
						idx = i
					}
				}
				next := all[wrapIndex(idx+d, len(all))]
				m.app.Settings.Theme = next.Name
				m.restyle()
			},
		},
		{
			label: "glyphs", help: "ASCII avoids width problems in some terminals",
			value: func(m *Model) string {
				if m.style.Glyphs.ASCII {
					return "ASCII"
				}
				return "Unicode"
			},
			adjust: func(m *Model, _ int) {
				m.app.Settings.ASCII = !m.app.Settings.ASCII
				m.restyle()
			},
		},
		{
			label: "colour", help: "detected from your terminal; --color overrides it",
			value: func(m *Model) string {
				detail := string(m.style.Mode)
				if m.app.ColorFlag != theme.ModeAuto {
					detail += " (set by --color)"
				}
				return detail
			},
			adjust: func(m *Model, d int) {
				if m.app.ColorFlag != theme.ModeAuto {
					return // an explicit flag wins for the whole run
				}
				modes := []theme.Mode{theme.ModeTrueColor, theme.Mode256, theme.Mode16, theme.ModeNone}
				idx := 0
				for i, mode := range modes {
					if mode == m.style.Mode {
						idx = i
					}
				}
				m.app.Settings.ColorMode = string(modes[wrapIndex(idx+d, len(modes))])
				m.restyle()
			},
		},
		{
			label: "node details", help: "show this device's tailnet address in the header",
			value: func(m *Model) string {
				if m.app.Settings.ShowNodeID {
					return "shown"
				}
				return "hidden"
			},
			adjust: func(m *Model, _ int) {
				m.app.Settings.ShowNodeID = !m.app.Settings.ShowNodeID
				m.settings.dirty = true
			},
		},
		{
			label: "re-authenticate", help: "restart the Tailscale device login for this node",
			value: func(m *Model) string { return "press enter" },
			action: func(m *Model) tea.Cmd {
				return tea.Batch(m.retryLogin(),
					m.setToast(toastInfo, "Restarting the Tailscale login%s", m.style.Glyphs.Ellipsis))
			},
		},
	}
}

// themeHelp lists the available themes for the help line.
func themeHelp() string {
	names := make([]string, 0, len(theme.All()))
	for _, t := range theme.All() {
		names = append(names, t.Name)
	}
	return "available: " + strings.Join(names, ", ")
}

// restyle rebuilds the renderer after a theme, glyph or colour change.
func (m *Model) restyle() {
	requested := m.app.ColorFlag
	if requested == theme.ModeAuto && m.app.Settings.ColorMode != "" {
		if parsed, ok := theme.ParseMode(m.app.Settings.ColorMode); ok {
			requested = parsed
		}
	}
	mode := theme.Resolve(requested, theme.EnvFromOS())
	ascii := m.app.ASCIIFlag || m.app.Settings.ASCII
	m.style = theme.NewStyle(theme.ByName(m.app.Settings.Theme), mode, ascii)
	m.settings.dirty = true
}

// updateSettings handles settings input.
func (m *Model) updateSettings(msg tea.KeyMsg) tea.Cmd {
	field := m.settings.fields[m.settings.cursor]

	switch {
	case key.Matches(msg, m.keys.Back):
		cmd := m.saveSettings()
		m.screen = m.returnTo
		return cmd
	case key.Matches(msg, m.keys.Up):
		m.settings.cursor = wrapIndex(m.settings.cursor-1, len(m.settings.fields))
		m.syncSettingsFocus()
		return nil
	case key.Matches(msg, m.keys.Down), msg.Type == tea.KeyTab:
		m.settings.cursor = wrapIndex(m.settings.cursor+1, len(m.settings.fields))
		m.syncSettingsFocus()
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
		if field.action != nil {
			return field.action(m)
		}
		if field.adjust != nil && !field.text {
			field.adjust(m, +1)
		}
		return nil
	}

	if field.text {
		var cmd tea.Cmd
		m.settings.name, cmd = m.settings.name.Update(msg)
		m.settings.dirty = true
		return cmd
	}
	if msg.String() == "q" {
		cmd := m.saveSettings()
		return tea.Batch(cmd, m.quit())
	}
	return nil
}

// syncSettingsFocus focuses the name input only on its own row.
func (m *Model) syncSettingsFocus() {
	if m.settings.fields[m.settings.cursor].text {
		m.settings.name.Focus()
	} else {
		m.settings.name.Blur()
	}
}

// saveSettings persists the settings and the display name.
//
// The name lives with the signing identity rather than in settings.json,
// because a rename must not look like a different player: the key is what ties
// a person's match history together.
func (m *Model) saveSettings() tea.Cmd {
	name := proto.SanitizeDisplayName(m.settings.name.Value())
	renamed := name != m.app.Ident.DisplayName

	if renamed {
		m.app.Ident.DisplayName = name
		if err := m.app.Ident.Save(m.app.StateDir); err != nil {
			return m.setToast(toastErr, "Could not save your name: %v", err)
		}
	}
	m.app.Settings.DisplayName = name
	if err := store.SaveSettings(m.app.StateDir, m.app.Settings); err != nil {
		return m.setToast(toastErr, "Could not save settings: %v", err)
	}
	if !m.settings.dirty && !renamed {
		return nil
	}
	m.settings.dirty = false
	return m.setToast(toastOk, "Settings saved")
}

// viewSettings renders the settings screen.
func (m *Model) viewSettings() string {
	th := m.style.Theme
	g := m.style.Glyphs

	rows := make([]string, 0, len(m.settings.fields)*2+6)
	for i, f := range m.settings.fields {
		selected := i == m.settings.cursor
		marker := "  "
		label := m.style.DimText(pad(f.label, 16))
		value := m.style.Text(th.Fg, f.value(m))
		if selected {
			marker = m.style.Accent(g.Arrow + " ")
			label = m.style.Text(th.Accent, pad(f.label, 16))
		}
		rows = append(rows, marker+label+value)
		if selected && f.help != "" {
			rows = append(rows, m.style.FaintText("     "+f.help))
		}
	}

	rows = append(rows, "", m.style.FaintText(strings.Repeat(g.Horizontal, 46)))
	rows = append(rows, m.identityLines()...)
	rows = append(rows, m.palettePreview())

	panel := m.style.Panel().Width(min(max(m.width-8, 44), 66)).Render(
		lipgloss.JoinVertical(lipgloss.Left, rows...))
	return m.chrome("settings", "", m.center(panel, m.bodyHeight()), []hint{
		{"↑/↓", "field"}, {"←/→", "change"}, {"esc", "save & back"},
	})
}

// identityLines show the signing key and where state lives, which is what a
// user needs when they wonder why their history followed them or did not.
//
// The path gets its own line and is trimmed from the left when long, because
// wrapping a path across two lines makes it useless for copying.
func (m *Model) identityLines() []string {
	width := min(max(m.width-24, 20), 48)
	return []string{
		m.style.FaintText(pad("signing key", 13)) + m.style.DimText(m.app.Ident.Short()),
		m.style.FaintText(pad("state", 13)) + m.style.DimText(truncateLeft(m.app.StateDir, width)),
	}
}

// palettePreview shows every seat colour and glyph, so a player can check they
// are distinguishable in their own terminal before a match starts.
func (m *Model) palettePreview() string {
	g := m.style.Glyphs
	th := m.style.Theme
	var b strings.Builder
	b.WriteString(m.style.FaintText("seats  "))
	for i := range 8 {
		b.WriteString(m.style.Text(th.HeadColor(i, m.phase(1800*time.Millisecond)), g.Head(i)))
		b.WriteString(m.style.Text(th.TailColor(i, 1, 3, m.phase(900*time.Millisecond)), g.Body))
		b.WriteString(" ")
	}
	return b.String()
}
