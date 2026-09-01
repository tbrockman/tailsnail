package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"go4.org/mem"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"github.com/tbrockman/tailsnail/internal/game"
	"github.com/tbrockman/tailsnail/internal/proto"
)

// fakePeer describes how a synthetic tailnet node behaves when probed.
type fakePeer struct {
	id     string
	dns    string
	host   string
	online bool
	ips    []string
	user   tailcfg.UserID

	// answer, when nil, means the node does not speak tailsnail: the dial
	// succeeds but nothing ever replies, which is how a non-player node looks.
	answer *proto.HelloOK
	// refuse makes the dial itself fail, like a node with the port closed.
	refuse bool
}

// fakeNet is a Dialer backed by in-memory pipes.
type fakeNet struct {
	mu    sync.Mutex
	peers map[string]*fakePeer // keyed by "ip:port"
	// probes counts dials per address, so backoff can be asserted.
	probes map[string]int
	// statusErr, when set, makes NetStatus fail.
	statusErr error
	// order is the peer list NetStatus reports.
	order []*fakePeer
}

func newFakeNet(peers ...*fakePeer) *fakeNet {
	f := &fakeNet{peers: map[string]*fakePeer{}, probes: map[string]int{}, order: peers}
	for _, p := range peers {
		for _, ip := range p.ips {
			f.peers[net.JoinHostPort(ip, "41649")] = p
		}
	}
	return f
}

func (f *fakeNet) NetStatus(context.Context) (*ipnstate.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	st := &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{},
		User: map[tailcfg.UserID]tailcfg.UserProfile{
			1: {LoginName: "ada@example.com"},
			2: {LoginName: "grace@example.com"},
		},
	}
	for i, p := range f.order {
		var addrs []netip.Addr
		for _, ip := range p.ips {
			addrs = append(addrs, netip.MustParseAddr(ip))
		}
		// The map key only has to be unique; discovery keys on StableNodeID.
		raw := [32]byte{}
		raw[0] = byte(i + 1)
		nk := key.NodePublicFromRaw32(mem.B(raw[:]))
		st.Peer[nk] = &ipnstate.PeerStatus{
			ID: tailcfg.StableNodeID(p.id), HostName: p.host, DNSName: p.dns,
			Online: p.online, TailscaleIPs: addrs, UserID: p.user,
		}
	}
	return st, nil
}

func (f *fakeNet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.probes[addr]++
	peer, ok := f.peers[addr]
	f.mu.Unlock()

	if !ok || peer.refuse {
		return nil, errors.New("connection refused")
	}

	client, server := net.Pipe()
	go serveFake(server, peer)
	return client, nil
}

// probeCount returns how many times an address has been dialled.
func (f *fakeNet) probeCount(ip string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes[net.JoinHostPort(ip, "41649")]
}

// serveFake plays the listening half of a handshake for a synthetic peer.
func serveFake(raw net.Conn, peer *fakePeer) {
	conn := proto.NewConn(raw)
	defer conn.Close()

	env, err := conn.RecvTimeout(3 * time.Second)
	if err != nil || env.Kind != proto.KindHello {
		return
	}
	if peer.answer == nil {
		// A node that is not running tailsnail: the port is open but nothing
		// answers, which is the case the probe timeout has to cover.
		<-time.After(5 * time.Second)
		return
	}
	conn.SendTimeout(3*time.Second, proto.KindHelloOK, *peer.answer)
	// Hold briefly so an opportunistic gossip round has somewhere to land.
	conn.RecvTimeout(500 * time.Millisecond)
}

// helloOK builds a valid handshake response, optionally advertising a lobby.
func helloOK(name string, advert *proto.Advert) *proto.HelloOK {
	return &proto.HelloOK{
		App: proto.AppName, Version: proto.Version, AppVersion: "0.1.0",
		PubKey: "abc", DisplayName: name, Hostname: "tsnail-" + name,
		Login: name + "@example.com", Advert: advert,
	}
}

// openLobby builds an advert for a joinable lobby.
func openLobby(name string, taken, seats int) *proto.Advert {
	return &proto.Advert{
		LobbyID: proto.NewMatchID(), Name: name, Config: game.DefaultConfig(),
		Seats: seats, Taken: taken, Phase: proto.PhaseOpen,
	}
}

func newProber(f *fakeNet) *Prober {
	return New(Options{Dialer: f, PubKey: "self", DisplayName: "me", Hostname: "tsnail-me"})
}

// sweepOnce runs a single forced sweep and returns the resulting peers.
func sweepOnce(t *testing.T, p *Prober, f *fakeNet) []Peer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.sweep(ctx, true)
	return p.Peers()
}

