package game

import (
	"reflect"
	"testing"
)

// testConfig returns a small, fully deterministic classic config. Tests that
// need snakes in exact positions build them with newTestSim rather than
// relying on the ring spawn layout.
func testConfig() Config {
	return Config{
		Name: "t", Width: 20, Height: 12,
		TickRate: 10, TicksPerMove: 1, MaxPlayers: 4,
		Wrap: false, Mode: ModeClassic, FoodCount: 1, ShrinkEvery: 10, Seed: 42,
	}
}

// newTestSim builds a simulation with hand-placed snakes and no food, so a test
// can assert on movement and collisions without the pellet RNG interfering.
// It bypasses Config.Validate deliberately: these fixtures use out-of-range
// values such as FoodCount 0 that the host form would never produce, and the
// validator has its own table test.
func newTestSim(t *testing.T, cfg Config, snakes ...Snake) *Sim {
	t.Helper()
	s := &Sim{
		cfg:   cfg,
		rng:   newTestRNG(cfg.Seed),
		state: State{Arena: Rect{0, 0, cfg.Width - 1, cfg.Height - 1}},
	}
	for _, sn := range snakes {
		sn.Alive = true
		sn.DiedAtTick = -1
		if sn.MaxLength == 0 {
			sn.MaxLength = len(sn.Body)
		}
		s.state.Snakes = append(s.state.Snakes, sn)
	}
	return s
}

func snakeAt(id PlayerID, dir Direction, cells ...Point) Snake {
	return Snake{ID: id, Dir: dir, Body: cells}
}

func TestMovementAdvancesHeadAndDropsTail(t *testing.T) {
	s := newTestSim(t, testConfig(),
		snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}, Point{3, 5}))
	s.cfg.FoodCount = 0

	st := s.Step()
	got := st.Snakes[0].Body
	want := []Point{{6, 5}, {5, 5}, {4, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body after move = %v, want %v", got, want)
	}
	if st.Tick != 1 {
		t.Errorf("tick = %d, want 1", st.Tick)
	}
}

func TestTicksPerMoveGatesMovement(t *testing.T) {
	cfg := testConfig()
	cfg.TicksPerMove = 3
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))

	for tick := 1; tick <= 2; tick++ {
		st := s.Step()
		if h := st.Snakes[0].Head(); h != (Point{5, 5}) {
			t.Fatalf("tick %d: head = %v, want stationary at {5 5}", tick, h)
		}
	}
	if h := s.Step().Snakes[0].Head(); h != (Point{6, 5}) {
		t.Fatalf("tick 3: head = %v, want {6 5}", h)
	}
}

func TestReversalIntoNeckIsRejected(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}, Point{3, 5}))

	s.SetDirection(0, DirLeft) // straight back into its own neck
	st := s.Step()
	if h := st.Snakes[0].Head(); h != (Point{6, 5}) {
		t.Fatalf("head = %v, want {6 5}: reversal should have been ignored", h)
	}
	if !st.Snakes[0].Alive {
		t.Error("snake died from a rejected reversal")
	}
}

func TestPerpendicularTurnIsAccepted(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))

	s.SetDirection(0, DirUp)
	if h := s.Step().Snakes[0].Head(); h != (Point{5, 4}) {
		t.Fatalf("head = %v, want {5 4}", h)
	}
}

func TestWallCollisionKillsWhenWrapDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = false
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirLeft, Point{0, 5}, Point{1, 5}))

	st := s.Step()
	if st.Snakes[0].Alive {
		t.Fatal("snake survived running into the west wall")
	}
	if !st.Over {
		t.Error("solo match should end when the only snake dies")
	}
}

func TestWrapAroundCarriesHeadToOppositeEdge(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirLeft, Point{0, 5}, Point{1, 5}))

	st := s.Step()
	if !st.Snakes[0].Alive {
		t.Fatal("snake died despite wrap being enabled")
	}
	if h := st.Snakes[0].Head(); h != (Point{cfg.Width - 1, 5}) {
		t.Fatalf("head = %v, want {%d 5}", h, cfg.Width-1)
	}
}

