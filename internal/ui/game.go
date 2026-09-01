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

	// camera is the top-left of the visible window when the arena is larger
	// than the terminal. It persists between frames so the view only moves
	// when the player approaches its edge.
	camera game.Point
	// clipped records whether the last frame showed only part of the arena.
	clipped bool
	// window is the region the last frame drew. On a wrapping axis it may run
	// past the arena's bounds; cells are looked up modulo the arena.
	window game.Rect
	// wrapX and wrapY record which axes the last frame drew seamlessly.
	wrapX, wrapY bool
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
	// Steering before the first tick would be discarded by the host anyway;
	// ignoring it here keeps the countdown from feeling unresponsive rather
	// than broken.
	counting := m.room.state.Phase == proto.PhaseCountdown

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

	if counting {
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
	hud := m.renderHUD(st)
	// Whatever the body has left after the scoreboard and the arena's own
	// frame is what the board gets to use.
	availW := m.width - 2
	availH := m.bodyHeight() - lipgloss.Height(hud) - 2
	arena := m.renderArena(st, availW, availH)

	body := lipgloss.JoinVertical(lipgloss.Center, hud, arena)
	frame, top, left := m.place(body, m.bodyHeight())

	counting := m.room.state.Phase == proto.PhaseCountdown
	if counting {
		frame = m.overlayCountdown(frame, body, arena, hud, top, left)
	}

	subtitle := duration(m.now.Sub(m.game.started))
	if m.game.clipped {
		// A player who cannot see the whole board should know that, rather
		// than wondering where the other snakes went. It leads the subtitle
		// because the header trims from the right on a narrow terminal, and
		// this matters more than the clock.
		subtitle = fmt.Sprintf("%d×%d of %d×%d  %s  %s",
			m.game.window.Width(), m.game.window.Height(),
			m.game.cfg.Width, m.game.cfg.Height, m.style.Glyphs.Bullet, subtitle)
	}
	hints := []hint{{"↑↓←→ / wasd", "steer"}, {"esc", "leave"}, {",", "settings"}}
	if counting {
		// The tick clock has not started, so elapsed time would read as zero.
		subtitle = fmt.Sprintf("find your snake — starting in %d", max(m.room.state.Countdown, 0))
		hints = []hint{{"esc", "leave"}}
	}
	return m.chrome(m.room.state.Name, subtitle, frame, hints)
}

// overlayCountdown draws the starting countdown over the middle of the arena.
//
// The board is already on screen by this point, so players can pick out their
// own snake and see the others before anything moves. Snakes are spawned clear
// of the centre so the digit does not cover one of them.
func (m *Model) overlayCountdown(frame, body, arena, hud string, top, left int) string {
	block := m.countdownBlock(m.room.state.Countdown, m.compactCountdown())

	arenaTop := top + lipgloss.Height(hud)
	arenaLeft := left + (lipgloss.Width(body)-lipgloss.Width(arena))/2

	row := arenaTop + (lipgloss.Height(arena)-lipgloss.Height(block))/2
	col := arenaLeft + (lipgloss.Width(arena)-lipgloss.Width(block))/2
	return overlayAt(frame, block, max(row, arenaTop), max(col, arenaLeft))
}

// countdownDigitWidth and countdownDigitHeight are the block digit's size, and
// the area of arena it covers.
const (
	countdownDigitWidth  = 6
	countdownDigitHeight = 5
)

// compactCountdown reports whether the arena is too small to give up a 6×5
// block to the countdown. Below this size the spawn ring cannot clear that
// area, so a single character is used instead.
func (m *Model) compactCountdown() bool {
	return m.game.cfg.Height < 14 || m.game.cfg.Width < 24
}

