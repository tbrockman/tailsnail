package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/netplay"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/ui/theme"
)

// Effect durations. All are short: an effect that outlives the moment it
// describes reads as lag rather than feedback.
const (
	deathFlashDuration  = 550 * time.Millisecond
	eatFlashDuration    = 260 * time.Millisecond
	shrinkFlashDuration = 700 * time.Millisecond
)

// effect is a transient visual marker anchored to a cell.
type effect struct {
	at   game.Point
	slot int
	born time.Time
	kind game.EventKind
}

// gameState is everything the arena screen renders from.
type gameState struct {
	cfg     game.Config
	seat    game.PlayerID
	matchID string
	players []proto.Player

	state    game.State
	lastTick time.Time
	started  time.Time

	// pendingDir is the heading the player has asked for but which the host
	// has not yet reflected back. It drives local prediction.
	pendingDir game.Direction
	hasPending bool

	effects []effect
	// arenaClosed remembers the largest arena seen, so the shrinking-mode
	// walls can be drawn closing in over the original grid.
	fullArena game.Rect
}

// start initialises the screen for a new match.
func (g *gameState) start(ev netplay.GameStarted, now time.Time) {
	*g = gameState{
		cfg:       ev.Config,
		seat:      ev.Seat,
		matchID:   ev.MatchID,
		players:   ev.Players,
		started:   now,
		fullArena: game.Rect{X0: 0, Y0: 0, X1: ev.Config.Width - 1, Y1: ev.Config.Height - 1},
	}
}

// apply folds in an authoritative state and spawns effects for its events.
func (g *gameState) apply(st game.State, now time.Time) {
	g.state = st
	g.lastTick = now
	if g.fullArena.X1 == 0 {
		g.fullArena = st.Arena
	}
	for _, ev := range st.Events {
		switch ev.Kind {
		case game.EventDeath, game.EventEat, game.EventShrink:
			g.effects = append(g.effects, effect{
				at: ev.At, slot: g.paletteFor(ev.Player), born: now, kind: ev.Kind,
			})
		}
	}
	// Once the host echoes the heading we asked for, prediction is done.
	if g.hasPending {
		if sn := st.SnakeByID(g.seat); sn != nil && sn.Dir == g.pendingDir {
			g.hasPending = false
		}
	}
}

// decayEffects drops effects that have played out.
func (g *gameState) decayEffects(now time.Time) {
	if len(g.effects) == 0 {
		return
	}
	keep := g.effects[:0]
	for _, e := range g.effects {
		if now.Sub(e.born) < effectLifetime(e.kind) {
			keep = append(keep, e)
		}
	}
	g.effects = keep
}

// effectLifetime returns how long an effect of the given kind lasts.
func effectLifetime(k game.EventKind) time.Duration {
	switch k {
	case game.EventDeath:
		return deathFlashDuration
	case game.EventShrink:
		return shrinkFlashDuration
	default:
		return eatFlashDuration
	}
}

// paletteFor maps a seat to its palette slot.
func (g *gameState) paletteFor(id game.PlayerID) int {
	for _, p := range g.players {
		if p.Seat == id {
			return p.Palette
		}
	}
	return int(id)
}

// playerFor returns a seat's roster entry.
func (g *gameState) playerFor(id game.PlayerID) (proto.Player, bool) {
	for _, p := range g.players {
		if p.Seat == id {
			return p, true
		}
	}
	return proto.Player{}, false
}

// arenaCells returns the grid dimensions, used to size the viewport check.
func (g *gameState) arenaCells() (int, int) {
	if g.cfg.Width == 0 {
		return 0, 0
	}
	return g.cfg.Width, g.cfg.Height
}

// moveInterval is the wall-clock gap between snake moves.
func (g *gameState) moveInterval() time.Duration {
	if g.cfg.TickRate == 0 {
		return time.Second
	}
	return time.Duration(g.cfg.TicksPerMove) * time.Second / time.Duration(g.cfg.TickRate)
}

// displayState returns the state to draw, applying local prediction when a
// turn is outstanding and the next authoritative move is imminent.
//
// Predicting only in the last part of the move window is what keeps the
// correction invisible: the predicted cell appears a few milliseconds before
// the host's own version of it, so a turn feels instant without the snake ever
// visibly snapping back.
func (m *Model) displayState() game.State {
	g := &m.game
	if !g.hasPending || g.state.Over {
		return g.state
	}
	interval := g.moveInterval()
	if interval <= 0 {
		return g.state
	}
	if elapsed := m.now.Sub(g.lastTick); elapsed < interval*6/10 {
		return g.state
	}
	return game.Predict(g.state, g.cfg, g.seat, g.pendingDir)
}

