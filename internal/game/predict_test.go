package game

import (
	"reflect"
	"testing"
)

func predictFixture(t *testing.T, wrap bool) (State, Config) {
	t.Helper()
	cfg := testConfig()
	cfg.Wrap = wrap
	cfg.FoodCount = 0
	s := newTestSim(t, cfg,
		snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}, Point{3, 5}),
		snakeAt(1, DirLeft, Point{12, 8}, Point{13, 8}))
	return s.State(), cfg
}

func TestPredictAdvancesOnlyTheNamedSnake(t *testing.T) {
	st, cfg := predictFixture(t, true)
	got := Predict(st, cfg, 0, DirRight)

	if h := got.SnakeByID(0).Head(); h != (Point{6, 5}) {
		t.Errorf("predicted head = %v, want {6 5}", h)
	}
	if h := got.SnakeByID(1).Head(); h != (Point{12, 8}) {
		t.Errorf("the other snake moved to %v; prediction must not touch it", h)
	}
	if l := len(got.SnakeByID(0).Body); l != 3 {
		t.Errorf("predicted length = %d, want 3", l)
	}
}

func TestPredictAppliesTheTurn(t *testing.T) {
	st, cfg := predictFixture(t, true)
	got := Predict(st, cfg, 0, DirUp)

	sn := got.SnakeByID(0)
	if sn.Head() != (Point{5, 4}) {
		t.Errorf("head = %v, want {5 4}", sn.Head())
	}
	if sn.Dir != DirUp {
		t.Errorf("direction = %v, want up", sn.Dir)
	}
}

func TestPredictRefusesAReversal(t *testing.T) {
	st, cfg := predictFixture(t, true)
	got := Predict(st, cfg, 0, DirLeft)

	// The host would reject this input, so prediction must not show it.
	if h := got.SnakeByID(0).Head(); h != (Point{6, 5}) {
		t.Errorf("head = %v, want the snake to carry straight on to {6 5}", h)
	}
}

func TestPredictWrapsWhenTheConfigDoes(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{19, 5}, Point{18, 5}))

	got := Predict(s.State(), cfg, 0, DirRight)
	if h := got.SnakeByID(0).Head(); h != (Point{0, 5}) {
		t.Errorf("head = %v, want the wrapped {0 5}", h)
	}
}

func TestPredictDeclinesToLeaveAWalledArena(t *testing.T) {
	cfg := testConfig()
	cfg.Wrap = false
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{19, 5}, Point{18, 5}))
	st := s.State()

	got := Predict(st, cfg, 0, DirRight)
	if h := got.SnakeByID(0).Head(); h != (Point{19, 5}) {
		t.Errorf("head = %v: a prediction must not draw a snake through a wall", h)
	}
	if got.SnakeByID(0).Dir != DirRight {
		t.Error("the heading should still update even when the step is declined")
	}
}

func TestPredictLeavesDeadAndCoastingSnakesAlone(t *testing.T) {
	st, cfg := predictFixture(t, true)
	st.SnakeByID(0).Alive = false
	if got := Predict(st, cfg, 0, DirUp); !reflect.DeepEqual(got, st) {
		t.Error("prediction moved a dead snake")
	}

	st, cfg = predictFixture(t, true)
	st.SnakeByID(0).Coasting = true
	if got := Predict(st, cfg, 0, DirUp); !reflect.DeepEqual(got, st) {
		t.Error("prediction steered a coasting snake")
	}
}

func TestPredictIsANoOpAfterTheMatchEnds(t *testing.T) {
	st, cfg := predictFixture(t, true)
	st.Over = true
	if got := Predict(st, cfg, 0, DirUp); !reflect.DeepEqual(got, st) {
		t.Error("prediction ran on a finished match")
	}
}

func TestPredictIgnoresAnUnknownSeat(t *testing.T) {
	st, cfg := predictFixture(t, true)
	if got := Predict(st, cfg, 7, DirUp); !reflect.DeepEqual(got, st) {
		t.Error("prediction changed the state for a seat that is not playing")
	}
}

func TestPredictDoesNotMutateTheInput(t *testing.T) {
	st, cfg := predictFixture(t, true)
	before := st.Clone()
	Predict(st, cfg, 0, DirUp)
	if !reflect.DeepEqual(st, before) {
		t.Fatal("Predict mutated the authoritative state it was given")
	}
}

func TestPredictionAgreesWithTheSimulationForOneStep(t *testing.T) {
	// A prediction is only useful if it usually matches what the host does.
	// On an empty stretch of arena the two must agree exactly.
	cfg := testConfig()
	cfg.Wrap = true
	cfg.FoodCount = 0
	s := newTestSim(t, cfg, snakeAt(0, DirRight, Point{5, 5}, Point{4, 5}, Point{3, 5}))

	predicted := Predict(s.State(), cfg, 0, DirUp)
	s.SetDirection(0, DirUp)
	actual := s.Step()

	if got, want := predicted.SnakeByID(0).Body, actual.SnakeByID(0).Body; !reflect.DeepEqual(got, want) {
		t.Fatalf("predicted body %v, simulation produced %v", got, want)
	}
}
