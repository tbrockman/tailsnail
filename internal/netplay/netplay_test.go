package netplay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
)

// fakeNode stands in for the tailnet with a loopback listener. It is the
// smallest thing that exercises the real framing, handshake, and session
// machinery without needing a tailnet.
type fakeNode struct {
	ready chan struct{}
	once  sync.Once

	mu   sync.Mutex
	addr string
}

func newFakeNode() *fakeNode { return &fakeNode{ready: make(chan struct{})} }

func (f *fakeNode) Listen(network, _ string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.addr = ln.Addr().String()
	f.mu.Unlock()
	f.once.Do(func() { close(f.ready) })
	return ln, nil
}

func (f *fakeNode) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (f *fakeNode) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	return &apitype.WhoIsResponse{
		UserProfile: &tailcfg.UserProfile{LoginName: "player@example.com"},
		Node:        &tailcfg.Node{Name: "tsnail-peer.tail1234.ts.net."},
	}, nil
}

// Addr blocks until the listener is bound.
func (f *fakeNode) Addr(t *testing.T) string {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listener never bound")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addr
}

// harness is a running host peer plus everything needed to join it.
type harness struct {
	node   *fakeNode
	server *Server
	st     *store.Store
	ident  *store.Identity
	addr   string
}

func newHarness(t *testing.T, name string) *harness {
	t.Helper()
	dir := t.TempDir()
	st, problems, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("store problems: %v", problems)
	}
	ident, err := store.LoadOrCreateIdentity(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	node := newFakeNode()
	srv := NewServer(node, st, ident, logring.New(64))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := srv.Serve(ctx); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	return &harness{node: node, server: srv, st: st, ident: ident, addr: node.Addr(t)}
}

// joiner is a second peer that only ever dials.
type joiner struct {
	st    *store.Store
	ident *store.Identity
}

