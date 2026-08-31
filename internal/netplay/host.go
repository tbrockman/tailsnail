package netplay

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/theolol/tailsnail/internal/game"
	"github.com/theolol/tailsnail/internal/proto"
)

// Host loop tunables.
const (
	// houseInterval is the cadence of lobby housekeeping: heartbeats, liveness
	// checks, and the start countdown.
	houseInterval = 200 * time.Millisecond
	// countdownSeconds is the animated 3-2-1 before the first tick.
	countdownSeconds = 3
	// heartbeatInterval is how often the host pings each client.
	heartbeatInterval = 2 * time.Second
	// coastAfter is the silence that marks a client disconnected. Its snake
	// keeps travelling straight from here.
	coastAfter = 4 * time.Second
	// dropAfter is the silence after which a client's seat is eliminated and
	// its connection torn down.
	dropAfter = 10 * time.Second
	// attestWindow is how long the host waits for participants to sign a
	// finished match before storing whatever signatures arrived.
	attestWindow = 4 * time.Second
	// outboxDepth is the per-client send queue. Tick states are snapshots, so
	// a backed-up client loses old frames rather than stalling the game loop.
	outboxDepth = 32
	// feedLimit caps the in-lobby activity feed.
	feedLimit = 40
)

// HostOptions configures a new lobby.
type HostOptions struct {
	// Name is the lobby name shown in the browser.
	Name string
	// Config is the match configuration.
	Config game.Config
}

// seat is one player's slot in the lobby.
type seat struct {
	id     game.PlayerID
	player proto.Player

	// bot marks a seat the host steers itself.
	bot bool

	// conn is nil for the host's own seat and for bots, neither of which needs
	// a network connection.
	conn *proto.Conn
	out  chan proto.Envelope

	lastSeen  time.Time
	coasting  bool
	lastInput int // highest client tick applied, echoed back for prediction
	signed    bool

	closeOnce sync.Once
	done      chan struct{}
}

// local reports whether the seat has no network connection: the host's own
// seat, or a bot. Nothing is ever sent to a local seat and its liveness is
// never in question.
func (s *seat) local() bool { return s.conn == nil }

// Host runs a lobby and, once started, the authoritative simulation.
//
// Every mutation happens on a single goroutine fed by the cmds channel, so the
// lobby roster and the simulation need no locking. Only the advert is shared
// with the listener, and it has its own mutex.
type Host struct {
	srv  *Server
	id   string
	name string
	cfg  game.Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cmds   chan func()
	events chan Event

	advertMu sync.RWMutex
	advert   proto.Advert

	// Everything below is owned by the run goroutine.
	seats []*seat
	phase proto.LobbyPhase
	feed  []proto.LobbyEvent
	sim   *game.Sim
	// matchCfg is the config the running match was built with, including the
	// seed drawn for it. The record has to describe the match that was played,
	// not the lobby template it came from.
	matchCfg game.Config
	// matchPlayers is the roster as it stood at kickoff. Results are built
	// from it rather than from the live seats, so a player who leaves
	// mid-match still appears in the record they took part in.
	matchPlayers []proto.Player
	matchID      string
	startedAt    time.Time
	pending      map[game.PlayerID]pendingInput

	// gen increments on every settings change; a ready that quotes an older
	// generation is stale and is refused.
	gen int

	countdownEnd time.Time
	lastCount    int
	lastPing     time.Time

	record       *proto.AttestedRecord
	attestUntil  time.Time
	finalPlayers []proto.Player

	closeOnce sync.Once
	closeErr  string
}

// pendingInput is a queued heading change awaiting the next tick.
type pendingInput struct {
	dir  game.Direction
	tick int
}

// newHost creates and starts a lobby with the local player in seat 0.
func newHost(ctx context.Context, srv *Server, opts HostOptions) (*Host, error) {
	if err := opts.Config.Validate(); err != nil {
		return nil, fmt.Errorf("netplay: %w", err)
	}
	name := proto.SanitizeDisplayName(opts.Name)

	hostCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &Host{
		srv:     srv,
		id:      proto.NewMatchID(),
		name:    name,
		cfg:     opts.Config,
		ctx:     hostCtx,
		cancel:  cancel,
		cmds:    make(chan func(), 64),
		events:  make(chan Event, 256),
		phase:   proto.PhaseOpen,
		pending: make(map[game.PlayerID]pendingInput),
	}

	ident := srv.Identity()
	h.seats = []*seat{{
		id: 0,
		player: proto.Player{
			Seat:        0,
			PubKey:      ident.PubKey(),
			DisplayName: ident.DisplayName,
			Palette:     0,
			Host:        true,
			Connected:   true,
		},
		lastSeen: time.Now(),
		done:     make(chan struct{}),
	}}
	h.note("%s opened the lobby", ident.DisplayName)
	h.syncBots()
	h.refreshAdvert()

	h.wg.Add(1)
	go h.run()
	return h, nil
}

