package game

import "testing"

func botConfig() Config {
	cfg := testConfig()
	cfg.Wrap = false
	cfg.FoodCount = 0
	return cfg
}

func TestBotNeverSteersIntoAWall(t *testing.T) {
	cfg := botConfig()
	// Pinned against the west wall heading west.
	s := newTestSim(t, cfg, snakeAt(0, DirLeft, Point{0, 5}, Point{1, 5}))
	st := s.State()

	got := ChooseDirection(st, cfg, 0)
	if got == DirLeft {
		t.Fatal("the bot drove into the wall it was already against")
	}
	if got == DirRight {
		t.Fatal("the bot chose a reversal the simulation would reject")
	}
}

func TestBotNeverReverses(t *testing.T) {
	cfg := botConfig()
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}, Point{3, 5}))
	if got := ChooseDirection(s.State(), cfg, 0); got == DirLeft {
		t.Fatal("the bot reversed into its own neck")
	}
}

func TestBotAvoidsItsOwnBody(t *testing.T) {
	cfg := botConfig()
	// A coil: up and right are body, so it must go down or left.
	s := newTestSim(t, cfg, snakeAt(0, DirUp,
		Point{5, 5}, Point{6, 5}, Point{6, 4}, Point{5, 4}, Point{4, 4}, Point{4, 5}))
	st := s.State()

	got := ChooseDirection(st, cfg, 0)
	v := got.Delta()
	next := Point{st.Snakes[0].Head().X + v.X, st.Snakes[0].Head().Y + v.Y}
	for _, p := range st.Snakes[0].Body[:len(st.Snakes[0].Body)-1] {
		if p == next {
			t.Fatalf("the bot moved onto its own body at %v", next)
		}
	}
}

func TestBotHeadsTowardsFood(t *testing.T) {
	cfg := botConfig()
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))
	s.state.Food = []Point{{5, 1}} // directly north

	if got := ChooseDirection(s.State(), cfg, 0); got != DirUp {
		t.Fatalf("direction = %v, want up towards the pellet", got)
	}
}

func TestBotPrefersTheNearerPellet(t *testing.T) {
	cfg := botConfig()
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))
	s.state.Food = []Point{{5, 9}, {5, 4}} // south is far, north is adjacent

	if got := ChooseDirection(s.State(), cfg, 0); got != DirUp {
		t.Fatalf("direction = %v, want up towards the nearer pellet", got)
	}
}

func TestBotAvoidsTheCellInFrontOfAnotherHead(t *testing.T) {
	cfg := botConfig()
	// Food sits north, but seat 1's head is one cell north of it, so moving
	// north risks a mutual kill. The bot should take a different route.
	s := newTestSim(t, cfg,
		snakeAt(0, DirUp, Point{5, 5}, Point{5, 6}),
		snakeAt(1, DirDown, Point{5, 3}, Point{5, 2}))
	s.state.Food = []Point{{5, 0}}

	if got := ChooseDirection(s.State(), cfg, 0); got == DirUp {
		t.Fatal("the bot drove into a head-on collision to reach food")
	}
}

func TestBotKeepsMovingWhenCornered(t *testing.T) {
	cfg := botConfig()
	// Boxed in on every side by its own body and the corner.
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{1, 0}, Point{0, 0}, Point{0, 1}, Point{1, 1}))
	// Whatever it returns must be a legal direction rather than a panic.
	got := ChooseDirection(s.State(), cfg, 0)
	if got > DirLeft {
		t.Fatalf("direction = %v, not a cardinal direction", got)
	}
}

func TestBotIsDeterministic(t *testing.T) {
	cfg := botConfig()
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}),
		snakeAt(1, DirLeft, Point{12, 8}, Point{13, 8}))
	s.state.Food = []Point{{7, 3}, {2, 9}}
	st := s.State()

	first := ChooseDirection(st, cfg, 0)
	for range 50 {
		if got := ChooseDirection(st, cfg, 0); got != first {
			t.Fatal("the bot returned different directions for identical state")
		}
	}
}

func TestBotIgnoresDeadAndMissingSeats(t *testing.T) {
	cfg := botConfig()
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))
	st := s.State()
	st.Snakes[0].Alive = false

	// These must return a usable direction rather than panicking.
	if got := ChooseDirection(st, cfg, 0); got > DirLeft {
		t.Errorf("direction for a dead snake = %v", got)
	}
	if got := ChooseDirection(st, cfg, 9); got > DirLeft {
		t.Errorf("direction for an absent seat = %v", got)
	}
}

func TestBotSurvivesAWholeMatch(t *testing.T) {
	// The real test of the policy: a board of bots should play for a while
	// rather than all colliding in the opening seconds.
	cfg := DefaultConfig()
	cfg.Seed = 7
	seats := []PlayerID{0, 1, 2, 3}
	s, err := New(cfg, seats)
	if err != nil {
		t.Fatal(err)
	}
	ticks := 0
	for range 4000 {
		st := s.State()
		if st.Over {
			break
		}
		for _, id := range seats {
			if sn := st.SnakeByID(id); sn != nil && sn.Alive {
				s.SetDirection(id, ChooseDirection(st, cfg, id))
			}
		}
		s.Step()
		ticks++
	}
	final := s.State()
	if ticks < 100 {
		t.Fatalf("a board of bots ended after %d ticks; the policy is too weak to be useful", ticks)
	}
	// Someone should have eaten something over that many moves.
	fed := false
	for _, sn := range final.Snakes {
		if sn.Score > 0 {
			fed = true
		}
	}
	if !fed {
		t.Error("no bot ate a single pellet across the whole match")
	}
}

func TestBotWrapsAroundWhenTheConfigDoes(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	// Against the east edge with food just past the wrap point.
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{19, 5}, Point{18, 5}))
	s.state.Food = []Point{{0, 5}}

	if got := ChooseDirection(s.State(), cfg, 0); got != DirRight {
		t.Fatalf("direction = %v, want right: wrapping is the short way to the pellet", got)
	}
}

func TestDistanceTakesTheShortWayAround(t *testing.T) {
	r := Rect{0, 0, 19, 11}
	if got := distance(Point{1, 0}, Point{18, 0}, r, false); got != 17 {
		t.Errorf("walled distance = %d, want 17", got)
	}
	if got := distance(Point{1, 0}, Point{18, 0}, r, true); got != 3 {
		t.Errorf("wrapped distance = %d, want 3", got)
	}
}
