// Package ui is the tailsnail terminal interface: a Bubble Tea program with
// one root model that owns shared state and delegates each screen to its own
// file.
//
// Every animation is driven from a single frame ticker that runs independently
// of the simulation's tick rate, so the interface stays smooth whether a match
// is running at 10 ticks per second or 60.
package ui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/theolol/tailsnail/internal/discovery"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/tsnode"
	"github.com/theolol/tailsnail/internal/ui/theme"
)

// frameInterval drives every animation. It is deliberately decoupled from the
// game tick rate: a 10-tick-per-second match still gets a smooth shimmer.
const frameInterval = 16 * time.Millisecond

// toastDuration is how long a transient notice stays on screen.
const toastDuration = 4 * time.Second

// screen identifies the view currently in front of the user.
type screen int

const (
	screenOnboarding screen = iota
	screenMenu
	screenBrowser
	screenHostForm
	screenRoom
	screenGame
	screenGameOver
	screenHistory
	screenSettings
)

// App bundles the services the UI drives. It is assembled by cmd/tsnail and
// handed to the model whole.
type App struct {
	Ctx      context.Context
	Node     *tsnode.Node
	Server   *netplay.Server
	Prober   *discovery.Prober
	Store    *store.Store
	Ident    *store.Identity
	Log      *logring.Ring
	StateDir string
	Settings store.Settings
	// ASCII and Color come from the command line and override the stored
	// settings for this run only.
	ASCIIFlag bool
	ColorFlag theme.Mode
}

// Model is the root Bubble Tea model.
type Model struct {
	app   *App
	style *theme.Style
	keys  keyMap

	screen screen
	// returnTo is where Escape goes from the current screen.
	returnTo screen

	width, height int
	frame         int
	now           time.Time

	// node holds the most recent embedded-node status.
	node tsnode.Status
	// everConnected records whether the node has reached Running at least
	// once, which is what distinguishes first-run onboarding from a reconnect.
	everConnected bool

	// session is the lobby this peer is in, host or client.
	session netplay.Session
	// sessionGen increments on every new session so events from a session the
	// user already left are ignored rather than mixed into the new one.
	sessionGen int

	toast    toast
	toastSeq int

	showLog bool
	logTop  int

	onboard  onboardState
	menu     menuState
	browser  browserState
	form     formState
	room     roomState
	game     gameState
	over     overState
	history  historyState
	settings settingsState

	quitting bool
}

// New builds the root model.
func New(app *App) *Model {
	mode := theme.Resolve(app.ColorFlag, theme.EnvFromOS())
	ascii := app.ASCIIFlag || app.Settings.ASCII
	m := &Model{
		app:      app,
		style:    theme.NewStyle(theme.ByName(app.Settings.Theme), mode, ascii),
		keys:     defaultKeys(),
		screen:   screenOnboarding,
		returnTo: screenMenu,
		now:      time.Now(),
	}
	m.initMenu()
	m.initForm()
	m.initSettings()
	m.initHistory()
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		frameTick(),
		waitNodeStatus(m.app.Node.Updates()),
		waitDiscovery(m.app.Prober.Snapshots()),
		tea.SetWindowTitle("tailsnail"),
	)
}

// --- messages -------------------------------------------------------------

// frameMsg advances every animation.
type frameMsg time.Time

// nodeStatusMsg carries an embedded-node state change.
type nodeStatusMsg tsnode.Status

// discoveryMsg carries a peer sweep result.
type discoveryMsg discovery.Snapshot

// sessionEventMsg carries one lobby or game event, tagged with the session
// generation it came from.
type sessionEventMsg struct {
	gen int
	ev  netplay.Event
}

// sessionGoneMsg reports that a session's event stream ended.
type sessionGoneMsg struct{ gen int }

// sessionReadyMsg delivers the result of a host or join attempt.
type sessionReadyMsg struct {
	session netplay.Session
	err     error
	// hosting distinguishes the two so the failure message can be specific.
	hosting bool
}

