package proto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tbrockman/tailsnail/internal/game"
)

type testPlayer struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	name string
}

func newTestPlayer(t *testing.T, name string) testPlayer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key for %s: %v", name, err)
	}
	return testPlayer{pub: pub, priv: priv, name: name}
}

// buildResult assembles a plausible finished match for the given players.
func buildResult(t *testing.T, players []testPlayer) MatchResult {
	t.Helper()
	start := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	r := MatchResult{
		Version:    MatchResultVersion,
		MatchID:    NewMatchID(),
		LobbyName:  "friday night",
		Config:     game.DefaultConfig(),
		StartedAt:  FormatTime(start),
		EndedAt:    FormatTime(start.Add(97 * time.Second)),
		HostPubKey: EncodeKey(players[0].pub),
	}
	for i, p := range players {
		r.Participants = append(r.Participants, Participant{
			PubKey:      EncodeKey(p.pub),
			DisplayName: p.name,
			Login:       p.name + "@example.com",
			Node:        "tsnail-" + p.name,
			Seat:        game.PlayerID(i),
		})
		r.Placements = append(r.Placements, Placement{
			PubKey:        EncodeKey(p.pub),
			Place:         i + 1,
			Length:        20 - i*3,
			Score:         9 - i,
			Kills:         len(players) - 1 - i,
			SurvivalTicks: 900 - i*40,
		})
	}
	r.Normalize()
	return r
}

func TestCanonicalFormIsIndependentOfKeyOrder(t *testing.T) {
	// Two JSON documents with identical content but different key order must
	// canonicalise to the same bytes — that is the whole point of the format.
	a := `{"b":1,"a":{"z":true,"y":[3,2,1]},"c":"x"}`
	b := `{"c":"x","a":{"y":[3,2,1],"z":true},"b":1}`
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Fatal(err)
	}
	ac, err := canonicalJSON(av)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := canonicalJSON(bv)
	if err != nil {
		t.Fatal(err)
	}
	if string(ac) != string(bc) {
		t.Fatalf("canonical forms differ:\n %s\n %s", ac, bc)
	}
	if got, want := string(ac), `{"a":{"y":[3,2,1],"z":true},"b":1,"c":"x"}`; got != want {
		t.Errorf("canonical form = %s, want %s", got, want)
	}
}

func TestCanonicalFormPreservesArrayOrder(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"a":[3,1,2]}`), &v); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":[3,1,2]}` {
		t.Fatalf("canonical form = %s: array order must be preserved", got)
	}
}

func TestDigestSurvivesAJSONRoundTrip(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	r := buildResult(t, players)

	want, err := r.HashHex()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the record crossing the wire and being decoded by a peer.
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MatchResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := decoded.HashHex()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hash changed across a JSON round trip: %s -> %s", want, got)
	}
}

func TestDigestIsIndependentOfParticipantOrder(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace"), newTestPlayer(t, "hedy")}
	r := buildResult(t, players)
	want, err := r.HashHex()
	if err != nil {
		t.Fatal(err)
	}

	shuffled := r
	shuffled.Participants = []Participant{r.Participants[2], r.Participants[0], r.Participants[1]}
	shuffled.Placements = []Placement{r.Placements[1], r.Placements[2], r.Placements[0]}
	got, err := shuffled.HashHex()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("hash depends on participant list order; Normalize should have removed that")
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace"), newTestPlayer(t, "hedy")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		sig, err := SignResult(p.priv, rec.Result)
		if err != nil {
			t.Fatalf("%s signing: %v", p.name, err)
		}
		if err := rec.AddSignature(sig); err != nil {
			t.Fatalf("%s attesting: %v", p.name, err)
		}
	}
	if err := rec.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rec.FullyAttested() {
		t.Errorf("record with every signature reports %s", rec.AttestationSummary())
	}
	for _, p := range players {
		if !rec.SignedBy(EncodeKey(p.pub)) {
			t.Errorf("%s's signature is missing", p.name)
		}
	}
}

func TestRecordSurvivesTheWireIntact(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		sig, _ := SignResult(p.priv, rec.Result)
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var got AttestedRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(); err != nil {
		t.Fatalf("record failed to verify after a round trip: %v", err)
	}
}

func TestPartialAttestationIsValidButFlagged(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace"), newTestPlayer(t, "hedy")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	// Only two of three sign — the third dropped before the match ended.
	for _, p := range players[:2] {
		sig, _ := SignResult(p.priv, rec.Result)
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Verify(); err != nil {
		t.Fatalf("a partially attested record must still verify: %v", err)
	}
	if rec.FullyAttested() {
		t.Error("record with 2 of 3 signatures claims to be fully attested")
	}
	if got := rec.AttestationSummary(); got != "partial 2/3" {
		t.Errorf("summary = %q, want %q", got, "partial 2/3")
	}
}

func TestTamperedResultFailsVerification(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		sig, _ := SignResult(p.priv, rec.Result)
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	// Grace rewrites history to claim the win, leaving the hash untouched.
	rec.Result.Placements[0].Place, rec.Result.Placements[1].Place = 2, 1
	if err := rec.Verify(); err == nil {
		t.Fatal("a record whose placements were edited after signing verified")
	}
}

func TestRehashedTamperFailsSignatureCheck(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		sig, _ := SignResult(p.priv, rec.Result)
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	// A more careful forger edits the result and recomputes the hash. The
	// signatures no longer cover the new digest.
	rec.Result.Placements[1].Kills = 999
	rec.Hash, _ = rec.Result.HashHex()
	if err := rec.Verify(); err == nil {
		t.Fatal("a re-hashed forgery verified")
	} else if !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("error = %v, want a signature failure", err)
	}
}

