package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/theolol/tailsnail/internal/discovery"
	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/tsnode"
	"github.com/theolol/tailsnail/internal/ui/theme"
)

// newTestModel builds a model with real storage but no network. Rendering
// touches the store, the identity and the settings but never the node or the
// prober, so the screens can all be exercised without a tailnet.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	dir := t.TempDir()
	st, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := store.LoadOrCreateIdentity(dir, "ada")
	if err != nil {
		t.Fatal(err)
	}
	log := logring.New(64)
	app := &App{
		Ctx:   context.Background(),
		Store: st,
		Ident: ident,
		Log:   log,
		// A prober with no dialer is enough for the update path: the UI only
		// ever asks it to schedule a sweep, which never touches the network.
		Node:      newFakeNode(),
		Prober:    discovery.New(discovery.Options{Log: log, PubKey: ident.PubKey()}),
		StateDir:  dir,
		Settings:  store.DefaultSettings(),
		ColorFlag: theme.ModeTrueColor,
		// Pinned so a render test does not depend on the terminal it runs in.
		EmojiFlag: theme.EmojiOff,
	}
	m := New(app)
	m.width, m.height = 120, 40
	m.now = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	return m
}

// checkFrame asserts that a rendered view fits the viewport it was given.
// A line wider than the terminal is what produces the sheared, duplicated
// output that makes a TUI look broken, so it is worth catching in a test.
func checkFrame(t *testing.T, m *Model, name, view string) {
	t.Helper()
	if strings.TrimSpace(view) == "" {
		t.Fatalf("%s rendered nothing", name)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("%s line %d is %d cells wide, viewport is %d:\n%q",
				name, i+1, w, m.width, line)
		}
	}
	if h := lipgloss.Height(view); h > m.height {
		t.Errorf("%s is %d lines tall, viewport is %d", name, h, m.height)
	}
}

// samplePlayers builds a roster for the lobby and game screens.
func samplePlayers(n int) []proto.Player {
	names := []string{"ada", "grace", "hedy", "katherine", "annie", "dorothy", "mary", "evelyn"}
	out := make([]proto.Player, 0, n)
	for i := range n {
		out = append(out, proto.Player{
			Seat:        game.PlayerID(i),
			PubKey:      strings.Repeat(string(rune('a'+i)), 43),
			DisplayName: names[i%len(names)],
			Login:       names[i%len(names)] + "@example.com",
			Node:        "tsnail-" + names[i%len(names)],
			Palette:     i,
			Ready:       i%2 == 0,
			Host:        i == 0,
			Connected:   true,
		})
	}
	return out
}

// sampleLobby builds a lobby state.
func sampleLobby(phase proto.LobbyPhase, players int) proto.LobbyState {
	cfg := game.DefaultConfig()
	return proto.LobbyState{
		LobbyID: proto.NewMatchID(),
		Name:    "friday night",
		Config:  cfg,
		Phase:   phase,
		Players: samplePlayers(players),
		Events: []proto.LobbyEvent{
			{At: time.Now().Add(-time.Minute), Text: "ada opened the lobby"},
			{At: time.Now().Add(-30 * time.Second), Text: "grace joined"},
			{At: time.Now(), Text: "grace is ready"},
		},
	}
}

// runningNode is a connected node status.
func runningNode() tsnode.Status {
	return tsnode.Status{
		Phase: tsnode.PhaseRunning,
		Self: tsnode.Self{
			DNSName: "tsnail-laptop.tail1234.ts.net", Hostname: "tsnail-laptop",
			IPv4: "100.64.1.2", Login: "ada@example.com", Tailnet: "example.com",
		},
		Since: time.Now(),
	}
}

func TestOnboardingRendersEveryPhase(t *testing.T) {
	cases := []struct {
		name   string
		status tsnode.Status
		setup  func(*Model)
		want   string
	}{
		{
			name:   "starting",
			status: tsnode.Status{Phase: tsnode.PhaseStarting, Since: time.Now()},
			want:   "starting",
		},
		{
			name:   "connecting",
			status: tsnode.Status{Phase: tsnode.PhaseConnecting, Since: time.Now()},
			want:   "connecting",
		},
		{
			name: "needs login",
			status: tsnode.Status{
				Phase:   tsnode.PhaseNeedsLogin,
				AuthURL: "https://login.tailscale.com/a/0123456789abcdef",
				Since:   time.Now(),
			},
			want: "login.tailscale.com/a/0123456789abcdef",
		},
		{
			name: "needs approval",
			status: tsnode.Status{
				Phase: tsnode.PhaseNeedsApproval,
				Self:  tsnode.Self{Hostname: "tsnail-laptop"},
				Since: time.Now(),
			},
			want: "approved",
		},
		{
			name:   "logged out",
			status: tsnode.Status{Phase: tsnode.PhaseStopped, Since: time.Now()},
			want:   "logged out",
		},
		{
			name: "failed",
			status: tsnode.Status{
				Phase: tsnode.PhaseFailed,
				Err:   errors.New("no internet connection"),
				Since: time.Now(),
			},
			want: "no internet connection",
		},
		{
			name:   "success beat",
			status: runningNode(),
			setup:  func(m *Model) { m.onboard.successAt = m.now },
			want:   "100.64.1.2",
		},
		{
			name: "health warning",
			status: tsnode.Status{
				Phase:  tsnode.PhaseConnecting,
				Health: []string{"DERP region unreachable"},
				Since:  time.Now(),
			},
			want: "DERP region unreachable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.screen = screenOnboarding
			m.node = tc.status
			if tc.setup != nil {
				tc.setup(m)
			}
			view := m.View()
			checkFrame(t, m, "onboarding/"+tc.name, view)
			if !strings.Contains(stripANSI(view), tc.want) {
				t.Errorf("onboarding/%s does not mention %q:\n%s", tc.name, tc.want, stripANSI(view))
			}
		})
	}
}

func TestOnboardingAuthURLIsNeverTruncated(t *testing.T) {
	// The URL is the one thing the user must be able to read and retype, so it
	// has to survive verbatim even in a narrow window.
	const url = "https://login.tailscale.com/a/0123456789abcdef0123456789abcdef"
	for _, width := range []int{62, 80, 120, 200} {
		m := newTestModel(t)
		m.width, m.height = width, 40
		m.screen = screenOnboarding
		m.node = tsnode.Status{Phase: tsnode.PhaseNeedsLogin, AuthURL: url, Since: time.Now()}

		view := stripANSI(m.View())
		// The URL wraps inside a panel at narrow widths, so flatten away the
		// whitespace and the box border before looking for it.
		flat := strings.NewReplacer("│", "", "|", "", " ", "", "\n", "").Replace(view)
		if !strings.Contains(flat, url) {
			t.Errorf("width %d: the auth URL was mangled:\n%s", width, view)
		}
	}
}

func TestMenuRenders(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu
	m.node = runningNode()

	view := m.View()
	checkFrame(t, m, "menu", view)
	plain := stripANSI(view)
	for _, want := range []string{"host a game", "find a game", "history", "settings", "quit"} {
		if !strings.Contains(plain, want) {
			t.Errorf("menu is missing %q", want)
		}
	}
	// The node badge tells the user which device they are.
	if !strings.Contains(plain, "tsnail-laptop") {
		t.Error("menu header does not show the node name")
	}
}

func TestMenuCursorMovesThroughEveryItem(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu
	m.node = runningNode()
	for i := range m.menu.items {
		m.menu.cursor = i
		checkFrame(t, m, "menu cursor", m.View())
	}
}