func TestOnlineCandidatesFiltersAndPrefersIPv4(t *testing.T) {
	f := newFakeNet(
		&fakePeer{id: "online4", dns: "a.tail.ts.net.", host: "a", online: true, ips: []string{"fd7a::1", "100.64.0.1"}, user: 1},
		&fakePeer{id: "offline", dns: "b.tail.ts.net.", host: "b", online: false, ips: []string{"100.64.0.2"}},
		&fakePeer{id: "noaddr", dns: "c.tail.ts.net.", host: "c", online: true},
		&fakePeer{id: "v6only", dns: "d.tail.ts.net.", host: "d", online: true, ips: []string{"fd7a::9"}},
	)
	status, err := f.NetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := onlineCandidates(status)

	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.nodeID)
	}
	want := []string{"online4", "v6only"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("candidates = %v, want %v (offline and address-less nodes are skipped)", ids, want)
	}
	if got[0].addr.String() != "100.64.0.1" {
		t.Errorf("addr = %s, want the IPv4 address to be preferred", got[0].addr)
	}
	if got[0].dnsName != "a.tail.ts.net" {
		t.Errorf("dnsName = %q, want the trailing dot stripped", got[0].dnsName)
	}
	if got[0].login != "ada@example.com" {
		t.Errorf("login = %q, want it resolved from the user map", got[0].login)
	}
	if got[1].addr.String() != "fd7a::9" {
		t.Errorf("a v6-only node should still be reachable, got %s", got[1].addr)
	}
}

func TestOnlineCandidatesIsDeterministic(t *testing.T) {
	// Status.Peer is a map, so the candidate list has to be sorted or the
	// netmap fingerprint would change on every sweep.
	f := newFakeNet(
		&fakePeer{id: "zebra", online: true, ips: []string{"100.64.0.3"}},
		&fakePeer{id: "alpha", online: true, ips: []string{"100.64.0.1"}},
		&fakePeer{id: "mango", online: true, ips: []string{"100.64.0.2"}},
	)
	var first string
	for range 20 {
		status, err := f.NetStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		fp := fingerprintOf(onlineCandidates(status))
		if first == "" {
			first = fp
		} else if fp != first {
			t.Fatal("the candidate fingerprint changed between identical sweeps")
		}
	}
}

func TestFingerprintChangesWhenAPeerJoinsOrLeaves(t *testing.T) {
	base := []candidate{{nodeID: "a"}, {nodeID: "b"}}
	joined := []candidate{{nodeID: "a"}, {nodeID: "b"}, {nodeID: "c"}}
	left := []candidate{{nodeID: "a"}}

	if fingerprintOf(base) == fingerprintOf(joined) {
		t.Error("a new peer did not change the fingerprint")
	}
	if fingerprintOf(base) == fingerprintOf(left) {
		t.Error("a departing peer did not change the fingerprint")
	}
	if fingerprintOf(base) != fingerprintOf([]candidate{{nodeID: "a"}, {nodeID: "b"}}) {
		t.Error("an unchanged peer set produced a different fingerprint")
	}
}

func TestSweepFindsAHostedLobby(t *testing.T) {
	host := &fakePeer{
		id: "host", dns: "grace.tail.ts.net.", host: "grace-laptop", online: true,
		ips: []string{"100.64.0.5"}, user: 2,
		answer: helloOK("grace", openLobby("friday night", 2, 4)),
	}
	f := newFakeNet(host)
	p := newProber(f)

	peers := sweepOnce(t, p, f)
	if len(peers) != 1 {
		t.Fatalf("found %d peers, want 1", len(peers))
	}
	got := peers[0]
	if got.DisplayName != "grace" {
		t.Errorf("display name = %q", got.DisplayName)
	}
	if got.Short != "grace" {
		t.Errorf("short name = %q, want the first DNS label", got.Short)
	}
	if !got.Hosting() {
		t.Fatal("the peer is not reported as hosting")
	}
	if got.Advert.Name != "friday night" || got.Advert.Taken != 2 {
		t.Errorf("advert = %+v", *got.Advert)
	}
	if got.Addr.String() != "100.64.0.5" {
		t.Errorf("addr = %s", got.Addr)
	}
	if got.RTT <= 0 {
		t.Error("no round-trip time was recorded")
	}
}