// Events implements Session.
func (h *Host) Events() <-chan Event { return h.events }

// Seat implements Session: the host always holds seat 0.
func (h *Host) Seat() game.PlayerID { return 0 }

// IsHost implements Session.
func (h *Host) IsHost() bool { return true }

// LobbyID implements Session.
func (h *Host) LobbyID() string { return h.id }

// Name returns the lobby name.
func (h *Host) Name() string { return h.name }

// Config returns the match configuration.
func (h *Host) Config() game.Config { return h.cfg }

// Advert returns the current lobby summary for discovery handshakes.
func (h *Host) Advert() *proto.Advert {
	h.advertMu.RLock()
	defer h.advertMu.RUnlock()
	a := h.advert
	return &a
}

// SetReady implements Session for the local player.
func (h *Host) SetReady(ready bool) {
	// The host is always looking at the current settings; it is the one
	// changing them.
	h.do(func() { h.setReady(0, ready, h.gen) })
}

// Input implements Session for the local player.
func (h *Host) Input(dir game.Direction) {
	h.do(func() { h.queueInput(0, dir, 0) })
}

// Kick implements Session, removing a seat at the host's request.
func (h *Host) Kick(target game.PlayerID) {
	h.do(func() {
		if target == 0 {
			return // the host cannot kick itself; use Close
		}
		s := h.seatByID(target)
		if s == nil {
			return
		}
		h.note("%s was removed by the host", s.player.DisplayName)
		h.sendTo(s, proto.KindKick, proto.Kick{Seat: target, Reason: "removed by the host"}, false)
		h.removeSeat(target, "kicked")
		h.broadcastLobby()
	})
}

// Close implements Session, shutting the lobby down for everyone.
func (h *Host) Close(reason string) {
	h.closeOnce.Do(func() {
		h.closeErr = reason
		h.cancel()
	})
}

// do queues a mutation on the host goroutine, dropping it if the lobby is
// already shutting down.
func (h *Host) do(fn func()) {
	select {
	case h.cmds <- fn:
	case <-h.ctx.Done():
	}
}

// run is the single goroutine that owns all lobby and simulation state.
func (h *Host) run() {
	defer h.wg.Done()

	// Publish the opening roster before anything else. Without it the host
	// sits on an empty lobby — no seats, no settings — until the first player
	// joins and something happens to trigger a broadcast.
	h.broadcastLobby()

	house := time.NewTicker(houseInterval)
	defer house.Stop()

	var gameTick *time.Ticker
	stopGameTick := func() {
		if gameTick != nil {
			gameTick.Stop()
			gameTick = nil
		}
	}
	defer stopGameTick()

	for {
		// Reconcile the simulation ticker with the current phase, so the game
		// clock exists exactly while a match is running.
		switch {
		case h.phase == proto.PhaseInGame && gameTick == nil:
			gameTick = time.NewTicker(time.Second / time.Duration(h.cfg.TickRate))
		case h.phase != proto.PhaseInGame && gameTick != nil:
			stopGameTick()
		}
		var gameC <-chan time.Time
		if gameTick != nil {
			gameC = gameTick.C
		}

		select {
		case <-h.ctx.Done():
			h.shutdown()
			return
		case fn := <-h.cmds:
			fn()
		case <-house.C:
			h.housekeeping()
		case <-gameC:
			h.tick()
		}
	}
}

// shutdown tears the lobby down, telling every client why.
func (h *Host) shutdown() {
	reason := h.closeErr
	if reason == "" {
		reason = "the host closed the lobby"
	}
	h.phase = proto.PhaseClosed
	h.refreshAdvert()
	for _, s := range h.seatsSnapshot() {
		if s.local() {
			continue
		}
		h.sendTo(s, proto.KindError, proto.ErrorMsg{Code: proto.ErrHostClosed, Message: reason}, false)
		h.closeSeat(s)
	}
	h.srv.setHost(nil)
	h.emit(SessionClosed{Reason: reason})
	close(h.events)
}

// housekeeping runs the periodic lobby chores: heartbeats, liveness, the
// start countdown, and closing the attestation window.
func (h *Host) housekeeping() {
	now := time.Now()

	if now.Sub(h.lastPing) >= heartbeatInterval {
		h.lastPing = now
		for _, s := range h.seatsSnapshot() {
			if !s.local() {
				h.sendTo(s, proto.KindPing, proto.Ping{Nonce: now.UnixNano()}, true)
			}
		}
	}
	h.checkLiveness(now)

	switch h.phase {
	case proto.PhaseOpen:
		if h.everyoneReady() {
			h.beginCountdown()
		}
	case proto.PhaseCountdown:
		if !h.everyoneReady() {
			// Somebody un-readied; abort rather than starting anyway.
			h.phase = proto.PhaseOpen
			h.lastCount = 0
			h.note("countdown cancelled")
			h.broadcastLobby()
			h.refreshAdvert()
			break
		}
		remaining := int(math.Ceil(time.Until(h.countdownEnd).Seconds()))
		if remaining <= 0 {
			h.startMatch()
			break
		}
		if remaining != h.lastCount {
			h.lastCount = remaining
			h.broadcastLobby()
		}
	}

	if h.record != nil && (h.allSigned() || now.After(h.attestUntil)) {
		h.finalizeRecord()
	}
}

