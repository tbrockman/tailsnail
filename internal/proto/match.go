package proto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/tbrockman/tailsnail/internal/game"
)

// MatchResultVersion is the schema version of a MatchResult. It is part of the
// canonical form, so bumping it changes every hash — old records stay valid
// under their own version.
const MatchResultVersion = 1

// sigDomain separates tailsnail signatures from any other use of the same key.
const sigDomain = "tailsnail/match-result/v1\n"

// Participant binds a player's signing key to the tailnet identity that
// WhoIs reported for them at join time, and to the seat they played.
type Participant struct {
	PubKey      string        `json:"pubkey"` // base64 raw-std ed25519 public key
	DisplayName string        `json:"display_name"`
	Login       string        `json:"login,omitempty"` // tailnet user, from WhoIs
	Node        string        `json:"node,omitempty"`  // tailnet device name, from WhoIs
	Seat        game.PlayerID `json:"seat"`
}

// Placement is one player's finishing position and statistics.
type Placement struct {
	PubKey        string `json:"pubkey"`
	Place         int    `json:"place"` // 1 is best
	Length        int    `json:"length"`
	Score         int    `json:"score"`
	Kills         int    `json:"kills"`
	SurvivalTicks int    `json:"survival_ticks"`
}

// MatchResult is the host's canonical description of a finished match. It is
// hashed and signed as-is, so every field here is part of the record's
// identity.
type MatchResult struct {
	Version      int           `json:"version"`
	MatchID      string        `json:"match_id"`
	LobbyName    string        `json:"lobby_name"`
	Config       game.Config   `json:"config"`
	StartedAt    string        `json:"started_at"` // RFC3339 nanoseconds, UTC
	EndedAt      string        `json:"ended_at"`
	HostPubKey   string        `json:"host_pubkey"`
	Participants []Participant `json:"participants"` // sorted by PubKey
	Placements   []Placement   `json:"placements"`   // sorted by Place
}

// Signature is one participant's attestation over a result hash.
type Signature struct {
	PubKey string `json:"pubkey"`
	Sig    string `json:"sig"` // base64 raw-std ed25519 signature
}

// AttestedRecord is a match result plus the signatures collected for it. A
// record whose participants did not all sign — someone dropped before the
// match ended, say — is still a valid record, just partially attested.
type AttestedRecord struct {
	Result     MatchResult `json:"result"`
	Hash       string      `json:"hash"` // hex sha256 of the canonical form
	Signatures []Signature `json:"signatures"`
}

