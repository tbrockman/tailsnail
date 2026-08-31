package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/tsnode"
)

// send pushes a message through Update and returns the model.
func send(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	return next
}

// key builds a keypress message.
func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// sessionModel returns a model already seated in a lobby.
func sessionModel(t *testing.T, host bool) (*Model, *recordingSession) {
	t.Helper()
	m := newTestModel(t)
	m.node = runningNode()
	m.everConnected = true
	sess := &recordingSession{fakeSession: fakeSession{host: host}}
	m.session = sess
	m.sessionGen = 1
	m.screen = screenRoom
	m.room.apply(sampleLobby(proto.PhaseOpen, 2))
	return m, sess
}

// recordingSession captures the calls the UI makes on a session.
type recordingSession struct {
	fakeSession
	ready       []bool
	inputs      []game.Direction
	kicked      []game.PlayerID
	closed      bool
	closeReason string
	events      chan netplay.Event
}

func (r *recordingSession) SetReady(v bool)        { r.ready = append(r.ready, v) }
func (r *recordingSession) Input(d game.Direction) { r.inputs = append(r.inputs, d) }
func (r *recordingSession) Kick(s game.PlayerID)   { r.kicked = append(r.kicked, s) }
func (r *recordingSession) Close(reason string)    { r.closed, r.closeReason = true, reason }
func (r *recordingSession) Events() <-chan netplay.Event {
	if r.events == nil {
		r.events = make(chan netplay.Event, 8)
	}
	return r.events
}

func TestSessionEventsDriveTheScreenFlow(t *testing.T) {
	m, _ := sessionModel(t, false)

	// A match starts.
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.GameStarted{
		MatchID: proto.NewMatchID(), Config: game.DefaultConfig(),
		Seat: 0, Players: samplePlayers(2),
	}})
	if m.screen != screenGame {
		t.Fatalf("screen = %v after GameStarted, want the arena", m.screen)
	}

	// Ticks arrive.
	sim, err := game.New(game.DefaultConfig(), []game.PlayerID{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.Tick{State: sim.Step()}})
	if m.screen != screenGame {
		t.Errorf("a tick moved us off the arena")
	}
	if m.game.state.Tick == 0 {
		t.Error("the tick was not applied")
	}

	// It ends.
	final := sim.State()
	final.Over = true
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.MatchOver{
		State: final, Players: samplePlayers(2), Reason: "match complete",
	}})
	if m.screen != screenGameOver {
		t.Fatalf("screen = %v after MatchOver, want the results", m.screen)
	}

	// The record lands.
	rec := signedRecord(t, m, 2, 2)
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.Attested{Record: rec}})
	if m.over.record == nil {
		t.Fatal("the attested record was not captured")
	}
}

func TestSessionClosedReturnsToTheBrowser(t *testing.T) {
	m, _ := sessionModel(t, false)
	m.screen = screenGame

	// This is the host-died path: the session ends mid-match.
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.SessionClosed{
		Reason: "the host went away",
	}})

	if m.screen != screenBrowser {
		t.Fatalf("screen = %v after the session closed, want the browser", m.screen)
	}
	if m.session != nil {
		t.Error("the closed session is still attached")
	}
	if !m.toast.active(m.now) {
		t.Error("no notice explaining why the session ended")
	}
	if m.toast.text == "" {
		t.Error("the notice is empty")
	}
}

func TestEventsFromAnAbandonedSessionAreIgnored(t *testing.T) {
	m, _ := sessionModel(t, false)
	m.screen = screenRoom

	// The user leaves and joins something else; the old session's events are
	// still in flight and must not disturb the new one.
	m.sessionGen = 7
	m = send(t, m, sessionEventMsg{gen: 1, ev: netplay.GameStarted{
		Config: game.DefaultConfig(), Players: samplePlayers(2),
	}})
	if m.screen != screenRoom {
		t.Fatalf("a stale event moved the screen to %v", m.screen)
	}

	m = send(t, m, sessionGoneMsg{gen: 1})
	if m.session == nil {
		t.Error("a stale stream ending detached the current session")
	}
}

