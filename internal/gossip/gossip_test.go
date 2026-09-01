package gossip

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/tbrockman/tailsnail/internal/game"
	"github.com/tbrockman/tailsnail/internal/proto"
)

// memStore is an in-memory Recorder, so the exchange can be exercised without
// touching disk.
type memStore struct {
	mu   sync.Mutex
	recs map[string]proto.AttestedRecord
	// rejectAll makes every Put fail, standing in for a peer whose disk is
	// full or whose records all fail verification.
	rejectAll bool
}

func newMem(recs ...proto.AttestedRecord) *memStore {
	m := &memStore{recs: map[string]proto.AttestedRecord{}}
	for _, r := range recs {
		m.recs[r.Result.MatchID] = r
	}
	return m
}

func (m *memStore) Inventory() []proto.InvEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]proto.InvEntry, 0, len(m.recs))
	for id, r := range m.recs {
		out = append(out, proto.InvEntry{MatchID: id, Hash: r.Hash, Sigs: len(r.Signatures)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MatchID < out[j].MatchID })
	return out
}

func (m *memStore) Get(id string) (proto.AttestedRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	return r, ok
}

func (m *memStore) Put(rec proto.AttestedRecord) (bool, error) {
	if m.rejectAll {
		return false, proto.ErrorMsg{Code: "nope", Message: "rejecting everything"}
	}
	if err := rec.Verify(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, held := m.recs[rec.Result.MatchID]
	if !held {
		m.recs[rec.Result.MatchID] = rec
		return true, nil
	}
	added := false
	for _, sig := range rec.Signatures {
		if existing.SignedBy(sig.PubKey) {
			continue
		}
		if err := existing.AddSignature(sig); err != nil {
			return false, err
		}
		added = true
	}
	if added {
		m.recs[rec.Result.MatchID] = existing
	}
	return added, nil
}

func (m *memStore) ids() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.recs))
	for id := range m.recs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

type keyed struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	name string
}

func newKeyed(t *testing.T, name string) keyed {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return keyed{pub, priv, name}
}

func makeRecord(t *testing.T, players []keyed, signers int) proto.AttestedRecord {
	t.Helper()
	r := proto.MatchResult{
		Version:    proto.MatchResultVersion,
		MatchID:    proto.NewMatchID(),
		LobbyName:  "gossip test",
		Config:     game.DefaultConfig(),
		StartedAt:  proto.FormatTime(time.Now().Add(-time.Minute)),
		EndedAt:    proto.FormatTime(time.Now()),
		HostPubKey: proto.EncodeKey(players[0].pub),
	}
	for i, p := range players {
		r.Participants = append(r.Participants, proto.Participant{
			PubKey: proto.EncodeKey(p.pub), DisplayName: p.name, Seat: game.PlayerID(i),
		})
		r.Placements = append(r.Placements, proto.Placement{
			PubKey: proto.EncodeKey(p.pub), Place: i + 1, Length: 10 + i,
		})
	}
	rec, err := proto.NewAttestedRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players[:signers] {
		sig, err := proto.SignResult(p.priv, rec.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func inv(entries ...proto.InvEntry) []proto.InvEntry { return entries }

func TestMissingFindsRecordsWeDoNotHold(t *testing.T) {
	mine := inv(
		proto.InvEntry{MatchID: "a", Hash: "h1", Sigs: 2},
		proto.InvEntry{MatchID: "b", Hash: "h2", Sigs: 2},
	)
	theirs := inv(
		proto.InvEntry{MatchID: "b", Hash: "h2", Sigs: 2},
		proto.InvEntry{MatchID: "c", Hash: "h3", Sigs: 1},
	)
	if got, want := Missing(mine, theirs), []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing = %v, want %v", got, want)
	}
}

func TestMissingFindsBetterAttestedCopies(t *testing.T) {
	mine := inv(proto.InvEntry{MatchID: "a", Hash: "h1", Sigs: 1})
	theirs := inv(proto.InvEntry{MatchID: "a", Hash: "h1", Sigs: 3})
	if got, want := Missing(mine, theirs), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing = %v, want %v: a better-signed copy of the same result is worth pulling", got, want)
	}
	// The reverse direction must not want anything.
	if got := Missing(theirs, mine); len(got) != 0 {
		t.Fatalf("Missing = %v, want nothing: our copy is already better attested", got)
	}
}

func TestMissingIgnoresConflictingHashes(t *testing.T) {
	mine := inv(proto.InvEntry{MatchID: "a", Hash: "h1", Sigs: 1})
	theirs := inv(proto.InvEntry{MatchID: "a", Hash: "DIFFERENT", Sigs: 9})
	if got := Missing(mine, theirs); len(got) != 0 {
		t.Fatalf("Missing = %v: a record whose hash disagrees with ours must not be pulled", got)
	}
}

func TestMissingOnEmptyInventories(t *testing.T) {
	if got := Missing(nil, nil); len(got) != 0 {
		t.Errorf("Missing(nil, nil) = %v", got)
	}
	theirs := inv(proto.InvEntry{MatchID: "a", Hash: "h", Sigs: 1}, proto.InvEntry{MatchID: "b", Hash: "h", Sigs: 1})
	if got, want := Missing(nil, theirs), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Missing(nil, theirs) = %v, want %v", got, want)
	}
	if got := Missing(theirs, nil); len(got) != 0 {
		t.Errorf("Missing(theirs, nil) = %v, want nothing", got)
	}
}