func TestWrapAroundOnAllFourEdges(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	cases := []struct {
		name  string
		dir   Direction
		start Point
		want  Point
	}{
		{"west", DirLeft, Point{0, 5}, Point{19, 5}},
		{"east", DirRight, Point{19, 5}, Point{0, 5}},
		{"north", DirUp, Point{5, 0}, Point{5, 11}},
		{"south", DirDown, Point{5, 11}, Point{5, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			behind := tc.dir.Opposite().Delta()
			tail := Point{tc.start.X + behind.X, tc.start.Y + behind.Y}
			s := newTestSim(t, cfg, snakeAt(0, tc.dir, tc.start, tail))
			if h := s.Step().Snakes[0].Head(); h != tc.want {
				t.Fatalf("head = %v, want %v", h, tc.want)
			}
		})
	}
}

func TestSelfCollisionKills(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	// A tight coil: heading up from {5,5} runs into its own body at {5,4}.
	s := newTestSim(t, cfg, snakeAt(0, DirUp,
		Point{5, 5}, Point{6, 5}, Point{6, 4}, Point{5, 4}, Point{4, 4}))

	if st := s.Step(); st.Snakes[0].Alive {
		t.Fatal("snake survived running into its own body")
	}
}

func TestChasingOwnTailIsLegal(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	// The tail cell {5,4} vacates on this move, so entering it must be safe.
	s := newTestSim(t, cfg, snakeAt(0, DirUp,
		Point{5, 5}, Point{6, 5}, Point{6, 4}, Point{5, 4}))

	st := s.Step()
	if !st.Snakes[0].Alive {
		t.Fatal("snake died chasing its own vacating tail")
	}
	if h := st.Snakes[0].Head(); h != (Point{5, 4}) {
		t.Fatalf("head = %v, want {5 4}", h)
	}
}

func TestCollidingWithAnotherBodyCreditsAKill(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	// Seat 0 drives east into the middle of seat 1's vertical body.
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{4, 5}, Point{3, 5}),
		snakeAt(1, DirUp, Point{5, 3}, Point{5, 4}, Point{5, 5}, Point{5, 6}))

	st := s.Step()
	victim := st.SnakeByID(0)
	killer := st.SnakeByID(1)
	if victim.Alive {
		t.Fatal("seat 0 survived driving into seat 1's body")
	}
	if killer.Kills != 1 {
		t.Errorf("seat 1 kills = %d, want 1", killer.Kills)
	}
	if !st.Over {
		t.Error("match should end once one of two snakes is eliminated")
	}
}

func TestHeadOnCollisionKillsBothAndCreditsNobody(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	// Heads two cells apart, closing: both claim {5,5}.
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{4, 5}, Point{3, 5}),
		snakeAt(1, DirLeft, Point{6, 5}, Point{7, 5}))

	st := s.Step()
	for _, sn := range st.Snakes {
		if sn.Alive {
			t.Errorf("seat %d survived a head-on collision", sn.ID)
		}
		if sn.Kills != 0 {
			t.Errorf("seat %d credited %d kills for a head-on", sn.ID, sn.Kills)
		}
	}
}

func TestSimultaneousDeathsShareNoOrderingAdvantage(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = false
	cfg.FoodCount = 0
	// Both snakes hit opposite walls on the same tick.
	s := newTestSim(t, cfg,
		snakeAt(0, DirLeft, Point{0, 3}, Point{1, 3}),
		snakeAt(1, DirRight, Point{19, 8}, Point{18, 8}))

	st := s.Step()
	if st.AliveCount() != 0 {
		t.Fatalf("alive = %d, want 0", st.AliveCount())
	}
	if !st.Over {
		t.Fatal("match should be over")
	}
}

func TestEatingFoodGrowsTheSnake(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))
	s.state.Food = []Point{{6, 5}}

	st := s.Step()
	sn := st.Snakes[0]
	if sn.Score != 1 {
		t.Errorf("score = %d, want 1", sn.Score)
	}
	if len(st.Food) != 0 {
		t.Errorf("food remained on the board: %v", st.Food)
	}
	// Growth is credited over the following moves; the body must end longer.
	for range 3 {
		st = s.Step()
	}
	if got := len(st.Snakes[0].Body); got != 4 {
		t.Errorf("length after eating = %d, want 4", got)
	}
	if !hasEvent(st.Events, EventEat) && st.Tick == 1 {
		t.Error("expected an eat event on the tick food was consumed")
	}
}