// countdownBlock renders the digit alone.
//
// Every cell it occupies covers the board underneath, so it is kept to the
// bare digit: a caption would blank a wider rectangle of arena, and the
// wording belongs in the subtitle where it costs nothing. Snakes are spawned
// clear of the centre so the digit does not land on one.
func (m *Model) countdownBlock(n int, compact bool) string {
	th := m.style.Theme
	if n <= 0 {
		return m.style.Text(th.Accent, "go!")
	}
	// Each digit swells as its second begins and settles as it ends.
	color := th.Accent.Scale(0.75 + 0.45*m.pulse(time.Second))
	if compact {
		return m.style.Text(color, fmt.Sprintf("%d", n))
	}

	lines := make([]string, 0, countdownDigitHeight)
	for _, l := range bigDigit(n, m.style.Glyphs.ASCII) {
		lines = append(lines, m.style.Text(color, l))
	}
	return lipgloss.JoinVertical(lipgloss.Center, lines...)
}

// minArenaView is the smallest window worth playing in. Below this the board
// tells a player nothing useful, so the resize overlay takes over instead.
const minArenaView = 24

// arenaWindow returns the region of the arena to draw, centred on the player.
//
// The camera follows the player continuously rather than sitting still and
// catching up in jumps. A dead zone is the usual way to keep a view calm, but
// it only works where the world has fixed landmarks to anchor against: in a
// wrapping arena there are none, so the catch-up reads as the board lurching
// for no reason. Moving by exactly one cell per move is both smoother and
// easier to predict.
//
// On a wrapping axis the window is not clamped to the edges. The world is a
// torus: the cell to the right of the last column really is the first column,
// so drawing it there is not a trick, it is the truth. The teleport a player
// used to experience at the boundary was an artefact of clamping the camera,
// not a fact about the game. The window is never wider than the arena, so no
// cell — and no snake — can appear in two places at once.
func (m *Model) arenaWindow(st game.State, availW, availH int) game.Rect {
	w, h := m.game.cfg.Width, m.game.cfg.Height
	viewW := min(w, max(availW, minArenaView))
	viewH := min(h, max(availH, minArenaView/2))

	// Seamless rendering needs the arena to be the whole grid: once the
	// shrinking mode has closed the walls in, the ground outside them is real
	// and has to stay visible, so that falls back to a clamped view.
	seamless := m.game.cfg.Wrap && st.Arena == m.game.fullArena
	m.game.wrapX, m.game.wrapY = seamless, seamless
	m.game.clipped = viewW < w || viewH < h

	// Centre on our own snake. A dead player's last position is as good an
	// anchor as any, and keeping it still is less jarring than snapping the
	// board somewhere else at the moment they are eliminated.
	origin := game.Point{X: (w - viewW) / 2, Y: (h - viewH) / 2}
	if sn := st.SnakeByID(m.game.seat); sn != nil && len(sn.Body) > 0 {
		head := sn.Head()
		origin = game.Point{X: head.X - viewW/2, Y: head.Y - viewH/2}
	}

	if m.game.wrapX {
		origin.X = mod(origin.X, w)
	} else {
		origin.X = clampInt(origin.X, 0, max(w-viewW, 0))
	}
	if m.game.wrapY {
		origin.Y = mod(origin.Y, h)
	} else {
		origin.Y = clampInt(origin.Y, 0, max(h-viewH, 0))
	}

	m.game.camera = origin
	m.game.window = game.Rect{X0: origin.X, Y0: origin.Y, X1: origin.X + viewW - 1, Y1: origin.Y + viewH - 1}
	return m.game.window
}

// mod is a modulo that returns a non-negative result, which is what wrapping
// around an arena needs and what Go's % does not give.
func mod(v, n int) int {
	if n <= 0 {
		return 0
	}
	v %= n
	if v < 0 {
		v += n
	}
	return v
}