func TestJoiningInstallsTheSessionAndOpensTheRoom(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.screen = screenBrowser
	sess := &recordingSession{}

	before := m.sessionGen
	m = send(t, m, sessionReadyMsg{session: sess})
	if m.screen != screenRoom {
		t.Fatalf("screen = %v after joining, want the lobby room", m.screen)
	}
	if m.session != sess {
		t.Error("the session was not installed")
	}
	if m.sessionGen == before {
		t.Error("the session generation did not advance")
	}
}

func TestAFailedJoinReportsWhyAndStaysInTheBrowser(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenBrowser

	m = send(t, m, sessionReadyMsg{err: proto.ErrorMsg{
		Code: proto.ErrLobbyFull, Message: "all 4 seats are taken",
	}})
	if m.screen != screenBrowser {
		t.Fatalf("screen = %v after a failed join", m.screen)
	}
	if m.session != nil {
		t.Error("a failed join installed a session")
	}
	if !m.toast.active(m.now) || m.toast.kind != toastErr {
		t.Fatal("a failed join did not report an error")
	}
	if !contains(m.toast.text, "seats are taken") {
		t.Errorf("notice = %q, want the host's reason", m.toast.text)
	}
}

func TestReadyTogglesThroughTheSession(t *testing.T) {
	m, sess := sessionModel(t, false)
	// sampleLobby readies even seats; seat 0 starts ready.
	m.session = sess

	m = send(t, m, press("r"))
	if len(sess.ready) != 1 {
		t.Fatalf("SetReady was called %d times, want 1", len(sess.ready))
	}
	if sess.ready[0] != false {
		t.Error("readying an already-ready player should un-ready them")
	}
}

func TestSteeringSendsInputAndPrimesPrediction(t *testing.T) {
	m, sess := sessionModel(t, false)
	m.screen = screenGame
	m.game.start(netplay.GameStarted{
		Config: game.DefaultConfig(), Seat: 0, Players: samplePlayers(2),
	}, m.now)

	for _, tc := range []struct {
		key  string
		want game.Direction
	}{
		{"up", game.DirUp}, {"down", game.DirDown},
		{"left", game.DirLeft}, {"right", game.DirRight},
		{"w", game.DirUp}, {"a", game.DirLeft},
	} {
		m = send(t, m, press(tc.key))
		if len(sess.inputs) == 0 || sess.inputs[len(sess.inputs)-1] != tc.want {
			t.Fatalf("%q sent %v, want %v", tc.key, sess.inputs, tc.want)
		}
		if !m.game.hasPending || m.game.pendingDir != tc.want {
			t.Errorf("%q did not prime prediction", tc.key)
		}
	}
}

func TestOnlyTheHostCanKick(t *testing.T) {
	m, sess := sessionModel(t, false)
	m.room.cursor = 1
	m = send(t, m, press("x"))
	if len(sess.kicked) != 0 {
		t.Fatal("a client was allowed to kick")
	}
	if !m.toast.active(m.now) {
		t.Error("the refusal was not explained")
	}

	hostModel, hostSess := sessionModel(t, true)
	hostModel.room.cursor = 1
	hostModel = send(t, hostModel, press("x"))
	if len(hostSess.kicked) != 1 || hostSess.kicked[0] != 1 {
		t.Fatalf("host kick recorded %v, want seat 1", hostSess.kicked)
	}
}

func TestTheHostCannotKickItself(t *testing.T) {
	m, sess := sessionModel(t, true)
	m.room.cursor = 0 // the host's own seat
	m = send(t, m, press("x"))
	if len(sess.kicked) != 0 {
		t.Fatal("the host kicked itself")
	}
}

func TestLeavingTheRoomClosesTheSession(t *testing.T) {
	m, sess := sessionModel(t, false)
	m = send(t, m, press("esc"))
	if !sess.closed {
		t.Fatal("leaving did not close the session")
	}
	if m.session != nil {
		t.Error("the session is still attached after leaving")
	}
	if m.screen != screenBrowser {
		t.Errorf("screen = %v after leaving, want the browser", m.screen)
	}
}

