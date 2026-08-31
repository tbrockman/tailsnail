// Package version carries the build identity of the binary.
package version

// Version is the release version. It is overridable at build time with
//
//	-ldflags "-X github.com/theolol/tailsnail/internal/version.Version=..."
var Version = "0.1.0"

// Commit is the git revision the binary was built from, when known.
var Commit = "unknown"

// String renders the full version for --version and the handshake.
func String() string {
	if Commit == "unknown" || Commit == "" {
		return Version
	}
	return Version + "+" + Commit
}
