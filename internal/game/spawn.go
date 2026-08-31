package game

import "math"

// spawnSnake places seat i of n evenly around a circle inscribed in the arena,
// heading tangentially (counter-clockwise). Tangential headings keep snakes
// from driving straight into each other in the opening seconds, which is what
// facing-inward spawns do at two and four seats.
func (s *Sim) spawnSnake(id PlayerID, i, n int) Snake {
	a := s.state.Arena
	cx := float64(a.X0+a.X1) / 2
	cy := float64(a.Y0+a.Y1) / 2
	// Far enough out that the countdown, which is drawn over the middle of the
	// arena, does not cover anybody while they are looking for their snake.
	radius := math.Min(float64(a.Width()), float64(a.Height())) * 0.38

	theta := 2 * math.Pi * float64(i) / float64(n)
	head := Point{
		X: clamp(int(math.Round(cx+radius*math.Cos(theta))), a.X0+1, a.X1-1),
		Y: clamp(int(math.Round(cy+radius*math.Sin(theta))), a.Y0+1, a.Y1-1),
	}
	dir := nearestCardinal(-math.Sin(theta), math.Cos(theta))

	// The body trails directly behind the head. If the arena edge is in the
	// way, flip to the opposite heading rather than spilling out of bounds.
	back := dir.Opposite().Delta()
	if !a.Contains(Point{head.X + back.X*(StartLength-1), head.Y + back.Y*(StartLength-1)}) {
		dir = dir.Opposite()
		back = dir.Opposite().Delta()
	}
	sn := Snake{
		ID:         id,
		Dir:        dir,
		Alive:      true,
		DiedAtTick: -1,
		MaxLength:  StartLength,
	}
	for j := range StartLength {
		p := Point{head.X + back.X*j, head.Y + back.Y*j}
		if !a.Contains(p) {
			p = s.wrap(p)
		}
		sn.Body = append(sn.Body, p)
	}
	// Nudge off any seat that landed on an already-placed snake. The scan is
	// deterministic so every peer computes the same layout.
	if s.overlapsExisting(sn.Body) {
		if free, ok := s.findClearRun(dir); ok {
			sn.Body = free
		}
	}
	return sn
}

// overlapsExisting reports whether any cell of body is already claimed.
func (s *Sim) overlapsExisting(body []Point) bool {
	for i := range s.state.Snakes {
		for _, p := range s.state.Snakes[i].Body {
			for _, q := range body {
				if p == q {
					return true
				}
			}
		}
	}
	return false
}

// findClearRun scans the arena in reading order for the first straight run of
// StartLength free cells trailing the given heading.
func (s *Sim) findClearRun(dir Direction) ([]Point, bool) {
	a := s.state.Arena
	back := dir.Opposite().Delta()
	for y := a.Y0 + 1; y <= a.Y1-1; y++ {
		for x := a.X0 + 1; x <= a.X1-1; x++ {
			run := make([]Point, 0, StartLength)
			okRun := true
			for j := range StartLength {
				p := Point{x + back.X*j, y + back.Y*j}
				if !a.Contains(p) {
					okRun = false
					break
				}
				run = append(run, p)
			}
			if okRun && !s.overlapsExisting(run) {
				return run, true
			}
		}
	}
	return nil, false
}

// nearestCardinal snaps a float vector to the closest cardinal direction. Note
// that y grows downward in grid space, which matches the sign convention used
// by Direction.Delta.
func nearestCardinal(dx, dy float64) Direction {
	best, bestDot := DirUp, math.Inf(-1)
	for _, d := range []Direction{DirUp, DirRight, DirDown, DirLeft} {
		v := d.Delta()
		if dot := dx*float64(v.X) + dy*float64(v.Y); dot > bestDot {
			best, bestDot = d, dot
		}
	}
	return best
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
