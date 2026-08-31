package netplay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/version"
)

// Client-side tunables.
const (
	// joinTimeout bounds the whole dial-handshake-join sequence.
	joinTimeout = 8 * time.Second
	// clientPingInterval is how often a client pings the host, which both
	// measures liveness and keeps the host's seat timer fed.
	clientPingInterval = 2 * time.Second
	// hostSilenceTimeout is how long a client waits on a silent host before
	// declaring the match over. The host pings every 2s, so this is generous.
	hostSilenceTimeout = 12 * time.Second
)

// Client is a session against a remote host.
type Client struct {
	conn  *proto.Conn
	ident *store.Identity
	store *store.Store
	log   *logring.Ring

	lobbyID string
	seat    game.PlayerID

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	events chan Event

	mu       sync.Mutex
	lastSeen time.Time
	matchID  string
	tick     int
	closeMsg string
	closeErr error

	closeOnce sync.Once
}

// JoinOptions configures a join attempt.
type JoinOptions struct {
	// Addr is the host's tailnet address and control port.
	Addr string
	// LobbyID is the advertised lobby to join.
	LobbyID string
	// Identity signs attestations and identifies this install.
	Identity *store.Identity
	// Store persists the attested record when the match ends.
	Store *store.Store
	// Log receives protocol diagnostics.
	Log *logring.Ring
	// Hostname is this node's tailnet name, sent for display.
	Hostname string
}

// Join dials a host, completes the handshake, and takes a seat. It returns
// once the seat is confirmed; everything after that arrives on Events.
func Join(ctx context.Context, dial func(context.Context, string) (net.Conn, error), opts JoinOptions) (*Client, error) {
	if opts.Log == nil {
		opts.Log = logring.New(logring.DefaultCapacity)
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, joinTimeout)
	defer cancelDial()

	raw, err := dial(dialCtx, opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("netplay: dialling %s: %w", opts.Addr, err)
	}
	conn := proto.NewConn(raw)

	if err := conn.SendTimeout(joinTimeout, proto.KindHello, proto.Hello{
		App:         proto.AppName,
		Version:     proto.Version,
		AppVersion:  version.String(),
		PubKey:      opts.Identity.PubKey(),
		DisplayName: opts.Identity.DisplayName,
		Hostname:    opts.Hostname,
		Intent:      proto.IntentPlay,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("netplay: sending hello: %w", err)
	}

	env, err := conn.RecvTimeout(joinTimeout)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("netplay: awaiting hello: %w", err)
	}
	if err := asError(env); err != nil {
		conn.Close()
		return nil, err
	}
	if env.Kind != proto.KindHelloOK {
		conn.Close()
		return nil, fmt.Errorf("netplay: expected %s, got %s", proto.KindHelloOK, env.Kind)
	}
	helloOK, err := proto.Decode[proto.HelloOK](env)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !helloOK.Compatible() {
		conn.Close()
		return nil, fmt.Errorf("netplay: host speaks %s protocol v%d, this build speaks v%d",
			helloOK.App, helloOK.Version, proto.Version)
	}

	if err := conn.SendTimeout(joinTimeout, proto.KindJoinLobby, proto.JoinLobby{
		LobbyID:     opts.LobbyID,
		PubKey:      opts.Identity.PubKey(),
		DisplayName: opts.Identity.DisplayName,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("netplay: sending join: %w", err)
	}

	env, err = conn.RecvTimeout(joinTimeout)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("netplay: awaiting seat: %w", err)
	}
	if err := asError(env); err != nil {
		conn.Close()
		return nil, err
	}
	if env.Kind != proto.KindJoinOK {
		conn.Close()
		return nil, fmt.Errorf("netplay: expected %s, got %s", proto.KindJoinOK, env.Kind)
	}
	joinOK, err := proto.Decode[proto.JoinOK](env)
	if err != nil {
		conn.Close()
		return nil, err
	}

	clientCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &Client{
		conn:     conn,
		ident:    opts.Identity,
		store:    opts.Store,
		log:      opts.Log,
		lobbyID:  joinOK.LobbyID,
		seat:     joinOK.Seat,
		ctx:      clientCtx,
		cancel:   cancel,
		events:   make(chan Event, 256),
		lastSeen: time.Now(),
	}
	c.wg.Add(2)
	go c.readLoop()
	go c.pingLoop()
	return c, nil
}