func TestBrowserRendersEmptyAndPopulated(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenBrowser
	m.node = runningNode()

	// Before the first sweep.
	checkFrame(t, m, "browser/initial", m.View())

	// Swept, nothing found.
	m.browser.snapshot = discovery.Snapshot{At: time.Now(), Candidates: 7}
	view := stripANSI(m.View())
	checkFrame(t, m, "browser/empty", m.View())
	if !strings.Contains(view, "no tailsnail peers found") {
		t.Errorf("browser does not explain an empty result:\n%s", view)
	}

	// Populated.
	cfg := game.DefaultConfig()
	m.browser.snapshot = discovery.Snapshot{
		At:         time.Now(),
		Candidates: 7,
		Peers: []discovery.Peer{
			{
				NodeID: "n1", DNSName: "grace-laptop.tail1234.ts.net", Short: "grace-laptop",
				DisplayName: "grace", Login: "grace@example.com", AppVersion: "0.1.0",
				RTT: 12 * time.Millisecond, LastSeen: time.Now(),
				Advert: &proto.Advert{
					LobbyID: "l1", Name: "friday night", Config: cfg,
					Seats: 4, Taken: 2, Phase: proto.PhaseOpen,
				},
			},
			{
				NodeID: "n2", DNSName: "hedy-desktop.tail1234.ts.net", Short: "hedy-desktop",
				DisplayName: "hedy", LastSeen: time.Now(),
				Advert: &proto.Advert{
					LobbyID: "l2", Name: "in progress", Config: cfg,
					Seats: 4, Taken: 4, Phase: proto.PhaseInGame,
				},
			},
			{
				NodeID: "n3", DNSName: "idle.tail1234.ts.net", Short: "idle",
				DisplayName: "katherine", LastSeen: time.Now(),
			},
		},
	}
	for cursor := range 3 {
		m.browser.cursor = cursor
		checkFrame(t, m, "browser/populated", m.View())
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"friday night", "grace", "2/4", "open", "in game"} {
		if !strings.Contains(plain, want) {
			t.Errorf("browser is missing %q:\n%s", want, plain)
		}
	}
}

func TestHostFormRenders(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	m.node = runningNode()

	for i := range m.form.fields {
		m.form.cursor = i
		checkFrame(t, m, "hostform", m.View())
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"lobby name", "arena width", "tick rate", "max players", "mode"} {
		if !strings.Contains(plain, want) {
			t.Errorf("host form is missing %q", want)
		}
	}
}

func TestHostFormShowsShrinkFieldOnlyInShrinkMode(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm

	m.form.cfg.Mode = game.ModeClassic
	if strings.Contains(stripANSI(m.View()), "shrink every") {
		t.Error("the shrink interval is offered in classic mode")
	}
	m.form.cfg.Mode = game.ModeShrink
	if !strings.Contains(stripANSI(m.View()), "shrink every") {
		t.Error("the shrink interval is hidden in shrinking mode")
	}
}

func TestHostFormWarnsWhenTheArenaWillNotFit(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	m.width, m.height = 70, 24
	m.form.cfg.Width, m.form.cfg.Height = 120, 48

	plain := stripANSI(m.View())
	if !strings.Contains(plain, "needs a 124×56 terminal") {
		t.Errorf("the form does not warn about the terminal size:\n%s", plain)
	}
}

func TestRoomRendersAllPhases(t *testing.T) {
	for _, phase := range []proto.LobbyPhase{proto.PhaseOpen, proto.PhaseCountdown, proto.PhaseInGame} {
		t.Run(string(phase), func(t *testing.T) {
			m := newTestModel(t)
			m.screen = screenRoom
			m.node = runningNode()
			m.session = &fakeSession{host: true}
			m.room.apply(sampleLobby(phase, 3))
			if phase == proto.PhaseCountdown {
				m.room.state.Countdown = 2
			}
			checkFrame(t, m, "room/"+string(phase), m.View())
			plain := stripANSI(m.View())

			if phase == proto.PhaseCountdown {
				// The countdown owns the screen; the roster would only be
				// noise under a swelling digit.
				for _, unwanted := range []string{"ada", "grace", "seats", "ready"} {
					if strings.Contains(plain, unwanted) {
						t.Errorf("the countdown still shows %q", unwanted)
					}
				}
				if !strings.Contains(plain, "starting in") {
					t.Errorf("the countdown does not say what is happening:\n%s", plain)
				}
				return
			}
			for _, want := range []string{"ada", "grace", "hedy", "friday night"} {
				if !strings.Contains(plain, want) {
					t.Errorf("room is missing %q", want)
				}
			}
		})
	}
}

func TestRoomRendersAtEverySeatCount(t *testing.T) {
	for n := 1; n <= game.MaxPlayers; n++ {
		m := newTestModel(t)
		m.screen = screenRoom
		m.session = &fakeSession{host: true}
		lobby := sampleLobby(proto.PhaseOpen, n)
		lobby.Config.MaxPlayers = game.MaxPlayers
		m.room.apply(lobby)
		for cursor := range n {
			m.room.cursor = cursor
			checkFrame(t, m, "room seats", m.View())
		}
	}
}

func TestRoomNarrowLayoutStacks(t *testing.T) {
	// Below the side-by-side threshold the feed must move under the roster
	// rather than being clipped.
	m := newTestModel(t)
	m.screen = screenRoom
	m.width, m.height = 70, 40
	m.session = &fakeSession{host: true}
	m.room.apply(sampleLobby(proto.PhaseOpen, 3))

	view := m.View()
	checkFrame(t, m, "room/narrow", view)
	if !strings.Contains(stripANSI(view), "activity") {
		t.Error("the activity feed disappeared in the narrow layout")
	}
}

func TestCountdownDigitsRender(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for n := 1; n <= 3; n++ {
			lines := bigDigit(n, ascii)
			if len(lines) != 5 {
				t.Fatalf("digit %d rendered %d lines, want 5", n, len(lines))
			}
			width := lipgloss.Width(lines[0])
			for i, l := range lines {
				if got := lipgloss.Width(l); got != width {
					t.Errorf("digit %d line %d is %d cells, want %d: ragged art shears the layout", n, i, got, width)
				}
			}
		}
	}
}

// gameFixture builds a model sitting in a live match.
func gameFixture(t *testing.T, players int, cfg game.Config) *Model {
	t.Helper()
	m := newTestModel(t)
	m.node = runningNode()
	m.session = &fakeSession{}
	roster := samplePlayers(players)
	m.room.apply(sampleLobby(proto.PhaseInGame, players))

	seats := make([]game.PlayerID, players)
	for i := range seats {
		seats[i] = game.PlayerID(i)
	}
	sim, err := game.New(cfg, seats)
	if err != nil {
		t.Fatal(err)
	}
	m.game.start(netplay.GameStarted{
		MatchID: proto.NewMatchID(), Config: cfg, Seat: 0, Players: roster,
	}, m.now)
	for range 20 {
		m.game.apply(sim.Step(), m.now)
	}
	m.screen = screenGame
	return m
}

func TestGameRendersAcrossArenaSizes(t *testing.T) {
	sizes := []struct{ w, h int }{
		{game.MinWidth, game.MinHeight},
		{40, 20},
		{80, 30},
		{game.MaxWidth, game.MaxHeight},
	}
	for _, size := range sizes {
		cfg := game.DefaultConfig()
		cfg.Width, cfg.Height = size.w, size.h
		m := gameFixture(t, 4, cfg)
		// The viewport must clear both the arena and the chrome's own minimum.
		m.width, m.height = max(size.w+20, minWidth+8), max(size.h+16, minHeight+8)

		view := m.View()
		checkFrame(t, m, "game", view)

		// The arena must be exactly as wide as configured, plus its border.
		if !strings.Contains(view, strings.Repeat(m.style.Glyphs.Horizontal, size.w)) {
			t.Errorf("%dx%d: arena border is not %d cells wide", size.w, size.h, size.w)
		}
	}
}

