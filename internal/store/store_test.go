package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tbrockman/tailsnail/internal/game"
	"github.com/tbrockman/tailsnail/internal/proto"
)

type player struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	name string
}

func newPlayer(t *testing.T, name string) player {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return player{pub: pub, priv: priv, name: name}
}

// makeRecord builds a signed record. signers controls how many participants
// attest, so tests can produce partially attested records.
func makeRecord(t *testing.T, players []player, endedAt time.Time, winner int, signers int) proto.AttestedRecord {
	t.Helper()
	r := proto.MatchResult{
		Version:    proto.MatchResultVersion,
		MatchID:    proto.NewMatchID(),
		LobbyName:  "test",
		Config:     game.DefaultConfig(),
		StartedAt:  proto.FormatTime(endedAt.Add(-time.Minute)),
		EndedAt:    proto.FormatTime(endedAt),
		HostPubKey: proto.EncodeKey(players[0].pub),
	}
	place := 2
	for i, p := range players {
		r.Participants = append(r.Participants, proto.Participant{
			PubKey: proto.EncodeKey(p.pub), DisplayName: p.name,
			Login: p.name + "@ts.net", Node: "tsnail-" + p.name, Seat: game.PlayerID(i),
		})
		pl := proto.Placement{PubKey: proto.EncodeKey(p.pub), Length: 10 + i, Score: i, Kills: i, SurvivalTicks: 100 * (i + 1)}
		if i == winner {
			pl.Place = 1
		} else {
			pl.Place = place
			place++
		}
		r.Placements = append(r.Placements, pl)
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

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, problems, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("fresh store reported problems: %v", problems)
	}
	return s, dir
}

func TestPutAndGetRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	rec := makeRecord(t, players, time.Now(), 0, 2)

	changed, err := s.Put(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Put reported no change for a brand new record")
	}
	got, ok := s.Get(rec.Result.MatchID)
	if !ok {
		t.Fatal("record is missing after Put")
	}
	if got.Hash != rec.Hash {
		t.Errorf("hash = %s, want %s", got.Hash, rec.Hash)
	}
	if s.Count() != 1 {
		t.Errorf("count = %d, want 1", s.Count())
	}
}

func TestRecordsSurviveReopening(t *testing.T) {
	s, dir := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	var ids []string
	for i := range 3 {
		rec := makeRecord(t, players, time.Now().Add(time.Duration(i)*time.Minute), i%2, 2)
		if _, err := s.Put(rec); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.Result.MatchID)
	}

	reopened, problems, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("reopen reported problems: %v", problems)
	}
	if reopened.Count() != 3 {
		t.Fatalf("count after reopen = %d, want 3", reopened.Count())
	}
	for _, id := range ids {
		if !reopened.Has(id) {
			t.Errorf("match %s did not survive a reopen", id)
		}
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s, _ := openStore(t)
	rec := makeRecord(t, []player{newPlayer(t, "ada"), newPlayer(t, "grace")}, time.Now(), 0, 2)

	if _, err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Put(rec)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-storing an identical record reported a change")
	}
	if s.Count() != 1 {
		t.Errorf("count = %d, want 1", s.Count())
	}
}

