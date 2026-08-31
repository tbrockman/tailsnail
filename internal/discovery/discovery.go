// Package discovery finds other tailsnail peers on the tailnet.
//
// There is no registry and no broadcast: every online node in the netmap is a
// candidate, and a candidate becomes a peer by answering a short handshake on
// the well-known port. Results are cached with a TTL and re-probed when the
// netmap changes, so lobbies appear and disappear within a few seconds without
// the app hammering nodes that are not playing.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"

	"github.com/theolol/tailsnail/internal/gossip"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/version"
)

// Tunables for the probe loop. The intervals are deliberately gentle: a
// tailnet may hold hundreds of nodes that will never run tailsnail, and the
// cost of finding a lobby two seconds later is nothing next to the cost of
// probing every node every second.
const (
	// PollInterval is how often the netmap is re-read. Reading local status is
	// cheap; it is the probes that are not.
	PollInterval = 2 * time.Second
	// ProbeTimeout bounds a single handshake attempt.
	ProbeTimeout = 2500 * time.Millisecond
	// FreshTTL is how long a successful probe of a tailsnail peer is trusted.
	FreshTTL = 4 * time.Second
	// QuietTTL is the starting re-probe interval for a node that did not
	// answer. It backs off from here so silent nodes cost almost nothing.
	QuietTTL = 20 * time.Second
	// MaxQuietTTL caps that backoff.
	MaxQuietTTL = 2 * time.Minute
	// MaxConcurrentProbes bounds the fan-out of a single sweep.
	MaxConcurrentProbes = 8
	// GossipInterval is the minimum gap between anti-entropy exchanges with
	// the same peer. Match history changes rarely, so this is generous.
	GossipInterval = 90 * time.Second
)

// Dialer is the slice of the tailnet node discovery needs. Keeping it an
// interface lets the probe loop be driven by a fake in tests.
type Dialer interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	NetStatus(ctx context.Context) (*ipnstate.Status, error)
}

// Peer is one discovered tailsnail node.
type Peer struct {
	// NodeID is the stable tailnet identifier, used as the map key.
	NodeID string
	// DNSName is the peer's MagicDNS name with the trailing dot removed.
	DNSName string
	// Short is the leftmost label of DNSName, which is what players read.
	Short string
	// HostName is the OS hostname the node reported.
	HostName string
	// Login is the tailnet user the node belongs to.
	Login string
	// Addr is the tailnet address the probe connected to.
	Addr netip.Addr

	// Online reflects the netmap, independent of whether the node runs tsnail.
	Online bool
	// PubKey is the peer's tailsnail signing key, from its handshake.
	PubKey string
	// DisplayName is the name the peer plays under.
	DisplayName string
	// AppVersion is the peer's tailsnail build.
	AppVersion string
	// Advert is the lobby the peer is hosting, if any.
	Advert *proto.Advert
	// LastSeen is when the peer last answered a probe.
	LastSeen time.Time
	// RTT is how long that handshake took.
	RTT time.Duration
}

// Hosting reports whether the peer is advertising a lobby.
func (p Peer) Hosting() bool { return p.Advert != nil }

// Snapshot is the discovery state handed to the UI.
type Snapshot struct {
	At time.Time
	// Peers holds every node that answered a tailsnail handshake recently,
	// ordered lobbies-first then by name.
	Peers []Peer
	// Candidates is how many online tailnet nodes were considered.
	Candidates int
	// Scanning is true while a sweep is in flight, so the UI can spin.
	Scanning bool
	// Err carries a netmap read failure; probe failures are per-peer and are
	// simply reflected by the peer's absence.
	Err error
}

// Lobbies returns only the peers currently advertising a lobby.
func (s Snapshot) Lobbies() []Peer {
	out := make([]Peer, 0, len(s.Peers))
	for _, p := range s.Peers {
		if p.Hosting() {
			out = append(out, p)
		}
	}
	return out
}