func TestGameArenaHasExactlyTheConfiguredRows(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Width, cfg.Height = 30, 14
	m := gameFixture(t, 3, cfg)

	arena := stripANSI(m.renderArena(m.game.state))
	lines := strings.Split(arena, "\n")
	if got, want := len(lines), cfg.Height+2; got != want {
		t.Fatalf("arena is %d lines, want %d (height plus two borders)", got, want)
	}
	for i, l := range lines {
		if got, want := lipgloss.Width(l), cfg.Width+2; got != want {
			t.Errorf("arena line %d is %d cells, want %d", i, got, want)
		}
	}
}

func TestGameFitsAtExactlyTheSizeItAsksFor(t *testing.T) {
	// requiredSize gates the resize overlay. If a frame overflows at exactly
	// that size, the overlay would hand control back to a layout that then
	// shears — so the boundary itself has to be correct.
	sizes := []struct{ w, h int }{
		{game.MinWidth, game.MinHeight},
		{40, 20},
		{80, 30},
		{game.MaxWidth, game.MaxHeight},
	}
	for _, size := range sizes {
		cfg := game.DefaultConfig()
		cfg.Width, cfg.Height = size.w, size.h
		m := gameFixture(t, game.MaxPlayers, cfg)

		needW, needH := m.requiredSize()
		m.width, m.height = needW, needH

		view := m.View()
		if strings.Contains(stripANSI(view), "too small") {
			t.Fatalf("%dx%d arena: the overlay fired at the size requiredSize asked for (%dx%d)",
				size.w, size.h, needW, needH)
		}
		checkFrame(t, m, fmt.Sprintf("game at exactly %dx%d", needW, needH), view)
	}
}

func TestGameRendersEveryPlayerCount(t *testing.T) {
	for n := 1; n <= game.MaxPlayers; n++ {
		cfg := game.DefaultConfig()
		m := gameFixture(t, n, cfg)
		checkFrame(t, m, "game players", m.View())
	}
}

func TestGameRendersWithEffectsInFlight(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Mode = game.ModeShrink
	cfg.ShrinkEvery = 5
	m := gameFixture(t, 4, cfg)

	// Seed one of every effect kind at a valid cell.
	m.game.effects = []effect{
		{at: game.Point{X: 3, Y: 3}, slot: 0, born: m.now, kind: game.EventDeath},
		{at: game.Point{X: 4, Y: 4}, slot: 1, born: m.now, kind: game.EventEat},
		{at: game.Point{X: 0, Y: 0}, slot: 2, born: m.now, kind: game.EventShrink},
	}
	checkFrame(t, m, "game effects", m.View())

	// And halfway through their fade.
	m.now = m.now.Add(deathFlashDuration / 2)
	checkFrame(t, m, "game effects mid-fade", m.View())
}

func TestGameEffectsOutsideTheArenaAreIgnored(t *testing.T) {
	// An effect anchored out of bounds must not panic or corrupt the frame.
	cfg := game.DefaultConfig()
	m := gameFixture(t, 2, cfg)
	m.game.effects = []effect{
		{at: game.Point{X: -5, Y: -5}, born: m.now, kind: game.EventDeath},
		{at: game.Point{X: 9999, Y: 9999}, born: m.now, kind: game.EventEat},
	}
	checkFrame(t, m, "game out-of-bounds effects", m.View())
}

func TestGameRendersDeadAndCoastingSnakes(t *testing.T) {
	cfg := game.DefaultConfig()
	m := gameFixture(t, 4, cfg)
	m.game.state.Snakes[1].Alive = false
	m.game.state.Snakes[2].Coasting = true

	view := m.View()
	checkFrame(t, m, "game/dead+coasting", view)
	plain := stripANSI(view)
	if !strings.Contains(plain, "out") {
		t.Error("the HUD does not mark an eliminated player")
	}
}

func TestGameOverRenders(t *testing.T) {
	cfg := game.DefaultConfig()
	m := gameFixture(t, 4, cfg)

	final := m.game.state
	final.Over = true
	for i := range final.Snakes {
		final.Snakes[i].Placement = i + 1
		final.Snakes[i].MaxLength = 10 - i
		final.Snakes[i].DiedAtTick = 100 * (i + 1)
	}
	final.Snakes[0].DiedAtTick = -1
	m.over.apply(netplay.MatchOver{State: final, Players: samplePlayers(4)}, m.room.state, m.now)
	m.screen = screenGameOver

	// Before the record arrives.
	checkFrame(t, m, "gameover/pending", m.View())
	if !strings.Contains(stripANSI(m.View()), "saving result") {
		t.Error("the results screen does not show that the result is being saved")
	}

	// And after.
	rec := signedRecord(t, m, 4, 4)
	m.over.record = &rec
	view := m.View()
	checkFrame(t, m, "gameover/attested", view)
	plain := stripANSI(view)
	if !strings.Contains(plain, "wins") {
		t.Error("the results screen does not name a winner")
	}
	if !strings.Contains(plain, "added to your history") {
		t.Errorf("the results screen does not confirm the result was saved:\n%s", plain)
	}
	// Attestation is machinery; the history screen is where it belongs.
	for _, jargon := range []string{"signed", "signature", "attest"} {
		if strings.Contains(strings.ToLower(plain), jargon) {
			t.Errorf("the results screen leaks %q at the player:\n%s", jargon, plain)
		}
	}

	// A partially attested record must not change what a player is told here.
	partial := signedRecord(t, m, 4, 2)
	m.over.record = &partial
	if !strings.Contains(stripANSI(m.View()), "added to your history") {
		t.Error("a partially attested record changed the confirmation")
	}

	// Mid-slide, before the dialog settles.
	m.over.at = m.now.Add(-gameOverSlide / 3)
	checkFrame(t, m, "gameover/sliding", m.View())
}

