#!/usr/bin/env bash
# Build release binaries for every supported target.
#
# tailsnail is pure Go with cgo disabled, so one machine cross-compiles the
# lot — there is no need for a build matrix or a runner per platform.
#
#   ./scripts/build-release.sh [version]
#
# Produces dist/tsnail_<version>_<os>_<arch>.tar.gz and a checksum file.
set -euo pipefail

VERSION="${1:-${GITHUB_REF_NAME:-dev}}"
VERSION="${VERSION#v}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
PKG="github.com/tbrockman/tailsnail/internal/version"

TARGETS=(
  "darwin/arm64"   # Apple silicon
  "darwin/amd64"   # Intel Macs
  "linux/amd64"
  "linux/arm64"
)

rm -rf dist && mkdir -p dist
for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  echo "building ${os}/${arch}"

  workdir="$(mktemp -d)"
  # -s -w drop the symbol table and DWARF, which production users have no use
  # for; -trimpath keeps absolute build paths out of the binary, so the same
  # source at the same commit produces the same bytes on any machine. Both
  # remove information rather than code, so neither can change how the binary
  # behaves.
  #
  # Tailscale's ts_omit_ tags would take another 15% off by compiling out
  # features a game cannot use. They are deliberately not used: 1 MB off a
  # download is imperceptible, while disabling code paths inside the
  # dependency that actually reaches the network risks a failure that only
  # appears on someone else's machine, in an environment we cannot test,
  # during onboarding — the worst possible moment for a confusing failure.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -trimpath \
    -ldflags "-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT}" \
    -o "${workdir}/tsnail" \
    ./cmd/tsnail

  cp README.md "${workdir}/" 2>/dev/null || true
  tar -czf "dist/tsnail_${VERSION}_${os}_${arch}.tar.gz" -C "${workdir}" tsnail README.md
  rm -rf "${workdir}"
done

( cd dist && shasum -a 256 ./*.tar.gz > SHA256SUMS )
echo
ls -lh dist
