// Package gossip implements anti-entropy for attested match records.
//
// Whenever two tailsnail peers connect — a discovery probe, a lobby join, or a
// dedicated sync — they compare compact inventories and exchange whatever the
// other is missing. Everyone's history converges just by people running the
// app; there is no central store and no ordering requirement.
//
// The exchange is three messages:
//
//	dialer  -> listener  GossipInv{entries}       "here is everything I hold"
//	listener -> dialer   GossipResp{want,records} "you are missing these; send me those"
//	dialer  -> listener  GossipRecords{records}   "here are the ones you asked for"
package gossip

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/theolol/tailsnail/internal/proto"
)

// MaxRecordsPerExchange bounds how many records move in one direction per
// sync, keeping a single frame well under proto.MaxFrame. A peer that is
// further behind simply catches up over several connections.
const MaxRecordsPerExchange = 48

// ExchangeTimeout bounds a whole sync so a stalled peer cannot pin a
// goroutine open indefinitely.
const ExchangeTimeout = 10 * time.Second

// Recorder is the subset of the match store gossip needs. Keeping it narrow
// lets the exchange be tested against an in-memory fake.
type Recorder interface {
	// Inventory returns what this peer holds, sorted by match ID.
	Inventory() []proto.InvEntry
	// Get returns a stored record.
	Get(matchID string) (proto.AttestedRecord, bool)
	// Put verifies and stores a record, reporting whether anything changed.
	Put(rec proto.AttestedRecord) (bool, error)
}

// Result summarises one completed exchange.
type Result struct {
	Sent     int // records handed to the peer
	Received int // records accepted from the peer
	Rejected int // records the peer offered that failed verification
}

// Empty reports whether the exchange moved nothing, which is the common case
// once two peers have converged.
func (r Result) Empty() bool { return r.Sent == 0 && r.Received == 0 }

// String renders the result for the log ring.
func (r Result) String() string {
	return fmt.Sprintf("sent=%d received=%d rejected=%d", r.Sent, r.Received, r.Rejected)
}

// Missing returns the match IDs present in theirs that mine either does not
// hold at all, or holds with strictly fewer signatures.
//
// An entry whose hash disagrees with a record we already hold is skipped: the
// trusted-peer model says that cannot happen, and pulling it would only let a
// buggy peer overwrite a result we already verified.
func Missing(mine, theirs []proto.InvEntry) []string {
	have := make(map[string]proto.InvEntry, len(mine))
	for _, e := range mine {
		have[e.MatchID] = e
	}
	var want []string
	for _, e := range theirs {
		held, ok := have[e.MatchID]
		switch {
		case !ok:
			want = append(want, e.MatchID)
		case held.Hash != e.Hash:
			continue // conflicting content for the same ID; keep ours
		case e.Sigs > held.Sigs:
			want = append(want, e.MatchID) // same result, better attested
		}
	}
	sort.Strings(want)
	return want
}

// collect gathers the records for the given IDs, skipping any we no longer
// hold and capping the batch at MaxRecordsPerExchange.
func collect(r Recorder, ids []string) []proto.AttestedRecord {
	if len(ids) > MaxRecordsPerExchange {
		ids = ids[:MaxRecordsPerExchange]
	}
	out := make([]proto.AttestedRecord, 0, len(ids))
	for _, id := range ids {
		if rec, ok := r.Get(id); ok {
			out = append(out, rec)
		}
	}
	return out
}

// absorb stores every record a peer offered, counting acceptances and
// rejections. A single bad record never aborts the batch — the rest of the
// peer's history is still worth having.
func absorb(r Recorder, records []proto.AttestedRecord, res *Result) {
	for _, rec := range records {
		changed, err := r.Put(rec)
		if err != nil {
			res.Rejected++
			continue
		}
		if changed {
			res.Received++
		}
	}
}

// Initiate runs the dialing half of an exchange on an already-open connection
// whose handshake is complete.
func Initiate(ctx context.Context, c *proto.Conn, r Recorder) (Result, error) {
	var res Result
	deadline, cancel := deadlineFor(ctx)
	defer cancel()

	mine := r.Inventory()
	if err := c.SendTimeout(time.Until(deadline), proto.KindGossipInv, proto.GossipInv{Entries: mine}); err != nil {
		return res, fmt.Errorf("gossip: sending inventory: %w", err)
	}

	env, err := c.RecvTimeout(time.Until(deadline))
	if err != nil {
		return res, fmt.Errorf("gossip: awaiting response: %w", err)
	}
	if env.Kind == proto.KindError {
		msg, _ := proto.Decode[proto.ErrorMsg](env)
		return res, fmt.Errorf("gossip: peer refused: %w", msg)
	}
	if env.Kind != proto.KindGossipResp {
		return res, fmt.Errorf("gossip: expected %s, got %s", proto.KindGossipResp, env.Kind)
	}
	resp, err := proto.Decode[proto.GossipResp](env)
	if err != nil {
		return res, err
	}
	absorb(r, resp.Records, &res)

	// Both halves decide from the same Want list whether a third message
	// exists, so the exchange stays in lockstep: when the peer wants nothing
	// it has already stopped reading, and sending anyway would deadlock.
	if len(resp.Want) == 0 {
		return res, nil
	}
	give := collect(r, resp.Want)
	res.Sent = len(give)
	if err := c.SendTimeout(time.Until(deadline), proto.KindGossipRecords, proto.GossipRecords{Records: give}); err != nil {
		return res, fmt.Errorf("gossip: sending records: %w", err)
	}
	return res, nil
}

// Respond runs the listening half, given the inventory the dialer already
// sent. The caller has read the GossipInv message off the wire as part of
// dispatching the connection.
func Respond(ctx context.Context, c *proto.Conn, r Recorder, inv proto.GossipInv) (Result, error) {
	var res Result
	deadline, cancel := deadlineFor(ctx)
	defer cancel()

	mine := r.Inventory()
	want := Missing(mine, inv.Entries)
	give := collect(r, Missing(inv.Entries, mine))
	res.Sent = len(give)

	if len(want) > MaxRecordsPerExchange {
		want = want[:MaxRecordsPerExchange]
	}
	if err := c.SendTimeout(time.Until(deadline), proto.KindGossipResp, proto.GossipResp{Want: want, Records: give}); err != nil {
		return res, fmt.Errorf("gossip: sending response: %w", err)
	}
	if len(want) == 0 {
		// We asked for nothing, so the dialer sends no third message. This
		// is the common case once two peers have converged, and it makes a
		// steady-state sync two messages rather than three.
		return res, nil
	}

	env, err := c.RecvTimeout(time.Until(deadline))
	if err != nil {
		return res, fmt.Errorf("gossip: awaiting records: %w", err)
	}
	if env.Kind != proto.KindGossipRecords {
		return res, fmt.Errorf("gossip: expected %s, got %s", proto.KindGossipRecords, env.Kind)
	}
	batch, err := proto.Decode[proto.GossipRecords](env)
	if err != nil {
		return res, err
	}
	absorb(r, batch.Records, &res)
	return res, nil
}

// deadlineFor derives the exchange deadline from ctx, capped at
// ExchangeTimeout.
func deadlineFor(ctx context.Context) (time.Time, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, ExchangeTimeout)
	d, ok := ctx.Deadline()
	if !ok { // unreachable: WithTimeout always sets one
		d = time.Now().Add(ExchangeTimeout)
	}
	return d, cancel
}
