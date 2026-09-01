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

# Tailscale carries a great deal that a game has no use for. Its ts_omit_
# build tags drop those features at compile time, which takes about 15% off
# the binary. Two are conspicuously absent: serve and acme cannot be omitted
# because tsnet itself references SetServeConfig and GetCertificate.
#
# Omitting logtail is not only about size — it also means the binary has no
# path for uploading logs to Tailscale's servers, which is the right default
# for a game.
OMIT=(
  # Integrations with platforms and daemons we never touch.
  aws kube bird synology cloud dbus networkmanager resolved systray
  desktop_sessions serviceclientprefs syspolicy sdnotify syslog tpm
  # Tailscale features a game peer does not offer or use.
  appconnectors captiveportal conn25 drive taildrop tailnetlock ssh tap
  webclient relayserver outboundproxy portlist posture wakeonlan
  identityfederation oauthkey remoteconfig
  # This node is a leaf: it neither advertises nor uses routes or exit nodes.
  advertiseexitnode advertiseroutes useexitnode useroutes c2n
  # Telemetry and log upload.
  logtail netlog runtimemetrics
  # CLI machinery — tailsnail has its own interface.
  cli cliconndiag completion completion_scripts qrcodes clientupdate
  flashappliance hujsonconf
  # Diagnostics.
  debug debugeventbus debugportmapper doctor capture
  # Linux host plumbing that tsnet's userspace stack does not use.
  iptables linkspeed linuxdnsfight tundevstats
)
TAGS="$(printf 'ts_omit_%s,' "${OMIT[@]}")"
TAGS="${TAGS%,}"

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
  # source at the same commit produces the same bytes on any machine.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -tags "$TAGS" \
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