// signedRecord builds an attested record for the model's own identity plus
// extra synthetic participants.
func signedRecord(t *testing.T, m *Model, participants, signers int) proto.AttestedRecord {
	t.Helper()
	idents := []*store.Identity{m.app.Ident}
	for i := 1; i < participants; i++ {
		dir := t.TempDir()
		id, err := store.LoadOrCreateIdentity(dir, samplePlayers(participants)[i].DisplayName)
		if err != nil {
			t.Fatal(err)
		}
		idents = append(idents, id)
	}

	r := proto.MatchResult{
		Version:    proto.MatchResultVersion,
		MatchID:    proto.NewMatchID(),
		LobbyName:  "friday night",
		Config:     game.DefaultConfig(),
		StartedAt:  proto.FormatTime(m.now.Add(-2 * time.Minute)),
		EndedAt:    proto.FormatTime(m.now),
		HostPubKey: idents[0].PubKey(),
	}
	for i, id := range idents {
		r.Participants = append(r.Participants, proto.Participant{
			PubKey: id.PubKey(), DisplayName: id.DisplayName,
			Login: id.DisplayName + "@example.com", Node: "tsnail-" + id.DisplayName,
			Seat: game.PlayerID(i),
		})
		r.Placements = append(r.Placements, proto.Placement{
			PubKey: id.PubKey(), Place: i + 1, Length: 12 - i, Score: 5 - i, Kills: participants - 1 - i,
			SurvivalTicks: 500 - i*50,
		})
	}
	rec, err := proto.NewAttestedRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range idents[:signers] {
		sig, err := proto.SignResult(id.Private, rec.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func TestHistoryRendersBothTabs(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.screen = screenHistory

	// Empty first.
	for _, tab := range []historyTab{tabLeaderboard, tabMatches} {
		m.history.tab = tab
		view := m.View()
		checkFrame(t, m, "history/empty", view)
		if !strings.Contains(stripANSI(view), "no matches recorded yet") {
			t.Error("the empty history does not explain itself")
		}
	}

	// Then with records.
	for range 5 {
		rec := signedRecord(t, m, 3, 3)
		if _, err := m.app.Store.Put(rec); err != nil {
			t.Fatal(err)
		}
	}
	partial := signedRecord(t, m, 3, 1)
	if _, err := m.app.Store.Put(partial); err != nil {
		t.Fatal(err)
	}
	m.history.reload(m.app.Store)

	for _, tab := range []historyTab{tabLeaderboard, tabMatches} {
		m.history.tab = tab
		for cursor := range 6 {
			m.history.cursor = cursor
			m.history.clamp()
			checkFrame(t, m, "history", m.View())
		}
	}

	m.history.tab = tabMatches
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "partial 1/3") {
		t.Errorf("the match list does not flag a partially attested record:\n%s", plain)
	}

	m.history.tab = tabLeaderboard
	plain = stripANSI(m.View())
	if !strings.Contains(plain, "ada") {
		t.Error("the leaderboard does not list the local player")
	}
	if !strings.Contains(plain, "(you)") {
		t.Error("the leaderboard does not mark the local player")
	}
}

func TestSettingsRenders(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.screen = screenSettings

	for i := range m.settings.fields {
		m.settings.cursor = i
		checkFrame(t, m, "settings", m.View())
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"display name", "theme", "glyphs", "colour", "re-authenticate"} {
		if !strings.Contains(plain, want) {
			t.Errorf("settings is missing %q", want)
		}
	}
}

func TestSettingsShowsTheStatePath(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenSettings
	m.app.StateDir = "/home/ada/.config/tsnail"

	if !strings.Contains(stripANSI(m.View()), "/home/ada/.config/tsnail") {
		t.Error("settings does not show where state lives")
	}
}

func TestSettingsTrimsALongStatePathFromTheLeft(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenSettings
	m.app.StateDir = "/very/deeply/nested/directory/that/goes/on/and/on/forever/tsnail-state"

	plain := stripANSI(m.View())
	checkFrame(t, m, "settings/long path", m.View())
	if !strings.Contains(plain, "tsnail-state") {
		t.Errorf("the informative tail of the path was trimmed away:\n%s", plain)
	}
	if strings.Contains(plain, "/very/deeply") {
		t.Error("a path too long to fit was not trimmed")
	}
}

func TestLogOverlayRenders(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.showLog = true

	// Empty.
	checkFrame(t, m, "log/empty", m.View())
	if !strings.Contains(stripANSI(m.View()), "nothing logged yet") {
		t.Error("the empty log does not say so")
	}

	// With output, including a line that tries to escape.
	for i := range 200 {
		m.app.Log.Logf("line %d \x1b[31mred\x1b[0m", i)
	}
	view := m.View()
	checkFrame(t, m, "log", view)
	if strings.Contains(view, "\x1b[31m") {
		t.Error("a log line's own colour escape reached the frame")
	}
	for _, top := range []int{0, 10, 500} {
		m.logTop = top
		checkFrame(t, m, "log scrolled", m.View())
	}
}

func TestResizeOverlayAppearsWhenTooSmall(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu
	for _, size := range [][2]int{{20, 8}, {40, 10}, {61, 17}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		checkFrame(t, m, "resize", view)
		if !strings.Contains(stripANSI(view), "too small") {
			t.Errorf("%dx%d did not produce the resize overlay:\n%s", size[0], size[1], stripANSI(view))
		}
	}
}

func TestResizeOverlayDegradesToFitAnyWindow(t *testing.T) {
	// Below the size that fits a sentence the overlay drops to the target
	// dimensions alone, and below that to nothing but an ellipsis. What matters
	// at these sizes is only that the frame still fits, since an overlay that
	// overflowed would produce the very corruption it exists to prevent.
	m := newTestModel(t)
	m.screen = screenMenu
	for _, size := range [][2]int{{30, 6}, {18, 3}, {8, 2}, {3, 1}, {1, 1}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		checkFrame(t, m, fmt.Sprintf("resize %dx%d", size[0], size[1]), view)
	}
}

func TestResizeOverlayStatesTheTargetSizeWhileItFits(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenMenu
	m.width, m.height = 24, 4

	plain := stripANSI(m.View())
	if !strings.Contains(plain, fmt.Sprintf("%d×%d", minWidth, minHeight)) {
		t.Errorf("the compact overlay does not state the target size:\n%s", plain)
	}
}

func TestGameShowsResizeOverlayForAnOversizedArena(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Width, cfg.Height = 100, 40
	m := gameFixture(t, 2, cfg)
	m.width, m.height = 80, 24

	view := m.View()
	checkFrame(t, m, "game/too small", view)
	plain := stripANSI(view)
	if !strings.Contains(plain, "too small") {
		t.Fatalf("an arena larger than the window did not trigger the overlay:\n%s", plain)
	}
	if !strings.Contains(plain, "104") {
		t.Errorf("the overlay does not state the required width:\n%s", plain)
	}
}

func TestEveryScreenRendersInBothThemesAndGlyphSets(t *testing.T) {
	screens := []struct {
		name  string
		setup func(*Model)
	}{
		{"onboarding", func(m *Model) {
			m.screen = screenOnboarding
			m.node = tsnode.Status{Phase: tsnode.PhaseNeedsLogin, AuthURL: "https://login.tailscale.com/a/abc", Since: time.Now()}
		}},
		{"menu", func(m *Model) { m.screen = screenMenu }},
		{"browser", func(m *Model) {
			m.screen = screenBrowser
			m.browser.snapshot = discovery.Snapshot{At: time.Now(), Candidates: 3}
		}},
		{"hostform", func(m *Model) { m.screen = screenHostForm }},
		{"room", func(m *Model) {
			m.screen = screenRoom
			m.session = &fakeSession{host: true}
			m.room.apply(sampleLobby(proto.PhaseOpen, 4))
		}},
		{"settings", func(m *Model) { m.screen = screenSettings }},
		{"history", func(m *Model) { m.screen = screenHistory }},
		{"log", func(m *Model) { m.showLog = true }},
	}

	for _, themeName := range []string{"neon", "mono"} {
		for _, ascii := range []bool{false, true} {
			for _, mode := range []theme.Mode{theme.ModeTrueColor, theme.Mode256, theme.Mode16, theme.ModeNone} {
				for _, sc := range screens {
					m := newTestModel(t)
					m.node = runningNode()
					m.app.Settings.Theme = themeName
					m.app.Settings.ASCII = ascii
					m.app.ColorFlag = mode
					m.restyle()
					sc.setup(m)

					label := sc.name + "/" + themeName
					if ascii {
						label += "/ascii"
					}
					label += "/" + string(mode)
					checkFrame(t, m, label, m.View())
				}
			}
		}
	}
}

func TestNoColorModeEmitsNoEscapes(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.app.ColorFlag = theme.ModeNone
	m.restyle()

	for _, sc := range []screen{screenMenu, screenBrowser, screenHostForm, screenSettings, screenHistory} {
		m.screen = sc
		if view := m.View(); strings.ContainsRune(view, 0x1b) {
			t.Errorf("screen %d emitted an escape sequence with colour disabled", sc)
		}
	}
}

func TestArenaEmitsNoEscapesWithoutColor(t *testing.T) {
	cfg := game.DefaultConfig()
	m := gameFixture(t, 4, cfg)
	m.app.ColorFlag = theme.ModeNone
	m.restyle()

	if arena := m.renderArena(m.game.state); strings.ContainsRune(arena, 0x1b) {
		t.Error("the arena emitted escape sequences with colour disabled")
	}
}

func TestViewIsStableAcrossAnimationFrames(t *testing.T) {
	// Advancing the frame counter must never change the shape of a frame, only
	// its colours; a layout that shifts with the animation clock flickers.
	cfg := game.DefaultConfig()
	m := gameFixture(t, 4, cfg)

	base := stripANSI(m.View())
	for f := 1; f < 120; f++ {
		m.frame = f
		got := stripANSI(m.View())
		if lipgloss.Height(got) != lipgloss.Height(base) {
			t.Fatalf("frame %d changed the view height", f)
		}
		checkFrame(t, m, "animated game", m.View())
	}
}

func TestATerminalThatReportsNoSizeStillRenders(t *testing.T) {
	// A pty with no window size set reports 0×0. Falling back to a
	// conventional viewport keeps a usable screen up; rendering nothing would
	// be indistinguishable from a hang.
	m := New(&App{
		Ctx: context.Background(), Store: newTestModel(t).app.Store,
		Ident: newTestModel(t).app.Ident, Log: logring.New(16),
		Settings: store.DefaultSettings(), ColorFlag: theme.ModeTrueColor,
	})
	m.screen = screenMenu

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m = updated.(*Model)
	if m.width != fallbackWidth || m.height != fallbackHeight {
		t.Fatalf("viewport = %dx%d, want the %dx%d fallback", m.width, m.height, fallbackWidth, fallbackHeight)
	}
	if strings.TrimSpace(stripANSI(m.View())) == "" {
		t.Fatal("a terminal reporting no size produced a blank screen")
	}
	checkFrame(t, m, "fallback viewport", m.View())

	// A real size must then take over.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)
	if m.width != 120 || m.height != 40 {
		t.Fatalf("viewport = %dx%d, want 120x40", m.width, m.height)
	}
}

// fakeNode stands in for the embedded Tailscale node. The interface is narrow
// enough that a channel and a counter cover it.
type fakeNode struct {
	updates  chan tsnode.Status
	relogins int
}

func newFakeNode() *fakeNode { return &fakeNode{updates: make(chan tsnode.Status, 8)} }

func (f *fakeNode) Updates() <-chan tsnode.Status { return f.updates }

func (f *fakeNode) Relogin(context.Context) error {
	f.relogins++
	return nil
}

// fakeSession stands in for a lobby session in render tests.
type fakeSession struct {
	host bool
	seat game.PlayerID
}

func (f *fakeSession) Events() <-chan netplay.Event    { return nil }
func (f *fakeSession) Seat() game.PlayerID             { return f.seat }
func (f *fakeSession) IsHost() bool                    { return f.host }
func (f *fakeSession) LobbyID() string                 { return "test-lobby" }
func (f *fakeSession) SetReady(bool)                   {}
func (f *fakeSession) Input(game.Direction)            {}
func (f *fakeSession) Kick(game.PlayerID)              {}
func (f *fakeSession) Reconfigure(string, game.Config) {}
func (f *fakeSession) Close(string)                    {}

// stripANSI removes escape sequences so assertions can look at the text.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestFieldListDoesNotShiftAsTheSelectionMoves(t *testing.T) {
	// This is the whole point of moving descriptions into a popover: an
	// inline description changes the panel's height, so every row below the
	// selection jumps by a line on each keypress.
	screens := []struct {
		name   string
		screen screen
		count  func(*Model) int
		set    func(*Model, int)
	}{
		{"host form", screenHostForm,
			func(m *Model) int { return len(m.visibleFields()) },
			func(m *Model, i int) { m.form.cursor = m.visibleFields()[i] }},
		{"settings", screenSettings,
			func(m *Model) int { return len(m.settings.fields) },
			func(m *Model, i int) { m.settings.cursor = i }},
	}

	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.screen = sc.screen

			// Where each field label sits vertically, for the first selection.
			var baseline map[string]int
			for i := range sc.count(m) {
				sc.set(m, i)
				rows := labelRows(stripANSI(m.View()))
				if baseline == nil {
					baseline = rows
					continue
				}
				for label, row := range baseline {
					got, ok := rows[label]
					if !ok {
						t.Fatalf("selection %d lost the row %q entirely", i, label)
					}
					if got != row {
						t.Errorf("selection %d moved %q from line %d to line %d", i, label, row, got)
					}
				}
			}
		})
	}
}

