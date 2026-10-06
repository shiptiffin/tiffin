#!/usr/bin/env bash
# Proves a Bun release before boxes get it. Apps build and run on Bun, and
# Bun has broken Next.js before (timers, CJS, native modules), so a Bun bump
# never merges on unit tests alone: it runs the demo apps on a fresh VM.
#
#   scripts/bun-canary.sh 1.4.3      # set the version, then run the apps
#   scripts/bun-canary.sh            # run the apps on the pinned version
#
# Setting a version updates internal/mod/runtime/versions.go (BunVersion and
# the BunImage digest, read from the registry) and the auth engine's pin and
# download checksums (from the release's SHASUMS256.txt).
# It then runs the e2e suites that build and serve apps: Next.js (SSR, ISR,
# Server Actions, "use cache", images, across two instances and a redeploy),
# a Bun server, a static site and the starters. Green means merge; red means
# stay on the old version, or report the break upstream.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ $# -gt 0 ]; then
  v=$1
  [[ $v =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: scripts/bun-canary.sh [x.y.z]" >&2; exit 2; }
  digest=$(docker buildx imagetools inspect "oven/bun:$v-slim" --format '{{json .Manifest.Digest}}' 2>/dev/null | tr -d '"') ||
    digest=$(crane digest "oven/bun:$v-slim" 2>/dev/null) ||
    { echo "can't read the digest of oven/bun:$v-slim (needs docker buildx or crane)" >&2; exit 1; }
  [[ $digest == sha256:* ]] || { echo "no image oven/bun:$v-slim" >&2; exit 1; }
  sed -i '' -E "s/BunVersion( +)= \"[0-9.]+\"/BunVersion\1= \"$v\"/" internal/mod/runtime/versions.go
  sed -i '' -E "s|(oven/bun:\" \+ BunVersion \+ \"-slim@)sha256:[0-9a-f]+|\1$digest|" internal/mod/runtime/versions.go
  sums=$(curl -fsSL "https://github.com/oven-sh/bun/releases/download/bun-v$v/SHASUMS256.txt") ||
    { echo "no SHASUMS256.txt for bun-v$v" >&2; exit 1; }
  sed -i '' -E "s/^const BunVersion = \"[0-9.]+\"/const BunVersion = \"$v\"/" internal/mod/auth/provision.go
  for asset in bun-linux-aarch64 bun-linux-x64; do
    sum=$(awk -v f="$asset.zip" '$2 == f { print $1 }' <<<"$sums")
    [[ $sum =~ ^[0-9a-f]{64}$ ]] || { echo "no checksum for $asset.zip" >&2; exit 1; }
    sed -i '' -E "s/(\"$asset\", \")[0-9a-f]{64}/\1$sum/" internal/mod/auth/provision.go
  done
  echo "==> Bun $v ($digest)"
  git --no-pager diff --stat -- internal/mod/runtime/versions.go internal/mod/auth/provision.go
fi

make build >/dev/null
${HEAVY:-} go test -tags e2e ./e2e/ -run '^(TestNext|TestRuntime|TestCreate)$' -timeout 75m -count=1 -v