// asError turns an error envelope into a Go error.
func asError(env proto.Envelope) error {
	if env.Kind != proto.KindError {
		return nil
	}
	msg, err := proto.Decode[proto.ErrorMsg](env)
	if err != nil {
		return errors.New("netplay: host refused the connection")
	}
	return msg
}

// Events implements Session.
func (c *Client) Events() <-chan Event { return c.events }

// Seat implements Session.
func (c *Client) Seat() game.PlayerID { return c.seat }

// IsHost implements Session.
func (c *Client) IsHost() bool { return false }

// LobbyID implements Session.
func (c *Client) LobbyID() string { return c.lobbyID }

// SetReady implements Session.
func (c *Client) SetReady(ready bool) {
	c.send(proto.KindReady, proto.Ready{Ready: ready})
}

// Input implements Session. The client tick stamp lets the host echo back
// which input it last applied, which is what makes local prediction safe to
// discard at the right moment.
func (c *Client) Input(dir game.Direction) {
	c.mu.Lock()
	c.tick++
	tick := c.tick
	c.mu.Unlock()
	c.send(proto.KindInput, proto.Input{Dir: dir, ClientTick: tick})
}

// Kick implements Session. Only a host can remove seats, so this is a no-op.
func (c *Client) Kick(game.PlayerID) {}

// Close implements Session, leaving the lobby.
func (c *Client) Close(reason string) {
	c.setReason(reason, nil)
	c.closeOnce.Do(func() {
		// Best-effort courtesy so the host frees the seat immediately rather
		// than waiting for the liveness timer to notice.
		c.conn.SendTimeout(time.Second, proto.KindLeave, proto.Leave{Reason: reason})
		c.cancel()
		c.conn.Close()
	})
}

// send writes a message, giving up rather than blocking the UI goroutine.
func (c *Client) send(kind proto.Kind, body any) {
	if c.ctx.Err() != nil {
		return
	}
	if err := c.conn.SendTimeout(3*time.Second, kind, body); err != nil {
		c.log.Logf("netplay: sending %s: %v", kind, err)
	}
}

// pingLoop keeps the connection warm and notices a host that has gone quiet.
func (c *Client) pingLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(clientPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			silence := now.Sub(c.lastSeen)
			c.mu.Unlock()
			if silence > hostSilenceTimeout {
				c.shutdown("the host stopped responding", nil)
				return
			}
			c.send(proto.KindPing, proto.Ping{Nonce: now.UnixNano()})
		}
	}
}

// readLoop consumes messages from the host until the connection ends.
//
// It is the sole owner of the event channel: every event is emitted here, and
// the channel is closed here when the loop exits. Nothing else sends or closes,
// so a shutdown racing an in-flight event is not possible.
func (c *Client) readLoop() {
	defer c.wg.Done()
	defer c.closeStream()

	for {
		env, err := c.conn.Recv()
		if err != nil {
			if c.ctx.Err() == nil {
				// A host that dies mid-match takes the match with it. There is
				// deliberately no host migration: the clients return to the
				// browser and someone else opens a lobby.
				c.setReason("the host went away", err)
			}
			return
		}
		c.mu.Lock()
		c.lastSeen = time.Now()
		c.mu.Unlock()
		c.handle(env)
	}
}

// closeStream emits the terminal event and closes the channel. It runs only on
// the readLoop goroutine, as that loop unwinds.
func (c *Client) closeStream() {
	c.mu.Lock()
	reason, err := c.closeMsg, c.closeErr
	c.mu.Unlock()
	if reason == "" {
		reason = "disconnected"
	}
	c.events <- SessionClosed{Reason: reason, Err: err}
	close(c.events)
}

// setReason records why the session is ending, keeping the first explanation:
// the original cause is more useful than the disconnect it goes on to produce.
func (c *Client) setReason(reason string, err error) {
	c.mu.Lock()
	if c.closeMsg == "" {
		c.closeMsg, c.closeErr = reason, err
	}
	c.mu.Unlock()
}

// shutdown records a reason and tears the connection down, which makes readLoop
// unwind and emit the terminal event.
func (c *Client) shutdown(reason string, err error) {
	c.setReason(reason, err)
	c.closeOnce.Do(func() {
		c.cancel()
		c.conn.Close()
	})
}