// labelRows maps each field label to the line it appears on. Labels are the
// left-most word on a row inside the panel, which is enough to track movement.
func labelRows(view string) map[string]int {
	out := map[string]int{}
	for i, line := range strings.Split(view, "\n") {
		// Strip the panel border and the selection marker, which is exactly
		// what distinguishes a selected row from an unselected one.
		trimmed := strings.TrimSpace(strings.Trim(line, "│|╭╮╰╯─-+›> "))
		if trimmed == "" {
			continue
		}
		for _, label := range []string{
			"lobby name", "arena width", "arena height", "tick rate", "snake speed",
			"max players", "bots", "walls", "mode", "shrink every", "food",
			"display name", "theme", "glyphs", "colour", "auto-resize",
			"node details", "re-authenticate",
		} {
			if strings.HasPrefix(trimmed, label) {
				if _, seen := out[label]; !seen {
					out[label] = i
				}
			}
		}
	}
	return out
}

func TestSelectedFieldGetsAPopoverDescription(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	m.width, m.height = 140, 40
	m.form.cursor = 1 // arena width

	plain := stripANSI(m.View())
	if !strings.Contains(plain, "120") {
		t.Errorf("the description for the selected field is missing:\n%s", plain)
	}
	// The pointer ties the popover to its row.
	if !strings.Contains(m.View(), m.style.Glyphs.PointLeft) &&
		!strings.Contains(m.View(), m.style.Glyphs.PointRight) {
		t.Error("the popover has no pointer tying it to the field")
	}
	checkFrame(t, m, "hostform/popover", m.View())
}

func TestHostFormShowsBotsAndReadsSpeedAsSpeed(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm

	plain := stripANSI(m.View())
	if !strings.Contains(plain, "bots") {
		t.Error("the host form does not offer bots")
	}
	if !strings.Contains(plain, "cells/s") {
		t.Errorf("snake speed is not expressed as a speed:\n%s", plain)
	}
}

func TestHostFormEditModeRendersDifferently(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenHostForm
	m.form.editFrom(sampleLobby(proto.PhaseOpen, 2))

	plain := stripANSI(m.View())
	if !strings.Contains(plain, "lobby settings") {
		t.Errorf("edit mode does not say what it is:\n%s", plain)
	}
	if !strings.Contains(plain, "ready up again") {
		t.Error("edit mode does not warn that everyone will be un-readied")
	}
	if !strings.Contains(plain, "cancel") {
		t.Error("edit mode offers no way to back out")
	}
	checkFrame(t, m, "hostform/editing", m.View())
}