func TestSweepIgnoresNodesThatDoNotAnswer(t *testing.T) {
	f := newFakeNet(
		&fakePeer{id: "quiet", dns: "quiet.tail.ts.net.", online: true, ips: []string{"100.64.0.6"}},
		&fakePeer{id: "closed", dns: "closed.tail.ts.net.", online: true, ips: []string{"100.64.0.7"}, refuse: true},
		&fakePeer{id: "player", dns: "player.tail.ts.net.", online: true, ips: []string{"100.64.0.8"},
			answer: helloOK("player", nil)},
	)
	p := newProber(f)

	peers := sweepOnce(t, p, f)
	if len(peers) != 1 {
		t.Fatalf("found %d peers, want only the one running tailsnail", len(peers))
	}
	if peers[0].DisplayName != "player" {
		t.Errorf("found %q", peers[0].DisplayName)
	}
	if peers[0].Hosting() {
		t.Error("a peer with no advert is reported as hosting")
	}
}

func TestIncompatiblePeersAreRejected(t *testing.T) {
	future := helloOK("future", nil)
	future.Version = proto.Version + 1
	other := helloOK("other", nil)
	other.App = "nethack"

	f := newFakeNet(
		&fakePeer{id: "future", dns: "f.ts.net.", online: true, ips: []string{"100.64.0.9"}, answer: future},
		&fakePeer{id: "other", dns: "o.ts.net.", online: true, ips: []string{"100.64.0.10"}, answer: other},
	)
	p := newProber(f)

	if peers := sweepOnce(t, p, f); len(peers) != 0 {
		t.Fatalf("found %d peers, want none: neither speaks this protocol", len(peers))
	}
}

func TestQuietNodesBackOff(t *testing.T) {
	quiet := &fakePeer{id: "quiet", dns: "q.ts.net.", online: true, ips: []string{"100.64.0.11"}, refuse: true}
	f := newFakeNet(quiet)
	p := newProber(f)
	ctx := context.Background()

	// A first sweep probes it and finds nothing.
	p.sweep(ctx, false)
	if got := f.probeCount("100.64.0.11"); got != 1 {
		t.Fatalf("probes = %d, want 1", got)
	}

	// An unforced sweep must not re-probe until the backoff lapses.
	p.sweep(ctx, false)
	if got := f.probeCount("100.64.0.11"); got != 1 {
		t.Fatalf("probes = %d, want the backoff to suppress the second sweep", got)
	}

	// The backoff must actually grow, so a tailnet full of non-players costs
	// almost nothing to keep scanning.
	p.mu.Lock()
	e := p.cache["quiet"]
	first := e.quietFor
	e.lastProbe = time.Now().Add(-2 * MaxQuietTTL)
	p.mu.Unlock()

	p.sweep(ctx, false)
	p.mu.Lock()
	second := p.cache["quiet"].quietFor
	p.mu.Unlock()
	if second <= first {
		t.Errorf("backoff did not grow: %v then %v", first, second)
	}
	if second > MaxQuietTTL {
		t.Errorf("backoff %v exceeded the %v cap", second, MaxQuietTTL)
	}
}

func TestAnsweringPeersAreRefreshedOften(t *testing.T) {
	player := &fakePeer{id: "p", dns: "p.ts.net.", online: true, ips: []string{"100.64.0.12"},
		answer: helloOK("p", openLobby("game", 1, 4))}
	f := newFakeNet(player)
	p := newProber(f)
	ctx := context.Background()

	p.sweep(ctx, false)
	// Still fresh, so no second probe.
	p.sweep(ctx, false)
	if got := f.probeCount("100.64.0.12"); got != 1 {
		t.Fatalf("probes = %d, want the fresh TTL to suppress a re-probe", got)
	}

	// Once the short fresh TTL lapses it must be re-probed, so a lobby filling
	// up or closing shows in the browser within a few seconds.
	p.mu.Lock()
	p.cache["p"].lastProbe = time.Now().Add(-2 * FreshTTL)
	p.mu.Unlock()
	p.sweep(ctx, false)
	if got := f.probeCount("100.64.0.12"); got != 2 {
		t.Fatalf("probes = %d, want a re-probe after the fresh TTL", got)
	}
}

