// Package tsnode owns the embedded Tailscale node: its lifecycle, its
// onboarding state machine, and the handful of network primitives the rest of
// tailsnail needs.
//
// The node is deliberately non-ephemeral and holds no auth key by default.
// That combination is what produces the intended first-run experience: with no
// key, tsnet begins Tailscale's interactive device-authorisation flow and
// publishes a browse-to URL on the IPN bus, which this package surfaces as a
// Status the TUI can render properly instead of letting it escape to stderr.
// Because the node key is persisted under Dir, every later run reaches Running
// with no interaction at all.
package tsnode

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

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/store"
)

// Phase is the coarse onboarding state the UI renders. It collapses the
// several ipn.State values that mean the same thing to a player.
type Phase int

// The onboarding phases, roughly in the order a first run passes through them.
const (
	// PhaseStarting is the brief window before the backend reports anything.
	PhaseStarting Phase = iota
	// PhaseNeedsLogin means a browse-to URL is available and the user has to
	// authorise this device once.
	PhaseNeedsLogin
	// PhaseNeedsApproval means the tailnet requires an admin to approve the
	// new device. There is nothing the user can do from here.
	PhaseNeedsApproval
	// PhaseConnecting is the node coming up with credentials it already has.
	PhaseConnecting
	// PhaseRunning means the node is on the tailnet and reachable.
	PhaseRunning
	// PhaseStopped means the backend is logged out or deliberately down.
	PhaseStopped
	// PhaseFailed carries a terminal error in Status.Err.
	PhaseFailed
)

// String implements fmt.Stringer.
func (p Phase) String() string {
	switch p {
	case PhaseStarting:
		return "starting"
	case PhaseNeedsLogin:
		return "needs-login"
	case PhaseNeedsApproval:
		return "needs-approval"
	case PhaseConnecting:
		return "connecting"
	case PhaseRunning:
		return "running"
	case PhaseStopped:
		return "stopped"
	case PhaseFailed:
		return "failed"
	}
	return "unknown"
}

// Self describes this node's own tailnet identity, shown after connecting.
type Self struct {
	DNSName  string // full MagicDNS name, trailing dot stripped
	Hostname string // the short name the node registered under
	IPv4     string
	IPv6     string
	Login    string // tailnet user this node belongs to
	Tailnet  string // tailnet display name, when control reports one
}

// Short returns the MagicDNS name without its tailnet suffix, which is what
// players actually recognise each other by.
func (s Self) Short() string {
	if s.DNSName == "" {
		return s.Hostname
	}
	if i := strings.IndexByte(s.DNSName, '.'); i > 0 {
		return s.DNSName[:i]
	}
	return s.DNSName
}

// Status is a snapshot of the node's onboarding and connection state.
type Status struct {
	Phase   Phase
	AuthURL string // set while PhaseNeedsLogin
	Self    Self
	Health  []string // backend health warnings, e.g. "no internet connection"
	Err     error
	// Since is when the node entered this phase, so the UI can show how long
	// a slow connect has been going.
	Since time.Time
}

// Options configures a node.
type Options struct {
	// StateDir is the tailsnail state directory; tsnet state lives beneath it.
	StateDir string
	// Hostname is the name this node registers under. It is sanitised.
	Hostname string
	// AuthKey, when set, skips the interactive flow. It comes from TS_AUTHKEY
	// and exists for CI and scripted testing; the interactive path is the
	// primary one.
	AuthKey string
	// Log receives all tsnet output so none of it reaches the terminal.
	Log *logring.Ring
}

// Node is a running embedded Tailscale node.
type Node struct {
	srv *tsnet.Server
	lc  *local.Client
	log *logring.Ring

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.RWMutex
	status  Status
	updates chan Status

	closeOnce sync.Once
}

// Start brings up the embedded node without blocking on authorisation. The
// caller drives the UI from Updates; the node reaches PhaseRunning on its own
// once the user (or a stored node key) has authorised it.
func Start(ctx context.Context, opts Options) (*Node, error) {
	if opts.Log == nil {
		opts.Log = logring.New(logring.DefaultCapacity)
	}
	if opts.StateDir == "" {
		return nil, errors.New("tsnode: no state directory")
	}
	tsDir := store.TsnetDir(opts.StateDir)
	if err := store.EnsureDir(tsDir); err != nil {
		return nil, err
	}

	hostname := SanitizeHostname(opts.Hostname)
	if hostname == "" {
		return nil, fmt.Errorf("tsnode: %q does not reduce to a usable hostname", opts.Hostname)
	}

	srv := &tsnet.Server{
		Dir:      tsDir,
		Hostname: hostname,
		// The node identity must persist across runs so that authorising the
		// device once is genuinely once.
		Ephemeral: false,
		AuthKey:   opts.AuthKey,
		// Both log sinks go to the ring. tsnet writes the auth URL through
		// Logf, so leaving either at its default would scribble over the TUI.
		Logf:     opts.Log.Logf,
		UserLogf: opts.Log.Logf,
	}

	nodeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	n := &Node{
		srv:     srv,
		log:     opts.Log,
		ctx:     nodeCtx,
		cancel:  cancel,
		updates: make(chan Status, 16),
		status:  Status{Phase: PhaseStarting, Since: time.Now()},
	}

	if err := srv.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("tsnode: starting node: %w", err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		cancel()
		return nil, fmt.Errorf("tsnode: obtaining local client: %w", err)
	}
	n.lc = lc

	n.wg.Add(1)
	go n.watch()

	// Stop the node when the caller's context is cancelled, so a SIGINT
	// during onboarding still tears the backend down cleanly.
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		select {
		case <-ctx.Done():
			n.cancel()
		case <-nodeCtx.Done():
		}
	}()

	return n, nil
}