// checkLiveness marks quiet clients as coasting and eliminates the ones that
// have gone entirely.
func (h *Host) checkLiveness(now time.Time) {
	for _, s := range h.seatsSnapshot() {
		if s.local() {
			continue
		}
		silence := now.Sub(s.lastSeen)
		switch {
		case silence >= dropAfter:
			h.note("%s dropped out", s.player.DisplayName)
			if h.sim != nil && h.phase == proto.PhaseInGame {
				h.sim.Eliminate(s.id)
			}
			h.removeSeat(s.id, "timed out")
			h.broadcastLobby()
		case silence >= coastAfter && !s.coasting:
			s.coasting = true
			s.player.Connected = false
			h.note("%s stopped responding", s.player.DisplayName)
			if h.sim != nil && h.phase == proto.PhaseInGame {
				h.sim.SetCoasting(s.id, true)
			}
			h.broadcastLobby()
		}
	}
}

// everyoneReady reports whether every seated player has readied up. A lobby of
// one is allowed to start, which makes hosting alone a practice mode.
func (h *Host) everyoneReady() bool {
	if len(h.seats) == 0 {
		return false
	}
	// A lobby of nothing but bots has nobody to play it.
	if h.humanSeats() == 0 {
		return false
	}
	for _, s := range h.seats {
		if !s.player.Ready {
			return false
		}
	}
	return true
}

// beginCountdown moves the lobby into its pre-match countdown.
func (h *Host) beginCountdown() {
	h.phase = proto.PhaseCountdown
	h.countdownEnd = time.Now().Add(countdownSeconds * time.Second)
	h.lastCount = countdownSeconds
	h.note("all players ready — starting")
	h.refreshAdvert()
	h.broadcastLobby()
}

// startMatch builds the simulation and announces the first tick.
func (h *Host) startMatch() {
	seats := make([]game.PlayerID, 0, len(h.seats))
	for _, s := range h.seats {
		seats = append(seats, s.id)
	}
	cfg := h.cfg
	cfg.Seed = time.Now().UnixNano()

	sim, err := game.New(cfg, seats)
	if err != nil {
		h.note("cannot start: %v", err)
		h.phase = proto.PhaseOpen
		h.clearReady()
		h.broadcastLobby()
		h.refreshAdvert()
		return
	}
	h.sim = sim
	h.matchCfg = cfg
	h.matchPlayers = h.playerList()
	h.matchID = proto.NewMatchID()
	h.startedAt = time.Now()
	h.phase = proto.PhaseInGame
	h.pending = make(map[game.PlayerID]pendingInput)
	h.refreshAdvert()

	players := h.matchPlayers
	for _, s := range h.seatsSnapshot() {
		if s.local() {
			continue
		}
		h.sendTo(s, proto.KindStart, proto.Start{
			MatchID: h.matchID, Config: cfg, Seats: players, YourSeat: s.id,
		}, false)
	}
	h.emit(GameStarted{MatchID: h.matchID, Config: cfg, Seat: 0, Players: players})
	h.broadcastState()
}

// tick advances the simulation one step and broadcasts the result.
func (h *Host) tick() {
	if h.sim == nil {
		return
	}
	// Bots decide from the state as it stands at the top of the tick, before
	// anyone's input is applied, so they see exactly what a player sees.
	if bots := h.botSeats(); len(bots) > 0 {
		state := h.sim.State()
		for _, s := range bots {
			if sn := state.SnakeByID(s.id); sn != nil && sn.Alive {
				h.sim.SetDirection(s.id, game.ChooseDirection(state, h.matchCfg, s.id))
			}
		}
	}
	for id, in := range h.pending {
		h.sim.SetDirection(id, in.dir)
		if s := h.seatByID(id); s != nil {
			s.lastInput = in.tick
		}
	}
	clear(h.pending)

	state := h.sim.Step()
	h.emit(Tick{State: state})
	for _, s := range h.seatsSnapshot() {
		if s.local() {
			continue
		}
		// Tick states are complete snapshots, so a lagging client may lose
		// intermediate frames without any loss of correctness.
		h.sendTo(s, proto.KindTickState, proto.TickState{State: state, AckTick: s.lastInput}, true)
	}
	if state.Over {
		h.endMatch(state)
	}
}