func TestActivityModalRendersAndPages(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.screen = screenRoom
	m.session = &fakeSession{host: true}

	lobby := sampleLobby(proto.PhaseOpen, 3)
	for i := range 30 {
		lobby.Events = append(lobby.Events, proto.LobbyEvent{
			At:   time.Now(),
			Text: fmt.Sprintf("event %d with quite a lot of text so that it has to wrap somewhere", i),
		})
	}
	m.room.apply(lobby)

	// Not shown until asked for: the room is about the roster.
	if strings.Contains(stripANSI(m.View()), "event 29") {
		t.Error("the activity feed is on screen without being asked for")
	}

	m.openModal(modalActivity)
	view := m.View()
	checkFrame(t, m, "activity modal", view)
	if !strings.Contains(stripANSI(view), "activity") {
		t.Error("the activity dialog has no title")
	}
	if !strings.Contains(stripANSI(view), "event 29") {
		t.Error("the activity dialog does not show the newest event")
	}

	for _, top := range []int{0, 5, 25, 100} {
		m.modalTop = top
		checkFrame(t, m, "activity modal scrolled", m.View())
	}

	// An empty feed says so rather than showing a blank box.
	m.room.state.Events = nil
	m.modalTop = 0
	if !strings.Contains(stripANSI(m.View()), "nothing has happened") {
		t.Error("an empty activity dialog does not explain itself")
	}
}

func TestResultsPlaceNumbersLineUp(t *testing.T) {
	cfg := game.DefaultConfig()
	m := gameFixture(t, 4, cfg)
	final := m.game.state
	final.Over = true
	for i := range final.Snakes {
		final.Snakes[i].Placement = i + 1
	}
	m.over.apply(netplay.MatchOver{State: final, Players: samplePlayers(4)}, m.room.state, m.now)
	m.screen = screenGameOver

	// Every place digit must start in the same display column, including first
	// place, whose winner mark lives in its own column. Positions are measured
	// in cells rather than bytes: the check mark is three bytes wide and one
	// cell, so a byte offset would report a misalignment that is not there.
	var columns []int
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		for place := 1; place <= 4; place++ {
			needle := fmt.Sprintf("%d ", place)
			idx := strings.Index(line, needle)
			// Only rows that also carry a player name are result rows.
			if idx > 0 && strings.Contains(line, samplePlayers(4)[place-1].DisplayName) {
				columns = append(columns, ansi.StringWidth(line[:idx]))
			}
		}
	}
	if len(columns) != 4 {
		t.Fatalf("found %d result rows, want 4", len(columns))
	}
	for i, c := range columns {
		if c != columns[0] {
			t.Errorf("place %d starts at column %d, but place 1 starts at %d", i+1, c, columns[0])
		}
	}
}

func TestHeaderCarriesTheSnailWhenEmojiAreAvailable(t *testing.T) {
	m := newTestModel(t)
	m.app.EmojiFlag = theme.EmojiOn
	m.restyle()
	m.screen = screenMenu
	m.node = runningNode()

	view := m.View()
	if !strings.Contains(view, theme.SnailIcon) {
		t.Error("the header does not carry the snail icon with emoji enabled")
	}
	// A double-width glyph must not push the header past the viewport.
	checkFrame(t, m, "menu with emoji", view)
	if got := m.windowTitle(); !strings.Contains(got, theme.SnailIcon) {
		t.Errorf("window title = %q, want the icon", got)
	}

	m.app.EmojiFlag = theme.EmojiOff
	m.restyle()
	if strings.Contains(m.View(), theme.SnailIcon) {
		t.Error("the icon survived turning emoji off")
	}
	if got := m.windowTitle(); strings.Contains(got, theme.SnailIcon) {
		t.Errorf("window title = %q, want no icon", got)
	}
}

