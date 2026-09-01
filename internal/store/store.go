package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tbrockman/tailsnail/internal/proto"
)

// Store is the append-only log of attested match records, keyed by match ID.
// It is safe for concurrent use: the gossip goroutine writes to it while the
// UI reads from it.
//
// "Append-only" describes the semantics, not the file format — an existing
// record is only ever rewritten to add signatures it was missing, never to
// change the result it attests to.
type Store struct {
	dir string

	mu   sync.RWMutex
	recs map[string]proto.AttestedRecord
}

// Open loads every record under stateDir/matches. Individual unreadable or
// unverifiable records are skipped and reported rather than failing the whole
// load, so one corrupt file cannot lock a user out of their history.
func Open(stateDir string) (*Store, []error, error) {
	dir := MatchesDir(stateDir)
	if err := EnsureDir(dir); err != nil {
		return nil, nil, err
	}
	s := &Store{dir: dir, recs: make(map[string]proto.AttestedRecord)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("store: listing %s: %w", dir, err)
	}
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("store: reading %s: %w", e.Name(), err))
			continue
		}
		var rec proto.AttestedRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			problems = append(problems, fmt.Errorf("store: parsing %s: %w", e.Name(), err))
			continue
		}
		if err := rec.Verify(); err != nil {
			problems = append(problems, fmt.Errorf("store: verifying %s: %w", e.Name(), err))
			continue
		}
		s.recs[rec.Result.MatchID] = rec
	}
	return s, problems, nil
}

// recordPath returns the file a match ID is stored in. The ID is a UUID we
// generated or received, so it is checked against the expected shape before
// being used in a path.
func (s *Store) recordPath(matchID string) (string, error) {
	if !validMatchID(matchID) {
		return "", fmt.Errorf("store: %q is not a valid match ID", matchID)
	}
	return filepath.Join(s.dir, matchID+".json"), nil
}

// validMatchID reports whether id is a canonical lowercase UUID. Rejecting
// anything else keeps a peer-supplied ID from escaping the matches directory.
func validMatchID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}

// Put verifies and stores a record. When a record with the same ID is already
// held, any signatures the incoming copy carries are merged into it; the
// result itself is never replaced. Put reports whether anything changed, which
// is what gossip uses to decide if a record is worth re-sharing.
func (s *Store) Put(rec proto.AttestedRecord) (bool, error) {
	if err := rec.Verify(); err != nil {
		return false, err
	}
	path, err := s.recordPath(rec.Result.MatchID)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, held := s.recs[rec.Result.MatchID]
	if held {
		if existing.Hash != rec.Hash {
			// Same match ID, different content. The trusted-peer model says
			// this should not happen; keep what we already verified rather
			// than letting a later copy overwrite history.
			return false, fmt.Errorf("store: match %s already stored with a different hash", rec.Result.MatchID)
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
		if !added {
			return false, nil
		}
		rec = existing
	}

	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return false, fmt.Errorf("store: encoding record: %w", err)
	}
	if err := writeFileAtomic(path, append(raw, '\n')); err != nil {
		return false, err
	}
	s.recs[rec.Result.MatchID] = rec
	return true, nil
}

// Get returns the record for a match ID.
func (s *Store) Get(matchID string) (proto.AttestedRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.recs[matchID]
	return rec, ok
}

// Has reports whether a match ID is already stored.
func (s *Store) Has(matchID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.recs[matchID]
	return ok
}

// Count returns the number of stored records.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.recs)
}

// All returns every record, most recently ended first.
func (s *Store) All() []proto.AttestedRecord {
	s.mu.RLock()
	out := make([]proto.AttestedRecord, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].Result.Ended(), out[j].Result.Ended()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].Result.MatchID < out[j].Result.MatchID
	})
	return out
}

// Inventory returns the compact match-ID/hash list exchanged during gossip.
// It is sorted so two peers produce byte-identical inventories for identical
// contents, which makes the exchange easy to reason about and to test.
func (s *Store) Inventory() []proto.InvEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]proto.InvEntry, 0, len(s.recs))
	for id, rec := range s.recs {
		out = append(out, proto.InvEntry{MatchID: id, Hash: rec.Hash, Sigs: len(rec.Signatures)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MatchID < out[j].MatchID })
	return out
}

// PlayerStats is one player's aggregate record across every stored match.
type PlayerStats struct {
	PubKey      string
	DisplayName string
	Login       string
	Matches     int
	Wins        int
	Podiums     int // top-three finishes
	Kills       int
	Score       int
	BestLength  int
}

// WinRate returns wins as a fraction of matches played, or 0 with no matches.
func (p PlayerStats) WinRate() float64 {
	if p.Matches == 0 {
		return 0
	}
	return float64(p.Wins) / float64(p.Matches)
}

// Leaderboard aggregates every stored record into per-player standings,
// ordered by wins, then win rate, then matches played, then name. Players are
// identified by signing key, so a rename carries their history forward; the
// display name shown is the one from their most recent match.
func (s *Store) Leaderboard() []PlayerStats {
	records := s.All() // newest first, so the first name seen is the newest

	byKey := make(map[string]*PlayerStats)
	for _, rec := range records {
		for _, part := range rec.Result.Participants {
			ps, ok := byKey[part.PubKey]
			if !ok {
				ps = &PlayerStats{
					PubKey:      part.PubKey,
					DisplayName: part.DisplayName,
					Login:       part.Login,
				}
				byKey[part.PubKey] = ps
			}
			ps.Matches++
		}
		for _, pl := range rec.Result.Placements {
			ps, ok := byKey[pl.PubKey]
			if !ok {
				continue // placement for someone absent from the participant list
			}
			if pl.Place == 1 {
				ps.Wins++
			}
			if pl.Place <= 3 {
				ps.Podiums++
			}
			ps.Kills += pl.Kills
			ps.Score += pl.Score
			if pl.Length > ps.BestLength {
				ps.BestLength = pl.Length
			}
		}
	}

	out := make([]PlayerStats, 0, len(byKey))
	for _, ps := range byKey {
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Wins != b.Wins {
			return a.Wins > b.Wins
		}
		if ar, br := a.WinRate(), b.WinRate(); ar != br {
			return ar > br
		}
		if a.Matches != b.Matches {
			return a.Matches > b.Matches
		}
		if a.DisplayName != b.DisplayName {
			return a.DisplayName < b.DisplayName
		}
		return a.PubKey < b.PubKey
	})
	return out
}

// ExportJSON renders the whole store as a JSON array, newest first. It backs
// `tsnail history export --json`.
func (s *Store) ExportJSON() ([]byte, error) {
	raw, err := json.MarshalIndent(s.All(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("store: exporting history: %w", err)
	}
	return append(raw, '\n'), nil
}