// updateGame handles input during a match.
func (m *Model) updateGame(msg tea.KeyMsg) tea.Cmd {
	if m.session == nil {
		m.screen = screenBrowser
		return nil
	}
	var dir game.Direction
	switch {
	case key.Matches(msg, m.keys.Up):
		dir = game.DirUp
	case key.Matches(msg, m.keys.Down):
		dir = game.DirDown
	case key.Matches(msg, m.keys.Left):
		dir = game.DirLeft
	case key.Matches(msg, m.keys.Right):
		dir = game.DirRight
	case key.Matches(msg, m.keys.Back):
		reason := "left the match"
		if m.session.IsHost() {
			reason = "the host ended the match"
		}
		m.leaveSession(reason)
		m.screen = screenBrowser
		return nil
	case msg.String() == "ctrl+c":
		return m.quit()
	default:
		return nil
	}

	m.session.Input(dir)
	m.game.pendingDir = dir
	m.game.hasPending = true
	return nil
}

// viewGame renders the arena, HUD and scoreboard.
func (m *Model) viewGame() string {
	st := m.displayState()
	arena := m.renderArena(st)
	hud := m.renderHUD(st)

	body := lipgloss.JoinVertical(lipgloss.Center, hud, arena)
	// The tick number means nothing to a player; elapsed time does.
	subtitle := duration(m.now.Sub(m.game.started))
	return m.chrome(m.room.state.Name, subtitle, m.center(body, m.bodyHeight()), []hint{
		{"↑↓←→ / wasd", "steer"}, {"esc", "leave"}, {",", "settings"},
	})
}

// cell is one rendered arena position.
type cell struct {
	glyph string
	color theme.RGB
}

// renderArena draws the playfield.
//
// The grid is written into a flat buffer and then serialised with run-length
// colour compression: an escape sequence is emitted only where the colour
// actually changes. At 60 frames a second on a 120×48 arena that is the
// difference between a few hundred escapes per frame and several thousand.
func (m *Model) renderArena(st game.State) string {
	g := m.style.Glyphs
	th := m.style.Theme
	w, h := m.game.cfg.Width, m.game.cfg.Height
	if w <= 0 || h <= 0 {
		return ""
	}

	buf := make([]cell, w*h)
	for i := range buf {
		buf[i] = cell{glyph: g.Empty, color: th.Grid}
	}
	at := func(p game.Point) int {
		if p.X < 0 || p.X >= w || p.Y < 0 || p.Y >= h {
			return -1
		}
		return p.Y*w + p.X
	}

	// Cells the shrinking arena has already swallowed, drawn as closed ground
	// so the walls visibly march inward.
	if st.Arena != m.game.fullArena {
		for y := range h {
			for x := range w {
				if !st.Arena.Contains(game.Point{X: x, Y: y}) {
					buf[y*w+x] = cell{glyph: g.Dead, color: th.Wall.Scale(0.7)}
				}
			}
		}
	}

	foodPhase := m.phase(1200 * time.Millisecond)
	foodGlyph := g.Food(foodPhase)
	foodColor := th.FoodColor(foodPhase)
	for _, f := range st.Food {
		if i := at(f); i >= 0 {
			buf[i] = cell{glyph: foodGlyph, color: foodColor}
		}
	}

	// Snakes are drawn tail-first so a head always wins its cell.
	tailPhase := m.phase(900 * time.Millisecond)
	for i := range st.Snakes {
		sn := &st.Snakes[i]
		if !sn.Alive || len(sn.Body) == 0 {
			continue
		}
		slot := m.game.paletteFor(sn.ID)
		n := len(sn.Body)
		for j := n - 1; j >= 1; j-- {
			idx := at(sn.Body[j])
			if idx < 0 {
				continue
			}
			glyph := g.Body
			if j >= n-2 {
				glyph = g.Tail
			}
			color := th.TailColor(slot, j, n, tailPhase)
			if sn.Coasting {
				// A disconnected snake goes grey so the table can see it is
				// running on rails rather than being steered.
				color = color.Mix(th.Faint)
			}
			buf[idx] = cell{glyph: glyph, color: color}
		}
		if idx := at(sn.Body[0]); idx >= 0 {
			color := th.HeadColor(slot, tailPhase)
			if sn.Coasting {
				color = color.Mix(th.Faint)
			}
			buf[idx] = cell{glyph: g.Head(slot), color: color}
		}
	}

	// Effects paint last: they are the thing the eye should catch.
	for _, e := range m.game.effects {
		idx := at(e.at)
		if idx < 0 {
			continue
		}
		progress := float64(m.now.Sub(e.born)) / float64(effectLifetime(e.kind))
		switch e.kind {
		case game.EventDeath:
			buf[idx] = cell{glyph: g.Ember(progress), color: th.DeathColor(e.slot, progress)}
		case game.EventEat:
			buf[idx] = cell{glyph: g.Spark[0], color: th.Food.Lerp(th.Player(e.slot), progress)}
		case game.EventShrink:
			// The shrink flash outlines the new boundary rather than a cell.
		}
	}

	return m.frameArena(buf, w, h, st)
}