func TestNetmapChangeForcesAnImmediateReprobe(t *testing.T) {
	player := &fakePeer{id: "p", dns: "p.ts.net.", online: true, ips: []string{"100.64.0.13"},
		answer: helloOK("p", nil)}
	f := newFakeNet(player)
	p := newProber(f)
	ctx := context.Background()

	p.sweep(ctx, false)
	before := f.probeCount("100.64.0.13")

	// A new node appearing is exactly when a lobby is likely to have opened.
	newcomer := &fakePeer{id: "new", dns: "n.ts.net.", online: true, ips: []string{"100.64.0.14"},
		answer: helloOK("newcomer", openLobby("just opened", 1, 2))}
	f.mu.Lock()
	f.order = append(f.order, newcomer)
	f.peers["100.64.0.14:41649"] = newcomer
	f.mu.Unlock()

	p.sweep(ctx, false)
	if got := f.probeCount("100.64.0.13"); got <= before {
		t.Error("an existing peer was not re-probed after the netmap changed")
	}
	peers := p.Peers()
	if len(peers) != 2 {
		t.Fatalf("found %d peers, want 2", len(peers))
	}
	if !peers[0].Hosting() || peers[0].Advert.Name != "just opened" {
		t.Errorf("the newly opened lobby did not sort to the top: %+v", peers[0])
	}
}

func TestDepartedPeersArePruned(t *testing.T) {
	a := &fakePeer{id: "a", dns: "a.ts.net.", online: true, ips: []string{"100.64.0.15"}, answer: helloOK("a", nil)}
	b := &fakePeer{id: "b", dns: "b.ts.net.", online: true, ips: []string{"100.64.0.16"}, answer: helloOK("b", nil)}
	f := newFakeNet(a, b)
	p := newProber(f)
	ctx := context.Background()

	p.sweep(ctx, true)
	if got := len(p.Peers()); got != 2 {
		t.Fatalf("found %d peers, want 2", got)
	}

	f.mu.Lock()
	f.order = []*fakePeer{a}
	f.mu.Unlock()
	p.sweep(ctx, true)

	peers := p.Peers()
	if len(peers) != 1 || peers[0].NodeID != "a" {
		t.Fatalf("peers = %+v, want only a", peers)
	}
	p.mu.Lock()
	_, stillCached := p.cache["b"]
	p.mu.Unlock()
	if stillCached {
		t.Error("a peer that left the netmap is still cached")
	}
}

func TestPeersSortLobbiesFirstThenJoinable(t *testing.T) {
	inGame := openLobby("running", 4, 4)
	inGame.Phase = proto.PhaseInGame
	full := openLobby("full", 4, 4)

	f := newFakeNet(
		&fakePeer{id: "1", dns: "zeta.ts.net.", online: true, ips: []string{"100.64.1.1"}, answer: helloOK("zeta", nil)},
		&fakePeer{id: "2", dns: "yank.ts.net.", online: true, ips: []string{"100.64.1.2"}, answer: helloOK("yank", inGame)},
		&fakePeer{id: "3", dns: "xray.ts.net.", online: true, ips: []string{"100.64.1.3"}, answer: helloOK("xray", openLobby("open", 1, 4))},
		&fakePeer{id: "4", dns: "whis.ts.net.", online: true, ips: []string{"100.64.1.4"}, answer: helloOK("whis", full)},
	)
	p := newProber(f)
	peers := sweepOnce(t, p, f)
	if len(peers) != 4 {
		t.Fatalf("found %d peers, want 4", len(peers))
	}

	// The joinable lobby must lead, then the other lobbies, then the idle peer.
	if !peers[0].Hosting() || !peers[0].Advert.Joinable() {
		t.Errorf("the joinable lobby is not first: %+v", peers[0])
	}
	if peers[3].Hosting() {
		t.Errorf("an idle peer should sort last, got %+v", peers[3])
	}
	for i := 1; i < 3; i++ {
		if !peers[i].Hosting() {
			t.Errorf("peer %d should be hosting", i)
		}
	}
}

