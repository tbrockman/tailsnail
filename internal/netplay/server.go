package netplay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"

	"github.com/theolol/tailsnail/internal/gossip"
	"github.com/theolol/tailsnail/internal/logring"
	"github.com/theolol/tailsnail/internal/proto"
	"github.com/theolol/tailsnail/internal/store"
	"github.com/theolol/tailsnail/internal/tsnode"
	"github.com/theolol/tailsnail/internal/version"
)

// handshakeTimeout bounds how long an unidentified connection may sit open.
const handshakeTimeout = 5 * time.Second

// probeTailTimeout is how long a probe connection is held open after the
// handshake, in case the prober wants to follow up with a gossip round.
const probeTailTimeout = 3 * time.Second

// Node is the slice of the tailnet this package needs.
type Node interface {
	Listen(network, addr string) (net.Listener, error)
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

// Server accepts inbound tailsnail connections on the well-known port and
// routes them by the intent declared in the handshake. Exactly one Server runs
// per process, whether or not this peer is hosting.
type Server struct {
	node  Node
	store *store.Store
	ident *store.Identity
	log   *logring.Ring

	mu   sync.RWMutex
	host *Host
}

// NewServer builds a server. Call Serve to bind the listener.
func NewServer(node Node, st *store.Store, ident *store.Identity, log *logring.Ring) *Server {
	if log == nil {
		log = logring.New(logring.DefaultCapacity)
	}
	return &Server{node: node, store: st, ident: ident, log: log}
}

// Serve binds the well-known port on the node's tailnet addresses and accepts
// until ctx is cancelled. It never binds a public interface: the listener
// comes from the embedded node, so it only exists inside the tailnet.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.node.Listen("tcp", fmt.Sprintf(":%d", proto.Port))
	if err != nil {
		return fmt.Errorf("netplay: listening on port %d: %w", proto.Port, err)
	}
	s.log.Logf("netplay: listening on tailnet port %d", proto.Port)

	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	defer func() {
		ln.Close()
		wg.Wait()
	}()

	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A transient accept failure should not take the app down; back
			// off briefly and keep serving.
			s.log.Logf("netplay: accept: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, raw)
		}()
	}
}

// setHost installs or clears the lobby this peer is hosting.
func (s *Server) setHost(h *Host) {
	s.mu.Lock()
	s.host = h
	s.mu.Unlock()
}

// currentHost returns the lobby this peer hosts, if any.
func (s *Server) currentHost() *Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host
}

// Advert returns the lobby summary to publish in handshakes, or nil.
func (s *Server) Advert() *proto.Advert {
	if h := s.currentHost(); h != nil {
		return h.Advert()
	}
	return nil
}

// handle runs one inbound connection through the handshake and dispatches it.
func (s *Server) handle(ctx context.Context, raw net.Conn) {
	conn := proto.NewConn(raw)
	remote := raw.RemoteAddr().String()

	env, err := conn.RecvTimeout(handshakeTimeout)
	if err != nil {
		conn.Close()
		return
	}
	if env.Kind != proto.KindHello {
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{
			Code: proto.ErrBadRequest, Message: "expected a hello",
		})
		conn.Close()
		return
	}
	hello, err := proto.Decode[proto.Hello](env)
	if err != nil {
		conn.Close()
		return
	}
	if !hello.Compatible() {
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{
			Code:    proto.ErrVersionMismatch,
			Message: fmt.Sprintf("this peer speaks %s protocol v%d", proto.AppName, proto.Version),
		})
		conn.Close()
		return
	}

	// WhoIs is display-only. The trusted-peer model puts the security boundary
	// at the tailnet ACL, so an answer here is used to label a player, never to
	// decide whether they may play.
	who := s.whoIs(ctx, remote)

	if err := conn.SendTimeout(handshakeTimeout, proto.KindHelloOK, proto.HelloOK{
		App:         proto.AppName,
		Version:     proto.Version,
		AppVersion:  version.String(),
		PubKey:      s.ident.PubKey(),
		DisplayName: s.ident.DisplayName,
		Hostname:    who.selfHint,
		Login:       who.login,
		Advert:      s.Advert(),
	}); err != nil {
		conn.Close()
		return
	}

	switch hello.Intent {
	case proto.IntentPlay:
		s.serveJoin(ctx, conn, hello, who)
	case proto.IntentGossip:
		s.serveGossip(ctx, conn)
		conn.Close()
	default: // IntentProbe, and anything unrecognised
		s.serveProbeTail(ctx, conn)
		conn.Close()
	}
}