func newJoiner(t *testing.T, name string) *joiner {
	t.Helper()
	dir := t.TempDir()
	st, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := store.LoadOrCreateIdentity(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return &joiner{st: st, ident: ident}
}

func (j *joiner) join(ctx context.Context, addr, lobbyID string) (*Client, error) {
	dial := func(ctx context.Context, a string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", a)
	}
	return Join(ctx, dial, JoinOptions{
		Addr:     addr,
		LobbyID:  lobbyID,
		Identity: j.ident,
		Store:    j.st,
		Log:      logring.New(64),
		Hostname: "tsnail-" + j.ident.DisplayName,
	})
}

// collector drains a session's events into a slice a test can assert on.
type collector struct {
	mu     sync.Mutex
	events []Event
	done   chan struct{}
}

func collect(s Session) *collector {
	c := &collector{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for ev := range s.Events() {
			c.mu.Lock()
			c.events = append(c.events, ev)
			c.mu.Unlock()
		}
	}()
	return c
}

// waitFor blocks until an event matching pred arrives, or the deadline passes.
func (c *collector) waitFor(t *testing.T, what string, timeout time.Duration, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, ev := range c.events {
			if pred(ev) {
				c.mu.Unlock()
				return ev
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("timed out waiting for %s; saw %d events: %s", what, len(c.events), summarise(c.events))
	return nil
}

func summarise(events []Event) string {
	var b strings.Builder
	seen := map[string]int{}
	for _, ev := range events {
		seen[fmt.Sprintf("%T", ev)]++
	}
	for k, n := range seen {
		fmt.Fprintf(&b, "%s×%d ", strings.TrimPrefix(k, "netplay."), n)
	}
	return b.String()
}

func isType[T Event](ev Event) bool {
	_, ok := ev.(T)
	return ok
}

// lastLobby returns the most recent roster the collector saw.
func (c *collector) lastLobby() (proto.LobbyState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if lu, ok := c.events[i].(LobbyUpdate); ok {
			return lu.State, true
		}
	}
	return proto.LobbyState{}, false
}

// fastConfig produces a match that resolves in a couple of seconds: a small
// walled arena at a high tick rate, so snakes reach a wall quickly.
func fastConfig() game.Config {
	cfg := game.DefaultConfig()
	cfg.Width, cfg.Height = 16, 10
	cfg.TickRate = 60
	cfg.TicksPerMove = 1
	cfg.MaxPlayers = 2
	cfg.Wrap = false
	cfg.FoodCount = 1
	return cfg
}

func TestJoinSeatsAPlayerAndBroadcastsTheRoster(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	host, err := h.server.Host(ctx, HostOptions{Name: "friday", Config: fastConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })
	hostEvents := collect(host)

	client, err := j.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { client.Close("test over") })
	clientEvents := collect(client)

	if client.Seat() == host.Seat() {
		t.Fatalf("client got seat %d, the same as the host", client.Seat())
	}
	if client.IsHost() {
		t.Error("client reports itself as the host")
	}
	if client.LobbyID() != host.LobbyID() {
		t.Errorf("client lobby ID = %q, want %q", client.LobbyID(), host.LobbyID())
	}

	hostEvents.waitFor(t, "the host to see two players", 5*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && len(lu.State.Players) == 2
	})
	clientEvents.waitFor(t, "the client to receive a roster", 5*time.Second, isType[LobbyUpdate])

	roster, ok := hostEvents.lastLobby()
	if !ok {
		t.Fatal("no roster seen")
	}
	var sawGrace bool
	for _, p := range roster.Players {
		if p.DisplayName == "grace" {
			sawGrace = true
			// WhoIs data must reach the roster for display.
			if p.Login != "player@example.com" {
				t.Errorf("login = %q, want the WhoIs login", p.Login)
			}
			if p.Node != "tsnail-peer" {
				t.Errorf("node = %q, want the short WhoIs node name", p.Node)
			}
		}
	}
	if !sawGrace {
		t.Errorf("roster does not contain the joiner: %+v", roster.Players)
	}
	// Palette slots must be distinct so players are visually separable.
	if roster.Players[0].Palette == roster.Players[1].Palette {
		t.Error("both players were assigned the same palette slot")
	}
}

func TestFullMatchProducesAnAttestedRecordOnBothPeers(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	host, err := h.server.Host(ctx, HostOptions{Name: "friday", Config: fastConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })
	hostEvents := collect(host)

	client, err := j.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { client.Close("test over") })
	clientEvents := collect(client)

	hostEvents.waitFor(t, "both seats", 5*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && len(lu.State.Players) == 2
	})

	host.SetReady(true)
	client.SetReady(true)

	// The countdown is three seconds, then the match runs to a wall.
	hostEvents.waitFor(t, "the match to start", 10*time.Second, isType[GameStarted])
	clientEvents.waitFor(t, "the client to see the start", 10*time.Second, isType[GameStarted])
	hostEvents.waitFor(t, "ticks", 5*time.Second, isType[Tick])
	clientEvents.waitFor(t, "the client to receive ticks", 5*time.Second, isType[Tick])

	over := hostEvents.waitFor(t, "the match to end", 20*time.Second, isType[MatchOver])
	final := over.(MatchOver).State
	if !final.Over {
		t.Error("the final state is not marked over")
	}
	for _, sn := range final.Snakes {
		if sn.Placement == 0 {
			t.Errorf("seat %d finished with no placement", sn.ID)
		}
	}

	hostEvents.waitFor(t, "the host to record the match", 15*time.Second, isType[Attested])
	clientEvents.waitFor(t, "the client to receive the record", 15*time.Second, isType[Attested])

	// Both peers must end up holding the same fully attested record.
	if h.st.Count() != 1 {
		t.Fatalf("host stored %d records, want 1", h.st.Count())
	}
	if j.st.Count() != 1 {
		t.Fatalf("client stored %d records, want 1", j.st.Count())
	}
	hostRecs, clientRecs := h.st.All(), j.st.All()
	if hostRecs[0].Hash != clientRecs[0].Hash {
		t.Fatal("the two peers stored different records for the same match")
	}
	if !hostRecs[0].FullyAttested() {
		t.Errorf("host record is %s, want both signatures", hostRecs[0].AttestationSummary())
	}
	if !clientRecs[0].FullyAttested() {
		t.Errorf("client record is %s, want both signatures", clientRecs[0].AttestationSummary())
	}
	if err := hostRecs[0].Verify(); err != nil {
		t.Errorf("stored record does not verify: %v", err)
	}
	// The record must name both installs.
	if got := len(hostRecs[0].Result.Participants); got != 2 {
		t.Errorf("record lists %d participants, want 2", got)
	}
	if _, ok := hostRecs[0].Result.Participant(j.ident.PubKey()); !ok {
		t.Error("the joiner is missing from the participant list")
	}
}

