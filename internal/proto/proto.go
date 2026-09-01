// Package proto defines the tailsnail wire protocol: the message envelope and
// bodies exchanged between peers, plus the match-result attestation format.
//
// All tailsnail traffic is JSON framed with a 4-byte big-endian length prefix
// and carried over TCP on the tailnet. A single well-known port serves every
// interaction — discovery probes, lobby control, in-game state, and gossip —
// with the flow determined by the first message a peer sends.
package proto

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tbrockman/tailsnail/internal/game"
)

// Version is the protocol version. Peers refuse handshakes from a different
// major version rather than guessing at compatibility.
const Version = 1

// Port is the well-known TCP port every tailsnail node listens on, bound only
// to the node's tailnet addresses. It sits clear of Tailscale's own WireGuard
// port (41641) while staying memorable alongside it.
const Port = 41649

// AppName identifies tailsnail in the handshake so that an unrelated service
// answering on the port is cleanly rejected rather than misparsed.
const AppName = "tailsnail"

// MaxFrame caps a single message at 4 MiB, comfortably above the largest tick
// state a maximum-size arena can produce.
const MaxFrame = 4 << 20

// Kind discriminates the body carried by an Envelope.
type Kind string

// The full message set. Discovery uses Hello/HelloOK; lobby control uses Join
// through Kick; play uses Start/Input/TickState/GameOver; attestation and
// gossip carry match records.
const (
	KindHello   Kind = "hello"
	KindHelloOK Kind = "hello_ok"

	KindJoinLobby  Kind = "join_lobby"
	KindJoinOK     Kind = "join_ok"
	KindLobbyState Kind = "lobby_state"
	KindReady      Kind = "ready"
	KindLeave      Kind = "leave"
	KindKick       Kind = "kick"

	KindStart     Kind = "start"
	KindInput     Kind = "input"
	KindTickState Kind = "tick_state"
	KindGameOver  Kind = "game_over"

	KindAttestRequest  Kind = "attest_request"
	KindAttestation    Kind = "attestation"
	KindAttestedRecord Kind = "attested_record"

	KindGossipInv     Kind = "gossip_inv"
	KindGossipResp    Kind = "gossip_resp"
	KindGossipRecords Kind = "gossip_records"

	KindError Kind = "error"
	KindPing  Kind = "ping"
	KindPong  Kind = "pong"
)

