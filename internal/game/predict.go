package game

// Predict advances one snake by a single move on a copy of an authoritative
// state, so a client has something responsive to draw in the gap between
// host broadcasts.
//
// It is deliberately naive: no collision resolution, no food, no other snakes.
// The host decides all of that, and its next state simply replaces whatever
// was predicted. On a tailnet the correction window is a few milliseconds, so
// a wrong guess is invisible; attempting rollback here would add real
// complexity to hide a problem that does not exist at this latency.
//
// A prediction that would leave a walled arena is declined rather than drawn:
// showing a snake through a wall for one frame looks like a bug, whereas
// showing it arrive a frame late does not.
func Predict(st State, cfg Config, id PlayerID, dir Direction) State {
	if st.Over {
		return st
	}
	sn := st.SnakeByID(id)
	if sn == nil || !sn.Alive || sn.Coasting || len(sn.Body) == 0 {
		return st
	}
	if dir > DirLeft {
		return st
	}
	// The same reversal rule the simulation applies, so prediction can never
	// show a turn the host will reject.
	if len(sn.Body) > 1 && dir == sn.Dir.Opposite() {
		dir = sn.Dir
	}

	out := st.Clone()
	moved := out.SnakeByID(id)
	d := dir.Delta()
	head := Point{moved.Body[0].X + d.X, moved.Body[0].Y + d.Y}
	if !out.Arena.Contains(head) {
		if !cfg.Wrap {
			// Turn in place: the heading is correct even though the step is not.
			moved.Dir = dir
			return out
		}
		head = wrapInto(head, out.Arena)
	}
	moved.Dir = dir
	moved.Body = append([]Point{head}, moved.Body...)
	moved.Body = moved.Body[:len(moved.Body)-1]
	return out
}

// wrapInto folds a point back inside r. It mirrors Sim.wrap, which cannot be
// reused here because that method reads the simulation's own arena.
func wrapInto(p Point, r Rect) Point {
	w, h := r.Width(), r.Height()
	x := (p.X-r.X0)%w + r.X0
	if x < r.X0 {
		x += w
	}
	y := (p.Y-r.Y0)%h + r.Y0
	if y < r.Y0 {
		y += h
	}
	return Point{x, y}
}