func TestMissingIsSorted(t *testing.T) {
	theirs := inv(
		proto.InvEntry{MatchID: "z"}, proto.InvEntry{MatchID: "m"}, proto.InvEntry{MatchID: "a"},
	)
	got := Missing(nil, theirs)
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Missing = %v, want sorted output for a deterministic exchange", got)
	}
}

// runExchange wires two stores together over an in-memory pipe and runs a full
// three-message sync, returning both halves' results.
func runExchange(t *testing.T, dialer, listener Recorder) (Result, Result) {
	t.Helper()
	a, b := net.Pipe()
	ca, cb := proto.NewConn(a), proto.NewConn(b)
	defer ca.Close()
	defer cb.Close()

	ctx := context.Background()
	var (
		wg               sync.WaitGroup
		dialRes, listRes Result
		dialErr, listErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		dialRes, dialErr = Initiate(ctx, ca, dialer)
	}()
	go func() {
		defer wg.Done()
		env, err := cb.RecvTimeout(5 * time.Second)
		if err != nil {
			listErr = err
			return
		}
		if env.Kind != proto.KindGossipInv {
			listErr = proto.ErrorMsg{Code: "unexpected", Message: string(env.Kind)}
			return
		}
		msg, err := proto.Decode[proto.GossipInv](env)
		if err != nil {
			listErr = err
			return
		}
		listRes, listErr = Respond(ctx, cb, listener, msg)
	}()
	wg.Wait()
	if dialErr != nil {
		t.Fatalf("dialer: %v", dialErr)
	}
	if listErr != nil {
		t.Fatalf("listener: %v", listErr)
	}
	return dialRes, listRes
}

func TestExchangeSyncsBothDirections(t *testing.T) {
	ada, grace := newKeyed(t, "ada"), newKeyed(t, "grace")
	players := []keyed{ada, grace}

	shared := makeRecord(t, players, 2)
	onlyDialer := makeRecord(t, players, 2)
	onlyListener := makeRecord(t, players, 2)

	dialer := newMem(shared, onlyDialer)
	listener := newMem(shared, onlyListener)

	dialRes, listRes := runExchange(t, dialer, listener)

	if dialRes.Received != 1 {
		t.Errorf("dialer received %d records, want 1", dialRes.Received)
	}
	if listRes.Received != 1 {
		t.Errorf("listener received %d records, want 1", listRes.Received)
	}
	want := []string{shared.Result.MatchID, onlyDialer.Result.MatchID, onlyListener.Result.MatchID}
	sort.Strings(want)
	if got := dialer.ids(); !reflect.DeepEqual(got, want) {
		t.Errorf("dialer holds %v, want %v", got, want)
	}
	if got := listener.ids(); !reflect.DeepEqual(got, want) {
		t.Errorf("listener holds %v, want %v", got, want)
	}
}

func TestExchangeConvergesFromAnEmptyPeer(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	var recs []proto.AttestedRecord
	for range 5 {
		recs = append(recs, makeRecord(t, players, 2))
	}
	full := newMem(recs...)
	empty := newMem()

	runExchange(t, empty, full)

	if got := len(empty.ids()); got != 5 {
		t.Fatalf("empty peer holds %d records after syncing, want 5", got)
	}
	if !reflect.DeepEqual(empty.ids(), full.ids()) {
		t.Error("the two peers did not converge")
	}
}

func TestExchangeIsANoOpWhenAlreadyConverged(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	recs := []proto.AttestedRecord{makeRecord(t, players, 2), makeRecord(t, players, 2)}
	a, b := newMem(recs...), newMem(recs...)

	dialRes, listRes := runExchange(t, a, b)
	if !dialRes.Empty() || !listRes.Empty() {
		t.Fatalf("converged peers still exchanged data: dialer %s, listener %s", dialRes, listRes)
	}
}

func TestExchangeMergesSignaturesForAKnownMatch(t *testing.T) {
	ada, grace, hedy := newKeyed(t, "ada"), newKeyed(t, "grace"), newKeyed(t, "hedy")
	players := []keyed{ada, grace, hedy}

	// The dialer has everyone's signature; the listener only has the host's.
	full := makeRecord(t, players, 3)
	partial := full
	partial.Signatures = full.Signatures[:1]

	dialer := newMem(full)
	listener := newMem(partial)

	_, listRes := runExchange(t, dialer, listener)
	if listRes.Received != 1 {
		t.Fatalf("listener received %d records, want 1 signature merge", listRes.Received)
	}
	got, _ := listener.Get(full.Result.MatchID)
	if !got.FullyAttested() {
		t.Fatalf("listener record is %s, want fully attested after the sync", got.AttestationSummary())
	}
}