func TestLobbyReturnsToOpenAfterAMatch(t *testing.T) {
	h := newHarness(t, "ada")
	ctx := context.Background()

	cfg := fastConfig()
	cfg.MaxPlayers = 2
	host, err := h.server.Host(ctx, HostOptions{Name: "solo", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })
	events := collect(host)

	// Hosting alone and readying up is the practice mode.
	host.SetReady(true)
	events.waitFor(t, "the solo match to start", 10*time.Second, isType[GameStarted])
	events.waitFor(t, "the solo match to end", 20*time.Second, isType[MatchOver])
	events.waitFor(t, "the record", 15*time.Second, isType[Attested])

	events.waitFor(t, "the lobby to reopen", 10*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && lu.State.Phase == proto.PhaseOpen && !lu.State.Players[0].Ready
	})
}

func TestJoinIsRefusedWhenTheLobbyIsFull(t *testing.T) {
	h := newHarness(t, "ada")
	ctx := context.Background()

	cfg := fastConfig()
	cfg.MaxPlayers = 2
	host, err := h.server.Host(ctx, HostOptions{Name: "small", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })

	first := newJoiner(t, "grace")
	c1, err := first.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	t.Cleanup(func() { c1.Close("test over") })

	second := newJoiner(t, "hedy")
	_, err = second.join(ctx, h.addr, host.LobbyID())
	if err == nil {
		t.Fatal("a third player was seated in a two-seat lobby")
	}
	var msg proto.ErrorMsg
	if !errors.As(err, &msg) || msg.Code != proto.ErrLobbyFull {
		t.Errorf("error = %v, want %s", err, proto.ErrLobbyFull)
	}
}

func TestJoinIsRefusedWhenNobodyIsHosting(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")

	_, err := j.join(context.Background(), h.addr, "no-such-lobby")
	if err == nil {
		t.Fatal("joined a peer that is not hosting")
	}
	var msg proto.ErrorMsg
	if !errors.As(err, &msg) || msg.Code != proto.ErrLobbyGone {
		t.Errorf("error = %v, want %s", err, proto.ErrLobbyGone)
	}
}

func TestTheSameInstallCannotTakeTwoSeats(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	cfg := fastConfig()
	cfg.MaxPlayers = 4
	host, err := h.server.Host(ctx, HostOptions{Name: "dupes", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })

	c1, err := j.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c1.Close("test over") })

	if _, err := j.join(ctx, h.addr, host.LobbyID()); err == nil {
		t.Fatal("the same signing key was seated twice")
	}
}

func TestKickRemovesASeat(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	host, err := h.server.Host(ctx, HostOptions{Name: "kickme", Config: fastConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })
	hostEvents := collect(host)

	client, err := j.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatal(err)
	}
	clientEvents := collect(client)

	hostEvents.waitFor(t, "both seats", 5*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && len(lu.State.Players) == 2
	})
	host.Kick(client.Seat())

	closed := clientEvents.waitFor(t, "the client to be told it was kicked", 5*time.Second, isType[SessionClosed])
	if reason := closed.(SessionClosed).Reason; !strings.Contains(reason, "host") {
		t.Errorf("close reason = %q, want it to mention the host", reason)
	}
	hostEvents.waitFor(t, "the roster to shrink", 5*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && len(lu.State.Players) == 1
	})
}

