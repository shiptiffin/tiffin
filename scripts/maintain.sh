#!/usr/bin/env bash
# The routine update pass, run on the Mac every few days (there is no hosted
# CI for now). It
#   1. scans our dependencies for known vulnerabilities (Go and JS),
#   2. tests each open Renovate pull request locally and merges the ones that
#      pass: security fixes always, every other update with --all (a Bun
#      release waits for scripts/bun-canary.sh, which runs the demo apps),
#   3. with --deploy, updates the box (HCLOUD_TOKEN from .env) once anything
#      was merged,
#   4. with --box, looks after the box itself: installs every pending Ubuntu
#      update (Postgres packages stay with Tiffin's maintenance window),
#      reboots when the kernel or a running service needs it, and rebuilds
#      each running app from its live source so base images, Node and Bun
#      pick up their fixes (zero downtime, like any deploy),
#   5. clears the Go, staticcheck and Bun caches its checks filled.
#
#   scripts/maintain.sh                        # defensive: scan + security fixes
#   scripts/maintain.sh --all --deploy --box   # the weekly full pass (Sunday agent)
#
# Between passes the box takes Ubuntu security patches daily (unattended-upgrades)
# and Postgres minor updates in its maintenance window. scripts/box-inventory.sh
# prints what it runs, to check against new advisories.
set -uo pipefail
cd "$(dirname "$0")/.."

REPO=shiptiffin/tiffin
BOX=shiptiffin
ALL=0 DEPLOY=0 BOXPASS=0
for a in "$@"; do
  case $a in
    --all) ALL=1 ;;
    --deploy) DEPLOY=1 ;;
    --box) BOXPASS=1 ;;
    *) echo "usage: scripts/maintain.sh [--all] [--deploy] [--box]" >&2; exit 2 ;;
  esac
done

step() { printf '\n==> %s\n' "$*"; }
merged=() failed=() skipped=() box=() found=0

checks() {
  make ci >/dev/null 2>&1 || { echo "  make ci failed"; return 1; }
  bun install --frozen-lockfile >/dev/null 2>&1 || { echo "  bun install failed"; return 1; }
  (cd apps/dashboard && bun run typecheck && bun run lint) >/dev/null 2>&1 || { echo "  dashboard checks failed"; return 1; }
  (cd packages/sdk && bun run build && bun test) >/dev/null 2>&1 || { echo "  SDK tests failed"; return 1; }
  make site-test >/dev/null 2>&1 || { echo "  website tests failed"; return 1; }
}

step "main is up to date"
git checkout -q main && git pull -q --ff-only origin main || { echo "cannot update main (local changes?)"; exit 1; }

step "known vulnerabilities"
if ! go run golang.org/x/vuln/cmd/govulncheck@latest ./...; then found=1; fi
if ! bun audit --audit-level=high; then found=1; fi

step "Renovate pull requests"
prs=$(gh pr list -R "$REPO" --author app/renovate --state open --json number,title,labels \
  --jq '.[] | "\(.number)\t\(.title)\t\([.labels[].name] | join(","))"')
while IFS=$'\t' read -r n title labels; do
  [ -z "$n" ] && continue
  if [[ ,$labels, == *,bun,* ]]; then
    skipped+=("#$n $title (Bun: run scripts/bun-canary.sh on its branch, then merge by hand)")
    continue
  fi
  security=0
  [[ $title == *"[SECURITY]"* ]] && security=1
  if [ "$security" = 0 ] && [ "$ALL" = 0 ]; then
    skipped+=("#$n $title (not a security fix; runs with --all)")
    continue
  fi
  echo "#$n $title"
  gh pr update-branch "$n" -R "$REPO" >/dev/null 2>&1 # on top of today's main
  sleep 5
  if gh pr checkout "$n" -R "$REPO" >/dev/null 2>&1 && checks; then
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