func TestNodeLoginLossReturnsToOnboarding(t *testing.T) {
	m, sess := sessionModel(t, false)
	m.screen = screenGame

	// A node key expiring mid-session drops everything.
	m = send(t, m, nodeStatusMsg(tsnode.Status{
		Phase: tsnode.PhaseNeedsLogin, AuthURL: "https://login.tailscale.com/a/x",
		Since: time.Now(),
	}))
	if m.screen != screenOnboarding {
		t.Fatalf("screen = %v after losing the tailnet, want onboarding", m.screen)
	}
	if !sess.closed {
		t.Error("the session was left open after the tailnet dropped")
	}
	if m.everConnected {
		t.Error("everConnected should reset so the success beat plays again")
	}
}

func TestFirstConnectionPlaysTheSuccessBeatThenOpensTheMenu(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOnboarding

	m = send(t, m, nodeStatusMsg(runningNode()))
	if !m.everConnected {
		t.Fatal("reaching Running did not mark the node connected")
	}
	if m.screen != screenOnboarding {
		t.Fatal("the success beat was skipped")
	}
	if m.onboard.successAt.IsZero() {
		t.Fatal("the success beat did not start")
	}

	// The beat plays, then the menu takes over.
	m.now = m.now.Add(onboardSuccessHold + time.Millisecond)
	m.tickAnimations()
	if m.screen != screenMenu {
		t.Fatalf("screen = %v after the beat, want the menu", m.screen)
	}
}

func TestReconnectDoesNotFlashOnboarding(t *testing.T) {
	// A run with a stored node key goes Starting → Connecting → Running. The
	// authorisation screen must never appear, because there is nothing to
	// authorise.
	m := newTestModel(t)
	m.screen = screenOnboarding

	for _, phase := range []tsnode.Phase{tsnode.PhaseStarting, tsnode.PhaseConnecting} {
		m = send(t, m, nodeStatusMsg(tsnode.Status{Phase: phase, Since: time.Now()}))
		view := stripANSI(m.View())
		if contains(view, "Authorise it once") {
			t.Fatalf("the authorisation screen appeared during %v", phase)
		}
		if !contains(view, "connecting") && !contains(view, "starting") {
			t.Errorf("phase %v did not render a connecting state:\n%s", phase, view)
		}
	}
}