func TestAnimationRunsOnWallClockNotFrameCount(t *testing.T) {
	// Decoration must look the same regardless of how often the loop happens
	// to run, which is what ties it to elapsed time rather than frames.
	m := newTestModel(t)
	base := m.phase(time.Second)

	m.frame += 1000
	if got := m.phase(time.Second); got != base {
		t.Fatal("the animation phase moved when only the frame counter did")
	}
	m.now = m.now.Add(250 * time.Millisecond)
	if got := m.phase(time.Second); got == base {
		t.Fatal("the animation phase did not move when time passed")
	}
	// A full cycle returns to where it started.
	m.now = m.now.Add(750 * time.Millisecond)
	if got := m.phase(time.Second); abs64(got-base) > 1e-9 {
		t.Errorf("phase after a full cycle = %v, want %v", got, base)
	}
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestEveryFieldOccupiesExactlyOneRow(t *testing.T) {
	// A value long enough to wrap would push every row below it down and move
	// the popover's anchor off the field it describes.
	cases := []struct {
		name   string
		screen screen
		labels []string
	}{
		{"host form", screenHostForm, []string{
			"lobby name", "arena width", "arena height", "tick rate",
			"snake speed", "max players", "bots", "walls", "mode", "food",
		}},
		{"settings", screenSettings, []string{
			"display name", "theme", "glyphs", "colour",
			"auto-resize", "node details", "re-authenticate",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, width := range []int{62, 80, 120} {
				m := newTestModel(t)
				m.screen = tc.screen
				m.width, m.height = width, 40

				rows := labelRows(stripANSI(m.View()))
				for _, label := range tc.labels {
					row, ok := rows[label]
					if !ok {
						t.Errorf("width %d: field %q is missing", width, label)
						continue
					}
					// The next field must be on the very next line.
					for _, other := range tc.labels {
						if other == label {
							continue
						}
						if rows[other] == row {
							t.Errorf("width %d: %q and %q share line %d", width, label, other, row)
						}
					}
				}
				// The rows must be consecutive: any gap means one wrapped.
				var lines []int
				for _, label := range tc.labels {
					if r, ok := rows[label]; ok {
						lines = append(lines, r)
					}
				}
				sort.Ints(lines)
				for i := 1; i < len(lines); i++ {
					if lines[i] != lines[i-1]+1 {
						t.Errorf("width %d: a gap between field rows %d and %d suggests a wrapped value",
							width, lines[i-1], lines[i])
					}
				}
			}
		})
	}
}

func TestAPeerIsOnlyMarkedJoiningWhileAJoinIsInFlight(t *testing.T) {
	m := newTestModel(t)
	m.node = runningNode()
	m.screen = screenBrowser
	cfg := game.DefaultConfig()
	adv := &proto.Advert{LobbyID: "l1", Name: "open lobby", Config: cfg, Seats: 4, Taken: 1, Phase: proto.PhaseOpen}

	// A peer whose node ID is empty must not collide with the "nothing in
	// flight" sentinel.
	m.browser.snapshot = discovery.Snapshot{At: time.Now(), Candidates: 1, Peers: []discovery.Peer{
		{NodeID: "", DNSName: "grace.ts.net", Short: "grace", DisplayName: "grace",
			LastSeen: time.Now(), Advert: adv},
	}}
	plain := stripANSI(m.View())
	if strings.Contains(plain, "joining") {
		t.Errorf("a lobby nobody is joining reads as joining:\n%s", plain)
	}
	if !strings.Contains(plain, "open") {
		t.Errorf("an open lobby is not shown as open:\n%s", plain)
	}

	// And a peer that is genuinely being joined does say so.
	m.browser.snapshot.Peers[0].NodeID = "peer-1"
	m.browser.joining = "peer-1"
	if !strings.Contains(stripANSI(m.View()), "joining") {
		t.Error("a join in flight is not shown")
	}
}

func TestANoticeDoesNotShiftTheLayout(t *testing.T) {
	// The notice line is always reserved, so a message appearing or expiring
	// must leave every other line exactly where it was.
	for _, sc := range []screen{screenMenu, screenBrowser, screenHostForm, screenSettings, screenHistory} {
		m := newTestModel(t)
		m.node = runningNode()
		m.screen = sc

		before := strings.Split(stripANSI(m.View()), "\n")
		m.setToast(toastOk, "Settings saved")
		during := strings.Split(stripANSI(m.View()), "\n")

		if len(before) != len(during) {
			t.Fatalf("screen %v: a notice changed the frame from %d to %d lines", sc, len(before), len(during))
		}
		differing := 0
		for i := range before {
			if before[i] != during[i] {
				differing++
			}
		}
		if differing != 1 {
			t.Errorf("screen %v: a notice changed %d lines, want only its own", sc, differing)
		}
		if !strings.Contains(during[len(during)-3], "Settings saved") {
			t.Errorf("screen %v: the notice is not on the reserved line", sc)
		}
	}
}

func TestTheCountdownShowsTheBoard(t *testing.T) {
	cfg := game.DefaultConfig()
	cfg.Width, cfg.Height = 46, 18
	m := gameFixture(t, 4, cfg)
	m.width, m.height = 100, 34
	m.node = runningNode()

	lobby := sampleLobby(proto.PhaseCountdown, 4)
	lobby.Config = cfg
	lobby.Countdown = 2
	m.room.apply(lobby)

	// A freshly built board: nothing has moved yet.
	sim, err := game.New(cfg, []game.PlayerID{0, 1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	state := sim.State()
	m.game.apply(state, m.now)

	view := m.View()
	checkFrame(t, m, "countdown over the arena", view)
	plain := stripANSI(view)

	// The arena and the scoreboard are both up, so a player can find their own
	// snake before anything moves.
	if !strings.Contains(plain, strings.Repeat(m.style.Glyphs.Horizontal, cfg.Width)) {
		t.Error("the arena is not drawn during the countdown")
	}
	for _, p := range samplePlayers(4) {
		if !strings.Contains(plain, m.style.Glyphs.Head(p.Palette)) {
			t.Errorf("seat %d's glyph is not on screen during the countdown", p.Seat)
		}
	}
	if !strings.Contains(plain, "starting in 2") {
		t.Errorf("the countdown does not say how long is left:\n%s", plain)
	}
}

func TestTheCountdownDigitDoesNotCoverASnake(t *testing.T) {
	// Snakes spawn clear of the middle precisely so the digit does not land on
	// one while players are looking for themselves.
	for _, size := range []struct{ w, h int }{
		{game.MinWidth, game.MinHeight}, {30, 16}, {46, 18}, {80, 30},
	} {
		cfg := game.DefaultConfig()
		cfg.Width, cfg.Height = size.w, size.h
		for seats := 2; seats <= game.MaxPlayers; seats++ {
			ids := make([]game.PlayerID, seats)
			for i := range ids {
				ids[i] = game.PlayerID(i)
			}
			sim, err := game.New(cfg, ids)
			if err != nil {
				t.Fatal(err)
			}
			// The block the digit occupies, centred on the arena. Small
			// arenas fall back to a single character, which cannot cover
			// anything the spawn ring keeps clear of the middle.
			digitW, digitH := countdownDigitWidth, countdownDigitHeight
			if cfg.Height < 14 || cfg.Width < 24 {
				digitW, digitH = 1, 1
			}
			x0 := (cfg.Width - digitW) / 2
			y0 := (cfg.Height - digitH) / 2

			for _, sn := range sim.State().Snakes {
				h := sn.Head()
				if h.X >= x0 && h.X < x0+digitW && h.Y >= y0 && h.Y < y0+digitH {
					t.Errorf("%dx%d with %d seats: seat %d spawns at %v, under the countdown",
						size.w, size.h, seats, sn.ID, h)
				}
			}
		}
	}
}

func TestTheActivityDialogHugsItsContents(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 30
	m.node = runningNode()
	m.screen = screenRoom
	m.session = &fakeSession{}

	lobby := sampleLobby(proto.PhaseOpen, 2)
	lobby.Events = []proto.LobbyEvent{{At: time.Now(), Text: "short"}}
	m.room.apply(lobby)
	m.openModal(modalActivity)

	// Find the dialog's width from its border.
	width := 0
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		if strings.Contains(line, "╭") {
			width = strings.Count(line, "─") + 2
		}
	}
	if width == 0 {
		t.Fatal("no dialog was drawn")
	}
	// A short feed must not produce a dialog anywhere near the full window.
	if width > m.width/2 {
		t.Errorf("the dialog is %d cells wide for one short entry in a %d-cell window", width, m.width)
	}
}

func TestTheActivityDialogKeepsItsSizeWhileScrolling(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 30
	m.node = runningNode()
	m.screen = screenRoom
	m.session = &fakeSession{}

	lobby := sampleLobby(proto.PhaseOpen, 2)
	for i := range 40 {
		lobby.Events = append(lobby.Events, proto.LobbyEvent{
			At: time.Now(), Text: fmt.Sprintf("entry %d", i),
		})
	}
	// One long entry sets the width; it must not shrink when scrolled away.
	lobby.Events[0].Text = strings.Repeat("wide ", 9)
	m.room.apply(lobby)
	m.openModal(modalActivity)

	measure := func() (w, h int) {
		for _, line := range strings.Split(stripANSI(m.View()), "\n") {
			if strings.Contains(line, "╭") {
				w = strings.Count(line, "─") + 2
			}
			if strings.Contains(line, "│") {
				h++
			}
		}
		return w, h
	}
	wantW, wantH := measure()
	limit := m.modalScrollLimit()
	for _, top := range []int{0, 1, limit / 2, limit} {
		m.modalTop = top
		gotW, gotH := measure()
		if gotW != wantW || gotH != wantH {
			t.Errorf("at scroll %d the dialog is %dx%d, want %dx%d", top, gotW, gotH, wantW, wantH)
		}
	}
}

func TestDescriptionsAreShownAsPopoversEverywhere(t *testing.T) {
	// Every list that describes its selection does so with a popover, so the
	// rows themselves never move as the selection travels.
	type listScreen struct {
		name   string
		setup  func(*Model)
		count  func(*Model) int
		set    func(*Model, int)
		labels []string
	}
	screens := []listScreen{
		{
			name:   "menu",
			setup:  func(m *Model) { m.screen = screenMenu },
			count:  func(m *Model) int { return len(m.menu.items) },
			set:    func(m *Model, i int) { m.menu.cursor = i },
			labels: []string{"host a game", "find a game", "history", "settings", "quit"},
		},
		{
			name: "history matches",
			setup: func(m *Model) {
				m.screen = screenHistory
				m.history.tab = tabMatches
			},
			count: func(m *Model) int { return min(len(m.history.records), 4) },
			set:   func(m *Model, i int) { m.history.cursor = i },
		},
		{
			name: "history leaderboard",
			setup: func(m *Model) {
				m.screen = screenHistory
				m.history.tab = tabLeaderboard
			},
			count: func(m *Model) int { return min(len(m.history.board), 3) },
			set:   func(m *Model, i int) { m.history.cursor = i },
		},
	}

	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.width, m.height = 110, 30
			m.node = runningNode()
			for range 4 {
				if _, err := m.app.Store.Put(signedRecord(t, m, 3, 3)); err != nil {
					t.Fatal(err)
				}
			}
			m.history.reload(m.app.Store)
			sc.setup(m)

			n := sc.count(m)
			if n < 2 {
				t.Fatalf("only %d entries; the test proves nothing", n)
			}
			// The rows the list itself draws must not move as the selection
			// travels. Rows the popover happens to cover are excluded: that
			// occlusion is the point.
			var baseline []string
			for i := range n {
				sc.set(m, i)
				view := stripANSI(m.View())
				checkFrame(t, m, sc.name, m.View())
				rows := strings.Split(view, "\n")
				if baseline == nil {
					baseline = rows
					continue
				}
				if len(rows) != len(baseline) {
					t.Fatalf("selection %d changed the frame height", i)
				}
			}
			for _, label := range sc.labels {
				m2 := newTestModel(t)
				m2.width, m2.height = 110, 30
				sc.setup(m2)
				if !strings.Contains(stripANSI(m2.View()), label) {
					t.Errorf("%q is missing from the list", label)
				}
			}
		})
	}
}