// broadcastState sends the current simulation state to everyone.
func (h *Host) broadcastState() {
	if h.sim == nil {
		return
	}
	state := h.sim.State()
	h.emit(Tick{State: state})
	for _, s := range h.seatsSnapshot() {
		if !s.local() {
			h.sendTo(s, proto.KindTickState, proto.TickState{State: state, AckTick: s.lastInput}, true)
		}
	}
}

// endMatch closes out a finished simulation, assembling the result and asking
// every participant to attest it.
func (h *Host) endMatch(final game.State) {
	// The record describes the match as it started, so someone who forfeited
	// part-way through still appears in it with the placement they earned.
	players := h.matchPlayers
	h.finalPlayers = players
	h.phase = proto.PhaseOpen
	h.clearReady()
	h.refreshAdvert()

	for _, s := range h.seatsSnapshot() {
		if !s.local() {
			h.sendTo(s, proto.KindGameOver, proto.GameOver{State: final}, false)
		}
	}
	h.emit(MatchOver{State: final, Players: players, Reason: "match complete"})

	result := h.buildResult(final, players)
	rec, err := proto.NewAttestedRecord(result)
	if err != nil {
		h.srv.Log().Logf("netplay: building match record: %v", err)
		h.resetAfterMatch()
		return
	}
	// The host signs its own result immediately; everyone else is asked.
	if sig, err := proto.SignResult(h.srv.Identity().Private, rec.Result); err == nil {
		if err := rec.AddSignature(sig); err != nil {
			h.srv.Log().Logf("netplay: signing own result: %v", err)
		}
	}
	h.record = &rec
	h.attestUntil = time.Now().Add(attestWindow)
	if s := h.seatByID(0); s != nil {
		s.signed = true
	}

	for _, s := range h.seatsSnapshot() {
		if s.local() {
			continue
		}
		s.signed = false
		h.sendTo(s, proto.KindAttestRequest, proto.AttestRequest{Result: rec.Result, Hash: rec.Hash}, false)
	}
	h.sim = nil
	h.broadcastLobby()
}

// buildResult renders the finished simulation as a canonical match result.
func (h *Host) buildResult(final game.State, players []proto.Player) proto.MatchResult {
	byKey := make(map[game.PlayerID]proto.Player, len(players))
	bots := 0
	for _, p := range players {
		if p.Bot {
			bots++
			continue
		}
		byKey[p.Seat] = p
	}
	r := proto.MatchResult{
		Version:    proto.MatchResultVersion,
		MatchID:    h.matchID,
		LobbyName:  h.name,
		Config:     h.matchCfg,
		StartedAt:  proto.FormatTime(h.startedAt),
		EndedAt:    proto.FormatTime(time.Now()),
		HostPubKey: h.srv.Identity().PubKey(),
	}
	// A bot has no key and signs nothing, so it is not a participant. The
	// count travels in the config instead, which keeps a record with bots in
	// it from reading as though it were a full field of people.
	r.Config.Bots = bots
	for _, p := range players {
		if p.Bot {
			continue
		}
		r.Participants = append(r.Participants, proto.Participant{
			PubKey:      p.PubKey,
			DisplayName: p.DisplayName,
			Login:       p.Login,
			Node:        p.Node,
			Seat:        p.Seat,
		})
	}
	for _, sn := range final.Snakes {
		p, ok := byKey[sn.ID]
		if !ok {
			continue
		}
		survival := sn.DiedAtTick
		if survival < 0 {
			survival = final.Tick
		}
		r.Placements = append(r.Placements, proto.Placement{
			PubKey:        p.PubKey,
			Place:         sn.Placement,
			Length:        sn.MaxLength,
			Score:         sn.Score,
			Kills:         sn.Kills,
			SurvivalTicks: survival,
		})
	}
	r.Normalize()
	return r
}

// allSigned reports whether every still-seated participant has attested.
func (h *Host) allSigned() bool {
	for _, s := range h.seats {
		if s.bot {
			continue // a bot has no key and will never answer
		}
		if !s.signed {
			return false
		}
	}
	return true
}

// finalizeRecord stores the assembled record and shares it with everyone.
// A record missing signatures — someone dropped, or did not answer in time —
// is stored anyway and marked partially attested.
func (h *Host) finalizeRecord() {
	rec := *h.record
	h.record = nil

	if _, err := h.srv.Store().Put(rec); err != nil {
		h.srv.Log().Logf("netplay: storing match record: %v", err)
	}
	for _, s := range h.seatsSnapshot() {
		if !s.local() {
			h.sendTo(s, proto.KindAttestedRecord, proto.AttestedRecordMsg{Record: rec}, false)
		}
	}
	h.emit(Attested{Record: rec})
	h.note("match recorded (%s)", rec.AttestationSummary())
	h.resetAfterMatch()
}

