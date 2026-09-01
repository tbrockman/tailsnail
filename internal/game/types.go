// Package game implements the pure, deterministic tailsnail simulation.
//
// The package performs no I/O and depends only on the standard library so that
// a host can run the authoritative simulation, a client can (optionally)
// forward-predict with the exact same code, and unit tests can drive whole
// matches without a network.
package game

import (
	"errors"
	"fmt"
)

// PlayerID is a seat index within a single match. Seats are assigned by the
// host at join time and are stable for the life of the match.
type PlayerID uint8

// Point is a cell in the arena grid. The origin is the top-left cell.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Direction is one of the four cardinal headings a snake may travel.
type Direction uint8

// The cardinal directions, ordered clockwise starting at north so that
// Opposite is a simple modular rotation.
const (
	DirUp Direction = iota
	DirRight
	DirDown
	DirLeft
)

// Delta returns the per-move offset for the direction.
func (d Direction) Delta() Point {
	switch d {
	case DirUp:
		return Point{0, -1}
	case DirRight:
		return Point{1, 0}
	case DirDown:
		return Point{0, 1}
	default:
		return Point{-1, 0}
	}
}

// Opposite returns the 180-degree reversal of d.
func (d Direction) Opposite() Direction { return (d + 2) % 4 }

// String implements fmt.Stringer.
func (d Direction) String() string {
	switch d {
	case DirUp:
		return "up"
	case DirRight:
		return "right"
	case DirDown:
		return "down"
	case DirLeft:
		return "left"
	}
	return "invalid"
}

// Rect is an inclusive rectangle of playable cells. It spans the whole grid
// for the life of a match.
type Rect struct {
	X0 int `json:"x0"`
	Y0 int `json:"y0"`
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
}

// Contains reports whether p lies inside r.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X0 && p.X <= r.X1 && p.Y >= r.Y0 && p.Y <= r.Y1
}

// Width returns the number of playable columns.
func (r Rect) Width() int { return r.X1 - r.X0 + 1 }

// Height returns the number of playable rows.
func (r Rect) Height() int { return r.Y1 - r.Y0 + 1 }

// Minimum and maximum arena dimensions and seat counts. These bound the host
// configuration form and are re-checked on the wire.
const (
	MinWidth   = 16
	MaxWidth   = 120
	MinHeight  = 10
	MaxHeight  = 48
	MinPlayers = 2
	MaxPlayers = 8

	// StartLength is the body length every snake spawns with.
	StartLength = 3
)