func TestPutMergesLateSignatures(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace"), newPlayer(t, "hedy")}
	partial := makeRecord(t, players, time.Now(), 0, 1)
	if _, err := s.Put(partial); err != nil {
		t.Fatal(err)
	}

	// A peer later hands us the same match with everyone's signature.
	full := partial
	full.Signatures = append([]proto.Signature(nil), partial.Signatures...)
	for _, p := range players[1:] {
		sig, err := proto.SignResult(p.priv, full.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := full.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := s.Put(full)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("merging new signatures reported no change")
	}
	got, _ := s.Get(partial.Result.MatchID)
	if !got.FullyAttested() {
		t.Fatalf("record is %s after merge, want fully attested", got.AttestationSummary())
	}
	if err := got.Verify(); err != nil {
		t.Fatalf("merged record no longer verifies: %v", err)
	}
}

func TestPutRejectsAnUnverifiableRecord(t *testing.T) {
	s, _ := openStore(t)
	rec := makeRecord(t, []player{newPlayer(t, "ada"), newPlayer(t, "grace")}, time.Now(), 0, 2)
	rec.Result.Placements[0].Kills = 4242 // hash no longer matches

	if _, err := s.Put(rec); err == nil {
		t.Fatal("stored a record that fails verification")
	}
	if s.Count() != 0 {
		t.Errorf("count = %d, want 0", s.Count())
	}
}

func TestPutRefusesToOverwriteAConflictingRecord(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	first := makeRecord(t, players, time.Now(), 0, 2)
	if _, err := s.Put(first); err != nil {
		t.Fatal(err)
	}

	// A different result reusing the same match ID must not replace it.
	second := makeRecord(t, players, time.Now().Add(time.Hour), 1, 2)
	second.Result.MatchID = first.Result.MatchID
	rebuilt, err := proto.NewAttestedRecord(second.Result)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		sig, _ := proto.SignResult(p.priv, rebuilt.Result)
		if err := rebuilt.AddSignature(sig); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(rebuilt); err == nil {
		t.Fatal("a conflicting record with the same ID replaced the stored one")
	}
	got, _ := s.Get(first.Result.MatchID)
	if got.Hash != first.Hash {
		t.Error("the originally stored record was modified")
	}
}

func TestPutRejectsAMalformedMatchID(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	r := makeRecord(t, players, time.Now(), 0, 0).Result
	r.MatchID = "../../etc/passwd"
	rec, err := proto.NewAttestedRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(rec); err == nil {
		t.Fatal("accepted a match ID that is not a UUID")
	}
}

func TestValidMatchID(t *testing.T) {
	if !validMatchID(proto.NewMatchID()) {
		t.Error("a freshly generated match ID was rejected")
	}
	for _, bad := range []string{
		"", "short",
		"../../escape",
		"ABCDEF01-0000-4000-8000-000000000000", // uppercase
		"gggggggg-0000-4000-8000-000000000000", // non-hex
		"00000000_0000_4000_8000_000000000000", // wrong separators
		"00000000-0000-4000-8000-0000000000000",
	} {
		if validMatchID(bad) {
			t.Errorf("validMatchID(%q) = true", bad)
		}
	}
}

func TestOpenSkipsCorruptFilesAndReportsThem(t *testing.T) {
	dir := t.TempDir()
	s, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	good := makeRecord(t, players, time.Now(), 0, 2)
	if _, err := s.Put(good); err != nil {
		t.Fatal(err)
	}
	// Drop a truncated file and a well-formed but unsigned-for forgery.
	if err := os.WriteFile(filepath.Join(MatchesDir(dir), "00000000-0000-4000-8000-000000000001.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	forged := makeRecord(t, players, time.Now(), 0, 2)
	forged.Result.Placements[0].Kills = 99
	raw, _ := json.Marshal(forged)
	if err := os.WriteFile(filepath.Join(MatchesDir(dir), forged.Result.MatchID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, problems, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %d (%v), want 2", len(problems), problems)
	}
	if reopened.Count() != 1 {
		t.Fatalf("count = %d, want 1: the good record must survive", reopened.Count())
	}
	if !reopened.Has(good.Result.MatchID) {
		t.Error("the valid record was not loaded")
	}
}

func TestAllIsNewestFirst(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{2 * time.Hour, 0, time.Hour} {
		if _, err := s.Put(makeRecord(t, players, base.Add(offset), 0, 2)); err != nil {
			t.Fatal(err)
		}
	}
	all := s.All()
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Result.Ended().Before(all[i].Result.Ended()) {
			t.Fatal("All is not ordered newest first")
		}
	}
}

func TestInventoryIsSortedAndCarriesSignatureCounts(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace"), newPlayer(t, "hedy")}
	rec := makeRecord(t, players, time.Now(), 0, 2)
	if _, err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.Put(makeRecord(t, players, time.Now(), 0, 3)); err != nil {
			t.Fatal(err)
		}
	}
	inv := s.Inventory()
	if len(inv) != 4 {
		t.Fatalf("inventory size = %d, want 4", len(inv))
	}
	for i := 1; i < len(inv); i++ {
		if inv[i-1].MatchID >= inv[i].MatchID {
			t.Fatal("inventory is not sorted by match ID")
		}
	}
	for _, e := range inv {
		if e.MatchID == rec.Result.MatchID && e.Sigs != 2 {
			t.Errorf("partially attested entry reports %d signatures, want 2", e.Sigs)
		}
	}
}

func TestLeaderboardAggregatesAcrossMatches(t *testing.T) {
	s, _ := openStore(t)
	ada, grace, hedy := newPlayer(t, "ada"), newPlayer(t, "grace"), newPlayer(t, "hedy")
	all := []player{ada, grace, hedy}
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	// Ada wins twice, Grace once. Hedy plays all three and never wins.
	for i, winner := range []int{0, 0, 1} {
		if _, err := s.Put(makeRecord(t, all, base.Add(time.Duration(i)*time.Minute), winner, 3)); err != nil {
			t.Fatal(err)
		}
	}
	board := s.Leaderboard()
	if len(board) != 3 {
		t.Fatalf("leaderboard has %d entries, want 3", len(board))
	}
	if board[0].DisplayName != "ada" || board[0].Wins != 2 {
		t.Fatalf("top entry = %s with %d wins, want ada with 2", board[0].DisplayName, board[0].Wins)
	}
	if board[1].DisplayName != "grace" || board[1].Wins != 1 {
		t.Errorf("second entry = %s with %d wins, want grace with 1", board[1].DisplayName, board[1].Wins)
	}
	for _, e := range board {
		if e.Matches != 3 {
			t.Errorf("%s played %d matches, want 3", e.DisplayName, e.Matches)
		}
	}
	if got := board[0].WinRate(); got < 0.66 || got > 0.67 {
		t.Errorf("ada win rate = %.3f, want ~0.667", got)
	}
	if (PlayerStats{}).WinRate() != 0 {
		t.Error("win rate with no matches should be 0")
	}
}

func TestLeaderboardTracksIdentityAcrossRenames(t *testing.T) {
	s, _ := openStore(t)
	ada, grace := newPlayer(t, "ada"), newPlayer(t, "grace")
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	if _, err := s.Put(makeRecord(t, []player{ada, grace}, base, 0, 2)); err != nil {
		t.Fatal(err)
	}
	// Ada renames herself and plays again; the same key must aggregate.
	renamed := ada
	renamed.name = "countess"
	if _, err := s.Put(makeRecord(t, []player{renamed, grace}, base.Add(time.Hour), 0, 2)); err != nil {
		t.Fatal(err)
	}
	board := s.Leaderboard()
	if len(board) != 2 {
		t.Fatalf("leaderboard has %d entries, want 2: a rename must not split a player", len(board))
	}
	if board[0].Wins != 2 {
		t.Errorf("wins = %d, want 2", board[0].Wins)
	}
	if board[0].DisplayName != "countess" {
		t.Errorf("display name = %q, want the most recent name %q", board[0].DisplayName, "countess")
	}
}

func TestExportJSONRoundTrips(t *testing.T) {
	s, _ := openStore(t)
	players := []player{newPlayer(t, "ada"), newPlayer(t, "grace")}
	for range 2 {
		if _, err := s.Put(makeRecord(t, players, time.Now(), 0, 2)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := s.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	var out []proto.AttestedRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("exported history is not valid JSON: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("exported %d records, want 2", len(out))
	}
	for _, rec := range out {
		if err := rec.Verify(); err != nil {
			t.Errorf("exported record does not verify: %v", err)
		}
	}
}

func TestIdentityIsGeneratedOnceAndReloaded(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateIdentity(dir, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if first.DisplayName != "ada" {
		t.Errorf("display name = %q, want %q", first.DisplayName, "ada")
	}
	second, err := LoadOrCreateIdentity(dir, "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Public.Equal(second.Public) {
		t.Fatal("a second run generated a new key instead of reloading the stored one")
	}
	if second.DisplayName != "ada" {
		t.Errorf("display name = %q: the stored name must win over the fallback", second.DisplayName)
	}
}

func TestIdentityKeyActuallySigns(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir, "ada")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("tailsnail")
	if !ed25519.Verify(id.Public, msg, ed25519.Sign(id.Private, msg)) {
		t.Fatal("the loaded key pair does not sign and verify")
	}
	if got, err := proto.DecodeKey(id.PubKey()); err != nil || !got.Equal(id.Public) {
		t.Fatalf("PubKey() does not round-trip: %v", err)
	}
}

func TestIdentityFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateIdentity(dir, "ada"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("identity.json mode = %o, want no group or world access", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("state dir mode = %o, want 0700", perm)
	}
}

func TestIdentityRenameIsPersisted(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir, "ada")
	if err != nil {
		t.Fatal(err)
	}
	id.DisplayName = "countess"
	if err := id.Save(dir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadOrCreateIdentity(dir, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DisplayName != "countess" {
		t.Errorf("display name = %q, want %q", reloaded.DisplayName, "countess")
	}
	if !reloaded.Public.Equal(id.Public) {
		t.Error("renaming changed the signing key")
	}
}

func TestCorruptIdentityIsReportedNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(dir, "ada"); err == nil {
		t.Fatal("a corrupt identity file was silently replaced, which would discard match history")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "neon" || got.ColorMode != "auto" {
		t.Fatalf("defaults = %+v", got)
	}

	got.Theme = "mono"
	got.ASCII = true
	got.DisplayName = "ada"
	got.LastConfig = &HostPrefs{Name: "friday", Width: 30, Height: 16, TickRate: 15, TicksPerMove: 2, MaxPlayers: 3, Wrap: false, Mode: "shrink"}
	if err := SaveSettings(dir, got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Theme != "mono" || !reloaded.ASCII || reloaded.DisplayName != "ada" {
		t.Fatalf("settings = %+v, want the saved values", reloaded)
	}
	if reloaded.LastConfig == nil || reloaded.LastConfig.Mode != "shrink" || reloaded.LastConfig.Width != 30 {
		t.Fatalf("last host config = %+v", reloaded.LastConfig)
	}
}

func TestCorruptSettingsFallBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(dir)
	if err == nil {
		t.Error("a corrupt settings file should be reported")
	}
	if got.Theme != DefaultSettings().Theme {
		t.Errorf("settings = %+v, want defaults on parse failure", got)
	}
}

func TestEnsureDirTightensLoosePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 0700", perm)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.json")
	if err := writeFileAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "second" {
		t.Errorf("content = %q, want %q", raw, "second")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want just the target file", len(entries))
	}
}

func TestSuggestDisplayNameIsSane(t *testing.T) {
	got := SuggestDisplayName()
	if got == "" || got == "anonymous" {
		t.Errorf("SuggestDisplayName() = %q", got)
	}
	if len([]rune(got)) > 24 {
		t.Errorf("SuggestDisplayName() = %q, longer than the 24-rune cap", got)
	}
}
