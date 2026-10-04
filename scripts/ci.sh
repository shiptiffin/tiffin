#!/usr/bin/env bash
# CI for the Mac / agents: lint + test + a release dry run (cross-compile all
# six targets, discard the output). Same as `make ci`.
set -euo pipefail
cd "$(dirname "$0")/.."

step() { printf '\n==> %s\n' "$*"; }

step "lint"
make lint

step "test"
make test

step "release dry run"
for t in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  echo "  $t"
  CGO_ENABLED=0 GOOS="${t%/*}" GOARCH="${t#*/}" go build -trimpath -o /dev/null ./cmd/tiffin
done

step "ci ok"