func TestWindowSizeIsTracked(t *testing.T) {
	m := newTestModel(t)
	m = send(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.width != 100 || m.height != 30 {
		t.Fatalf("viewport = %dx%d, want 100x30", m.width, m.height)
	}
}

func TestFrameTickAdvancesAnimation(t *testing.T) {
	m := newTestModel(t)
	before := m.frame
	m = send(t, m, frameMsg(time.Now()))
	if m.frame != before+1 {
		t.Fatalf("frame = %d, want %d", m.frame, before+1)
	}
}

func TestLogOverlayTogglesAndSwallowsInput(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu
	cursorBefore := m.menu.cursor

	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if !m.showLog {
		t.Fatal("ctrl+l did not open the log")
	}
	// Navigation now scrolls the log rather than the menu behind it.
	m = send(t, m, press("down"))
	if m.menu.cursor != cursorBefore {
		t.Error("input reached the menu while the log overlay was up")
	}
	m = send(t, m, press("esc"))
	if m.showLog {
		t.Fatal("esc did not close the log")
	}
}

func TestMenuNavigationWraps(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu

	m = send(t, m, press("up"))
	if m.menu.cursor != len(m.menu.items)-1 {
		t.Fatalf("cursor = %d, want it to wrap to the last item", m.menu.cursor)
	}
	m = send(t, m, press("down"))
	if m.menu.cursor != 0 {
		t.Fatalf("cursor = %d, want it to wrap to the first item", m.menu.cursor)
	}
}

func TestMenuOpensEachScreen(t *testing.T) {
	targets := []struct {
		item int
		want screen
	}{
		{0, screenHostForm},
		{1, screenBrowser},
		{2, screenHistory},
		{3, screenSettings},
	}
	for _, tc := range targets {
		m := newTestModel(t)
		m.screen = screenMenu
		m.menu.cursor = tc.item
		m = send(t, m, press("enter"))
		if m.screen != tc.want {
			t.Errorf("menu item %d opened %v, want %v", tc.item, m.screen, tc.want)
		}
	}
}

func TestBrowserJoinWithNothingSelectedIsRefusedPolitely(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenBrowser
	m = send(t, m, press("enter"))
	if m.screen != screenBrowser {
		t.Fatal("pressing enter with no lobby selected navigated somewhere")
	}
	if !m.toast.active(m.now) {
		t.Error("no explanation was given")
	}
}

func TestHostFormAdjustsValuesWithinRange(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	m.form.cursor = 1 // arena width

	for range 200 {
		m = send(t, m, press("right"))
	}
	if m.form.cfg.Width != game.MaxWidth {
		t.Errorf("width = %d, want it clamped to %d", m.form.cfg.Width, game.MaxWidth)
	}
	for range 200 {
		m = send(t, m, press("left"))
	}
	if m.form.cfg.Width != game.MinWidth {
		t.Errorf("width = %d, want it clamped to %d", m.form.cfg.Width, game.MinWidth)
	}
	if err := m.form.cfg.Validate(); err != nil {
		t.Errorf("the form produced an invalid config: %v", err)
	}
}

func TestHostFormNeverProducesAnInvalidConfig(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	// Walk every field to both extremes.
	for i := range m.form.fields {
		if m.form.fields[i].text {
			continue
		}
		m.form.cursor = i
		for range 60 {
			m = send(t, m, press("left"))
		}
		if err := m.form.cfg.Validate(); err != nil {
			t.Fatalf("field %q at its minimum produced %v", m.form.fields[i].label, err)
		}
		for range 60 {
			m = send(t, m, press("right"))
		}
		if err := m.form.cfg.Validate(); err != nil {
			t.Fatalf("field %q at its maximum produced %v", m.form.fields[i].label, err)
		}
	}
}

func TestSettingsThemeAndGlyphChangesApplyImmediately(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenSettings
	m.settings.cursor = 1 // theme

	before := m.style.Theme.Name
	m = send(t, m, press("right"))
	if m.style.Theme.Name == before {
		t.Fatal("changing the theme did not restyle")
	}

	m.settings.cursor = 2 // glyphs
	asciiBefore := m.style.Glyphs.ASCII
	m = send(t, m, press("right"))
	if m.style.Glyphs.ASCII == asciiBefore {
		t.Fatal("toggling glyphs did not restyle")
	}
}

func TestSettingsPersistOnLeaving(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenSettings
	m.returnTo = screenMenu
	m.settings.cursor = 1
	m = send(t, m, press("right")) // change the theme

	m = send(t, m, press("esc"))
	if m.screen != screenMenu {
		t.Fatalf("screen = %v after leaving settings", m.screen)
	}
	if m.app.Settings.Theme != m.style.Theme.Name {
		t.Error("the settings were not updated before saving")
	}
}

func TestHistoryTabsSwitch(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHistory
	if m.history.tab != tabLeaderboard {
		t.Fatal("history should open on the leaderboard")
	}
	m = send(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.history.tab != tabMatches {
		t.Fatal("tab did not switch to the match list")
	}
	m = send(t, m, press("right"))
	if m.history.tab != tabLeaderboard {
		t.Fatal("tab did not switch back")
	}
}

func TestDiscoverySnapshotsReachTheBrowser(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenBrowser
	m.browser.cursor = 9

	m = send(t, m, discoveryMsg{At: time.Now(), Candidates: 4})
	if m.browser.snapshot.Candidates != 4 {
		t.Fatal("the snapshot was not stored")
	}
	if m.browser.cursor != 0 {
		t.Errorf("cursor = %d, want it clamped into an empty list", m.browser.cursor)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
