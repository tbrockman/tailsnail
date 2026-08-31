package game

import (
	"math/rand/v2"
	"sort"
)

// Sim is the authoritative simulation for a single match. It is not safe for
// concurrent use; the host serialises all access from its tick loop.
type Sim struct {
	cfg   Config
	state State
	rng   *rand.Rand

	movesDone  int
	deathOrder []PlayerID // order of elimination, earliest first
	finished   bool
}

// New builds a simulation for the given configuration and seat list. Seats are
// spawned evenly around the arena travelling tangentially, which avoids the
// instant head-on collisions that facing-inward spawns produce at low seat
// counts. The returned Sim is deterministic in cfg.Seed and the seat order.
func New(cfg Config, seats []PlayerID) (*Sim, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(seats) == 0 {
		return nil, ErrNoPlayers
	}
	s := &Sim{
		cfg: cfg,
		rng: rand.New(rand.NewPCG(uint64(cfg.Seed), 0x9E3779B97F4A7C15)),
		state: State{
			Arena:  Rect{0, 0, cfg.Width - 1, cfg.Height - 1},
			Snakes: make([]Snake, 0, len(seats)),
		},
	}
	ordered := append([]PlayerID(nil), seats...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for i, id := range ordered {
		s.state.Snakes = append(s.state.Snakes, s.spawnSnake(id, i, len(ordered)))
	}
	for range cfg.FoodCount {
		if p, ok := s.freeCell(); ok {
			s.state.Food = append(s.state.Food, p)
		}
	}
	return s, nil
}

// Config returns the configuration the simulation was built with.
func (s *Sim) Config() Config { return s.cfg }

// State returns a deep copy of the current authoritative state.
func (s *Sim) State() State { return s.state.Clone() }

// Tick returns the current tick counter.
func (s *Sim) Tick() int { return s.state.Tick }

// Over reports whether the match has ended.
func (s *Sim) Over() bool { return s.state.Over }

// SetDirection queues a heading change for the next move. A reversal into the
// snake's own neck is ignored, as is any input for a dead or coasting snake.
// The queued direction replaces any earlier one queued in the same move window,
// so mashing keys can never make a snake skip a cell.
func (s *Sim) SetDirection(id PlayerID, d Direction) {
	sn := s.state.SnakeByID(id)
	if sn == nil || !sn.Alive || sn.Coasting {
		return
	}
	if d > DirLeft {
		return
	}
	if sn.Len() > 1 && d == sn.Dir.Opposite() {
		return
	}
	sn.pending, sn.hasPending = d, true
}

// SetCoasting marks a snake as owner-disconnected. A coasting snake keeps
// travelling in its current heading and ignores input, but is still a hazard
// and can still be eliminated.
func (s *Sim) SetCoasting(id PlayerID, coasting bool) {
	if sn := s.state.SnakeByID(id); sn != nil {
		sn.Coasting = coasting
		if coasting {
			sn.hasPending = false
		}
	}
}

// Eliminate removes a snake immediately, used when a disconnected player's
// grace period expires.
func (s *Sim) Eliminate(id PlayerID) {
	sn := s.state.SnakeByID(id)
	if sn == nil || !sn.Alive {
		return
	}
	s.state.Events = append(s.state.Events, Event{Kind: EventDeath, Player: id, At: sn.Head()})
	s.kill(sn)
	s.checkOver()
}

// Step advances the simulation by exactly one tick and returns a copy of the
// resulting state. Snakes only move on ticks that are a multiple of
// TicksPerMove, which is how the configured snake speed is expressed.
func (s *Sim) Step() State {
	s.state.Events = s.state.Events[:0]
	if s.state.Over {
		s.state.Tick++
		return s.State()
	}
	s.state.Tick++
	if s.state.Tick%s.cfg.TicksPerMove != 0 {
		return s.State()
	}
	s.movesDone++
	s.advance()
	if s.cfg.Mode == ModeShrink && s.movesDone%s.cfg.ShrinkEvery == 0 {
		s.shrink()
	}
	s.checkOver()
	return s.State()
}

// advance performs one movement step for every living snake, resolving all
// collisions simultaneously so that no snake gets an ordering advantage.
func (s *Sim) advance() {
	// Cells that are solid for the duration of this move. A snake's own tail
	// cell vacates as it moves, so chasing your own tail is legal — the
	// classic behaviour. A growing snake's tail does not vacate.
	solid := make(map[Point]PlayerID, len(s.state.Snakes)*8)
	for i := range s.state.Snakes {
		sn := &s.state.Snakes[i]
		if !sn.Alive {
			continue
		}
		last := len(sn.Body) - 1
		for j, p := range sn.Body {
			if j == last && sn.Grow == 0 {
				continue
			}
			solid[p] = sn.ID
		}
	}

	// Compute every new head position before resolving any outcome.
	type move struct {
		idx  int
		head Point
		ok   bool // still inside the arena
	}
	moves := make([]move, 0, len(s.state.Snakes))
	heads := make(map[Point]int, len(s.state.Snakes))
	for i := range s.state.Snakes {
		sn := &s.state.Snakes[i]
		if !sn.Alive {
			continue
		}
		if sn.hasPending {
			sn.Dir, sn.hasPending = sn.pending, false
		}
		d := sn.Dir.Delta()
		h := Point{sn.Head().X + d.X, sn.Head().Y + d.Y}
		ok := true
		if !s.state.Arena.Contains(h) {
			if s.cfg.Wrap {
				h = s.wrap(h)
			} else {
				ok = false
			}
		}
		moves = append(moves, move{idx: i, head: h, ok: ok})
		if ok {
			heads[h]++
		}
	}

	// Resolve deaths. Everything is decided against the pre-move world, so
	// two snakes entering the same cell both die rather than the earlier one
	// in seat order winning.
	doomed := make([]int, 0, len(moves))
	killers := make(map[int]PlayerID, len(moves))
	for _, m := range moves {
		sn := &s.state.Snakes[m.idx]
		if !m.ok {
			doomed = append(doomed, m.idx)
			continue
		}
		if heads[m.head] > 1 { // head-on: mutual, nobody is credited
			doomed = append(doomed, m.idx)
			continue
		}
		if owner, hit := solid[m.head]; hit {
			doomed = append(doomed, m.idx)
			if owner != sn.ID {
				killers[m.idx] = owner
			}
			continue
		}
	}

	// Survivors move first so the death events carry the cell they died on.
	dead := make(map[int]bool, len(doomed))
	for _, i := range doomed {
		dead[i] = true
	}
	for _, m := range moves {
		if dead[m.idx] {
			continue
		}
		sn := &s.state.Snakes[m.idx]
		sn.Body = append([]Point{m.head}, sn.Body...)
		if s.eatAt(m.head) {
			sn.Grow += 2
			sn.Score++
			s.state.Events = append(s.state.Events, Event{Kind: EventEat, Player: sn.ID, At: m.head})
		}
		if sn.Grow > 0 {
			sn.Grow--
		} else {
			sn.Body = sn.Body[:len(sn.Body)-1]
		}
		if sn.Len() > sn.MaxLength {
			sn.MaxLength = sn.Len()
		}
	}
	for _, i := range doomed {
		sn := &s.state.Snakes[i]
		ev := Event{Kind: EventDeath, Player: sn.ID, At: sn.Head()}
		if k, ok := killers[i]; ok {
			kp := k
			ev.Killer = &kp
			if ks := s.state.SnakeByID(k); ks != nil {
				ks.Kills++
			}
		}
		s.state.Events = append(s.state.Events, ev)
		s.kill(sn)
	}
	s.replenishFood()
}

// wrap folds a point that has left the arena around to the opposite edge.
func (s *Sim) wrap(p Point) Point {
	a := s.state.Arena
	w, h := a.Width(), a.Height()
	x := (p.X-a.X0)%w + a.X0
	if x < a.X0 {
		x += w
	}
	y := (p.Y-a.Y0)%h + a.Y0
	if y < a.Y0 {
		y += h
	}
	return Point{x, y}
}

// eatAt consumes food at p, reporting whether any was there.
func (s *Sim) eatAt(p Point) bool {
	for i, f := range s.state.Food {
		if f == p {
			s.state.Food = append(s.state.Food[:i], s.state.Food[i+1:]...)
			return true
		}
	}
	return false
}

// replenishFood tops the arena back up to the configured pellet count.
func (s *Sim) replenishFood() {
	for len(s.state.Food) < s.cfg.FoodCount {
		p, ok := s.freeCell()
		if !ok {
			return
		}
		s.state.Food = append(s.state.Food, p)
		s.state.Events = append(s.state.Events, Event{Kind: EventSpawn, At: p})
	}
}

// kill marks a snake eliminated and records its position in the death order,
// which drives final placements.
func (s *Sim) kill(sn *Snake) {
	if !sn.Alive {
		return
	}
	sn.Alive = false
	sn.DiedAtTick = s.state.Tick
	s.deathOrder = append(s.deathOrder, sn.ID)
}

// shrink contracts the arena by one ring, culling anything left outside.
func (s *Sim) shrink() {
	a := s.state.Arena
	// Each step removes a cell from both sides of an axis, so only contract an
	// axis when the result would still leave a playable span.
	shrunk := false
	if a.Width()-2 >= MinArenaSpan {
		a.X0++
		a.X1--
		shrunk = true
	}
	if a.Height()-2 >= MinArenaSpan {
		a.Y0++
		a.Y1--
		shrunk = true
	}
	if !shrunk {
		return
	}
	s.state.Arena = a
	s.state.Events = append(s.state.Events, Event{Kind: EventShrink, At: Point{a.X0, a.Y0}})

	for i := range s.state.Snakes {
		sn := &s.state.Snakes[i]
		if !sn.Alive {
			continue
		}
		if !a.Contains(sn.Head()) {
			s.state.Events = append(s.state.Events, Event{Kind: EventDeath, Player: sn.ID, At: sn.Head()})
			s.kill(sn)
			continue
		}
		// Trim any trailing body that the wall swallowed so the render stays
		// inside the arena.
		keep := sn.Body[:0]
		for _, p := range sn.Body {
			if a.Contains(p) {
				keep = append(keep, p)
			}
		}
		sn.Body = keep
	}
	food := s.state.Food[:0]
	for _, f := range s.state.Food {
		if a.Contains(f) {
			food = append(food, f)
		}
	}
	s.state.Food = food
	s.replenishFood()
}

// freeCell picks a uniformly random unoccupied cell inside the arena. It
// samples first and falls back to an exhaustive scan so that a nearly full
// arena still terminates.
func (s *Sim) freeCell() (Point, bool) {
	a := s.state.Arena
	occupied := make(map[Point]bool, len(s.state.Snakes)*8+len(s.state.Food))
	for i := range s.state.Snakes {
		if !s.state.Snakes[i].Alive {
			continue
		}
		for _, p := range s.state.Snakes[i].Body {
			occupied[p] = true
		}
	}
	for _, f := range s.state.Food {
		occupied[f] = true
	}
	for range 64 {
		p := Point{a.X0 + s.rng.IntN(a.Width()), a.Y0 + s.rng.IntN(a.Height())}
		if !occupied[p] {
			return p, true
		}
	}
	free := make([]Point, 0, a.Width()*a.Height())
	for y := a.Y0; y <= a.Y1; y++ {
		for x := a.X0; x <= a.X1; x++ {
			p := Point{x, y}
			if !occupied[p] {
				free = append(free, p)
			}
		}
	}
	if len(free) == 0 {
		return Point{}, false
	}
	return free[s.rng.IntN(len(free))], true
}

// checkOver ends the match once a winner is determined: the last snake
// standing in a multiplayer match, or death in a solo practice match.
func (s *Sim) checkOver() {
	if s.finished {
		return
	}
	alive := s.state.AliveCount()
	total := len(s.state.Snakes)
	if (total > 1 && alive > 1) || (total == 1 && alive > 0) {
		return
	}
	s.finished = true
	s.state.Over = true
	s.assignPlacements()
}

// assignPlacements ranks players: survivors first, then by elimination order
// (later deaths rank higher), breaking ties on longest body and finally seat
// order so the result is fully deterministic.
func (s *Sim) assignPlacements() {
	order := make([]int, len(s.state.Snakes))
	for i := range order {
		order[i] = i
	}
	deathIdx := make(map[PlayerID]int, len(s.deathOrder))
	for i, id := range s.deathOrder {
		deathIdx[id] = i
	}
	rank := func(i int) (bool, int, int, PlayerID) {
		sn := s.state.Snakes[i]
		return sn.Alive, deathIdx[sn.ID], sn.MaxLength, sn.ID
	}
	sort.SliceStable(order, func(a, b int) bool {
		aAlive, aDeath, aLen, aID := rank(order[a])
		bAlive, bDeath, bLen, bID := rank(order[b])
		if aAlive != bAlive {
			return aAlive
		}
		if !aAlive && aDeath != bDeath {
			return aDeath > bDeath // died later ⇒ ranks better
		}
		if aLen != bLen {
			return aLen > bLen
		}
		return aID < bID
	})
	for place, idx := range order {
		s.state.Snakes[idx].Placement = place + 1
	}
}