// toastExpiredMsg clears a transient notice if it is still the current one.
type toastExpiredMsg struct{ seq int }

// browserOpenedMsg is emitted when the browser is entered, to force a sweep.
type browserOpenedMsg struct{}

// frameTick schedules the next animation frame.
func frameTick() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// waitNodeStatus blocks on the next node status change.
func waitNodeStatus(ch <-chan tsnode.Status) tea.Cmd {
	return func() tea.Msg { return nodeStatusMsg(<-ch) }
}

// waitDiscovery blocks on the next peer sweep.
func waitDiscovery(ch <-chan discovery.Snapshot) tea.Cmd {
	return func() tea.Msg { return discoveryMsg(<-ch) }
}

// waitSession blocks on the next session event, reporting the stream's end
// when the session closes its channel.
func waitSession(gen int, ch <-chan netplay.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return sessionGoneMsg{gen: gen}
		}
		return sessionEventMsg{gen: gen, ev: ev}
	}
}

// --- update ---------------------------------------------------------------

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case frameMsg:
		m.frame++
		m.now = time.Time(msg)
		m.tickAnimations()
		return m, frameTick()

	case nodeStatusMsg:
		return m, m.handleNodeStatus(tsnode.Status(msg))

	case discoveryMsg:
		m.browser.snapshot = discovery.Snapshot(msg)
		m.browser.clampCursor()
		return m, waitDiscovery(m.app.Prober.Snapshots())

	case sessionEventMsg:
		if msg.gen != m.sessionGen {
			return m, nil // an event from a session the user already left
		}
		cmd := m.handleSessionEvent(msg.ev)
		return m, tea.Batch(cmd, waitSession(msg.gen, m.session.Events()))

	case sessionGoneMsg:
		if msg.gen != m.sessionGen {
			return m, nil
		}
		m.session = nil
		if m.screen == screenRoom || m.screen == screenGame {
			m.screen = screenBrowser
			m.app.Prober.Refresh()
		}
		return m, nil

	case sessionReadyMsg:
		return m, m.handleSessionReady(msg)

	case toastExpiredMsg:
		if msg.seq == m.toastSeq {
			m.toast = toast{}
		}
		return m, nil

	case browserOpenedMsg:
		m.app.Prober.Refresh()
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

// handleNodeStatus folds a node state change into the model, moving between
// onboarding and the menu as the node connects and disconnects.
func (m *Model) handleNodeStatus(st tsnode.Status) tea.Cmd {
	prev := m.node
	m.node = st
	next := waitNodeStatus(m.app.Node.Updates())

	switch st.Phase {
	case tsnode.PhaseRunning:
		if !m.everConnected {
			m.everConnected = true
			// A short success beat before the menu, so the user sees which
			// device they came up as rather than a screen that just vanishes.
			m.onboard.successAt = m.now
		}
		if m.screen == screenOnboarding && !m.onboard.successAt.IsZero() {
			// The transition itself happens in tickAnimations once the beat
			// has played out.
			return next
		}
	case tsnode.PhaseNeedsLogin, tsnode.PhaseNeedsApproval:
		// A node key that expired mid-session drops us back to onboarding,
		// which is exactly the screen that knows how to explain it.
		if m.screen != screenOnboarding {
			m.leaveSession("the tailnet connection dropped")
			m.screen = screenOnboarding
			m.onboard.successAt = time.Time{}
			m.everConnected = false
		}
		if prev.AuthURL != st.AuthURL && st.AuthURL != "" {
			return tea.Batch(next, m.openAuthURL(st.AuthURL))
		}
	}
	return next
}