// entry is the prober's per-candidate cache record.
type entry struct {
	peer      Peer
	lastProbe time.Time
	// quietFor is the current backoff for a node that is not running
	// tailsnail. It doubles on each silent probe and resets on a reply.
	quietFor time.Duration
	// lastGossip is when anti-entropy last ran with this peer.
	lastGossip time.Time
	// answered records whether the most recent probe got a valid handshake.
	answered bool
}

// Prober maintains the peer cache and publishes snapshots.
type Prober struct {
	dialer Dialer
	store  gossip.Recorder
	log    *logring.Ring

	// identity of this node, sent in every probe handshake.
	pubKey      string
	displayName string
	hostname    string

	mu    sync.Mutex
	cache map[string]*entry

	snapshots chan Snapshot
	refresh   chan struct{}

	// lastNetmap is a cheap fingerprint of the online node set; a change in it
	// forces an immediate sweep rather than waiting for TTLs to lapse.
	lastNetmap string
}

// Options configures a Prober.
type Options struct {
	Dialer      Dialer
	Store       gossip.Recorder
	Log         *logring.Ring
	PubKey      string
	DisplayName string
	Hostname    string
}

// New returns a Prober. Call Run to start sweeping.
func New(opts Options) *Prober {
	log := opts.Log
	if log == nil {
		log = logring.New(logring.DefaultCapacity)
	}
	return &Prober{
		dialer:      opts.Dialer,
		store:       opts.Store,
		log:         log,
		pubKey:      opts.PubKey,
		displayName: opts.DisplayName,
		hostname:    opts.Hostname,
		cache:       make(map[string]*entry),
		snapshots:   make(chan Snapshot, 4),
		refresh:     make(chan struct{}, 1),
	}
}

// Snapshots returns the channel of discovery updates.
func (p *Prober) Snapshots() <-chan Snapshot { return p.snapshots }

// Refresh asks for an immediate sweep, ignoring cache TTLs. The lobby browser
// calls it when the user presses refresh, and after leaving a lobby.
func (p *Prober) Refresh() {
	select {
	case p.refresh <- struct{}{}:
	default: // a sweep is already pending
	}
}

// Run sweeps until ctx is cancelled.
func (p *Prober) Run(ctx context.Context) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	p.sweep(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweep(ctx, false)
		case <-p.refresh:
			p.sweep(ctx, true)
		}
	}
}

// sweep reads the netmap and probes whichever candidates are due.
func (p *Prober) sweep(ctx context.Context, force bool) {
	status, err := p.dialer.NetStatus(ctx)
	if err != nil {
		p.publish(Snapshot{At: time.Now(), Err: err, Peers: p.freshPeers()})
		return
	}

	candidates := onlineCandidates(status)
	// A change in the online node set means someone just joined or left the
	// tailnet, which is exactly when a lobby is likely to have appeared.
	fingerprint := fingerprintOf(candidates)
	if fingerprint != p.lastNetmap {
		p.lastNetmap = fingerprint
		force = true
	}
	p.prune(candidates)

	due := p.due(candidates, force)
	if len(due) == 0 {
		p.publish(Snapshot{At: time.Now(), Peers: p.freshPeers(), Candidates: len(candidates)})
		return
	}

	p.publish(Snapshot{At: time.Now(), Peers: p.freshPeers(), Candidates: len(candidates), Scanning: true})

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxConcurrentProbes)
	for _, c := range due {
		wg.Add(1)
		go func(c candidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			p.probe(ctx, c)
		}(c)
	}
	wg.Wait()

	p.publish(Snapshot{At: time.Now(), Peers: p.freshPeers(), Candidates: len(candidates)})
}

// candidate is an online tailnet node worth handshaking with.
type candidate struct {
	nodeID   string
	dnsName  string
	hostName string
	login    string
	addr     netip.Addr
}