func TestClientsAreToldWhenTheHostCloses(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	host, err := h.server.Host(ctx, HostOptions{Name: "closing", Config: fastConfig()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := j.join(ctx, h.addr, host.LobbyID())
	if err != nil {
		t.Fatal(err)
	}
	clientEvents := collect(client)
	hostEvents := collect(host)

	hostEvents.waitFor(t, "both seats", 5*time.Second, func(ev Event) bool {
		lu, ok := ev.(LobbyUpdate)
		return ok && len(lu.State.Players) == 2
	})

	host.Close("host is going to bed")

	// There is deliberately no host migration: the client's session simply ends.
	closed := clientEvents.waitFor(t, "the client session to end", 8*time.Second, isType[SessionClosed])
	if got := closed.(SessionClosed).Reason; got == "" {
		t.Error("the client was given no reason for the close")
	}
	hostEvents.waitFor(t, "the host session to end", 5*time.Second, isType[SessionClosed])

	// The listener must stop advertising a lobby once it closes.
	if adv := h.server.Advert(); adv != nil && adv.Phase != proto.PhaseClosed {
		t.Errorf("still advertising %+v after close", adv)
	}
}

func TestProbeHandshakeReportsTheAdvert(t *testing.T) {
	h := newHarness(t, "ada")
	ctx := context.Background()

	cfg := fastConfig()
	cfg.MaxPlayers = 3
	host, err := h.server.Host(ctx, HostOptions{Name: "visible", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })

	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	if err := conn.SendTimeout(3*time.Second, proto.KindHello, proto.Hello{
		App: proto.AppName, Version: proto.Version, Intent: proto.IntentProbe,
		PubKey: "probe", DisplayName: "prober",
	}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.RecvTimeout(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != proto.KindHelloOK {
		t.Fatalf("kind = %s, want %s", env.Kind, proto.KindHelloOK)
	}
	ok, err := proto.Decode[proto.HelloOK](env)
	if err != nil {
		t.Fatal(err)
	}
	if !ok.Compatible() {
		t.Fatalf("handshake reports %s v%d", ok.App, ok.Version)
	}
	if ok.PubKey != h.ident.PubKey() {
		t.Error("the handshake did not carry the host's signing key")
	}
	if ok.Advert == nil {
		t.Fatal("no advert in the handshake")
	}
	if ok.Advert.Name != "visible" || ok.Advert.Seats != 3 || ok.Advert.Taken != 1 {
		t.Errorf("advert = %+v", *ok.Advert)
	}
	if !ok.Advert.Joinable() {
		t.Error("an open lobby with free seats reports itself unjoinable")
	}
}

func TestProbeAnsweredWithNoLobby(t *testing.T) {
	h := newHarness(t, "ada")
	var d net.Dialer
	raw, err := d.DialContext(context.Background(), "tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	if err := conn.SendTimeout(3*time.Second, proto.KindHello, proto.Hello{
		App: proto.AppName, Version: proto.Version, Intent: proto.IntentProbe,
	}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.RecvTimeout(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := proto.Decode[proto.HelloOK](env)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Advert != nil {
		t.Errorf("advert = %+v, want none from a peer that is not hosting", *ok.Advert)
	}
}

func TestIncompatibleProtocolIsRefused(t *testing.T) {
	h := newHarness(t, "ada")
	var d net.Dialer
	raw, err := d.DialContext(context.Background(), "tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	if err := conn.SendTimeout(3*time.Second, proto.KindHello, proto.Hello{
		App: proto.AppName, Version: proto.Version + 99, Intent: proto.IntentProbe,
	}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.RecvTimeout(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != proto.KindError {
		t.Fatalf("kind = %s, want an error", env.Kind)
	}
	msg, err := proto.Decode[proto.ErrorMsg](env)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Code != proto.ErrVersionMismatch {
		t.Errorf("code = %s, want %s", msg.Code, proto.ErrVersionMismatch)
	}
}

func TestAnUnrelatedServiceGetsAPoliteRefusal(t *testing.T) {
	h := newHarness(t, "ada")
	var d net.Dialer
	raw, err := d.DialContext(context.Background(), "tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	// Something that speaks the framing but not the handshake.
	if err := conn.SendTimeout(3*time.Second, proto.KindPing, proto.Ping{Nonce: 1}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.RecvTimeout(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != proto.KindError {
		t.Fatalf("kind = %s, want an error", env.Kind)
	}
}

func TestHostRejectsAJoinDuringAMatch(t *testing.T) {
	h := newHarness(t, "ada")
	ctx := context.Background()

	cfg := fastConfig()
	cfg.MaxPlayers = 4
	cfg.Wrap = true // keep the solo match running while we try to join
	host, err := h.server.Host(ctx, HostOptions{Name: "busy", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close("test over") })
	events := collect(host)

	host.SetReady(true)
	events.waitFor(t, "the match to start", 10*time.Second, isType[GameStarted])

	j := newJoiner(t, "latecomer")
	_, err = j.join(ctx, h.addr, host.LobbyID())
	if err == nil {
		t.Fatal("a player joined a match already in progress")
	}
	var msg proto.ErrorMsg
	if !errors.As(err, &msg) || msg.Code != proto.ErrInProgress {
		t.Errorf("error = %v, want %s", err, proto.ErrInProgress)
	}
}

func TestGossipRunsOverAProbeConnection(t *testing.T) {
	h := newHarness(t, "ada")
	j := newJoiner(t, "grace")
	ctx := context.Background()

	// Give the joiner a record the host has never seen.
	rec := makeSharedRecord(t, j.ident, h.ident)
	if _, err := j.st.Put(rec); err != nil {
		t.Fatal(err)
	}

	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	if err := conn.SendTimeout(3*time.Second, proto.KindHello, proto.Hello{
		App: proto.AppName, Version: proto.Version, Intent: proto.IntentProbe,
		PubKey: j.ident.PubKey(), DisplayName: "grace",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RecvTimeout(3 * time.Second); err != nil {
		t.Fatal(err)
	}

	// The probe connection stays open for one anti-entropy round.
	if _, err := gossipInitiate(ctx, conn, j.st); err != nil {
		t.Fatalf("gossip over the probe connection: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.st.Count() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.st.Count() != 1 {
		t.Fatalf("host holds %d records after gossip, want 1", h.st.Count())
	}
	if got, _ := h.st.Get(rec.Result.MatchID); got.Hash != rec.Hash {
		t.Error("the host stored a different record")
	}
}

// makeSharedRecord builds a two-player record signed by both identities.
func makeSharedRecord(t *testing.T, a, b *store.Identity) proto.AttestedRecord {
	t.Helper()
	r := proto.MatchResult{
		Version:    proto.MatchResultVersion,
		MatchID:    proto.NewMatchID(),
		LobbyName:  "earlier",
		Config:     game.DefaultConfig(),
		StartedAt:  proto.FormatTime(time.Now().Add(-time.Minute)),
		EndedAt:    proto.FormatTime(time.Now()),
		HostPubKey: a.PubKey(),
		Participants: []proto.Participant{
			{PubKey: a.PubKey(), DisplayName: a.DisplayName, Seat: 0},
			{PubKey: b.PubKey(), DisplayName: b.DisplayName, Seat: 1},
		},
		Placements: []proto.Placement{
			{PubKey: a.PubKey(), Place: 1, Length: 12},
			{PubKey: b.PubKey(), Place: 2, Length: 8},
		},
	}
	rec, err := proto.NewAttestedRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []*store.Identity{a, b} {
		sig, err := proto.SignResult(id.Private, rec.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}