func TestSnapshotReportsANetmapFailure(t *testing.T) {
	f := newFakeNet()
	f.statusErr = errors.New("backend is down")
	p := newProber(f)

	p.sweep(context.Background(), true)
	select {
	case snap := <-p.Snapshots():
		if snap.Err == nil {
			t.Fatal("the snapshot does not report the netmap failure")
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot was published")
	}
}

func TestStalePeersExpireFromTheSnapshot(t *testing.T) {
	player := &fakePeer{id: "p", dns: "p.ts.net.", online: true, ips: []string{"100.64.0.17"},
		answer: helloOK("p", nil)}
	f := newFakeNet(player)
	p := newProber(f)
	p.sweep(context.Background(), true)

	if len(p.Peers()) != 1 {
		t.Fatal("the peer was not found")
	}
	// A peer whose last successful probe is long past must drop off rather
	// than lingering as a lobby nobody can join.
	p.mu.Lock()
	p.cache["p"].peer.LastSeen = time.Now().Add(-10 * FreshTTL)
	p.mu.Unlock()
	if got := len(p.Peers()); got != 0 {
		t.Fatalf("%d stale peers remain in the snapshot", got)
	}
}

func TestSnapshotsAlwaysCarryTheLatest(t *testing.T) {
	p := newProber(newFakeNet())
	for i := range 20 {
		p.publish(Snapshot{Candidates: i})
	}
	p.publish(Snapshot{Candidates: 999})

	var last Snapshot
	for {
		select {
		case s := <-p.Snapshots():
			last = s
			continue
		default:
		}
		break
	}
	if last.Candidates != 999 {
		t.Fatalf("last snapshot = %d, want the most recent", last.Candidates)
	}
}

func TestSnapshotLobbiesFiltersNonHosts(t *testing.T) {
	snap := Snapshot{Peers: []Peer{
		{NodeID: "a", Advert: openLobby("one", 1, 4)},
		{NodeID: "b"},
		{NodeID: "c", Advert: openLobby("two", 2, 4)},
	}}
	if got := len(snap.Lobbies()); got != 2 {
		t.Fatalf("Lobbies() returned %d, want 2", got)
	}
}

func TestRefreshIsCoalesced(t *testing.T) {
	p := newProber(newFakeNet())
	// Repeated requests must not queue up work; one pending sweep is enough.
	for range 10 {
		p.Refresh()
	}
	select {
	case <-p.refresh:
	default:
		t.Fatal("no sweep was requested")
	}
	select {
	case <-p.refresh:
		t.Fatal("refresh requests accumulated instead of coalescing")
	default:
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	p := newProber(newFakeNet())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestDialAddr(t *testing.T) {
	peer := Peer{Addr: netip.MustParseAddr("100.64.0.1")}
	got, err := DialAddr(peer)
	if err != nil {
		t.Fatal(err)
	}
	if want := "100.64.0.1:41649"; got != want {
		t.Errorf("DialAddr = %q, want %q", got, want)
	}

	// An IPv6 peer must come back bracketed so it is a valid dial target.
	v6, err := DialAddr(Peer{Addr: netip.MustParseAddr("fd7a::1")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "[fd7a::1]:41649"; v6 != want {
		t.Errorf("DialAddr = %q, want %q", v6, want)
	}

	if _, err := DialAddr(Peer{}); !errors.Is(err, ErrNoAddress) {
		t.Errorf("err = %v, want ErrNoAddress", err)
	}
}

func TestShortName(t *testing.T) {
	cases := []struct{ dns, host, want string }{
		{"laptop.tail1234.ts.net", "laptop-hostname", "laptop"},
		{"laptop", "other", "laptop"},
		{"", "fallback", "fallback"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := shortName(tc.dns, tc.host); got != tc.want {
			t.Errorf("shortName(%q, %q) = %q, want %q", tc.dns, tc.host, got, tc.want)
		}
	}
}

func TestProbeConcurrencyIsBounded(t *testing.T) {
	// Many candidates must not mean many simultaneous dials.
	var peers []*fakePeer
	for i := range 40 {
		ip := netip.AddrFrom4([4]byte{100, 64, 2, byte(i)}).String()
		peers = append(peers, &fakePeer{
			id: ip, dns: ip + ".ts.net.", online: true, ips: []string{ip},
			answer: helloOK("p", nil),
		})
	}
	f := newFakeNet(peers...)

	var (
		mu             sync.Mutex
		inFlight, peak int
	)
	tracked := &trackingNet{fakeNet: f, onDial: func(delta int) {
		mu.Lock()
		inFlight += delta
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
	}}

	p := New(Options{Dialer: tracked, PubKey: "self", DisplayName: "me"})
	p.sweep(context.Background(), true)

	mu.Lock()
	defer mu.Unlock()
	if peak > MaxConcurrentProbes {
		t.Fatalf("peak concurrency was %d, want at most %d", peak, MaxConcurrentProbes)
	}
	if peak < 2 {
		t.Errorf("peak concurrency was %d; probes are not running in parallel at all", peak)
	}
}

// trackingNet wraps fakeNet to observe dial concurrency.
type trackingNet struct {
	*fakeNet
	onDial func(delta int)
}

func (t *trackingNet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	t.onDial(1)
	conn, err := t.fakeNet.Dial(ctx, network, addr)
	if err != nil {
		t.onDial(-1)
		return nil, err
	}
	return &trackedConn{Conn: conn, done: func() { t.onDial(-1) }}, nil
}

type trackedConn struct {
	net.Conn
	once sync.Once
	done func()
}

func (c *trackedConn) Close() error {
	c.once.Do(c.done)
	return c.Conn.Close()
}