func TestSignatureFromNonParticipantIsRejected(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	outsider := newTestPlayer(t, "mallory")
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := SignResult(outsider.priv, rec.Result)
	if err := rec.AddSignature(sig); err == nil {
		t.Fatal("accepted a signature from someone who did not play")
	}
}

func TestSignatureUnderTheWrongKeyIsRejected(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	// Claim to be Ada while signing with Grace's key.
	sig, _ := SignResult(players[1].priv, rec.Result)
	sig.PubKey = EncodeKey(players[0].pub)
	if err := rec.AddSignature(sig); err == nil {
		t.Fatal("accepted a signature attributed to the wrong key")
	}
}

func TestResigningReplacesRatherThanDuplicates(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := SignResult(players[0].priv, rec.Result)
	for range 3 {
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.Signatures) != 1 {
		t.Fatalf("signature count = %d, want 1 after re-submitting the same one", len(rec.Signatures))
	}
}

func TestSignaturesAreStoredInCanonicalOrder(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "a"), newTestPlayer(t, "b"), newTestPlayer(t, "c")}
	rec, err := NewAttestedRecord(buildResult(t, players))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 0, 1} {
		sig, _ := SignResult(players[i].priv, rec.Result)
		if err := rec.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(rec.Signatures); i++ {
		if rec.Signatures[i-1].PubKey >= rec.Signatures[i].PubKey {
			t.Fatal("signatures are not sorted by public key")
		}
	}
}

func TestResultValidation(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	cases := []struct {
		name   string
		mutate func(*MatchResult)
	}{
		{"wrong version", func(r *MatchResult) { r.Version = 99 }},
		{"no id", func(r *MatchResult) { r.MatchID = "" }},
		{"no participants", func(r *MatchResult) { r.Participants = nil }},
		{"no host key", func(r *MatchResult) { r.HostPubKey = "" }},
		{"host did not play", func(r *MatchResult) { r.HostPubKey = EncodeKey(newTestPlayer(t, "x").pub) }},
		{"malformed key", func(r *MatchResult) { r.Participants[0].PubKey = "not-base64!!" }},
		{"placement for a stranger", func(r *MatchResult) {
			r.Placements[0].PubKey = EncodeKey(newTestPlayer(t, "x").pub)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := buildResult(t, players)
			if err := r.Validate(); err != nil {
				t.Fatalf("baseline result is invalid: %v", err)
			}
			tc.mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("Validate accepted a malformed result")
			}
		})
	}
}

func TestDuplicateParticipantIsRejected(t *testing.T) {
	p := newTestPlayer(t, "ada")
	r := buildResult(t, []testPlayer{p, p})
	if err := r.Validate(); err == nil {
		t.Fatal("accepted a result listing the same key twice")
	}
}

func TestKeyEncodingRoundTrip(t *testing.T) {
	p := newTestPlayer(t, "ada")
	got, err := DecodeKey(EncodeKey(p.pub))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(p.pub) {
		t.Fatal("public key did not survive an encode/decode round trip")
	}
	if _, err := DecodeKey("short"); err == nil {
		t.Error("accepted an undersized key")
	}
	if _, err := DecodeKey("!!!not base64!!!"); err == nil {
		t.Error("accepted a non-base64 key")
	}
}

func TestMatchIDsAreUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		id := NewMatchID()
		if len(id) != 36 || strings.Count(id, "-") != 4 {
			t.Fatalf("match ID %q is not a UUID", id)
		}
		if id[14] != '4' {
			t.Fatalf("match ID %q is not version 4", id)
		}
		if seen[id] {
			t.Fatalf("duplicate match ID %q", id)
		}
		seen[id] = true
	}
}

func TestWinnerReportsFirstPlace(t *testing.T) {
	players := []testPlayer{newTestPlayer(t, "ada"), newTestPlayer(t, "grace")}
	r := buildResult(t, players)
	got, ok := r.Winner()
	if !ok {
		t.Fatal("no winner reported")
	}
	if got != EncodeKey(players[0].pub) {
		t.Errorf("winner = %s, want ada", shortKey(got))
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ada", "ada"},
		{"  ada  ", "ada"},
		{"", "anonymous"},
		{"   ", "anonymous"},
		{"ada\x1b[31mred", "ada[31mred"},
		{"line\nbreak", "linebreak"},
		{strings.Repeat("x", 40), strings.Repeat("x", 24)},
	}
	for _, tc := range cases {
		if got := SanitizeDisplayName(tc.in); got != tc.want {
			t.Errorf("SanitizeDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