// openAuthURL tries the platform browser opener. Failure is fine: the URL is
// always on screen, which is the path that works over SSH.
func (m *Model) openAuthURL(url string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.app.Ctx, 6*time.Second)
		defer cancel()
		if err := tsnode.OpenURL(ctx, url); err != nil {
			m.app.Log.Logf("ui: opening browser: %v", err)
		}
		return nil
	}
}

// tickAnimations advances anything that is time- rather than event-driven.
func (m *Model) tickAnimations() {
	if m.screen == screenOnboarding && !m.onboard.successAt.IsZero() {
		if m.now.Sub(m.onboard.successAt) > onboardSuccessHold {
			m.screen = screenMenu
			m.app.Prober.Refresh()
		}
	}
	m.game.decayEffects(m.now)

	// Records arrive by gossip while the history screen is open, so refresh it
	// periodically rather than only on entry.
	if m.screen == screenHistory && m.now.Sub(m.history.loadedAt) > historyRefreshInterval {
		m.history.reload(m.app.Store)
	}
}

// --- session lifecycle ----------------------------------------------------

// handleSessionReady installs a newly created session or reports its failure.
func (m *Model) handleSessionReady(msg sessionReadyMsg) tea.Cmd {
	if msg.err != nil {
		verb := "join"
		if msg.hosting {
			verb = "host"
		}
		m.setToast(toastErr, "Could not %s: %v", verb, msg.err)
		m.screen = screenBrowser
		return nil
	}
	m.sessionGen++
	m.session = msg.session
	m.room.reset()
	m.screen = screenRoom
	return waitSession(m.sessionGen, m.session.Events())
}

// leaveSession closes the current session, if any.
func (m *Model) leaveSession(reason string) {
	if m.session == nil {
		return
	}
	m.session.Close(reason)
	m.session = nil
	// Bumping the generation drops any events still queued from this session.
	m.sessionGen++
	m.app.Prober.Refresh()
}

// handleSessionEvent folds one lobby or game event into the model.
func (m *Model) handleSessionEvent(ev netplay.Event) tea.Cmd {
	switch e := ev.(type) {
	case netplay.LobbyUpdate:
		m.room.apply(e.State)
		if m.screen == screenGame && e.State.Phase != "in_game" {
			// The host reset the lobby after a match; follow it back.
			if m.screen != screenGameOver {
				m.screen = screenRoom
			}
		}
	case netplay.GameStarted:
		m.game.start(e, m.now)
		m.screen = screenGame
	case netplay.Tick:
		m.game.apply(e.State, m.now)
	case netplay.MatchOver:
		m.over.apply(e, m.room.state, m.now)
		m.game.apply(e.State, m.now)
		m.screen = screenGameOver
	case netplay.Attested:
		m.over.record = &e.Record
		m.history.reload(m.app.Store)
	case netplay.Notice:
		m.setToast(toastInfo, "%s", e.Text)
	case netplay.SessionClosed:
		m.session = nil
		reason := e.Reason
		if reason == "" {
			reason = "the session ended"
		}
		if m.screen == screenRoom || m.screen == screenGame {
			m.screen = screenBrowser
			m.app.Prober.Refresh()
		}
		m.setToast(toastWarn, "%s", capitalise(reason))
	}
	return nil
}

// --- toasts ---------------------------------------------------------------

// toastKind selects a toast's colour.
type toastKind int

const (
	toastInfo toastKind = iota
	toastWarn
	toastErr
	toastOk
)

// toast is a transient notice shown above the help bar.
type toast struct {
	kind toastKind
	text string
	at   time.Time
}

// active reports whether a toast should currently be drawn.
func (t toast) active(now time.Time) bool {
	return t.text != "" && now.Sub(t.at) < toastDuration
}

// setToast shows a transient notice.
func (m *Model) setToast(kind toastKind, format string, args ...any) tea.Cmd {
	m.toastSeq++
	m.toast = toast{kind: kind, text: sprintf(format, args...), at: m.now}
	seq := m.toastSeq
	return tea.Tick(toastDuration, func(time.Time) tea.Msg { return toastExpiredMsg{seq: seq} })
}