// Config is the full, wire-serialisable description of a match. Two peers with
// the same Config and the same input stream produce byte-identical states.
type Config struct {
	Name         string `json:"name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	TickRate     int    `json:"tick_rate"`      // simulation ticks per second
	TicksPerMove int    `json:"ticks_per_move"` // snakes advance one cell every N ticks
	MaxPlayers   int    `json:"max_players"`
	Wrap         bool   `json:"wrap"`
	FoodCount    int    `json:"food_count"`
	Seed         int64  `json:"seed"`

	// Bots is how many seats the host fills with computer players. The
	// simulation itself knows nothing about it — bots are ordinary snakes that
	// the host steers — but it belongs with the rest of the match
	// configuration, and it is recorded so a result reads honestly.
	//
	// It is omitempty so that a match without bots canonicalises exactly as it
	// did before the field existed, leaving already-stored record hashes valid.
	Bots int `json:"bots,omitempty"`
}

// DefaultConfig returns the configuration the host form starts from.
func DefaultConfig() Config {
	return Config{
		Name:         "tailsnail",
		Width:        40,
		Height:       20,
		TickRate:     20,
		TicksPerMove: 2,
		MaxPlayers:   4,
		Wrap:         true,
		FoodCount:    3,
	}
}

// Validate reports whether the configuration is playable.
func (c Config) Validate() error {
	switch {
	case c.Width < MinWidth || c.Width > MaxWidth:
		return fmt.Errorf("width %d out of range %d..%d", c.Width, MinWidth, MaxWidth)
	case c.Height < MinHeight || c.Height > MaxHeight:
		return fmt.Errorf("height %d out of range %d..%d", c.Height, MinHeight, MaxHeight)
	case c.TickRate < 5 || c.TickRate > 60:
		return fmt.Errorf("tick rate %d out of range 5..60", c.TickRate)
	case c.TicksPerMove < 1 || c.TicksPerMove > 10:
		return fmt.Errorf("ticks per move %d out of range 1..10", c.TicksPerMove)
	case c.MaxPlayers < MinPlayers || c.MaxPlayers > MaxPlayers:
		return fmt.Errorf("max players %d out of range %d..%d", c.MaxPlayers, MinPlayers, MaxPlayers)
	case c.FoodCount < 1 || c.FoodCount > 16:
		return fmt.Errorf("food count %d out of range 1..16", c.FoodCount)
	case c.Bots < 0 || c.Bots > c.MaxPlayers-1:
		return fmt.Errorf("bot count %d out of range 0..%d", c.Bots, c.MaxPlayers-1)
	}
	return nil
}

// ErrNoPlayers is returned by New when asked to simulate an empty match.
var ErrNoPlayers = errors.New("game: no players")

// Snake is one player's serpent plus its per-match statistics.
type Snake struct {
	ID   PlayerID  `json:"id"`
	Body []Point   `json:"body"` // head first
	Dir  Direction `json:"dir"`

	Alive     bool `json:"alive"`
	Coasting  bool `json:"coasting"` // owner disconnected; travels straight
	Grow      int  `json:"grow"`     // pending growth segments
	Score     int  `json:"score"`    // food eaten
	Kills     int  `json:"kills"`
	MaxLength int  `json:"max_length"`

	DiedAtTick int `json:"died_at_tick"` // -1 while alive
	Placement  int `json:"placement"`    // 0 until the match ends; 1 is best

	pending    Direction
	hasPending bool
}

// Head returns the snake's leading cell. It is only meaningful when the snake
// has a body, which is always true for a snake produced by this package.
func (s *Snake) Head() Point { return s.Body[0] }

// Len returns the current body length.
func (s *Snake) Len() int { return len(s.Body) }

// EventKind classifies a per-tick simulation event. Events exist purely so the
// renderer can play short animations; they carry no authority.
type EventKind string

const (
	// EventEat marks a snake consuming food.
	EventEat EventKind = "eat"
	// EventDeath marks an elimination.
	EventDeath EventKind = "death"
	// EventSpawn marks food appearing.
	EventSpawn EventKind = "spawn"
)

// Event is a notable thing that happened during a single Step.
type Event struct {
	Kind   EventKind `json:"kind"`
	Player PlayerID  `json:"player,omitempty"`
	At     Point     `json:"at"`
	Killer *PlayerID `json:"killer,omitempty"`
}

// State is the complete authoritative snapshot broadcast to clients each tick.
type State struct {
	Tick   int     `json:"tick"`
	Snakes []Snake `json:"snakes"` // sorted by ID
	Food   []Point `json:"food"`
	Arena  Rect    `json:"arena"`
	Over   bool    `json:"over"`
	Events []Event `json:"events,omitempty"`
}

// SnakeByID returns a pointer to the snake with the given id, or nil.
func (s *State) SnakeByID(id PlayerID) *Snake {
	for i := range s.Snakes {
		if s.Snakes[i].ID == id {
			return &s.Snakes[i]
		}
	}
	return nil
}

// Clone returns a deep copy of the state, safe to hand to another goroutine.
func (s State) Clone() State {
	out := s
	out.Snakes = make([]Snake, len(s.Snakes))
	for i, sn := range s.Snakes {
		sn.Body = append([]Point(nil), sn.Body...)
		out.Snakes[i] = sn
	}
	out.Food = append([]Point(nil), s.Food...)
	out.Events = append([]Event(nil), s.Events...)
	return out
}

// AliveCount returns the number of snakes still in play.
func (s *State) AliveCount() int {
	n := 0
	for i := range s.Snakes {
		if s.Snakes[i].Alive {
			n++
		}
	}
	return n
}