// NewMatchID returns a random RFC 4122 version 4 UUID string.
func NewMatchID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// crypto/rand does not fail on any platform tailsnail supports; a
		// panic here is preferable to silently issuing colliding match IDs.
		panic(fmt.Sprintf("proto: reading randomness for match ID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// FormatTime renders a timestamp in the canonical record format.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// EncodeKey renders a public key for the wire.
func EncodeKey(pub ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(pub)
}

// DecodeKey parses a wire-encoded public key.
func DecodeKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("proto: decoding public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("proto: public key is %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// Normalize sorts the participant and placement lists into their canonical
// order. Sign and Digest call it, so callers building a result by hand need
// not sort themselves.
func (r *MatchResult) Normalize() {
	sort.Slice(r.Participants, func(i, j int) bool { return r.Participants[i].PubKey < r.Participants[j].PubKey })
	sort.Slice(r.Placements, func(i, j int) bool {
		if r.Placements[i].Place != r.Placements[j].Place {
			return r.Placements[i].Place < r.Placements[j].Place
		}
		return r.Placements[i].PubKey < r.Placements[j].PubKey
	})
}

// Canonical returns the deterministic byte encoding that is hashed and signed.
//
// The result is JSON re-serialised with every object key sorted, so the hash
// survives struct field reordering, differing Go versions, and a round trip
// through any other JSON implementation.
func (r MatchResult) Canonical() ([]byte, error) {
	r.Normalize()
	return canonicalJSON(r)
}

// Digest returns the sha256 over the domain-separated canonical form.
func (r MatchResult) Digest() ([]byte, error) {
	canon, err := r.Canonical()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(sigDomain))
	h.Write(canon)
	return h.Sum(nil), nil
}

// HashHex returns Digest in the hex form used for record IDs and inventories.
func (r MatchResult) HashHex() (string, error) {
	d, err := r.Digest()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(d), nil
}

// Participant returns the participant with the given key, if present.
func (r MatchResult) Participant(pub string) (Participant, bool) {
	for _, p := range r.Participants {
		if p.PubKey == pub {
			return p, true
		}
	}
	return Participant{}, false
}

// Winner returns the public key of the first-place finisher.
func (r MatchResult) Winner() (string, bool) {
	for _, p := range r.Placements {
		if p.Place == 1 {
			return p.PubKey, true
		}
	}
	return "", false
}

// Started parses StartedAt, returning the zero time if it is malformed.
func (r MatchResult) Started() time.Time {
	t, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Ended parses EndedAt, returning the zero time if it is malformed.
func (r MatchResult) Ended() time.Time {
	t, err := time.Parse(time.RFC3339Nano, r.EndedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Validate checks structural invariants that a well-formed record must hold,
// independent of any signature.
func (r MatchResult) Validate() error {
	switch {
	case r.Version != MatchResultVersion:
		return fmt.Errorf("proto: unsupported match result version %d", r.Version)
	case r.MatchID == "":
		return errors.New("proto: match result has no ID")
	case len(r.Participants) == 0:
		return errors.New("proto: match result has no participants")
	case r.HostPubKey == "":
		return errors.New("proto: match result has no host key")
	}
	seen := make(map[string]bool, len(r.Participants))
	for _, p := range r.Participants {
		if _, err := DecodeKey(p.PubKey); err != nil {
			return err
		}
		if seen[p.PubKey] {
			return fmt.Errorf("proto: duplicate participant %s", shortKey(p.PubKey))
		}
		seen[p.PubKey] = true
	}
	if !seen[r.HostPubKey] {
		return errors.New("proto: host is not among the participants")
	}
	for _, p := range r.Placements {
		if !seen[p.PubKey] {
			return fmt.Errorf("proto: placement for non-participant %s", shortKey(p.PubKey))
		}
	}
	return nil
}

// SignResult produces this install's signature over a result.
func SignResult(priv ed25519.PrivateKey, r MatchResult) (Signature, error) {
	digest, err := r.Digest()
	if err != nil {
		return Signature{}, err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return Signature{}, errors.New("proto: private key has no ed25519 public half")
	}
	return Signature{
		PubKey: EncodeKey(pub),
		Sig:    base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, digest)),
	}, nil
}

// NewAttestedRecord wraps a result with its hash and no signatures yet.
func NewAttestedRecord(r MatchResult) (AttestedRecord, error) {
	r.Normalize()
	if err := r.Validate(); err != nil {
		return AttestedRecord{}, err
	}
	h, err := r.HashHex()
	if err != nil {
		return AttestedRecord{}, err
	}
	return AttestedRecord{Result: r, Hash: h}, nil
}

// AddSignature verifies sig against the record and inserts it in canonical
// order, replacing any earlier signature from the same key. A signature from a
// key that did not play is rejected.
func (a *AttestedRecord) AddSignature(sig Signature) error {
	if _, ok := a.Result.Participant(sig.PubKey); !ok {
		return fmt.Errorf("proto: %s did not play in this match", shortKey(sig.PubKey))
	}
	if err := a.verifyOne(sig); err != nil {
		return err
	}
	for i, existing := range a.Signatures {
		if existing.PubKey == sig.PubKey {
			a.Signatures[i] = sig
			return nil
		}
	}
	a.Signatures = append(a.Signatures, sig)
	sort.Slice(a.Signatures, func(i, j int) bool { return a.Signatures[i].PubKey < a.Signatures[j].PubKey })
	return nil
}

// verifyOne checks a single signature against the record's digest.
func (a *AttestedRecord) verifyOne(sig Signature) error {
	pub, err := DecodeKey(sig.PubKey)
	if err != nil {
		return err
	}
	raw, err := base64.RawStdEncoding.DecodeString(sig.Sig)
	if err != nil {
		return fmt.Errorf("proto: decoding signature from %s: %w", shortKey(sig.PubKey), err)
	}
	digest, err := a.Result.Digest()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, digest, raw) {
		return fmt.Errorf("proto: signature from %s does not verify", shortKey(sig.PubKey))
	}
	return nil
}

// Verify checks the record end to end: the result is structurally sound, the
// stored hash matches the canonical form, and every signature present is valid
// and belongs to a participant. Missing signatures are not an error — see
// FullyAttested.
func (a *AttestedRecord) Verify() error {
	if err := a.Result.Validate(); err != nil {
		return err
	}
	want, err := a.Result.HashHex()
	if err != nil {
		return err
	}
	if a.Hash != want {
		return fmt.Errorf("proto: record hash %s does not match its contents (%s)", shortHash(a.Hash), shortHash(want))
	}
	seen := make(map[string]bool, len(a.Signatures))
	for _, sig := range a.Signatures {
		if seen[sig.PubKey] {
			return fmt.Errorf("proto: duplicate signature from %s", shortKey(sig.PubKey))
		}
		seen[sig.PubKey] = true
		if _, ok := a.Result.Participant(sig.PubKey); !ok {
			return fmt.Errorf("proto: signature from %s, who did not play", shortKey(sig.PubKey))
		}
		if err := a.verifyOne(sig); err != nil {
			return err
		}
	}
	return nil
}

// SignedBy reports whether the given key has attested this record.
func (a *AttestedRecord) SignedBy(pub string) bool {
	for _, s := range a.Signatures {
		if s.PubKey == pub {
			return true
		}
	}
	return false
}

// FullyAttested reports whether every participant signed.
func (a *AttestedRecord) FullyAttested() bool {
	return len(a.Signatures) == len(a.Result.Participants)
}

// AttestationSummary renders the signature coverage for the history screen.
func (a *AttestedRecord) AttestationSummary() string {
	if a.FullyAttested() {
		return fmt.Sprintf("attested %d/%d", len(a.Signatures), len(a.Result.Participants))
	}
	return fmt.Sprintf("partial %d/%d", len(a.Signatures), len(a.Result.Participants))
}

// canonicalJSON renders v as JSON with every object key sorted and no
// insignificant whitespace. Marshalling to generic values first means the
// output depends only on the data, never on Go struct declaration order.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("proto: canonicalising: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve the exact numeric literal rather than reformatting via float64
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("proto: canonicalising: %w", err)
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeCanonical emits one generic JSON value in canonical form.
func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(t.String())
	case string:
		enc, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(enc)
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			enc, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(enc)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("proto: cannot canonicalise %T", v)
	}
	return nil
}

// shortKey abbreviates a public key for error messages and compact UI.
func shortKey(pub string) string {
	if len(pub) <= 8 {
		return pub
	}
	return pub[:8]
}

// shortHash abbreviates a hex hash for error messages.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// ShortKey is the exported form of shortKey, used by the UI to label players
// whose display names collide.
func ShortKey(pub string) string { return shortKey(pub) }

// SanitizeDisplayName trims a peer-supplied name to something safe to render
// in a terminal: no control characters, no newlines, bounded length.
func SanitizeDisplayName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 24 {
		s = string([]rune(s)[:24])
	}
	if s == "" {
		return "anonymous"
	}
	return s
}