// Envelope wraps every message on the wire.
type Envelope struct {
	V    int             `json:"v"`
	Kind Kind            `json:"kind"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Decode unmarshals an envelope body into the requested type.
func Decode[T any](e Envelope) (T, error) {
	var out T
	if len(e.Body) == 0 {
		return out, fmt.Errorf("proto: %s message has an empty body", e.Kind)
	}
	if err := json.Unmarshal(e.Body, &out); err != nil {
		return out, fmt.Errorf("proto: decoding %s: %w", e.Kind, err)
	}
	return out, nil
}

// Hello opens every connection, identifying the dialer.
type Hello struct {
	App         string `json:"app"`
	Version     int    `json:"version"`
	AppVersion  string `json:"app_version"`
	PubKey      string `json:"pubkey"`
	DisplayName string `json:"display_name"`
	Hostname    string `json:"hostname"`
	// Intent tells the listener what the dialer wants, so a discovery probe
	// can be answered and closed without spinning up lobby machinery.
	Intent Intent `json:"intent"`
}

// Intent describes why a peer dialled.
type Intent string

const (
	// IntentProbe is a discovery probe: answer with an advert and hang up.
	IntentProbe Intent = "probe"
	// IntentPlay is a lobby join attempt; the connection becomes a session.
	IntentPlay Intent = "play"
	// IntentGossip is an anti-entropy exchange of match records.
	IntentGossip Intent = "gossip"
)

// HelloOK answers a Hello, describing the responder and any lobby it hosts.
type HelloOK struct {
	App         string  `json:"app"`
	Version     int     `json:"version"`
	AppVersion  string  `json:"app_version"`
	PubKey      string  `json:"pubkey"`
	DisplayName string  `json:"display_name"`
	Hostname    string  `json:"hostname"`
	Login       string  `json:"login,omitempty"`
	Advert      *Advert `json:"advert,omitempty"`
}

// Compatible reports whether a handshake came from a peer we can talk to.
func (h HelloOK) Compatible() bool { return h.App == AppName && h.Version == Version }

// Compatible reports whether an inbound dialer speaks our protocol.
func (h Hello) Compatible() bool { return h.App == AppName && h.Version == Version }

// LobbyPhase is the lifecycle stage of a hosted lobby.
type LobbyPhase string

const (
	// PhaseOpen accepts joins and is waiting on ready checks.
	PhaseOpen LobbyPhase = "open"
	// PhaseCountdown is the animated 3-2-1 before the first tick.
	PhaseCountdown LobbyPhase = "countdown"
	// PhaseInGame is a match in progress; joins are refused.
	PhaseInGame LobbyPhase = "in_game"
	// PhaseClosed means the host shut the lobby down.
	PhaseClosed LobbyPhase = "closed"
)

// Advert is the lobby summary shown in the peer browser.
type Advert struct {
	LobbyID   string      `json:"lobby_id"`
	Name      string      `json:"name"`
	HostName  string      `json:"host_name"`
	HostLogin string      `json:"host_login"`
	Config    game.Config `json:"config"`
	Seats     int         `json:"seats"`
	Taken     int         `json:"taken"`
	Bots      int         `json:"bots,omitempty"`
	Phase     LobbyPhase  `json:"phase"`
}

// Joinable reports whether the browser should offer a join action.
func (a *Advert) Joinable() bool {
	return a != nil && a.Phase == PhaseOpen && a.Taken < a.Seats
}

// JoinLobby asks a host for a seat.
type JoinLobby struct {
	LobbyID     string `json:"lobby_id"`
	PubKey      string `json:"pubkey"`
	DisplayName string `json:"display_name"`
}

// JoinOK confirms a seat assignment.
type JoinOK struct {
	LobbyID string        `json:"lobby_id"`
	Seat    game.PlayerID `json:"seat"`
}

// Player is one seat in a lobby roster.
type Player struct {
	Seat        game.PlayerID `json:"seat"`
	PubKey      string        `json:"pubkey"`
	DisplayName string        `json:"display_name"`
	Login       string        `json:"login,omitempty"`
	Node        string        `json:"node,omitempty"`
	Palette     int           `json:"palette"` // index into the colour/glyph palette
	Ready       bool          `json:"ready"`
	Host        bool          `json:"host"`
	Connected   bool          `json:"connected"`
	// Bot marks a seat the host steers. Bots never sign a result, so a match
	// with bots in it is attested by its people alone.
	Bot bool `json:"bot,omitempty"`
}

// LobbyEvent is one line of the in-lobby activity feed.
type LobbyEvent struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// LobbyState is the full roster broadcast after every change.
type LobbyState struct {
	LobbyID string      `json:"lobby_id"`
	Name    string      `json:"name"`
	Config  game.Config `json:"config"`
	// Gen increments every time the host changes the settings. A client echoes
	// it back when it readies up, so a ready sent while a change was in flight
	// cannot commit that player to a configuration they never saw.
	Gen       int          `json:"gen"`
	Phase     LobbyPhase   `json:"phase"`
	Players   []Player     `json:"players"`
	Events    []LobbyEvent `json:"events,omitempty"`
	Countdown int          `json:"countdown,omitempty"` // seconds remaining
}

// Ready toggles a seat's ready flag. Gen is the settings generation the
// player was looking at when they decided.
type Ready struct {
	Ready bool `json:"ready"`
	Gen   int  `json:"gen"`
}

// Leave announces a voluntary departure.
type Leave struct {
	Reason string `json:"reason,omitempty"`
}

// Kick removes a seat at the host's request.
type Kick struct {
	Seat   game.PlayerID `json:"seat"`
	Reason string        `json:"reason,omitempty"`
}

// Start announces the first tick of a match.
type Start struct {
	MatchID  string        `json:"match_id"`
	Config   game.Config   `json:"config"`
	Seats    []Player      `json:"seats"`
	YourSeat game.PlayerID `json:"your_seat"`
}

// Input is a client's requested heading change.
type Input struct {
	Dir        game.Direction `json:"dir"`
	ClientTick int            `json:"client_tick"`
}

// TickState is the host's authoritative snapshot for one tick.
type TickState struct {
	State game.State `json:"state"`
	// AckTick echoes the highest ClientTick the host has applied from this
	// client, letting the client discard superseded predictions.
	AckTick int `json:"ack_tick"`
}

// GameOver ends a match and carries the final ranked state.
type GameOver struct {
	State  game.State `json:"state"`
	Reason string     `json:"reason,omitempty"`
}

// AttestRequest asks a participant to sign a finished match result.
type AttestRequest struct {
	Result MatchResult `json:"result"`
	Hash   string      `json:"hash"`
}

// Attestation returns one participant's signature over a result hash.
type Attestation struct {
	MatchID string `json:"match_id"`
	PubKey  string `json:"pubkey"`
	Sig     string `json:"sig"`
}

// AttestedRecordMsg distributes the assembled, signed record.
type AttestedRecordMsg struct {
	Record AttestedRecord `json:"record"`
}

// InvEntry is one match in a gossip inventory. Sigs is the number of
// signatures the sender holds for that record: two peers can hold the same
// result — and therefore the same Hash — with different signature sets, so the
// count is what lets partially attested records converge as well as missing
// ones.
type InvEntry struct {
	MatchID string `json:"match_id"`
	Hash    string `json:"hash"`
	Sigs    int    `json:"sigs"`
}

// GossipInv opens an anti-entropy exchange with the dialer's inventory.
type GossipInv struct {
	Entries []InvEntry `json:"entries"`
}

// GossipResp answers an inventory with the records the dialer lacks and a list
// of the match IDs the responder wants in return.
type GossipResp struct {
	Want    []string         `json:"want,omitempty"`
	Records []AttestedRecord `json:"records,omitempty"`
}

// GossipRecords delivers the records the responder asked for.
type GossipRecords struct {
	Records []AttestedRecord `json:"records,omitempty"`
}

// ErrorMsg reports a refusal. It is advisory; the sender may close afterwards.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes used across the protocol.
const (
	ErrVersionMismatch = "version_mismatch"
	ErrLobbyGone       = "lobby_gone"
	ErrLobbyFull       = "lobby_full"
	ErrInProgress      = "in_progress"
	ErrNotHost         = "not_host"
	ErrKicked          = "kicked"
	ErrBadRequest      = "bad_request"
	ErrHostClosed      = "host_closed"
)

// Error implements the error interface so a received ErrorMsg can be returned
// straight up a call stack.
func (e ErrorMsg) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Ping and Pong carry a nonce so round-trip time can be measured.
type Ping struct {
	Nonce int64 `json:"nonce"`
}

// Pong echoes a Ping nonce.
type Pong struct {
	Nonce int64 `json:"nonce"`
}