// resetAfterMatch returns the lobby to its open state so the group can play
// again without rebuilding it.
func (h *Host) resetAfterMatch() {
	h.phase = proto.PhaseOpen
	h.clearReady()
	h.refreshAdvert()
	h.broadcastLobby()
}

// clearReady un-readies everyone, which is what a match ending should do.
func (h *Host) clearReady() {
	for _, s := range h.seats {
		// A bot has nothing to decide, so it stays ready.
		s.player.Ready = s.bot
	}
	h.lastCount = 0
}

// accept takes ownership of an inbound play connection and tries to seat it.
// It runs on the listener's goroutine, so all state changes are handed to the
// host goroutine and awaited.
func (h *Host) accept(ctx context.Context, conn *proto.Conn, hello proto.Hello, who whoIsResult) {
	env, err := conn.RecvTimeout(handshakeTimeout)
	if err != nil {
		conn.Close()
		return
	}
	if env.Kind != proto.KindJoinLobby {
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{
			Code: proto.ErrBadRequest, Message: "expected a join request",
		})
		conn.Close()
		return
	}
	join, err := proto.Decode[proto.JoinLobby](env)
	if err != nil {
		conn.Close()
		return
	}

	type reply struct {
		seat game.PlayerID
		err  *proto.ErrorMsg
	}
	replies := make(chan reply, 1)
	select {
	case h.cmds <- func() {
		s, errMsg := h.allocate(conn, join, hello, who)
		if errMsg != nil {
			replies <- reply{err: errMsg}
			return
		}
		replies <- reply{seat: s.id}
	}:
	case <-h.ctx.Done():
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{Code: proto.ErrLobbyGone, Message: "the lobby closed"})
		conn.Close()
		return
	case <-ctx.Done():
		conn.Close()
		return
	}

	select {
	case r := <-replies:
		if r.err != nil {
			conn.SendTimeout(time.Second, proto.KindError, *r.err)
			conn.Close()
		}
	case <-h.ctx.Done():
		conn.Close()
	}
}

// allocate seats a joining player. It runs on the host goroutine and, on
// success, spins up that seat's reader and writer.
func (h *Host) allocate(conn *proto.Conn, join proto.JoinLobby, hello proto.Hello, who whoIsResult) (*seat, *proto.ErrorMsg) {
	switch {
	case h.phase == proto.PhaseClosed:
		return nil, &proto.ErrorMsg{Code: proto.ErrLobbyGone, Message: "the lobby closed"}
	case h.phase == proto.PhaseInGame || h.phase == proto.PhaseCountdown:
		return nil, &proto.ErrorMsg{Code: proto.ErrInProgress, Message: "a match is already under way"}
	case len(h.seats) >= h.cfg.MaxPlayers:
		return nil, &proto.ErrorMsg{Code: proto.ErrLobbyFull, Message: fmt.Sprintf("all %d seats are taken", h.cfg.MaxPlayers)}
	}
	pubKey := firstNonEmpty(join.PubKey, hello.PubKey)
	if pubKey == "" {
		return nil, &proto.ErrorMsg{Code: proto.ErrBadRequest, Message: "no signing key offered"}
	}
	if _, err := proto.DecodeKey(pubKey); err != nil {
		return nil, &proto.ErrorMsg{Code: proto.ErrBadRequest, Message: "malformed signing key"}
	}
	for _, s := range h.seats {
		if s.player.PubKey == pubKey {
			return nil, &proto.ErrorMsg{Code: proto.ErrBadRequest, Message: "that install is already seated"}
		}
	}

	id := h.freeSeat()
	s := &seat{
		id:   id,
		conn: conn,
		out:  make(chan proto.Envelope, outboxDepth),
		player: proto.Player{
			Seat:        id,
			PubKey:      pubKey,
			DisplayName: proto.SanitizeDisplayName(firstNonEmpty(join.DisplayName, hello.DisplayName)),
			Login:       who.login,
			Node:        firstNonEmpty(who.node, hello.Hostname),
			Palette:     h.freePalette(),
			Connected:   true,
		},
		lastSeen: time.Now(),
		done:     make(chan struct{}),
	}
	h.seats = append(h.seats, s)
	sort.Slice(h.seats, func(i, j int) bool { return h.seats[i].id < h.seats[j].id })

	h.wg.Add(2)
	go h.writeLoop(s)
	go h.readLoop(s)

	h.sendTo(s, proto.KindJoinOK, proto.JoinOK{LobbyID: h.id, Seat: id}, false)
	h.note("%s joined", s.player.DisplayName)
	h.refreshAdvert()
	h.broadcastLobby()
	return s, nil
}