// onlineCandidates extracts the online, addressable peers from a status.
func onlineCandidates(status *ipnstate.Status) []candidate {
	if status == nil {
		return nil
	}
	out := make([]candidate, 0, len(status.Peer))
	for _, ps := range status.Peer {
		if ps == nil || !ps.Online || len(ps.TailscaleIPs) == 0 {
			continue
		}
		// Prefer IPv4: it is the address users recognise, and every tailnet
		// node has one.
		addr := ps.TailscaleIPs[0]
		for _, ip := range ps.TailscaleIPs {
			if ip.Is4() {
				addr = ip
				break
			}
		}
		c := candidate{
			nodeID:   string(ps.ID),
			dnsName:  strings.TrimSuffix(ps.DNSName, "."),
			hostName: ps.HostName,
			addr:     addr,
		}
		if u, ok := status.User[ps.UserID]; ok {
			c.login = u.LoginName
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].nodeID < out[j].nodeID })
	return out
}

// fingerprintOf summarises the candidate set so changes can be detected
// without diffing the whole netmap.
func fingerprintOf(cands []candidate) string {
	var b strings.Builder
	for _, c := range cands {
		b.WriteString(c.nodeID)
		b.WriteByte(0)
	}
	return b.String()
}

// prune drops cache entries for nodes that left the netmap.
func (p *Prober) prune(cands []candidate) {
	live := make(map[string]bool, len(cands))
	for _, c := range cands {
		live[c.nodeID] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.cache {
		if !live[id] {
			delete(p.cache, id)
		}
	}
}

// due returns the candidates whose cache entry has lapsed.
func (p *Prober) due(cands []candidate, force bool) []candidate {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []candidate
	for _, c := range cands {
		e, known := p.cache[c.nodeID]
		if !known {
			out = append(out, c)
			continue
		}
		if force {
			// A forced sweep re-probes peers and nodes that answered before,
			// but still respects the backoff for nodes that have never
			// answered — those are almost certainly not running tailsnail.
			if e.answered || e.quietFor <= QuietTTL {
				out = append(out, c)
			}
			continue
		}
		ttl := e.quietFor
		if e.answered {
			ttl = FreshTTL
		}
		if now.Sub(e.lastProbe) >= ttl {
			out = append(out, c)
		}
	}
	return out
}

// probe handshakes with one candidate and folds the result into the cache.
func (p *Prober) probe(ctx context.Context, c candidate) {
	start := time.Now()
	hello, err := p.handshake(ctx, c)

	p.mu.Lock()
	e, ok := p.cache[c.nodeID]
	if !ok {
		e = &entry{quietFor: QuietTTL}
		p.cache[c.nodeID] = e
	}
	e.lastProbe = time.Now()
	if err != nil {
		e.answered = false
		e.quietFor = min(e.quietFor*2, MaxQuietTTL)
		if e.quietFor < QuietTTL {
			e.quietFor = QuietTTL
		}
		p.mu.Unlock()
		return
	}
	e.answered = true
	e.quietFor = QuietTTL
	e.peer = Peer{
		NodeID:      c.nodeID,
		DNSName:     c.dnsName,
		Short:       shortName(c.dnsName, c.hostName),
		HostName:    c.hostName,
		Login:       firstNonEmpty(hello.Login, c.login),
		Addr:        c.addr,
		Online:      true,
		PubKey:      hello.PubKey,
		DisplayName: proto.SanitizeDisplayName(hello.DisplayName),
		AppVersion:  hello.AppVersion,
		Advert:      hello.Advert,
		LastSeen:    time.Now(),
		RTT:         time.Since(start),
	}
	p.mu.Unlock()
}

// handshake dials a candidate, exchanges Hello/HelloOK, and — when the peer is
// due an anti-entropy round — reuses the same connection to sync match
// records before hanging up.
func (p *Prober) handshake(ctx context.Context, c candidate) (proto.HelloOK, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	addr := net.JoinHostPort(c.addr.String(), fmt.Sprint(proto.Port))
	raw, err := p.dialer.Dial(ctx, "tcp", addr)
	if err != nil {
		return proto.HelloOK{}, err
	}
	conn := proto.NewConn(raw)
	defer conn.Close()

	hello := proto.Hello{
		App:         proto.AppName,
		Version:     proto.Version,
		AppVersion:  version.String(),
		PubKey:      p.pubKey,
		DisplayName: p.displayName,
		Hostname:    p.hostname,
		Intent:      proto.IntentProbe,
	}
	if err := conn.SendTimeout(ProbeTimeout, proto.KindHello, hello); err != nil {
		return proto.HelloOK{}, err
	}
	env, err := conn.RecvTimeout(ProbeTimeout)
	if err != nil {
		return proto.HelloOK{}, err
	}
	if env.Kind != proto.KindHelloOK {
		return proto.HelloOK{}, fmt.Errorf("discovery: %s answered with %s", c.dnsName, env.Kind)
	}
	ok, err := proto.Decode[proto.HelloOK](env)
	if err != nil {
		return proto.HelloOK{}, err
	}
	if !ok.Compatible() {
		return proto.HelloOK{}, fmt.Errorf("discovery: %s speaks %s protocol v%d", c.dnsName, ok.App, ok.Version)
	}

	p.maybeGossip(ctx, c, conn)
	return ok, nil
}

// maybeGossip runs an anti-entropy exchange on an open probe connection if
// this peer is due one. Failures are logged and otherwise ignored: the probe
// itself already succeeded, and history will converge on a later connection.
func (p *Prober) maybeGossip(ctx context.Context, c candidate, conn *proto.Conn) {
	if p.store == nil {
		return
	}
	p.mu.Lock()
	e, ok := p.cache[c.nodeID]
	due := !ok || time.Since(e.lastGossip) >= GossipInterval
	if due && ok {
		e.lastGossip = time.Now()
	}
	p.mu.Unlock()
	if !due {
		return
	}

	res, err := gossip.Initiate(ctx, conn, p.store)
	if err != nil {
		p.log.Logf("discovery: gossip with %s: %v", c.dnsName, err)
		return
	}
	if !res.Empty() {
		p.log.Logf("discovery: gossip with %s: %s", c.dnsName, res)
	}
	p.mu.Lock()
	if e, ok := p.cache[c.nodeID]; ok {
		e.lastGossip = time.Now()
	}
	p.mu.Unlock()
}

// freshPeers returns the cached peers that answered recently enough to still
// be believed, ordered lobbies first and then by display name.
func (p *Prober) freshPeers() []Peer {
	cutoff := time.Now().Add(-3 * FreshTTL)
	p.mu.Lock()
	out := make([]Peer, 0, len(p.cache))
	for _, e := range p.cache {
		if e.answered && e.peer.LastSeen.After(cutoff) {
			out = append(out, e.peer)
		}
	}
	p.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Hosting() != b.Hosting() {
			return a.Hosting()
		}
		// Open lobbies sort above ones already in play.
		if a.Hosting() && b.Hosting() {
			if aj, bj := a.Advert.Joinable(), b.Advert.Joinable(); aj != bj {
				return aj
			}
		}
		if a.DisplayName != b.DisplayName {
			return a.DisplayName < b.DisplayName
		}
		return a.NodeID < b.NodeID
	})
	return out
}

// Peers returns the current fresh peer list without waiting for a sweep.
func (p *Prober) Peers() []Peer { return p.freshPeers() }

// publish hands a snapshot to the UI, dropping a stale one if the consumer is
// behind. Each snapshot is complete, so losing an old one costs nothing.
func (p *Prober) publish(s Snapshot) {
	select {
	case p.snapshots <- s:
		return
	default:
	}
	select {
	case <-p.snapshots:
	default:
	}
	select {
	case p.snapshots <- s:
	default:
	}
}

// shortName picks the friendliest label available for a node.
func shortName(dnsName, hostName string) string {
	if dnsName != "" {
		if i := strings.IndexByte(dnsName, '.'); i > 0 {
			return dnsName[:i]
		}
		return dnsName
	}
	return hostName
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ErrNoAddress is returned when a peer has no usable tailnet address.
var ErrNoAddress = errors.New("discovery: peer has no tailnet address")

// DialAddr returns the address to dial for a peer's control port.
func DialAddr(p Peer) (string, error) {
	if !p.Addr.IsValid() {
		return "", ErrNoAddress
	}
	return net.JoinHostPort(p.Addr.String(), fmt.Sprint(proto.Port)), nil
}
