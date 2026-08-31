// Package netplay carries a match across the tailnet: the listener that
// accepts inbound connections, the host-authoritative lobby and game loop, and
// the client half that talks to a host.
//
// The consensus model is host-authoritative with no lockstep. The host runs
// the only simulation; clients send inputs and render whatever the host last
// broadcast. Correctness therefore never depends on clients agreeing, which
// keeps the failure modes small: a slow client falls behind and catches up, a
// dead client's snake coasts and is eliminated, and a dead host ends the match
// for everyone. Host migration is explicitly not attempted.
package netplay

import (
	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/proto"
)

// Event is something the UI needs to react to. Both the host and the client
// emit the same set, so the game screen has exactly one code path regardless
// of which side of the connection this peer is on.
type Event interface{ isNetplayEvent() }

// LobbyUpdate carries a new roster.
type LobbyUpdate struct{ State proto.LobbyState }

// GameStarted announces the first tick of a match.
type GameStarted struct {
	MatchID string
	Config  game.Config
	Seat    game.PlayerID
	Players []proto.Player
}

// Tick carries one authoritative simulation snapshot.
type Tick struct{ State game.State }

// MatchOver ends a match with its final, ranked state.
type MatchOver struct {
	State   game.State
	Players []proto.Player
	Reason  string
}

// Attested delivers the signed record for a finished match.
type Attested struct{ Record proto.AttestedRecord }

// SessionClosed ends the session, whether cleanly or because something broke.
type SessionClosed struct {
	Reason string
	Err    error
}

// Notice is a transient line for the event feed.
type Notice struct{ Text string }

func (LobbyUpdate) isNetplayEvent()   {}
func (GameStarted) isNetplayEvent()   {}
func (Tick) isNetplayEvent()          {}
func (MatchOver) isNetplayEvent()     {}
func (Attested) isNetplayEvent()      {}
func (SessionClosed) isNetplayEvent() {}
func (Notice) isNetplayEvent()        {}

// Session is the UI-facing handle on a lobby, implemented by both Host and
// Client. Every method is safe to call from the UI goroutine and returns
// immediately; results arrive on Events.
type Session interface {
	// Events is the stream of updates for this session.
	Events() <-chan Event
	// Seat is this peer's own seat in the lobby.
	Seat() game.PlayerID
	// IsHost reports whether this peer runs the simulation.
	IsHost() bool
	// LobbyID identifies the lobby.
	LobbyID() string
	// SetReady toggles this peer's ready flag.
	SetReady(ready bool)
	// Input queues a heading change for the next tick.
	Input(dir game.Direction)
	// Kick removes a seat. It is ignored on a client.
	Kick(seat game.PlayerID)
	// Reconfigure changes an open lobby's settings. Only the host can do this;
	// it is ignored on a client.
	Reconfigure(name string, cfg game.Config)
	// Close leaves the lobby, telling the peer why.
	Close(reason string)
}