// syncBots adds or removes bot seats so their number matches the configured
// count. Bots are seated in the lobby rather than conjured at kickoff, so the
// roster always shows who is actually going to play and joins are naturally
// limited to the seats left over.
func (h *Host) syncBots() {
	want := h.cfg.Bots
	if want > h.cfg.MaxPlayers-1 {
		want = h.cfg.MaxPlayers - 1
	}
	if want < 0 {
		want = 0
	}

	current := h.botSeats()
	for len(current) > want {
		last := current[len(current)-1]
		h.note("%s left", last.player.DisplayName)
		h.removeSeat(last.id, "bot removed")
		current = h.botSeats()
	}
	for len(current) < want {
		if len(h.seats) >= h.cfg.MaxPlayers {
			break
		}
		id := h.freeSeat()
		s := &seat{
			id:  id,
			bot: true,
			player: proto.Player{
				Seat:        id,
				DisplayName: botName(len(current)),
				Palette:     h.freePalette(),
				// A bot is always ready; it is never what the lobby waits for.
				Ready:     true,
				Bot:       true,
				Connected: true,
			},
			lastSeen: time.Now(),
			done:     make(chan struct{}),
		}
		h.seats = append(h.seats, s)
		sort.Slice(h.seats, func(i, j int) bool { return h.seats[i].id < h.seats[j].id })
		h.note("%s joined", s.player.DisplayName)
		current = h.botSeats()
	}
}

// botSeats returns the bot seats in seat order.
func (h *Host) botSeats() []*seat {
	var out []*seat
	for _, s := range h.seats {
		if s.bot {
			out = append(out, s)
		}
	}
	return out
}

// botName labels the nth bot.
func botName(n int) string { return fmt.Sprintf("bot %d", n+1) }

// humanSeats returns the seats held by people.
func (h *Host) humanSeats() int {
	n := 0
	for _, s := range h.seats {
		if !s.bot {
			n++
		}
	}
	return n
}

// Reconfigure applies new settings to an open lobby, so a host can adjust the
// arena without tearing the room down and asking everyone to rejoin.
//
// Changing the settings un-readies everyone: people agreed to play the old
// configuration, and starting them on a different one without a fresh ready
// check would be a surprise.
func (h *Host) Reconfigure(name string, cfg game.Config) {
	h.do(func() {
		if h.phase != proto.PhaseOpen && h.phase != proto.PhaseCountdown {
			h.note("settings cannot change once a match has started")
			h.broadcastLobby()
			return
		}
		if err := cfg.Validate(); err != nil {
			h.note("settings rejected: %v", err)
			h.broadcastLobby()
			return
		}
		// Seats already taken by people put a floor under the seat count.
		if humans := h.humanSeats(); cfg.MaxPlayers < humans {
			cfg.MaxPlayers = humans
		}
		if cfg.Bots > cfg.MaxPlayers-1 {
			cfg.Bots = cfg.MaxPlayers - 1
		}

		h.cfg = cfg
		h.gen++
		if trimmed := proto.SanitizeDisplayName(name); trimmed != "" {
			h.name = trimmed
		}
		h.phase = proto.PhaseOpen
		h.clearReady()
		h.syncBots()
		h.dropSeatsBeyondCapacity()
		h.note("the host changed the settings")
		h.refreshAdvert()
		h.broadcastLobby()
	})
}

// dropSeatsBeyondCapacity removes the most recently seated players when the
// seat count is lowered below the number of people in the room.
func (h *Host) dropSeatsBeyondCapacity() {
	for len(h.seats) > h.cfg.MaxPlayers {
		victim := h.seats[len(h.seats)-1]
		if victim.player.Host {
			return // never the host's own seat
		}
		h.note("%s was removed: the lobby got smaller", victim.player.DisplayName)
		if !victim.local() {
			h.sendTo(victim, proto.KindKick, proto.Kick{
				Seat: victim.id, Reason: "the host reduced the number of seats",
			}, false)
		}
		h.removeSeat(victim.id, "lobby resized")
	}
}

// freeSeat returns the lowest unused seat index.
func (h *Host) freeSeat() game.PlayerID {
	used := make(map[game.PlayerID]bool, len(h.seats))
	for _, s := range h.seats {
		used[s.id] = true
	}
	for id := game.PlayerID(0); int(id) < game.MaxPlayers; id++ {
		if !used[id] {
			return id
		}
	}
	return game.PlayerID(len(h.seats))
}

// freePalette returns the lowest unused colour/glyph slot, so players keep
// distinct identities even as seats are vacated and refilled.
func (h *Host) freePalette() int {
	used := make(map[int]bool, len(h.seats))
	for _, s := range h.seats {
		used[s.player.Palette] = true
	}
	for i := range game.MaxPlayers {
		if !used[i] {
			return i
		}
	}
	return 0
}