func TestFoodIsReplenishedToConfiguredCount(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 3
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))

	st := s.Step()
	if len(st.Food) != 3 {
		t.Fatalf("food count = %d, want 3", len(st.Food))
	}
	for _, f := range st.Food {
		if !st.Arena.Contains(f) {
			t.Errorf("food %v spawned outside the arena", f)
		}
	}
}

func TestLastSnakeStandingWinsAndPlacementsAreRanked(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = false
	cfg.FoodCount = 0
	s := newTestSim(t, cfg,
		snakeAt(0, DirLeft, Point{1, 3}, Point{2, 3}),  // hits the wall on tick 2
		snakeAt(1, DirLeft, Point{0, 6}, Point{1, 6}),  // hits the wall on tick 1
		snakeAt(2, DirRight, Point{5, 9}, Point{4, 9})) // survives

	var st State
	for range 3 {
		st = s.Step()
		if st.Over {
			break
		}
	}
	if !st.Over {
		t.Fatal("match did not end")
	}
	if p := st.SnakeByID(2).Placement; p != 1 {
		t.Errorf("survivor placement = %d, want 1", p)
	}
	// Seat 0 outlived seat 1, so it must rank ahead of it.
	if p0, p1 := st.SnakeByID(0).Placement, st.SnakeByID(1).Placement; p0 >= p1 {
		t.Errorf("placements: seat0=%d seat1=%d, want seat0 < seat1", p0, p1)
	}
	seen := map[int]bool{}
	for _, sn := range st.Snakes {
		if sn.Placement < 1 || sn.Placement > len(st.Snakes) {
			t.Errorf("seat %d placement %d out of range", sn.ID, sn.Placement)
		}
		if seen[sn.Placement] {
			t.Errorf("placement %d assigned twice", sn.Placement)
		}
		seen[sn.Placement] = true
	}
}

func TestSoloMatchEndsOnlyWhenTheSnakeDies(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))

	for range 10 {
		if s.Step().Over {
			t.Fatal("solo match ended while the snake was alive")
		}
	}
}

func TestShrinkingArenaContractsAndCulls(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = ModeShrink
	cfg.ShrinkEvery = 1
	cfg.Wrap = false
	cfg.FoodCount = 0
	// Seat 1 sits in the corner that the first shrink step swallows.
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{9, 5}, Point{8, 5}),
		snakeAt(1, DirDown, Point{0, 0}, Point{0, 1}))
	// Seat 1 heads south so it is still at x=0 when the west wall closes in.
	s.state.Snakes[1].Body = []Point{{0, 1}, {0, 0}}

	st := s.Step()
	want := Rect{1, 1, 18, 10}
	if st.Arena != want {
		t.Fatalf("arena = %+v, want %+v", st.Arena, want)
	}
	if st.SnakeByID(1).Alive {
		t.Error("snake left outside the contracted arena survived")
	}
	if !hasEvent(st.Events, EventShrink) {
		t.Error("expected a shrink event")
	}
}

func TestShrinkStopsAtMinimumSpan(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = ModeShrink
	cfg.ShrinkEvery = 1
	cfg.Wrap = true
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{9, 5}, Point{8, 5}))

	for range 30 {
		s.Step()
	}
	a := s.state.Arena
	if a.Width() < MinArenaSpan || a.Height() < MinArenaSpan {
		t.Fatalf("arena %+v shrank below the %d-cell minimum", a, MinArenaSpan)
	}
}

func TestCoastingSnakeIgnoresInputButKeepsMoving(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))

	s.SetCoasting(0, true)
	s.SetDirection(0, DirUp)
	st := s.Step()
	if h := st.Snakes[0].Head(); h != (Point{6, 5}) {
		t.Fatalf("head = %v, want {6 5}: a coasting snake must travel straight", h)
	}
	if !st.Snakes[0].Coasting {
		t.Error("coasting flag was not reported in the state")
	}
}

func TestEliminateEndsTheMatch(t *testing.T) {
	cfg := testConfig()
	cfg.FoodCount = 0
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{2, 2}, Point{1, 2}),
		snakeAt(1, DirRight, Point{2, 8}, Point{1, 8}))

	s.Eliminate(0)
	if s.state.SnakeByID(0).Alive {
		t.Fatal("eliminated snake is still alive")
	}
	if !s.Over() {
		t.Fatal("match should end when only one snake remains")
	}
	st := s.State()
	if p := st.SnakeByID(1).Placement; p != 1 {
		t.Errorf("survivor placement = %d, want 1", p)
	}
}