// handle dispatches one message from the host.
func (c *Client) handle(env proto.Envelope) {
	switch env.Kind {
	case proto.KindLobbyState:
		msg, err := proto.Decode[proto.LobbyState](env)
		if err != nil {
			return
		}
		c.emit(LobbyUpdate{State: msg})

	case proto.KindStart:
		msg, err := proto.Decode[proto.Start](env)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.matchID = msg.MatchID
		c.tick = 0
		c.mu.Unlock()
		c.emit(GameStarted{
			MatchID: msg.MatchID, Config: msg.Config,
			Seat: msg.YourSeat, Players: msg.Seats,
		})

	case proto.KindTickState:
		msg, err := proto.Decode[proto.TickState](env)
		if err != nil {
			return
		}
		c.emit(Tick{State: msg.State})

	case proto.KindGameOver:
		msg, err := proto.Decode[proto.GameOver](env)
		if err != nil {
			return
		}
		c.emit(MatchOver{State: msg.State, Reason: firstNonEmpty(msg.Reason, "match complete")})

	case proto.KindAttestRequest:
		msg, err := proto.Decode[proto.AttestRequest](env)
		if err != nil {
			return
		}
		c.attest(msg)

	case proto.KindAttestedRecord:
		msg, err := proto.Decode[proto.AttestedRecordMsg](env)
		if err != nil {
			return
		}
		c.storeRecord(msg.Record)

	case proto.KindKick:
		msg, err := proto.Decode[proto.Kick](env)
		if err != nil {
			return
		}
		c.shutdown(firstNonEmpty(msg.Reason, "removed by the host"), nil)

	case proto.KindError:
		msg, err := proto.Decode[proto.ErrorMsg](env)
		if err != nil {
			return
		}
		c.shutdown(msg.Message, msg)

	case proto.KindPing:
		msg, err := proto.Decode[proto.Ping](env)
		if err != nil {
			return
		}
		c.send(proto.KindPong, proto.Pong{Nonce: msg.Nonce})

	case proto.KindPong:
		// lastSeen was already refreshed.
	}
}

// attest verifies that the host's result is one this peer is willing to sign,
// then returns a signature. The trusted-peer model means this is provenance,
// not adjudication: the check is that the record is well formed, names this
// install as a participant, and hashes to what the host claims.
func (c *Client) attest(req proto.AttestRequest) {
	rec, err := proto.NewAttestedRecord(req.Result)
	if err != nil {
		c.log.Logf("netplay: refusing to attest a malformed result: %v", err)
		return
	}
	if rec.Hash != req.Hash {
		c.log.Logf("netplay: host's hash %s does not match the result it sent", req.Hash)
		return
	}
	if _, ok := rec.Result.Participant(c.ident.PubKey()); !ok {
		c.log.Logf("netplay: refusing to attest a match this install did not play")
		return
	}
	sig, err := proto.SignResult(c.ident.Private, rec.Result)
	if err != nil {
		c.log.Logf("netplay: signing result: %v", err)
		return
	}
	c.send(proto.KindAttestation, proto.Attestation{
		MatchID: rec.Result.MatchID, PubKey: sig.PubKey, Sig: sig.Sig,
	})
}

// storeRecord persists the assembled record and tells the UI.
func (c *Client) storeRecord(rec proto.AttestedRecord) {
	if c.store != nil {
		if _, err := c.store.Put(rec); err != nil {
			c.log.Logf("netplay: storing match record: %v", err)
			return
		}
	}
	c.emit(Attested{Record: rec})
}

// emit hands an event to the UI, dropping a stale tick if it falls behind.
// Only readLoop calls it, so it never races the channel's close.
func (c *Client) emit(ev Event) {
	select {
	case c.events <- ev:
		return
	default:
	}
	// Tick states are complete snapshots, so discarding an old one to make room
	// costs nothing. Anything else has to wait for the UI to catch up.
	if _, isTick := ev.(Tick); isTick {
		select {
		case <-c.events:
		default:
		}
		select {
		case c.events <- ev:
		default:
		}
		return
	}
	select {
	case c.events <- ev:
	case <-c.ctx.Done():
	}
}