if [ "$BOXPASS" = 1 ]; then
  srv=$(jq -r --arg b "$BOX" '.boxes[$b].server' ~/.tiffin/boxes.json)
  SSH=(ssh -o BatchMode=yes -o ConnectTimeout=15
    -o UserKnownHostsFile="$(jq -r .knownHosts <<<"$srv")"
    -i "$(jq -r .identity <<<"$srv")" "$(jq -r .ssh <<<"$srv")")
  tf() { bin/tiffin "$@"; }

  step "box: Ubuntu updates"
  # Everything pending except PGDG: Postgres minors go through Tiffin's own
  # maintenance window, which restarts Postgres safely.
  if "${SSH[@]}" 'bash -s' <<'EOF'
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
pkgs=$(apt list --upgradable 2>/dev/null | awk -F/ 'NR>1 && $2 !~ /pgdg/ {print $1}')
if [ -n "$pkgs" ]; then
  echo "$pkgs" | tr '\n' ' '; echo
  apt-get install -y -qq --only-upgrade -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold $pkgs >/dev/null
fi
apt-get autoremove -y -qq --purge >/dev/null
apt-get clean
EOF
  then box+=("Ubuntu updates installed"); else failed+=("box: Ubuntu updates"); fi

  step "box: reboot if needed"
  why=$("${SSH[@]}" '[ -f /var/run/reboot-required ] && echo "kernel/system: $(tr "\n" " " </var/run/reboot-required.pkgs 2>/dev/null)";
    needrestart -b 2>/dev/null | sed -n "s/^NEEDRESTART-SVC: //p" | tr "\n" " "')
  if [ -n "${why// /}" ]; then
    echo "rebooting for: $why"
    "${SSH[@]}" 'systemctl reboot' >/dev/null 2>&1
    sleep 30
    back=0
    for _ in $(seq 60); do # up to 10 minutes
      if "${SSH[@]}" true 2>/dev/null && [ "$(tf health --json 2>/dev/null | jq -r .status)" = ok ]; then back=1; break; fi
      sleep 10
    done
    if [ "$back" = 1 ]; then box+=("rebooted ($why) and healthy again"); else failed+=("box: not healthy 10 minutes after reboot"); fi
  else
    echo "no reboot needed"
  fi

  step "box: rebuild running apps"
  # Static sites have no runtime to patch. One build at a time: the box is small.
  for p in $(tf projects list --json | jq -r '.[].name'); do
    for a in $(tf projects get "$p" --json | jq -r '
        [.resources[] | select(.address|startswith("app/")) | select(.spec.framework != "static") | .address] as $apps
        | .status | to_entries[] | select(.key as $k | $apps | index($k)) | select(.value.release == "live")
        | .key | sub("app/";"")'); do
      printf '%s/%s: ' "$p" "$a"
      id=$(tf deploys redeploy "$p" "$a" --json 2>/dev/null | jq -r '.id // empty')
      if [ -z "$id" ]; then echo "cannot rebuild (prebuilt image or no source kept)"; skipped+=("$p/$a (no source to rebuild: redeploy it from its source)"); continue; fi
      for _ in $(seq 8); do # the API waits at most 300s per call: up to 40 minutes
        st=$(tf deploys get "$p" "$a" "$id" --wait 300 --json 2>/dev/null | jq -r '.status // "unknown"')
        case $st in queued | building | starting | restarting) ;; *) break ;; esac
      done
      echo "$st"
      if [ "$st" = live ]; then box+=("rebuilt $p/$a"); else failed+=("box: rebuild of $p/$a ($id: $st; tiffin deploys build-log $p $a $id)"); fi
    done
  done

  step "box: health"
  tf doctor >/dev/null 2>&1 && echo ok || failed+=("box: tiffin doctor reports a problem")
fi

step "build caches"
# Every check rebuilds everything: give the disk back (the next run fills them again).
go clean -cache -testcache
rm -rf "${XDG_CACHE_HOME:-$HOME/Library/Caches}/staticcheck" "$HOME/.cache/staticcheck"
bun pm cache rm >/dev/null 2>&1 || true

step "summary"
[ "$found" = 1 ] && echo "! known vulnerabilities reported above; fix them or wait for Renovate's pull request"
for x in ${merged[@]+"${merged[@]}"}; do echo "merged   $x"; done
for x in ${box[@]+"${box[@]}"}; do echo "box      $x"; done
for x in ${failed[@]+"${failed[@]}"}; do echo "FAILED   $x"; done
for x in ${skipped[@]+"${skipped[@]}"}; do echo "waiting  $x"; done
[ ${#merged[@]} -eq 0 ] && [ ${#failed[@]} -eq 0 ] && [ ${#box[@]} -eq 0 ] && echo "nothing to merge"
[ ${#failed[@]} -eq 0 ] && [ "$found" = 0 ]
