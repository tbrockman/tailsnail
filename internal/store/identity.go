package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/theolol/tailsnail/internal/proto"
)

// identityVersion is the on-disk schema version of identity.json.
const identityVersion = 1

// Identity is this install's persistent signing key plus the name it plays
// under. The key is generated once and never leaves the machine; only the
// public half goes on the wire.
type Identity struct {
	Public      ed25519.PublicKey
	Private     ed25519.PrivateKey
	DisplayName string
}

// identityFile is the JSON shape stored on disk.
type identityFile struct {
	Version     int    `json:"version"`
	PrivateKey  string `json:"private_key"` // base64 raw-std ed25519 seed+public
	DisplayName string `json:"display_name"`
}

// PubKey returns the wire encoding of the public key.
func (i *Identity) PubKey() string { return proto.EncodeKey(i.Public) }

// Short returns an abbreviated key for disambiguating identical display names.
func (i *Identity) Short() string { return proto.ShortKey(i.PubKey()) }

// identityPath returns the location of the identity file.
func identityPath(stateDir string) string { return filepath.Join(stateDir, "identity.json") }

// LoadOrCreateIdentity reads the install identity, generating one on first run.
// fallbackName is used only when creating a new identity.
func LoadOrCreateIdentity(stateDir, fallbackName string) (*Identity, error) {
	if err := EnsureDir(stateDir); err != nil {
		return nil, err
	}
	path := identityPath(stateDir)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		id, err := parseIdentity(raw)
		if err != nil {
			return nil, fmt.Errorf("store: reading %s: %w", path, err)
		}
		return id, nil
	case errors.Is(err, os.ErrNotExist):
		// First run on this machine: mint a key.
	default:
		return nil, fmt.Errorf("store: reading %s: %w", path, err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("store: generating signing key: %w", err)
	}
	id := &Identity{Public: pub, Private: priv, DisplayName: proto.SanitizeDisplayName(fallbackName)}
	if err := id.Save(stateDir); err != nil {
		return nil, err
	}
	return id, nil
}

// parseIdentity decodes an identity file.
func parseIdentity(raw []byte) (*Identity, error) {
	var f identityFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Version != identityVersion {
		return nil, fmt.Errorf("unsupported identity version %d", f.Version)
	}
	key, err := base64.RawStdEncoding.DecodeString(f.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key is %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(key)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("private key has no ed25519 public half")
	}
	return &Identity{
		Public:      pub,
		Private:     priv,
		DisplayName: proto.SanitizeDisplayName(f.DisplayName),
	}, nil
}

// Save writes the identity back to disk with private permissions.
func (i *Identity) Save(stateDir string) error {
	raw, err := json.MarshalIndent(identityFile{
		Version:     identityVersion,
		PrivateKey:  base64.RawStdEncoding.EncodeToString(i.Private),
		DisplayName: i.DisplayName,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding identity: %w", err)
	}
	return writeFileAtomic(identityPath(stateDir), append(raw, '\n'))
}

// SuggestDisplayName derives a friendly default name from the OS username and
// hostname, e.g. "ada@laptop".
func SuggestDisplayName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "terminal"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	if user == "" {
		return proto.SanitizeDisplayName(host)
	}
	return proto.SanitizeDisplayName(user + "@" + host)
}