// readLoop consumes one client's messages until the connection ends.
func (h *Host) readLoop(s *seat) {
	defer h.wg.Done()
	defer h.do(func() {
		if h.seatByID(s.id) == s {
			h.note("%s left", s.player.DisplayName)
			h.removeSeat(s.id, "disconnected")
			h.broadcastLobby()
		}
	})

	for {
		env, err := s.conn.Recv()
		if err != nil {
			return
		}
		h.do(func() { h.handleClient(s, env) })
		select {
		case <-s.done:
			return
		case <-h.ctx.Done():
			return
		default:
		}
	}
}

// handleClient folds one client message into lobby or game state. It runs on
// the host goroutine.
func (h *Host) handleClient(s *seat, env proto.Envelope) {
	if h.seatByID(s.id) != s {
		return // the seat was already removed
	}
	s.lastSeen = time.Now()
	if s.coasting {
		s.coasting = false
		s.player.Connected = true
		if h.sim != nil {
			h.sim.SetCoasting(s.id, false)
		}
		h.note("%s reconnected", s.player.DisplayName)
		h.broadcastLobby()
	}

	switch env.Kind {
	case proto.KindReady:
		msg, err := proto.Decode[proto.Ready](env)
		if err != nil {
			return
		}
		h.setReady(s.id, msg.Ready, msg.Gen)
	case proto.KindInput:
		msg, err := proto.Decode[proto.Input](env)
		if err != nil {
			return
		}
		h.queueInput(s.id, msg.Dir, msg.ClientTick)
	case proto.KindAttestation:
		msg, err := proto.Decode[proto.Attestation](env)
		if err != nil {
			return
		}
		h.addAttestation(s, msg)
	case proto.KindLeave:
		h.note("%s left", s.player.DisplayName)
		h.removeSeat(s.id, "left")
		h.broadcastLobby()
	case proto.KindPing:
		msg, err := proto.Decode[proto.Ping](env)
		if err != nil {
			return
		}
		h.sendTo(s, proto.KindPong, proto.Pong{Nonce: msg.Nonce}, true)
	case proto.KindPong:
		// lastSeen was already refreshed above; nothing else to do.
	}
}

// setReady updates a seat's ready flag and broadcasts the change.
//
// gen is the settings generation the player was looking at. A ready that
// crossed paths with a settings change is dropped, so nobody is committed to a
// configuration they never saw; the re-broadcast puts them back in sync.
func (h *Host) setReady(id game.PlayerID, ready bool, gen int) {
	s := h.seatByID(id)
	if s == nil {
		return
	}
	if gen != h.gen {
		h.broadcastLobby()
		return
	}
	if s.player.Ready == ready {
		return
	}
	if h.phase != proto.PhaseOpen && h.phase != proto.PhaseCountdown {
		return
	}
	s.player.Ready = ready
	if ready {
		h.note("%s is ready", s.player.DisplayName)
	} else {
		h.note("%s is not ready", s.player.DisplayName)
	}
	h.broadcastLobby()
}

// queueInput records a heading change for the next tick. Only the most recent
// input in a tick window counts, so holding a key cannot outrun the simulation.
func (h *Host) queueInput(id game.PlayerID, dir game.Direction, clientTick int) {
	if h.phase != proto.PhaseInGame {
		return
	}
	h.pending[id] = pendingInput{dir: dir, tick: clientTick}
}

// addAttestation folds a participant's signature into the pending record.
func (h *Host) addAttestation(s *seat, msg proto.Attestation) {
	if h.record == nil || msg.MatchID != h.record.Result.MatchID {
		return
	}
	if msg.PubKey != s.player.PubKey {
		h.srv.Log().Logf("netplay: %s attested under a different key", s.player.DisplayName)
		return
	}
	if err := h.record.AddSignature(proto.Signature{PubKey: msg.PubKey, Sig: msg.Sig}); err != nil {
		h.srv.Log().Logf("netplay: attestation from %s: %v", s.player.DisplayName, err)
		return
	}
	s.signed = true
}

// removeSeat drops a seat and tears its connection down.
//
// Leaving a match in progress forfeits it: the snake is eliminated rather than
// left to coast, so the board reflects who is actually still playing and the
// match can reach its end.
func (h *Host) removeSeat(id game.PlayerID, reason string) {
	for i, s := range h.seats {
		if s.id != id {
			continue
		}
		if s.player.Host {
			return // the host's own seat only goes away with the lobby
		}
		if h.sim != nil && h.phase == proto.PhaseInGame {
			h.sim.Eliminate(id)
		}
		if s.local() {
			// A bot: nothing to disconnect, just take the seat away.
			h.seats = append(h.seats[:i], h.seats[i+1:]...)
			h.refreshAdvert()
			return
		}
		h.seats = append(h.seats[:i], h.seats[i+1:]...)
		h.closeSeat(s)
		h.refreshAdvert()
		return
	}
}