// Updates returns the channel of status changes. It is never closed while the
// node is running; Close stops publishing to it.
func (n *Node) Updates() <-chan Status { return n.updates }

// Status returns the most recent status.
func (n *Node) Status() Status {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.status
}

// LocalClient exposes the Tailscale local API client, used for peer status
// and WhoIs lookups.
func (n *Node) LocalClient() *local.Client { return n.lc }

// Listen binds a listener on the node's tailnet addresses only. Nothing in
// tailsnail ever binds a public interface; this is the single entry point.
func (n *Node) Listen(network, addr string) (net.Listener, error) {
	return n.srv.Listen(network, addr)
}

// Dial opens a connection over the tailnet.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// WhoIs identifies the tailnet user and device behind a connection. The
// trusted-peer model takes its answer at face value: Tailscale ACLs are the
// security boundary, so this is for display, not authorisation.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	return n.lc.WhoIs(ctx, remoteAddr)
}

// NetStatus returns the full tailnet status including peers, which discovery
// uses to enumerate candidates.
func (n *Node) NetStatus(ctx context.Context) (*ipnstate.Status, error) {
	return n.lc.Status(ctx)
}

// TailscaleIPs returns this node's addresses.
func (n *Node) TailscaleIPs() (v4, v6 netip.Addr) { return n.srv.TailscaleIPs() }

// Relogin restarts the interactive login flow, used when a node key expires
// or the user asks to re-authenticate from the settings screen.
func (n *Node) Relogin(ctx context.Context) error {
	if err := n.lc.StartLoginInteractive(ctx); err != nil {
		return fmt.Errorf("tsnode: restarting login: %w", err)
	}
	return nil
}

// Close shuts the node down. It is safe to call more than once.
func (n *Node) Close() error {
	var err error
	n.closeOnce.Do(func() {
		n.cancel()
		err = n.srv.Close()
		n.wg.Wait()
	})
	return err
}

// watch consumes the IPN bus for the life of the node, translating backend
// notifications into Status updates. The bus can drop out — the backend
// restarting, say — so the watch reconnects rather than leaving the UI stuck.
func (n *Node) watch() {
	defer n.wg.Done()

	backoff := 250 * time.Millisecond
	for n.ctx.Err() == nil {
		watcher, err := n.lc.WatchIPNBus(n.ctx, ipn.NotifyInitialState)
		if err != nil {
			if n.ctx.Err() != nil {
				return
			}
			n.log.Logf("tsnode: watching IPN bus: %v", err)
			n.publishErr(fmt.Errorf("cannot reach the Tailscale backend: %w", err))
			if !n.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = 250 * time.Millisecond

		for {
			notify, err := watcher.Next()
			if err != nil {
				if n.ctx.Err() == nil {
					n.log.Logf("tsnode: IPN bus ended: %v", err)
				}
				break
			}
			n.handle(notify)
		}
		watcher.Close()
		if !n.sleep(backoff) {
			return
		}
	}
}

// sleep waits for d, reporting false if the node was closed meanwhile.
func (n *Node) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-n.ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handle folds one bus notification into the published status.
func (n *Node) handle(notify ipn.Notify) {
	n.mu.Lock()
	st := n.status
	changed := false
	n.mu.Unlock()

	if notify.ErrMessage != nil && *notify.ErrMessage != "" {
		st.Err = errors.New(*notify.ErrMessage)
		st.Phase = PhaseFailed
		changed = true
	}
	if notify.BrowseToURL != nil && *notify.BrowseToURL != "" && st.AuthURL != *notify.BrowseToURL {
		st.AuthURL = *notify.BrowseToURL
		st.Phase = PhaseNeedsLogin
		st.Err = nil
		changed = true
	}
	if h := notify.Health; h != nil {
		warnings := healthWarnings(h)
		if !equalStrings(warnings, st.Health) {
			st.Health = warnings
			changed = true
		}
	}
	if notify.State != nil {
		phase := phaseFor(*notify.State)
		if phase != st.Phase {
			st.Phase = phase
			st.Since = time.Now()
			changed = true
		}
		switch *notify.State {
		case ipn.Running:
			st.AuthURL = ""
			st.Err = nil
			if self, ok := n.selfInfo(); ok && self != st.Self {
				st.Self = self
				changed = true
			}
		case ipn.NeedsLogin:
			// tsnet has already kicked off StartLoginInteractive; the URL
			// normally arrives as BrowseToURL. Fall back to polling status in
			// case this watch attached after the URL was first published.
			if st.AuthURL == "" {
				if url := n.pollAuthURL(); url != "" {
					st.AuthURL = url
					changed = true
				}
			}
		case ipn.InUseOtherUser:
			st.Err = errors.New("another user is already using this Tailscale state directory")
			changed = true
		}
	}
	if changed {
		n.publish(st)
	}
}

// phaseFor maps a backend state onto the phase the UI renders.
func phaseFor(s ipn.State) Phase {
	switch s {
	case ipn.NeedsLogin:
		return PhaseNeedsLogin
	case ipn.NeedsMachineAuth:
		return PhaseNeedsApproval
	case ipn.Starting:
		return PhaseConnecting
	case ipn.Running:
		return PhaseRunning
	case ipn.Stopped:
		return PhaseStopped
	case ipn.InUseOtherUser:
		return PhaseFailed
	default: // ipn.NoState
		return PhaseStarting
	}
}

// pollAuthURL asks the backend directly for the current authorisation URL.
// It is a short, bounded fallback for the case where the URL was published
// before this watch attached.
func (n *Node) pollAuthURL() string {
	ctx, cancel := context.WithTimeout(n.ctx, 2*time.Second)
	defer cancel()
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		n.log.Logf("tsnode: reading status for auth URL: %v", err)
		return ""
	}
	return st.AuthURL
}

