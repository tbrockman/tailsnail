// Package store persists everything tailsnail keeps between runs: the embedded
// Tailscale node state, this install's signing identity, user settings, and the
// append-only log of attested match records.
//
// The layout under the state directory is:
//
//	tsnet/            embedded tailscale node state (managed by tsnet)
//	identity.json     ed25519 signing key for this install
//	settings.json     user preferences
//	matches/*.json    one attested match record per file
//	tsnail.log        verbose log mirror (only with --verbose)
package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// dirPerm keeps the state directory private: it holds a signing key and the
// Tailscale node key.
const dirPerm = 0o700

// filePerm applies to every file the store writes.
const filePerm = 0o600

// DefaultStateDir returns the per-user state directory, honouring the platform
// convention via os.UserConfigDir: ~/.config/tsnail on Linux and
// ~/Library/Application Support/tsnail on macOS.
func DefaultStateDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: locating user config directory: %w", err)
	}
	return filepath.Join(base, "tsnail"), nil
}

// EnsureDir creates dir and any parents with private permissions. It also
// tightens the permissions of an existing directory that is too permissive,
// which matters because the directory holds private keys.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("store: creating %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("store: stat %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, dirPerm); err != nil {
			return fmt.Errorf("store: tightening permissions on %s: %w", dir, err)
		}
	}
	return nil
}

// TsnetDir returns the directory tsnet uses for its node state.
func TsnetDir(stateDir string) string { return filepath.Join(stateDir, "tsnet") }

// MatchesDir returns the directory holding attested match records.
func MatchesDir(stateDir string) string { return filepath.Join(stateDir, "matches") }

// LogPath returns the file --verbose mirrors the log ring into.
func LogPath(stateDir string) string { return filepath.Join(stateDir, "tsnail.log") }

// writeFileAtomic writes data to path via a temporary file and a rename, so a
// crash mid-write can never leave a half-written record or key behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("store: creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("store: setting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: renaming into %s: %w", path, err)
	}
	return nil
}