// cell is one rendered arena position.// cell is one rendered arena position.// cell is one rendered arena position.
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
func (m *Model) renderArena(st game.State, availW, availH int) string {
	g := m.style.Glyphs
	th := m.style.Theme
	w, h := m.game.cfg.Width, m.game.cfg.Height
	if w <= 0 || h <= 0 {
		return ""
	}
	win := m.arenaWindow(st, availW, availH)

	buf := make([]cell, w*h)
	for i := range buf {
		buf[i] = cell{glyph: g.Empty, color: th.Grid}
	}
	// Where the world folds, dash the ground. This is laid down first so a
	// snake, a pellet or an effect always paints over it: the mark explains
	// the topology and must never hide anything that matters.
	if m.game.wrapX {
		for y := range h {
			buf[y*w] = cell{glyph: g.FoldVertical, color: th.Faint}
		}
	}
	if m.game.wrapY {
		for x := range w {
			buf[x] = cell{glyph: g.FoldHorizontal, color: th.Faint}
		}
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

	return m.frameArena(buf, w, win)
}

// frameArena serialises the visible region of the cell buffer inside a border.
//
// On a wrapping axis the window may run past the arena, and cells are looked
// up modulo it: the board simply continues, with no boundary to cross. The
// frame carries a mark where the world folds, so a player can see why someone
// who looks adjacent is in fact all the way across the arena.
func (m *Model) frameArena(buf []cell, stride int, win game.Rect) string {
	g := m.style.Glyphs
	th := m.style.Theme
	w, h := m.game.cfg.Width, m.game.cfg.Height

	// The border pulses when the arena is contracting, so the closing walls
	// announce themselves before they reach anybody.
	borderColor := th.Wall
	for _, e := range m.game.effects {
		if e.kind == game.EventShrink {
			progress := float64(m.now.Sub(e.born)) / float64(shrinkFlashDuration)
			borderColor = th.Warn.Lerp(th.Wall, progress)
		}
	}
	// A clipped view is marked on its own frame, so a player can tell at a
	// glance that there is board they cannot see.
	if m.game.clipped {
		borderColor = borderColor.Mix(th.Warn)
	}
	border := m.style.SGR(borderColor)
	seam := m.style.SGR(th.Accent2)
	reset := ""
	if m.style.Colored() {
		reset = theme.Reset
	}

	viewW, viewH := win.Width(), win.Height()
	var b strings.Builder
	b.Grow(viewW*viewH*3 + viewH*8)

	// seamAtColumn and seamAtRow report where the world folds inside the view.
	seamAtColumn := func(i int) bool { return m.game.wrapX && mod(win.X0+i, w) == 0 }
	seamAtRow := func(i int) bool { return m.game.wrapY && mod(win.Y0+i, h) == 0 }

	horizontal := func(left, right, mark string) string {
		var row strings.Builder
		row.WriteString(border + left)
		for i := range viewW {
			if seamAtColumn(i) {
				row.WriteString(seam + mark + border)
				continue
			}
			row.WriteString(g.Horizontal)
		}
		row.WriteString(right + reset)
		return row.String()
	}

	b.WriteString(horizontal(g.TopLeft, g.TopRight, g.SeamTop) + "\n")
	for y := win.Y0; y <= win.Y1; y++ {
		sy := y
		if m.game.wrapY {
			sy = mod(y, h)
		}
		edge := g.Vertical
		edgeStyle := border
		if seamAtRow(y - win.Y0) {
			edge, edgeStyle = g.SeamLeft, seam
		}
		b.WriteString(edgeStyle + edge + reset)

		last := theme.RGB{}
		haveLast := false
		for x := win.X0; x <= win.X1; x++ {
			sx := x
			if m.game.wrapX {
				sx = mod(x, w)
			}
			c := buf[sy*stride+sx]
			if !haveLast || c.color != last {
				b.WriteString(m.style.SGR(c.color))
				last, haveLast = c.color, true
			}
			b.WriteString(c.glyph)
		}

		edge, edgeStyle = g.Vertical, border
		if seamAtRow(y - win.Y0) {
			edge, edgeStyle = g.SeamRight, seam
		}
		b.WriteString(reset + edgeStyle + edge + reset + "\n")
	}
	b.WriteString(horizontal(g.BottomLeft, g.BottomRight, g.SeamBottom))
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