func TestExchangeSurvivesRecordsThePeerRejects(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	dialer := newMem(makeRecord(t, players, 2), makeRecord(t, players, 2))
	listener := newMem()
	listener.rejectAll = true

	dialRes, listRes := runExchange(t, dialer, listener)
	if listRes.Rejected != 2 {
		t.Errorf("listener rejected %d records, want 2", listRes.Rejected)
	}
	if listRes.Received != 0 {
		t.Errorf("listener received %d records despite rejecting everything", listRes.Received)
	}
	// The dialer's own store must be untouched by the peer's failure.
	if dialRes.Received != 0 {
		t.Errorf("dialer received %d records from an empty peer", dialRes.Received)
	}
	if len(dialer.ids()) != 2 {
		t.Error("the dialer lost records because the peer rejected them")
	}
}

func TestExchangeRejectsAForgedRecord(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	forged := makeRecord(t, players, 2)
	forged.Result.Placements[0].Kills = 9999 // signatures no longer cover this

	dialer := newMem(forged)
	listener := newMem()

	_, listRes := runExchange(t, dialer, listener)
	if listRes.Received != 0 {
		t.Fatal("a forged record was accepted")
	}
	if listRes.Rejected != 1 {
		t.Errorf("rejected = %d, want 1", listRes.Rejected)
	}
	if len(listener.ids()) != 0 {
		t.Error("the forged record was stored")
	}
}

func TestExchangeIsBoundedPerRound(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	var recs []proto.AttestedRecord
	for range MaxRecordsPerExchange + 20 {
		recs = append(recs, makeRecord(t, players, 2))
	}
	full := newMem(recs...)
	empty := newMem()

	_, listRes := runExchange(t, empty, full)
	if listRes.Sent > MaxRecordsPerExchange {
		t.Fatalf("sent %d records in one exchange, want at most %d", listRes.Sent, MaxRecordsPerExchange)
	}
	if got := len(empty.ids()); got != MaxRecordsPerExchange {
		t.Fatalf("empty peer holds %d records, want the %d-record cap", got, MaxRecordsPerExchange)
	}
	// A second round must make further progress rather than stalling.
	runExchange(t, empty, full)
	if got := len(empty.ids()); got <= MaxRecordsPerExchange {
		t.Fatalf("a second exchange made no progress: %d records", got)
	}
}

func TestRepeatedExchangesConverge(t *testing.T) {
	players := []keyed{newKeyed(t, "ada"), newKeyed(t, "grace")}
	var recs []proto.AttestedRecord
	for range MaxRecordsPerExchange*2 + 5 {
		recs = append(recs, makeRecord(t, players, 2))
	}
	full := newMem(recs...)
	empty := newMem()

	for range 6 {
		runExchange(t, empty, full)
		if len(empty.ids()) == len(full.ids()) {
			break
		}
	}
	if !reflect.DeepEqual(empty.ids(), full.ids()) {
		t.Fatalf("peers did not converge: %d vs %d records", len(empty.ids()), len(full.ids()))
	}
}

func TestInitiateFailsOnAnUnexpectedReply(t *testing.T) {
	a, b := net.Pipe()
	ca, cb := proto.NewConn(a), proto.NewConn(b)
	defer ca.Close()
	defer cb.Close()

	go func() {
		if _, err := cb.RecvTimeout(2 * time.Second); err != nil {
			return
		}
		cb.Send(proto.KindPong, proto.Pong{Nonce: 1})
	}()
	if _, err := Initiate(context.Background(), ca, newMem()); err == nil {
		t.Fatal("Initiate accepted a pong where a gossip response was due")
	}
}

func TestInitiateSurfacesAPeerError(t *testing.T) {
	a, b := net.Pipe()
	ca, cb := proto.NewConn(a), proto.NewConn(b)
	defer ca.Close()
	defer cb.Close()

	go func() {
		if _, err := cb.RecvTimeout(2 * time.Second); err != nil {
			return
		}
		cb.Send(proto.KindError, proto.ErrorMsg{Code: proto.ErrBadRequest, Message: "go away"})
	}()
	_, err := Initiate(context.Background(), ca, newMem())
	if err == nil {
		t.Fatal("Initiate ignored an error response")
	}
}

func TestExchangeHonoursContextCancellation(t *testing.T) {
	a, b := net.Pipe()
	ca, cb := proto.NewConn(a), proto.NewConn(b)
	defer ca.Close()
	defer cb.Close()

	// The listener reads the inventory and then never replies.
	go func() { cb.RecvTimeout(5 * time.Second) }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Initiate(ctx, ca, newMem()); err == nil {
		t.Fatal("Initiate returned successfully against a silent peer")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Initiate blocked for %v despite a 100ms context deadline", elapsed)
	}
}

func TestResultString(t *testing.T) {
	got := Result{Sent: 1, Received: 2, Rejected: 3}.String()
	if want := "sent=1 received=2 rejected=3"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