// frameArena serialises the cell buffer inside a box border.
func (m *Model) frameArena(buf []cell, w, h int, st game.State) string {
	g := m.style.Glyphs
	th := m.style.Theme

	// The border pulses when the arena is contracting, so the closing walls
	// announce themselves before they reach anybody.
	borderColor := th.Wall
	for _, e := range m.game.effects {
		if e.kind == game.EventShrink {
			progress := float64(m.now.Sub(e.born)) / float64(shrinkFlashDuration)
			borderColor = th.Warn.Lerp(th.Wall, progress)
		}
	}
	border := m.style.SGR(borderColor)
	reset := ""
	if m.style.Colored() {
		reset = theme.Reset
	}

	var b strings.Builder
	b.Grow(w*h*3 + h*8)

	b.WriteString(border + g.TopLeft + strings.Repeat(g.Horizontal, w) + g.TopRight + reset + "\n")
	for y := range h {
		b.WriteString(border + g.Vertical + reset)
		last := theme.RGB{}
		haveLast := false
		for x := range w {
			c := buf[y*w+x]
			if !haveLast || c.color != last {
				b.WriteString(m.style.SGR(c.color))
				last, haveLast = c.color, true
			}
			b.WriteString(c.glyph)
		}
		b.WriteString(reset + border + g.Vertical + reset + "\n")
	}
	b.WriteString(border + g.BottomLeft + strings.Repeat(g.Horizontal, w) + g.BottomRight + reset)
	return b.String()
}

// hudCellWidth is the fixed width of one scoreboard entry. Fixing it lets the
// HUD pack into a predictable number of rows, which requiredSize needs in
// order to reserve the right amount of vertical space.
const hudCellWidth = 20

// hudColumnsAt is how many scoreboard entries fit across a given width.
func hudColumnsAt(width int) int {
	return max(width/hudCellWidth, 1)
}

// hudRowsAt is how many rows the scoreboard occupies for n players at a given
// width. It takes the width explicitly because requiredSize must evaluate it
// at the width it is about to demand, not the current one — otherwise
// shrinking the window to exactly the required width could raise the required
// height, and the resize overlay would oscillate.
func hudRowsAt(n, width int) int {
	if n <= 0 {
		return 0
	}
	cols := hudColumnsAt(width)
	return (n + cols - 1) / cols
}

// renderHUD draws the per-player scoreboard above the arena.
//
// Entries are packed into fixed-width cells and wrapped explicitly rather than
// being left to the layout: eight players on a narrow terminal would otherwise
// soft-wrap into however many lines they happened to need, and push the arena
// off the bottom of the screen.
func (m *Model) renderHUD(st game.State) string {
	g := m.style.Glyphs
	th := m.style.Theme

	cells := make([]string, 0, len(st.Snakes))
	for i := range st.Snakes {
		sn := &st.Snakes[i]
		slot := m.game.paletteFor(sn.ID)
		p, _ := m.game.playerFor(sn.ID)
		name := p.DisplayName
		if name == "" {
			name = fmt.Sprintf("seat %d", sn.ID)
		}
		if sn.ID == m.game.seat {
			name = "you"
		}

		color := th.Player(slot)
		glyph := g.Head(slot)
		suffix := fmt.Sprintf(" %d", sn.Len())
		switch {
		case !sn.Alive:
			color = th.Faint
			glyph = g.Dead
			suffix = " out"
		case sn.Coasting:
			color = th.Warn
			suffix += " ?"
		}
		if sn.Kills > 0 && sn.Alive {
			suffix += fmt.Sprintf(" %s%d", g.Cross, sn.Kills)
		}

		// Trim the name, not the statistics: the numbers are why the HUD exists.
		budget := hudCellWidth - 2 - lipgloss.Width(suffix) - 1
		text := glyph + " " + truncate(name, max(budget, 3)) + suffix
		cells = append(cells, m.style.Text(color, pad(text, hudCellWidth-1)))
	}

	cols := hudColumnsAt(m.width)
	var rows []string
	for i := 0; i < len(cells); i += cols {
		rows = append(rows, strings.Join(cells[i:min(i+cols, len(cells))], " "))
	}
	return strings.Join(rows, "\n")
}