// --- keys -----------------------------------------------------------------

// keyMap is the full set of bindings. Individual screens advertise the subset
// that applies to them in the help bar.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Left    key.Binding
	Right   key.Binding
	Enter   key.Binding
	Back    key.Binding
	Quit    key.Binding
	Ready   key.Binding
	Refresh key.Binding
	Kick    key.Binding
	Log     key.Binding
	Copy    key.Binding
	Retry   key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k", "w"), key.WithHelp("↑/k/w", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j", "s"), key.WithHelp("↓/j/s", "down")),
		Left:    key.NewBinding(key.WithKeys("left", "h", "a"), key.WithHelp("←/h/a", "left")),
		Right:   key.NewBinding(key.WithKeys("right", "l", "d"), key.WithHelp("→/l/d", "right")),
		Enter:   key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "select")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:    key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("q", "quit")),
		Ready:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "ready")),
		Refresh: key.NewBinding(key.WithKeys("R", "ctrl+r"), key.WithHelp("R", "refresh")),
		Kick:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "kick")),
		Log:     key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "logs")),
		Copy:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open browser")),
		Retry:   key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "retry")),
	}
}

// handleKey routes a keypress to the active screen, after the few global
// bindings have had a look.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// The log overlay swallows input while it is up.
	if m.showLog {
		return m.updateLogOverlay(msg)
	}
	if key.Matches(msg, m.keys.Log) {
		m.showLog = true
		m.logTop = 0
		return nil
	}
	// Ctrl+C always quits, everywhere. Plain "q" is handled per screen so it
	// can be typed into text fields.
	if msg.String() == "ctrl+c" {
		return m.quit()
	}

	switch m.screen {
	case screenOnboarding:
		return m.updateOnboarding(msg)
	case screenMenu:
		return m.updateMenu(msg)
	case screenBrowser:
		return m.updateBrowser(msg)
	case screenHostForm:
		return m.updateForm(msg)
	case screenRoom:
		return m.updateRoom(msg)
	case screenGame:
		return m.updateGame(msg)
	case screenGameOver:
		return m.updateGameOver(msg)
	case screenHistory:
		return m.updateHistory(msg)
	case screenSettings:
		return m.updateSettings(msg)
	}
	return nil
}

// quit tears the session down and ends the program.
func (m *Model) quit() tea.Cmd {
	m.quitting = true
	m.leaveSession("quitting")
	m.app.Server.StopHosting("the host quit")
	return tea.Quit
}

// --- view -----------------------------------------------------------------

// View implements tea.Model.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "" // the first frame arrives before the size does
	}
	if body, tooSmall := m.resizeOverlay(); tooSmall {
		return body
	}
	if m.showLog {
		return m.viewLogOverlay()
	}

	switch m.screen {
	case screenOnboarding:
		return m.viewOnboarding()
	case screenMenu:
		return m.viewMenu()
	case screenBrowser:
		return m.viewBrowser()
	case screenHostForm:
		return m.viewForm()
	case screenRoom:
		return m.viewRoom()
	case screenGame:
		return m.viewGame()
	case screenGameOver:
		return m.viewGameOver()
	case screenHistory:
		return m.viewHistory()
	case screenSettings:
		return m.viewSettings()
	}
	return ""
}

// phase returns the shared animation phase in [0,1) for a cycle of the given
// duration. Everything that pulses reads from here, so the whole interface
// beats together.
func (m *Model) phase(cycle time.Duration) float64 {
	if cycle <= 0 {
		return 0
	}
	elapsed := float64(m.frame) * float64(frameInterval)
	return mod1(elapsed / float64(cycle))
}

// mod1 returns the fractional part of v, always in [0,1).
func mod1(v float64) float64 {
	v -= float64(int64(v))
	if v < 0 {
		v++
	}
	return v
}