// serveProbeTail lets a prober reuse its handshake connection for one gossip
// round. A prober with nothing to sync simply hangs up, which surfaces here as
// a read error and is not worth logging.
func (s *Server) serveProbeTail(ctx context.Context, conn *proto.Conn) {
	env, err := conn.RecvTimeout(probeTailTimeout)
	if err != nil {
		return
	}
	if env.Kind != proto.KindGossipInv {
		return
	}
	inv, err := proto.Decode[proto.GossipInv](env)
	if err != nil {
		return
	}
	s.runGossip(ctx, conn, inv)
}

// serveGossip handles a connection opened solely to sync match records.
func (s *Server) serveGossip(ctx context.Context, conn *proto.Conn) {
	env, err := conn.RecvTimeout(gossip.ExchangeTimeout)
	if err != nil {
		return
	}
	if env.Kind != proto.KindGossipInv {
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{
			Code: proto.ErrBadRequest, Message: "expected a gossip inventory",
		})
		return
	}
	inv, err := proto.Decode[proto.GossipInv](env)
	if err != nil {
		return
	}
	s.runGossip(ctx, conn, inv)
}

// runGossip performs the listening half of an anti-entropy exchange.
func (s *Server) runGossip(ctx context.Context, conn *proto.Conn, inv proto.GossipInv) {
	if s.store == nil {
		return
	}
	res, err := gossip.Respond(ctx, conn, s.store, inv)
	if err != nil {
		s.log.Logf("netplay: gossip: %v", err)
		return
	}
	if !res.Empty() {
		s.log.Logf("netplay: gossip: %s", res)
	}
}

// serveJoin hands a play connection to the hosted lobby, or refuses it.
func (s *Server) serveJoin(ctx context.Context, conn *proto.Conn, hello proto.Hello, who whoIsResult) {
	h := s.currentHost()
	if h == nil {
		conn.SendTimeout(time.Second, proto.KindError, proto.ErrorMsg{
			Code: proto.ErrLobbyGone, Message: "this peer is not hosting a lobby",
		})
		conn.Close()
		return
	}
	// Ownership of conn passes to the host, which closes it when the seat ends.
	h.accept(ctx, conn, hello, who)
}

// whoIsResult is the display information WhoIs produced for a connection.
type whoIsResult struct {
	login string
	node  string
	// selfHint is this node's own name as far as the handshake is concerned.
	selfHint string
}

// whoIs looks up the tailnet identity behind a remote address. A failure is
// not fatal — the peer simply shows up without a login attached.
func (s *Server) whoIs(ctx context.Context, remoteAddr string) whoIsResult {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var out whoIsResult
	resp, err := s.node.WhoIs(ctx, remoteAddr)
	if err != nil {
		s.log.Logf("netplay: whois %s: %v", remoteAddr, err)
		return out
	}
	if resp.UserProfile != nil {
		out.login = resp.UserProfile.LoginName
	}
	if resp.Node != nil {
		out.node = strings.TrimSuffix(resp.Node.Name, ".")
		if i := strings.IndexByte(out.node, '.'); i > 0 {
			out.node = out.node[:i]
		}
	}
	return out
}

// Host starts hosting a lobby, replacing any lobby already running. It returns
// the Session the local player drives.
func (s *Server) Host(ctx context.Context, opts HostOptions) (*Host, error) {
	if existing := s.currentHost(); existing != nil {
		existing.Close("replaced by a new lobby")
	}
	h, err := newHost(ctx, s, opts)
	if err != nil {
		return nil, err
	}
	s.setHost(h)
	return h, nil
}

// StopHosting closes the current lobby, if any.
func (s *Server) StopHosting(reason string) {
	if h := s.currentHost(); h != nil {
		h.Close(reason)
	}
}

// ErrNotHosting is returned when an operation needs a lobby and there is none.
var ErrNotHosting = errors.New("netplay: not hosting a lobby")

// Dial opens a connection to a peer's control port over the tailnet.
func (s *Server) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return s.node.Dial(ctx, "tcp", addr)
}

// Identity returns this install's signing identity.
func (s *Server) Identity() *store.Identity { return s.ident }

// Store returns the match record store.
func (s *Server) Store() *store.Store { return s.store }

// Log returns the shared log ring.
func (s *Server) Log() *logring.Ring { return s.log }

// compile-time assertion that the concrete node satisfies the interface.
var _ Node = (*tsnode.Node)(nil)
