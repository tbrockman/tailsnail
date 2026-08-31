package game

import "sort"

// ChooseDirection picks a heading for a bot-controlled snake.
//
// The policy is deliberately simple and readable rather than strong: prefer a
// move that is not immediately fatal, then head towards the nearest food, then
// towards open space. Bots exist so the game can be played and tested solo,
// not to be a challenge, and a simple policy is one that can be reasoned about
// when a match looks wrong.
//
// It is a pure function of the state, so it is deterministic and testable, and
// the host can call it inside its tick without any extra machinery.
func ChooseDirection(st State, cfg Config, id PlayerID) Direction {
	sn := st.SnakeByID(id)
	if sn == nil || !sn.Alive || len(sn.Body) == 0 {
		return DirUp
	}
	head := sn.Head()

	// Cells that would end the match for this snake. A tail cell that is about
	// to vacate is not an obstacle, which mirrors the simulation's own rule.
	blocked := make(map[Point]bool, len(st.Snakes)*8)
	for i := range st.Snakes {
		other := &st.Snakes[i]
		if !other.Alive {
			continue
		}
		last := len(other.Body) - 1
		for j, p := range other.Body {
			if j == last && other.Grow == 0 {
				continue
			}
			blocked[p] = true
		}
	}
	// Treat the cell in front of another snake's head as risky: arriving there
	// at the same moment is a mutual kill, which is the one death a greedy bot
	// would otherwise walk into constantly.
	contested := make(map[Point]bool, len(st.Snakes))
	for i := range st.Snakes {
		other := &st.Snakes[i]
		if !other.Alive || other.ID == id || len(other.Body) == 0 {
			continue
		}
		for _, d := range []Direction{DirUp, DirRight, DirDown, DirLeft} {
			v := d.Delta()
			contested[normalise(Point{other.Head().X + v.X, other.Head().Y + v.Y}, st.Arena, cfg.Wrap)] = true
		}
	}

	type option struct {
		dir      Direction
		safe     bool
		risky    bool
		distance int
		room     int
	}
	var options []option
	for _, d := range []Direction{DirUp, DirRight, DirDown, DirLeft} {
		if len(sn.Body) > 1 && d == sn.Dir.Opposite() {
			continue // the simulation would reject it anyway
		}
		v := d.Delta()
		next := Point{head.X + v.X, head.Y + v.Y}
		if !st.Arena.Contains(next) {
			if !cfg.Wrap {
				continue // straight into a wall
			}
			next = normalise(next, st.Arena, true)
		}
		if blocked[next] {
			continue
		}
		options = append(options, option{
			dir:      d,
			safe:     true,
			risky:    contested[next],
			distance: nearestFood(next, st, cfg),
			room:     openRoom(next, st.Arena, blocked, cfg.Wrap),
		})
	}
	if len(options) == 0 {
		return sn.Dir // cornered; carry on and take it
	}

	// Rank: avoid head-on risk first, then chase food, then keep options open.
	// Ties break on direction order so the choice is fully deterministic.
	sort.SliceStable(options, func(i, j int) bool {
		a, b := options[i], options[j]
		if a.risky != b.risky {
			return !a.risky
		}
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		if a.room != b.room {
			return a.room > b.room
		}
		return a.dir < b.dir
	})
	return options[0].dir
}

// normalise folds a point inside the arena when wrap-around is on.
func normalise(p Point, r Rect, wrap bool) Point {
	if r.Contains(p) || !wrap {
		return p
	}
	return wrapInto(p, r)
}

// nearestFood returns the grid distance from p to the closest pellet, or a
// large value when the board has none.
func nearestFood(p Point, st State, cfg Config) int {
	best := 1 << 30
	for _, f := range st.Food {
		if d := distance(p, f, st.Arena, cfg.Wrap); d < best {
			best = d
		}
	}
	return best
}

// distance is the Manhattan distance, taking the shorter way around when
// wrap-around is enabled.
func distance(a, b Point, r Rect, wrap bool) int {
	dx := abs(a.X - b.X)
	dy := abs(a.Y - b.Y)
	if wrap {
		dx = min(dx, r.Width()-dx)
		dy = min(dy, r.Height()-dy)
	}
	return dx + dy
}

// openRoom counts the free cells reachable from p within a short flood fill.
// It is a cheap proxy for "does this move lead somewhere", enough to stop a
// bot from sealing itself into a pocket while chasing a pellet.
func openRoom(p Point, arena Rect, blocked map[Point]bool, wrap bool) int {
	const budget = 24
	seen := map[Point]bool{p: true}
	queue := []Point{p}
	count := 0
	for len(queue) > 0 && count < budget {
		cur := queue[0]
		queue = queue[1:]
		count++
		for _, d := range []Direction{DirUp, DirRight, DirDown, DirLeft} {
			v := d.Delta()
			next := Point{cur.X + v.X, cur.Y + v.Y}
			if !arena.Contains(next) {
				if !wrap {
					continue
				}
				next = wrapInto(next, arena)
			}
			if seen[next] || blocked[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}
	return count
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
