#!/usr/bin/env bash
# The routine update pass, run on the Mac every few days (there is no hosted
# CI for now). It
#   1. scans our dependencies for known vulnerabilities (Go and JS),
#   2. tests each open Renovate pull request locally and merges the ones that
#      pass: security fixes always, every other update with --all,
#   3. with --deploy, updates the box (HCLOUD_TOKEN from .env) once anything
#      was merged.
#
#   scripts/maintain.sh                  # defensive: scan + security fixes
#   scripts/maintain.sh --all --deploy   # the weekly full pass
#
# The box itself takes Ubuntu security patches daily (unattended-upgrades)
# and Postgres minor updates in its maintenance window.
set -uo pipefail
cd "$(dirname "$0")/.."

REPO=shiptiffin/tiffin
BOX=shiptiffin
ALL=0 DEPLOY=0
for a in "$@"; do
  case $a in
    --all) ALL=1 ;;
    --deploy) DEPLOY=1 ;;
    *) echo "usage: scripts/maintain.sh [--all] [--deploy]" >&2; exit 2 ;;
  esac
done

step() { printf '\n==> %s\n' "$*"; }
merged=() failed=() skipped=() found=0

checks() {
  make ci >/dev/null 2>&1 || { echo "  make ci failed"; return 1; }
  bun install --frozen-lockfile >/dev/null 2>&1 || { echo "  bun install failed"; return 1; }
  (cd apps/dashboard && bun run typecheck && bun run lint) >/dev/null 2>&1 || { echo "  dashboard checks failed"; return 1; }
  (cd packages/sdk && bun run build && bun test) >/dev/null 2>&1 || { echo "  SDK tests failed"; return 1; }
}

step "main is up to date"
git checkout -q main && git pull -q --ff-only origin main || { echo "cannot update main (local changes?)"; exit 1; }

step "known vulnerabilities"
if ! go run golang.org/x/vuln/cmd/govulncheck@latest ./...; then found=1; fi
if ! bun audit --audit-level=high; then found=1; fi

step "Renovate pull requests"
prs=$(gh pr list -R "$REPO" --author app/renovate --state open --json number,title \
  --jq '.[] | "\(.number)\t\(.title)"')
while IFS=$'\t' read -r n title; do
  [ -z "$n" ] && continue
  security=0
  [[ $title == *"[SECURITY]"* ]] && security=1
  if [ "$security" = 0 ] && [ "$ALL" = 0 ]; then
    skipped+=("#$n $title")
    continue
  fi
  echo "#$n $title"
  gh pr update-branch "$n" -R "$REPO" >/dev/null 2>&1 # on top of today's main
  sleep 5
  if gh pr checkout -q "$n" -R "$REPO" && checks; then
    git checkout -q main
    if gh pr merge "$n" -R "$REPO" --squash --delete-branch >/dev/null; then
      merged+=("#$n $title")
      git pull -q --ff-only origin main
    else
      failed+=("#$n $title (merge refused)")
    fi
  else
    git checkout -q main
    failed+=("#$n $title")
  fi
done <<<"$prs"

if [ "$DEPLOY" = 1 ] && [ ${#merged[@]} -gt 0 ]; then
  step "update the box"
  make build >/dev/null && (set -a && . ./.env && set +a &&
    bin/tiffin up --provider hetzner --name "$BOX" --ssh-key ~/.ssh/shiptiffin_ed25519 >/dev/null) ||
    failed+=("box update")
fi

step "summary"
[ "$found" = 1 ] && echo "! known vulnerabilities reported above; fix them or wait for Renovate's pull request"
for x in "${merged[@]}"; do echo "merged   $x"; done
for x in "${failed[@]}"; do echo "FAILED   $x"; done
for x in "${skipped[@]}"; do echo "waiting  $x (not a security fix; runs with --all)"; done
[ ${#merged[@]} -eq 0 ] && [ ${#failed[@]} -eq 0 ] && echo "nothing to merge"
[ ${#failed[@]} -eq 0 ] && [ "$found" = 0 ]