func TestNewSpawnsDoNotOverlap(t *testing.T) {
	for seats := MinPlayers; seats <= MaxPlayers; seats++ {
		cfg := DefaultConfig()
		cfg.MaxPlayers = MaxPlayers
		cfg.Seed = int64(seats)
		ids := make([]PlayerID, seats)
		for i := range ids {
			ids[i] = PlayerID(i)
		}
		s, err := New(cfg, ids)
		if err != nil {
			t.Fatalf("%d seats: %v", seats, err)
		}
		occupied := map[Point]PlayerID{}
		st := s.State()
		for _, sn := range st.Snakes {
			if len(sn.Body) != StartLength {
				t.Errorf("%d seats: seat %d spawned with %d segments, want %d", seats, sn.ID, len(sn.Body), StartLength)
			}
			for _, p := range sn.Body {
				if !st.Arena.Contains(p) {
					t.Errorf("%d seats: seat %d segment %v outside arena", seats, sn.ID, p)
				}
				if other, dup := occupied[p]; dup {
					t.Errorf("%d seats: seats %d and %d both occupy %v", seats, other, sn.ID, p)
				}
				occupied[p] = sn.ID
			}
		}
	}
}

func TestSimulationIsDeterministicForAGivenSeed(t *testing.T) {
	run := func() State {
		cfg := DefaultConfig()
		cfg.Seed = 1234
		s, err := New(cfg, []PlayerID{0, 1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		var st State
		for i := range 200 {
			// A fixed, arbitrary input schedule exercised identically in both runs.
			if i%7 == 0 {
				s.SetDirection(PlayerID(i%4), Direction(i%4))
			}
			st = s.Step()
			if st.Over {
				break
			}
		}
		return st
	}
	a, b := run(), run()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two runs with the same seed and inputs diverged")
	}
}

func TestDifferentSeedsDiverge(t *testing.T) {
	food := func(seed int64) []Point {
		cfg := DefaultConfig()
		cfg.Seed = seed
		s, err := New(cfg, []PlayerID{0, 1})
		if err != nil {
			t.Fatal(err)
		}
		return s.State().Food
	}
	if reflect.DeepEqual(food(1), food(999)) {
		t.Error("food layout is identical across different seeds")
	}
}

func TestStateCloneIsDeep(t *testing.T) {
	cfg := testConfig()
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}))
	st := s.State()
	st.Snakes[0].Body[0] = Point{99, 99}
	if h := s.State().Snakes[0].Head(); h != (Point{5, 5}) {
		t.Fatalf("mutating a clone changed the simulation: head = %v", h)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"default", func(*Config) {}, false},
		{"narrow", func(c *Config) { c.Width = MinWidth - 1 }, true},
		{"wide", func(c *Config) { c.Width = MaxWidth + 1 }, true},
		{"short", func(c *Config) { c.Height = MinHeight - 1 }, true},
		{"too many seats", func(c *Config) { c.MaxPlayers = MaxPlayers + 1 }, true},
		{"too few seats", func(c *Config) { c.MaxPlayers = 1 }, true},
		{"bad mode", func(c *Config) { c.Mode = "battleship" }, true},
		{"no food", func(c *Config) { c.FoodCount = 0 }, true},
		{"zero tick rate", func(c *Config) { c.TickRate = 0 }, true},
		{"zero move interval", func(c *Config) { c.TicksPerMove = 0 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewRejectsEmptySeatList(t *testing.T) {
	if _, err := New(DefaultConfig(), nil); err != ErrNoPlayers {
		t.Fatalf("err = %v, want ErrNoPlayers", err)
	}
}

func TestDirectionOpposite(t *testing.T) {
	for _, d := range []Direction{DirUp, DirRight, DirDown, DirLeft} {
		if got := d.Opposite().Opposite(); got != d {
			t.Errorf("%v.Opposite().Opposite() = %v", d, got)
		}
		v, o := d.Delta(), d.Opposite().Delta()
		if v.X+o.X != 0 || v.Y+o.Y != 0 {
			t.Errorf("%v and its opposite are not inverse deltas", d)
		}
	}
}

func hasEvent(events []Event, kind EventKind) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