func TestMenuDescribesTheSelectionInAPopover(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 30
	m.node = runningNode()
	m.screen = screenMenu

	for i, item := range m.menu.items {
		m.menu.cursor = i
		view := m.View()
		checkFrame(t, m, "menu popover", view)
		// The description wraps inside the box, and the box sits beside rows
		// that carry their own text, so the words cannot be matched as one
		// contiguous run. Their presence is what matters.
		plain := stripANSI(view)
		for _, word := range strings.Fields(item.desc) {
			if !strings.Contains(plain, word) {
				t.Errorf("item %q is missing %q from its description:\n%s", item.title, word, plain)
				break
			}
		}
		// The description must not be inline: it sits inside a bordered box.
		if !strings.Contains(view, m.style.Glyphs.PointLeft) &&
			!strings.Contains(view, m.style.Glyphs.PointUp) &&
			!strings.Contains(view, m.style.Glyphs.PointDown) {
			t.Errorf("item %q's description has no pointer, so it is not a popover", item.title)
		}
	}
}

func TestHistoryMatchDetailHangsUnderTheRow(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 30
	m.node = runningNode()
	for range 3 {
		if _, err := m.app.Store.Put(signedRecord(t, m, 3, 3)); err != nil {
			t.Fatal(err)
		}
	}
	m.history.reload(m.app.Store)
	m.screen = screenHistory
	m.history.tab = tabMatches
	m.history.cursor = 0

	view := m.View()
	checkFrame(t, m, "history match detail", view)
	plain := stripANSI(view)
	if !strings.Contains(plain, "40×20 classic") {
		t.Errorf("the match detail is not shown:\n%s", plain)
	}
	// A full-width table leaves no room beside it, so the box hangs below.
	if !strings.Contains(view, m.style.Glyphs.PointUp) {
		t.Error("the detail is not notched under the row it describes")
	}
}

func TestBrowserPeerDetailIsAPopover(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 26
	m.node = runningNode()
	m.screen = screenBrowser
	cfg := game.DefaultConfig()
	m.browser.snapshot = discovery.Snapshot{At: time.Now(), Candidates: 3, Peers: []discovery.Peer{
		{NodeID: "n1", DNSName: "grace-laptop.tail1234.ts.net", Short: "grace-laptop",
			DisplayName: "grace", Login: "grace@example.com", AppVersion: "0.1.0",
			RTT: 12 * time.Millisecond, LastSeen: time.Now(),
			Advert: &proto.Advert{LobbyID: "l1", Name: "friday night", Config: cfg,
				Seats: 4, Taken: 2, Phase: proto.PhaseOpen}},
		{NodeID: "n2", DNSName: "hedy.tail1234.ts.net", Short: "hedy", DisplayName: "hedy", LastSeen: time.Now()},
	}}

	for cursor := range 2 {
		m.browser.cursor = cursor
		checkFrame(t, m, "browser popover", m.View())
	}
	m.browser.cursor = 0
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "grace@example.com") {
		t.Errorf("the peer detail is not shown:\n%s", plain)
	}
	if !strings.Contains(plain, "grace-laptop.tail1234.ts.net") {
		t.Error("the peer's full DNS name is missing from the detail")
	}
}

func TestTheFormPromisesExactlyWhatTheGameDemands(t *testing.T) {
	// If the form quotes a different figure from the one the resize overlay
	// checks, a player sizes their terminal to what they were told and is then
	// shown the overlay anyway.
	sizes := []struct{ w, h int }{
		{game.MinWidth, game.MinHeight}, {40, 20}, {60, 20}, {98, 40},
		{game.MaxWidth, game.MaxHeight},
	}
	for _, size := range sizes {
		for _, seats := range []int{2, 4, game.MaxPlayers} {
			cfg := game.DefaultConfig()
			cfg.Width, cfg.Height, cfg.MaxPlayers = size.w, size.h, seats

			m := newTestModel(t)
			m.screen = screenHostForm
			m.form.cfg = cfg
			m.width, m.height = 200, 80
			advice := stripANSI(m.sizeAdvice())

			wantW, wantH := arenaViewport(cfg, seats)
			if !strings.Contains(advice, fmt.Sprintf("%d×%d", wantW, wantH)) {
				t.Errorf("%dx%d, %d seats: advice %q does not quote %d×%d",
					size.w, size.h, seats, advice, wantW, wantH)
			}

			// And a terminal of exactly that size must actually render.
			g := gameFixture(t, seats, cfg)
			g.width, g.height = wantW, wantH
			if strings.Contains(stripANSI(g.View()), "too small") {
				t.Errorf("%dx%d, %d seats: %d×%d was promised but shows the resize overlay",
					size.w, size.h, seats, wantW, wantH)
			}
			checkFrame(t, g, "arena at the promised size", g.View())
		}
	}
}

func TestTableDetailHangsFromTheLeftOfItsRow(t *testing.T) {
	// A full-width row has no meaningful right-hand end to attach to, so the
	// detail sits under the start of the row rather than out at the far edge.
	build := func(t *testing.T) *Model {
		m := newTestModel(t)
		m.width, m.height = 110, 26
		m.node = runningNode()
		return m
	}

	t.Run("lobby browser", func(t *testing.T) {
		m := build(t)
		m.screen = screenBrowser
		cfg := game.DefaultConfig()
		m.browser.snapshot = discovery.Snapshot{At: time.Now(), Candidates: 2, Peers: []discovery.Peer{
			{NodeID: "n1", DNSName: "grace-laptop.tail1234.ts.net", Short: "grace-laptop",
				DisplayName: "grace", Login: "grace@example.com", LastSeen: time.Now(),
				Advert: &proto.Advert{LobbyID: "l1", Name: "friday night", Config: cfg,
					Seats: 4, Taken: 2, Phase: proto.PhaseOpen}},
		}}
		assertHangsLeft(t, m, "grace-laptop.tail1234.ts.net")
	})

	t.Run("match list", func(t *testing.T) {
		m := build(t)
		for range 3 {
			if _, err := m.app.Store.Put(signedRecord(t, m, 3, 3)); err != nil {
				t.Fatal(err)
			}
		}
		m.history.reload(m.app.Store)
		m.screen = screenHistory
		m.history.tab = tabMatches
		assertHangsLeft(t, m, "40×20 classic")
	})
}

// assertHangsLeft checks that the popover box starts near the left edge rather
// than out at the end of the row, and that the wanted text is not wrapped.
func assertHangsLeft(t *testing.T, m *Model, want string) {
	t.Helper()
	view := stripANSI(m.View())
	checkFrame(t, m, "table detail", m.View())

	boxLeft := -1
	for _, line := range strings.Split(view, "\n") {
		if i := strings.Index(line, "╭"); i >= 0 {
			boxLeft = ansi.StringWidth(line[:i])
			break
		}
	}
	if boxLeft < 0 {
		t.Fatalf("no popover was drawn:\n%s", view)
	}
	if boxLeft > 8 {
		t.Errorf("the detail box starts at column %d, want it under the start of the row:\n%s", boxLeft, view)
	}
	// With the box given room, text that fits on one line must not be wrapped.
	if !strings.Contains(view, want) {
		t.Errorf("%q was wrapped or is missing:\n%s", want, view)
	}
}