// closeSeat shuts one seat's connection and goroutines down exactly once.
func (h *Host) closeSeat(s *seat) {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.out != nil {
			close(s.out)
		}
		if s.conn != nil {
			// Give the writer a moment to flush a final control message such
			// as a kick or a close notice before the socket goes away.
			time.AfterFunc(250*time.Millisecond, func() { s.conn.Close() })
		}
	})
}

// writeLoop drains a seat's outbox. Keeping writes off the host goroutine is
// what stops one slow client from stalling everybody else's game.
func (h *Host) writeLoop(s *seat) {
	defer h.wg.Done()
	for env := range s.out {
		s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := s.conn.SendEnvelope(env); err != nil {
			s.conn.Close()
			// Drain so the closer never blocks on a full buffer.
			for range s.out {
			}
			return
		}
	}
}

// sendTo queues a message for one seat. A droppable message is discarded when
// the client is backed up — tick states are snapshots, so losing an old one is
// harmless. A non-droppable message that will not fit means the client is
// beyond saving, and its seat is closed.
func (h *Host) sendTo(s *seat, kind proto.Kind, body any, droppable bool) {
	if s.local() || s.out == nil {
		return
	}
	env, err := proto.NewEnvelope(kind, body)
	if err != nil {
		h.srv.Log().Logf("netplay: encoding %s: %v", kind, err)
		return
	}
	select {
	case s.out <- env:
		return
	default:
	}
	if !droppable {
		h.srv.Log().Logf("netplay: %s is too far behind; dropping the seat", s.player.DisplayName)
		h.removeSeat(s.id, "too far behind")
		return
	}
	// Make room by discarding the oldest queued frame.
	select {
	case <-s.out:
	default:
	}
	select {
	case s.out <- env:
	default:
	}
}

// broadcastLobby publishes the roster to every client and the local UI.
func (h *Host) broadcastLobby() {
	state := h.lobbyState()
	h.emit(LobbyUpdate{State: state})
	for _, s := range h.seatsSnapshot() {
		if !s.local() {
			h.sendTo(s, proto.KindLobbyState, state, false)
		}
	}
}

// lobbyState renders the current roster.
func (h *Host) lobbyState() proto.LobbyState {
	st := proto.LobbyState{
		LobbyID: h.id,
		Name:    h.name,
		Config:  h.cfg,
		Gen:     h.gen,
		Phase:   h.phase,
		Players: h.playerList(),
		Events:  append([]proto.LobbyEvent(nil), h.feed...),
	}
	if h.phase == proto.PhaseCountdown {
		st.Countdown = max(h.lastCount, 0)
	}
	return st
}

// playerList returns the roster in seat order.
func (h *Host) playerList() []proto.Player {
	out := make([]proto.Player, 0, len(h.seats))
	for _, s := range h.seats {
		out = append(out, s.player)
	}
	return out
}

// seatsSnapshot copies the seat list. Any loop that might remove a seat — and
// a non-droppable send can, when a client is too far behind — must walk a copy:
// removeSeat shifts the backing array in place, so mutating the slice a range
// is walking would silently skip or revisit seats.
func (h *Host) seatsSnapshot() []*seat {
	return append([]*seat(nil), h.seats...)
}

// seatByID finds a seat.
func (h *Host) seatByID(id game.PlayerID) *seat {
	for _, s := range h.seats {
		if s.id == id {
			return s
		}
	}
	return nil
}

// note appends a line to the in-lobby activity feed.
func (h *Host) note(format string, args ...any) {
	h.feed = append(h.feed, proto.LobbyEvent{At: time.Now(), Text: fmt.Sprintf(format, args...)})
	if len(h.feed) > feedLimit {
		h.feed = h.feed[len(h.feed)-feedLimit:]
	}
}

// refreshAdvert republishes the summary the listener hands to probers.
func (h *Host) refreshAdvert() {
	a := proto.Advert{
		LobbyID:   h.id,
		Name:      h.name,
		HostName:  h.srv.Identity().DisplayName,
		HostLogin: "",
		Config:    h.cfg,
		Seats:     h.cfg.MaxPlayers,
		Taken:     len(h.seats),
		Bots:      len(h.botSeats()),
		Phase:     h.phase,
	}
	h.advertMu.Lock()
	h.advert = a
	h.advertMu.Unlock()
}

// emit hands an event to the UI. Tick events are droppable for the same reason
// they are droppable on the wire; everything else must arrive.
func (h *Host) emit(ev Event) {
	select {
	case h.events <- ev:
		return
	default:
	}
	if _, isTick := ev.(Tick); isTick {
		select {
		case <-h.events:
		default:
		}
	}
	select {
	case h.events <- ev:
	default:
	}
}

// Wait blocks until the lobby has fully shut down. It is used on quit so the
// process does not exit with connections half-closed.
func (h *Host) Wait() { h.wg.Wait() }

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