// selfInfo reads this node's identity once it is running.
func (n *Node) selfInfo() (Self, bool) {
	ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
	defer cancel()
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		n.log.Logf("tsnode: reading self status: %v", err)
		return Self{}, false
	}
	var self Self
	if st.Self != nil {
		self.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		self.Hostname = st.Self.HostName
		for _, ip := range st.Self.TailscaleIPs {
			if ip.Is4() && self.IPv4 == "" {
				self.IPv4 = ip.String()
			}
			if ip.Is6() && self.IPv6 == "" {
				self.IPv6 = ip.String()
			}
		}
		if u, ok := st.User[st.Self.UserID]; ok {
			self.Login = u.LoginName
		}
	}
	if st.CurrentTailnet != nil {
		self.Tailnet = st.CurrentTailnet.Name
	}
	return self, true
}

// healthWarnings flattens the backend health state into display strings,
// keeping only what a player can act on: warnings that affect connectivity.
// Warnings arrive in a map, so the output is sorted by warnable code to give
// the UI a stable list rather than one that reshuffles on every notification.
func healthWarnings(h *health.State) []string {
	if h == nil || len(h.Warnings) == 0 {
		return nil
	}
	codes := make([]string, 0, len(h.Warnings))
	texts := make(map[string]string, len(h.Warnings))
	for code, w := range h.Warnings {
		if !w.ImpactsConnectivity && w.Severity != health.SeverityHigh {
			continue
		}
		text := strings.TrimSpace(w.Text)
		if text == "" {
			text = strings.TrimSpace(w.Title)
		}
		if text == "" {
			continue
		}
		codes = append(codes, string(code))
		texts[string(code)] = logring.Sanitize(text)
	}
	sort.Strings(codes)
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, texts[c])
	}
	return out
}

// publish records a status and hands it to the UI, evicting a stale update if
// the consumer has fallen behind. Dropping an old status is always safe — each
// one is a complete snapshot — but the newest must never be lost.
func (n *Node) publish(st Status) {
	n.mu.Lock()
	n.status = st
	n.mu.Unlock()

	select {
	case n.updates <- st:
		return
	default:
	}
	select {
	case <-n.updates:
	default:
	}
	select {
	case n.updates <- st:
	default:
	}
}

// publishErr records a transient failure without discarding what we know.
func (n *Node) publishErr(err error) {
	n.mu.Lock()
	st := n.status
	n.mu.Unlock()
	st.Err = err
	n.publish(st)
}

// equalStrings reports whether two string slices match element for element.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SanitizeHostname reduces s to a DNS-safe label: lowercase, alphanumerics and
// hyphens only, no leading or trailing hyphen, at most 63 characters.
func SanitizeHostname(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			// Collapse any run of separators into a single hyphen.
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// DefaultHostname returns the name a node registers under when --hostname is
// not given: "tsnail-" prefixed to the machine's short hostname.
func DefaultHostname(osHostname string) string {
	if i := strings.IndexByte(osHostname, '.'); i > 0 {
		osHostname = osHostname[:i]
	}
	name := SanitizeHostname("tsnail-" + osHostname)
	if name == "" || name == "tsnail" {
		return "tsnail-node"
	}
	return name
}
