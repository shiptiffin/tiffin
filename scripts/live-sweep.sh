#!/usr/bin/env bash
# live-sweep.sh: deploy every starter template (plus a Dockerfile app) to a
# real box, measure it, check it, and remove everything it made.
#
#   scripts/live-sweep.sh                 # run the sweep against the current box
#   scripts/live-sweep.sh inventory       # what is on the box (needs SWEEP_SSH)
#   scripts/live-sweep.sh cleanup         # destroy leftover sweep-* projects
#   scripts/live-sweep.sh reset --yes     # destroy every project but $SWEEP_KEEP, prune the box
#
#   SWEEP_SSH=root@<box ip> SWEEP_SSH_KEY=~/.ssh/<key> scripts/live-sweep.sh   # the full sweep
#
# For each template it creates a project named sweep-<template> (plan, apply,
# deploy the starter) and records: build and deploy time, GET / status and
# time to first byte (the first request after the deploy, then a warm one),
# Cache-Control on a hashed static asset, idle memory, sleep then wake time,
# requests that failed during a redeploy (a curl loop runs through it), whether
# logs come back, and whether the database answers. It destroys each project
# when done (and any sweep-* project on exit), then checks the box for
# leftovers. Results print as a Markdown table and are written to
# notes/<date>-live-sweep.md.
#
# Environment:
#   TIFFIN             the CLI (default: bin/tiffin, else tiffin on PATH); it talks to
#                      the current box (~/.tiffin/boxes.json) unless TIFFIN_URL is set
#   SWEEP_SSH          user@host of the box, for the sleep knob and the leftover checks
#   SWEEP_SSH_KEY      ssh identity file for SWEEP_SSH
#   SWEEP_ONLY         space-separated template ids to run (default: every template, plus dockerfile)
#   SWEEP_JOBS         templates in flight at once (default 3; the box runs one build at a
#                      time, so the others' deploys queue, and sleep checks overlap builds)
#   SWEEP_SLEEP_AFTER  idle time before apps sleep during the sweep (default 45s). Needs
#                      SWEEP_SSH: it sets TIFFIN_SLEEP_AFTER on the box's tiffin service
#                      (a systemd drop-in, removed on exit). It only affects projects that
#                      set sleepAfter, which here are the sweep's own. 0 skips sleep checks.
#   SWEEP_REPORT       report path (default notes/<date>-live-sweep.md)
#   SWEEP_KEEP         projects reset leaves alone (default "website")
#
# Needs bash (3.2 is fine), curl, jq and perl.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)

TIFFIN=${TIFFIN:-}
if [ -z "$TIFFIN" ]; then
  if [ -x "$ROOT/bin/tiffin" ]; then TIFFIN=$ROOT/bin/tiffin; else TIFFIN=$(command -v tiffin || true); fi
fi
[ -n "$TIFFIN" ] || { echo "no tiffin CLI: run make build or set TIFFIN" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

SSH_HOST=${SWEEP_SSH:-}
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=15)
[ -n "${SWEEP_SSH_KEY:-}" ] && SSH_OPTS+=(-i "$SWEEP_SSH_KEY")
JOBS=${SWEEP_JOBS:-3}
SLEEP_AFTER=${SWEEP_SLEEP_AFTER:-45s}
KEEP=${SWEEP_KEEP:-website}
PREFIX=sweep-
REPORT=${SWEEP_REPORT:-$ROOT/notes/$(date +%F)-live-sweep.md}
WORK=""  # made by run_sweep
DROPIN=/etc/systemd/system/tiffin.service.d/live-sweep-sleep.conf

t() { "$TIFFIN" --json "$@"; }
box() { [ -n "$SSH_HOST" ] && ssh "${SSH_OPTS[@]}" "$SSH_HOST" "$@"; }
exec 3>&2
log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" >&3; }
now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }
since() { awk -v a="$1" -v b="$(now)" 'BEGIN{printf "%.1f", b-a}'; }

# ---- box inventory over SSH -------------------------------------------------

# inventory [prefix]: the box's resources; with a prefix, only those of
# projects whose names start with it (each line is then a leftover).
inventory() {
  [ -n "$SSH_HOST" ] || { echo "(no SWEEP_SSH: box inventory skipped)"; return 0; }
  box bash -s -- "${1:-}" <<'EOF'
p=$1; db=$(printf 'p_%s' "$p" | tr - _ | sed 's/_/\\_/g'); ns="nerdctl -n tiffin"
L=/var/lib/tiffin
if [ -z "$p" ]; then
  echo "## load";  uptime; free -m | sed -n 1,2p
  echo "## disk";  df -h / | tail -1; du -shx $L/containerd $L/buildkit $L/logs $L/observe $L/backups $L/cache $L/runtime 2>/dev/null
  echo "## buildkit cache"; buildctl --addr unix:///run/buildkit/buildkitd.sock du 2>/dev/null | tail -2
fi
echo "## containers"; $ns ps -a --format '{{.Names}}  {{.Status}}' | grep -E "^tf\.${p}" ; [ -z "$p" ] && $ns ps -a --format '{{.Names}}  {{.Status}}' | grep -v '^tf\.'
# Build and copy helpers that stopped: --rm is nerdctl's client's job, so a killed client left them (a running one is in use).
echo "## stopped helper containers"; $ns ps -a --filter label=tiffin.helper --format '{{.Names}}  {{.Status}}' | grep -vi '^[^ ]*  *up'
echo "## images"; $ns images --format '{{.Repository}}:{{.Tag}}  {{.Size}}' | grep -E "^tiffin/${p}" ; [ -z "$p" ] && $ns images --format '{{.Repository}}:{{.Tag}}  {{.Size}}' | grep -v '^tiffin/'
q() { sudo -u postgres psql -h /var/run/postgresql -Atc "$1" 2>/dev/null; }
echo "## databases"; q "select datname from pg_database where datname like '${db}%' and datname not in ('postgres','template0','template1') order by 1"
echo "## roles"; q "select rolname from pg_roles where rolname like '${db}%' and rolname not like 'pg\_%' order by 1"
echo "## valkey users"; awk '{print $2}' $L/valkey/users.acl 2>/dev/null | grep -vE '^(default|tiffin)$' | grep -E "^${p}"
echo "## buckets"; ls $L/storage/data 2>/dev/null | grep -E "^${p}"
echo "## storage trash"; ls $L/trash/storage 2>/dev/null | wc -l | tr -d ' ' | sed 's/$/ entries/'
for d in deploys static assets build-cache next-cache disks home; do
  echo "## runtime/$d"; ls $L/runtime/$d 2>/dev/null | grep -E "^${p}"
done
echo "## edge hosts"; grep -oE "\"${p:-[a-z0-9]}[a-z0-9-]*\.[a-z0-9.-]+\"" $L/platform/edge-snapshot.json 2>/dev/null | sort -u | grep -E "^\"${p}" | grep -v '^"http\.log' | head -50
EOF
}

# leftovers prefix: inventory lines that are real resources (not headings or counts).
leftovers() {
  inventory "$1" | awk '/^## /{sec=substr($0,4); next} /entries$/||/^$/{next} {print sec ": " $0}'
}

# ---- the sleep knob ---------------------------------------------------------

sleep_on=0
sleep_knob_on() {
  [ "$SLEEP_AFTER" = 0 ] && return 0
  [ -n "$SSH_HOST" ] || { log "no SWEEP_SSH: sleep checks skipped"; SLEEP_AFTER=0; return 0; }
  log "box: TIFFIN_SLEEP_AFTER=$SLEEP_AFTER (restarts the tiffin service)"
  box "install -d ${DROPIN%/*} && printf '[Service]\nEnvironment=TIFFIN_SLEEP_AFTER=$SLEEP_AFTER\n' > $DROPIN && systemctl daemon-reload && systemctl restart tiffin &&
    for i in \$(seq 1 120); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && exit 0; sleep 0.5; done; exit 1" ||
    { log "could not set the sleep knob: sleep checks skipped"; SLEEP_AFTER=0; return 0; }
  sleep_on=1
}
sleep_knob_off() {
  [ "$sleep_on" = 1 ] || return 0
  log "box: removing TIFFIN_SLEEP_AFTER"
  box "rm -f $DROPIN && rmdir ${DROPIN%/*} 2>/dev/null; systemctl daemon-reload && systemctl restart tiffin &&
    for i in \$(seq 1 120); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && exit 0; sleep 0.5; done; exit 1"
  sleep_on=0
}

# ---- project helpers --------------------------------------------------------

destroy() { # project
  local out hash
  out=$(t projects destroy "$1" 2>&1)
  hash=$(printf '%s' "$out" | jq -r '.plan.hash // empty' 2>/dev/null)
  [ -n "$hash" ] || { printf 'destroy %s: no plan: %s\n' "$1" "$out" >&2; return 1; }
  t projects destroy "$1" --confirm "$hash" -m "live sweep cleanup" >/dev/null 2>&1 ||
    { printf 'destroy %s failed\n' "$1" >&2; return 1; }
}

apply_manifest() { # file-or-dir
  local plan hash
  plan=$(t plan "$1" 2>&1)
  hash=$(printf '%s' "$plan" | jq -r '.hash // empty' 2>/dev/null)
  [ -n "$hash" ] || { echo "plan failed: $plan"; return 1; }
  t apply "$1" --confirm "$hash" -m "live sweep" >/dev/null 2>&1 || { echo "apply failed"; return 1; }
}

wait_services() { # project
  local i st
  for i in $(seq 1 90); do
    st=$(t projects get "$1" 2>/dev/null | jq -r '[.status | to_entries[] | select(.key|startswith("service/")) | .value.state] | if any(.=="failed") then "failed" elif all(.=="ready") then "ready" else "wait" end')
    [ "$st" = ready ] && return 0
    [ "$st" = failed ] && return 1
    sleep 2
  done
  return 1
}

wait_deploy() { # project app id → prints the deploy JSON once live; 1 when it failed
  local i dep st
  for i in $(seq 1 450); do
    dep=$(t deploys get "$1" "$2" "$3" 2>/dev/null)
    st=$(printf '%s' "$dep" | jq -r '.status // empty' 2>/dev/null)
    case $st in
      live) printf '%s' "$dep"; return 0 ;;
      failed|canceled|cancelled) printf '%s' "$dep"; return 1 ;;
    esac
    sleep 2
  done
  printf '{"status":"timeout"}'; return 1
}

# start_deploy id project app dir → prints the deploy id
start_deploy() {
  if [ "$1" = dockerfile ]; then
    t deploy "$4" --no-wait 2>&1 | jq -r '(.deploys // [.])[0].id // .id // empty' 2>/dev/null
  else
    t deploys template "$2" "$3" --template "$1" 2>&1 | jq -r '.id // empty' 2>/dev/null
  fi
}

# fetch url [body-file] → "code seconds": seconds from the request (TLS already
# up) to its first byte, so it is the box's time plus one network round trip.
fetch() {
  curl -s -o "${2:-/dev/null}" -w '%{http_code} %{time_appconnect} %{time_starttransfer}' --max-time 120 "$1" 2>/dev/null |
    awk '{printf "%s %.2f\n", $1, $3-$2}' || echo "000 0"
}

dockerfile_app() { # dir project
  mkdir -p "$1"
  cat >"$1/Dockerfile" <<'EOF'
FROM golang:1-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /app .
FROM scratch
COPY --from=build /app /app
CMD ["/app"]
EOF
  printf 'module sweep\n\ngo 1.22\n' >"$1/go.mod"
  cat >"$1/main.go" <<'EOF'
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		fmt.Fprintln(w, "hello from a Dockerfile app")
	})
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
EOF
  local sleep_line=""
  [ "$SLEEP_AFTER" != 0 ] && sleep_line='  sleepAfter: "1h",'
  cat >"$1/tiffin.config.ts" <<EOF
export default {
  project: "$2",
$sleep_line
  apps: { web: { builder: "dockerfile", healthcheck: "/healthz" } },
};
EOF
}

# ---- one template -----------------------------------------------------------

# sweep_one id: writes key=value lines to $WORK/<id>.res
sweep_one() {
  local id=$1 p="$PREFIX$1" res="$WORK/$1.res" app kind svcs dir="" t0 dep did url
  : >"$res"
  put() { printf '%s=%s\n' "$1" "$2" >>"$res"; }
  bad() { printf 'fail=%s\n' "$*" >>"$res"; log "$id: FAIL $*"; }
  put project "$p"

  # Manifest: the template's fragment, as the dashboard merges it.
  if [ "$id" = dockerfile ]; then
    app=web kind=docker svcs=""
    dir="$WORK/dockerfile"; dockerfile_app "$dir" "$p"
    apply_manifest "$dir" >"$WORK/$id.apply" 2>&1 || { bad "apply: $(cat "$WORK/$id.apply")"; return; }
  else
    local tpl; tpl=$(jq -c --arg id "$id" '.templates[] | select(.id==$id)' "$WORK/templates.json")
    app=$(printf '%s' "$tpl" | jq -r '.fragment.apps | keys[0]')
    kind=$(printf '%s' "$tpl" | jq -r '.kind')
    svcs=$(printf '%s' "$tpl" | jq -r '(.services // []) | join(" ")')
    local sa=""; [ "$SLEEP_AFTER" != 0 ] && [ "$kind" != static ] && sa=1h
    printf '%s' "$tpl" | jq --arg p "$p" --arg sa "$sa" \
      '{version: 1, project: $p, apps: .fragment.apps, services: (.fragment.services // {})} + (if $sa != "" then {sleepAfter: $sa} else {} end)' \
      >"$WORK/$id.json"
    apply_manifest "$WORK/$id.json" >"$WORK/$id.apply" 2>&1 || { bad "apply: $(cat "$WORK/$id.apply")"; return; }
  fi
  put app "$app"; put kind "$kind"; put services "${svcs:--}"
  wait_services "$p" || { bad "services not ready"; return; }

  # Deploy.
  log "$id: deploying"
  did=$(start_deploy "$id" "$p" "$app" "$dir")
  [ -n "$did" ] || { bad "deploy did not start"; return; }
  if ! dep=$(wait_deploy "$p" "$app" "$did"); then
    t deploys build-log "$p" "$app" "$did" 2>/dev/null | jq -r '.text // empty' >"$WORK/$id.build.log"
    bad "deploy $did $(printf '%s' "$dep" | jq -r '.status + ": " + (.error // "" | tostring)' | head -c 300) (build log: $WORK/$id.build.log)"
    return
  fi
  put build "$(printf '%s' "$dep" | jq -r '.buildSeconds // 0')"
  put deploy "$(printf '%s' "$dep" | jq -r '.durationSeconds // 0')"
  url=$(printf '%s' "$dep" | jq -r '.url // empty')
  [ -n "$url" ] || url=$(t apps status "$p" "$app" | jq -r '.production.url // empty')
  put url "$url"
  log "$id: live at $url"

  # GET /: the first response after the deploy, then a warm one.
  local r code ttfb
  r=$(fetch "$url/" "$WORK/$id.html"); code=${r% *}; ttfb=${r#* }
  put root "$code"; put first "$ttfb"
  [ "$code" = 200 ] || bad "GET / returned $code"
  r=$(fetch "$url/"); put warm "${r#* }"

  # A hashed static asset carries a long cache lifetime.
  local asset cc acode
  asset=$(grep -aoE '(src|href)="\.?/[^"?#]+\.(css|js|mjs|woff2|webp|avif|png|jpg|svg)"' "$WORK/$id.html" 2>/dev/null |
    sed -E 's/^(src|href)="\.?//; s/"$//' |
    grep -E '^/(_next/static|_astro|_app/immutable|_nuxt|assets|_build|build)/' | head -1)
  if [ -n "$asset" ]; then
    cc=$(curl -s -o /dev/null -D - --max-time 30 "$url$asset" | tr -d '\r')
    acode=$(printf '%s\n' "$cc" | awk 'NR==1{print $2}')
    cc=$(printf '%s\n' "$cc" | grep -i '^cache-control:' | sed 's/^[^:]*: *//')
    put asset "$asset"; put cache "${cc:-none}"
    if [ "$acode" != 200 ]; then bad "asset $asset returned $acode"
    elif ! printf '%s' "$cc" | grep -qE 'max-age=(31536000|[0-9]{8,})' || ! printf '%s' "$cc" | grep -q immutable; then
      bad "asset $asset Cache-Control: ${cc:-none}"
    fi
  elif [ "$kind" = api ] || [ "$kind" = docker ]; then
    put cache "n/a"
  else
    # No hashed file: show what an unhashed one gets (it should revalidate).
    asset=$(grep -aoE '(src|href)="\.?/[^"?#]+\.(css|js)"' "$WORK/$id.html" 2>/dev/null | sed -E 's/^(src|href)="\.?//; s/"$//' | head -1)
    if [ -n "$asset" ]; then
      cc=$(curl -s -o /dev/null -D - --max-time 30 "$url$asset" | tr -d '\r' | grep -i '^cache-control:' | sed 's/^[^:]*: *//')
      put cache "no hashed file; $asset: ${cc:-none}"
    else
      put cache "no hashed file"
    fi
  fi

  # Database: the app's page read it; the project's role can query it.
  if printf '%s' "$svcs" | grep -qw postgres; then
    local n
    n=$(t sql "$p" "select count(*) as n from information_schema.tables where table_schema = 'public'" 2>/dev/null |
      jq -r '(.rows // .results[0].rows // [])[0] | if type=="object" then .n elif type=="array" then .[0] else . end' 2>/dev/null)
    if [ -n "$n" ] && [ "$n" != null ]; then put db "ok ($n tables)"; else put db "no answer"; bad "tiffin sql did not answer"; fi
  else
    put db "n/a"
  fi

  # Logs: the app's instances wrote something.
  if [ "$kind" = static ]; then
    put logs "n/a"
  else
    local lines
    lines=$(t logs "$app" --project "$p" --limit 50 2>/dev/null | jq -r '(.lines // []) | length' 2>/dev/null)
    put logs "${lines:-0} lines"
    [ "${lines:-0}" -gt 0 ] 2>/dev/null || bad "no log lines"
  fi

  # Idle memory: a few seconds after the last request.
  if [ "$kind" = static ]; then
    put mem "n/a"
  else
    sleep 8
    local mb
    mb=$(t projects usage "$p" 2>/dev/null | jq -r --arg a "$app" '[.apps[]? | select(.app==$a and ((.preview // "") == "")) | .memoryBytes] | add // empty | . / 1048576 | floor' 2>/dev/null)
    # The resident memory of the app's processes, when the box is reachable over
    # SSH. The usage figure is what the app's cgroup is charged, which leaves out
    # pages (the runtime's binary, say) first charged to another container.
    local ctr=""
    [ -n "$SSH_HOST" ] && ctr=$(box bash -s -- "$p" "$app" <<'EOF'
c=$(nerdctl -n tiffin ps --format '{{.Names}}' | grep -F "tf.$1.$2.prod." | head -1)
[ -n "$c" ] || exit 0
pid=$(nerdctl -n tiffin inspect "$c" --format '{{.State.Pid}}')
cg=$(cut -d: -f3 "/proc/$pid/cgroup")
ps -o rss= -p "$(paste -sd, "/sys/fs/cgroup$cg/cgroup.procs")" | awk '{s+=$1} END {printf "%d RSS", s/1024}'
EOF
)
    put mem "${mb:-?}${ctr:+ / $ctr}"
  fi

  # Redeploy under a curl loop: no request may fail.
  log "$id: redeploying under load"
  local loop="$WORK/$id.loop" lp did2 dep2 total badn
  : >"$loop"
  ( while :; do curl -s -o /dev/null -w '%{http_code}\n' --max-time 15 "$url/" >>"$loop" 2>/dev/null || echo 000 >>"$loop"; sleep 0.2; done ) &
  lp=$!; echo "$lp" >>"$WORK/loops"
  did2=$(start_deploy "$id" "$p" "$app" "$dir")
  if [ -z "$did2" ]; then bad "redeploy did not start"
  elif ! dep2=$(wait_deploy "$p" "$app" "$did2"); then bad "redeploy $did2 $(printf '%s' "$dep2" | jq -r .status)"
  else put rebuild "$(printf '%s' "$dep2" | jq -r '.buildSeconds // 0')"
  fi
  sleep 5
  kill "$lp" 2>/dev/null; wait "$lp" 2>/dev/null
  total=$(grep -c . "$loop"); badn=$(grep -cvE '^[23][0-9][0-9]$' "$loop")
  put redeploy "$badn/$total"
  [ "$badn" = 0 ] || bad "redeploy: $badn of $total requests failed ($(grep -vE '^[23][0-9][0-9]$' "$loop" | sort | uniq -c | tr '\n' ' '))"

  # Sleep, then a request wakes it; twice, as the first start after a deploy
  # makes a new container and later ones start the kept one.
  if [ "$SLEEP_AFTER" = 0 ] || [ "$kind" = static ]; then
    put wake "n/a"; put boxwake "n/a"
  else
    local n i slept st w client="" boxw=""
    for n in 1 2; do
      slept=0
      for i in $(seq 1 120); do
        st=$(t apps status "$p" "$app" 2>/dev/null | jq -r '.production.sleeping // false')
        [ "$st" = true ] && { slept=1; break; }
        sleep 3
      done
      if [ "$slept" != 1 ]; then
        bad "did not fall asleep within 6 minutes of idle (sleepAfter $SLEEP_AFTER)"; client="${client:+$client / }never slept"; break
      fi
      r=$(fetch "$url/"); code=${r% *}
      [ "$code" = 200 ] || bad "wake $n: GET / returned $code"
      w=$(t apps status "$p" "$app" 2>/dev/null)
      [ "$(printf '%s' "$w" | jq -r '.production.sleeping // false')" = false ] || bad "wake $n: still sleeping after a request"
      client="${client:+$client / }${r#* }"
      boxw="${boxw:+$boxw / }$(printf '%s' "$w" | jq -r '.production.lastWake | if . == null then "?" else "\(.firstByteSeconds // "?" | tostring | .[0:4]) (start \(.startSeconds // "?" | tostring | .[0:4]))" end')"
    done
    put wake "$client"; put boxwake "$boxw"
  fi

  log "$id: destroying"
  destroy "$p" && put destroyed yes || { put destroyed no; bad "destroy failed"; }
}

# ---- report -----------------------------------------------------------------

val() { local v; v=$(sed -n "s/^$2=//p" "$WORK/$1.res" 2>/dev/null | tail -1); printf '%s' "${v:--}"; }
row() {
  local id=$1 fails res
  fails=$(grep -c '^fail=' "$WORK/$id.res" 2>/dev/null)
  if [ ! -s "$WORK/$id.res" ]; then res="not run"; elif [ "${fails:-0}" = 0 ]; then res=PASS; else res="FAIL ($fails)"; fi
  printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
    "$id" "$(val "$id" services)" "$(val "$id" build)" "$(val "$id" deploy)" "$(val "$id" root)" \
    "$(val "$id" first)" "$(val "$id" warm)" "$(val "$id" cache)" "$(val "$id" mem)" "$(val "$id" wake)" "$(val "$id" boxwake)" \
    "$(val "$id" redeploy)" "$(val "$id" logs)" "$(val "$id" db)" "$res"
}

report() {
  local id
  echo "# Live sweep $(date '+%Y-%m-%d %H:%M %Z')"
  echo
  echo "Box: $BOX_URL (tiffin $(t status 2>/dev/null | jq -r '.version // "?"')). Sweep script at $(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null). Jobs: $JOBS. Sleep after: $SLEEP_AFTER. Took $(since "$START")s."
  echo
  echo "Times in seconds. First, warm and wake are seconds from sending GET / (connection already up) to its first byte, so they include one round trip from this machine to the box; first is the first request after the deploy, wake the request that woke the sleeping app. Idle MB is the app's memory 8 s after its last request: what tiffin projects usage reports (the app's cgroup charge) / the resident size of its processes. Wake is the same measure for the request that woke the sleeping app, the first time after the deploy (a new container) and the second (the kept container); box wake is the box's own figure from apps status lastWake (first byte, of which starting the container). Deploy includes time queued behind other builds. Redeploy is failed/total requests during a redeploy."
  echo
  echo "| Template | Services | Build | Deploy | GET / | First | Warm | Asset Cache-Control | Idle MB | Wake 1st / 2nd | Box wake 1st / 2nd | Redeploy | Logs | DB | Result |"
  echo "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|"
  for id in $IDS; do row "$id"; done
  echo
  if grep -q '^fail=' "$WORK"/*.res 2>/dev/null; then
    echo "## Failures"; echo
    for id in $IDS; do sed -n "s/^fail=/- **$id**: /p" "$WORK/$id.res" 2>/dev/null; done
    echo
  fi
  echo "## Leftovers after destroy"; echo
  if [ -s "$WORK/leftovers" ]; then echo '```'; cat "$WORK/leftovers"; echo '```'; else echo "None found."; fi
  echo
  echo "## Box before"; echo; echo '```'; cat "$WORK/box.before" 2>/dev/null; echo '```'
  echo "## Box after"; echo; echo '```'; cat "$WORK/box.after" 2>/dev/null; echo '```'
  echo
  echo "Work files: $WORK"
}

# ---- cleanup on exit --------------------------------------------------------

cleanup_projects() {
  local p
  for p in $(t projects list 2>/dev/null | jq -r '.[].name' | grep "^$PREFIX"); do
    log "destroying $p"; destroy "$p"
  done
}

finish() {
  local rc=$?
  trap - EXIT INT TERM
  # shellcheck disable=SC2046
  kill $(cat "$WORK/loops" 2>/dev/null) $(jobs -p) 2>/dev/null; wait 2>/dev/null
  cleanup_projects
  sleep_knob_off
  if [ -n "$SSH_HOST" ]; then
    # Destroy finishes asynchronously; give it a minute before calling anything a leak.
    local i
    for i in 1 2 3 4 5 6; do
      leftovers "$PREFIX" >"$WORK/leftovers" 2>/dev/null
      [ -s "$WORK/leftovers" ] || break
      sleep 10
    done
    inventory "" >"$WORK/box.after" 2>&1
  fi
  mkdir -p "$(dirname "$REPORT")"
  report | tee "$REPORT"
  log "report: $REPORT"
  if grep -q '^fail=' "$WORK"/*.res 2>/dev/null || [ -s "$WORK/leftovers" ]; then exit 1; fi
  exit "$rc"
}

# ---- commands ---------------------------------------------------------------

BOX_URL=${TIFFIN_URL:-$(jq -r '.current // "?"' "${TIFFIN_HOME:-$HOME/.tiffin}/boxes.json" 2>/dev/null)}

reset_box() {
  [ "${1:-}" = --yes ] || { echo "reset destroys every project except: $KEEP. Run with --yes." >&2; exit 2; }
  local p keep
  for p in $(t projects list | jq -r '.[].name'); do
    case " $KEEP " in *" $p "*) continue ;; esac
    log "destroying $p"; destroy "$p"
  done
  [ -n "$SSH_HOST" ] || return 0
  log "pruning stopped containers, unused project images, static build caches of gone projects and the BuildKit cache"
  keep=$(t projects list | jq -r '.[].name' | tr '\n' ' ')
  box bash -s -- "$keep" <<'EOF'
ns="nerdctl -n tiffin"
$ns container prune -f >/dev/null
used=$($ns ps -a --format '{{.Image}}' | sed 's#^docker.io/##' | sort -u)
for im in $($ns images --format '{{.Repository}}:{{.Tag}}' | grep '^tiffin/'); do
  echo "$used" | grep -qx "$im" || $ns rmi "$im" >/dev/null 2>&1
done
for d in /var/lib/tiffin/runtime/build-cache/*; do
  [ -d "$d" ] || continue
  case " $1 " in *" ${d##*/} "*) ;; *) echo "removing $d"; rm -rf "$d" ;; esac
done
buildctl --addr unix:///run/buildkit/buildkitd.sock prune --all >/dev/null
EOF
  inventory ""
}

run_sweep() {
  local id p stale
  WORK=$(mktemp -d "${TMPDIR:-/tmp}/live-sweep.XXXXXX")
  START=$(now)
  t templates list >"$WORK/templates.json" || { echo "tiffin templates list failed" >&2; exit 1; }
  IDS=${SWEEP_ONLY:-"$(jq -r '.templates[].id' "$WORK/templates.json" | tr '\n' ' ')dockerfile"}
  log "box $BOX_URL, templates: $IDS"
  trap finish EXIT
  trap 'exit 130' INT TERM

  stale=$(t projects list | jq -r '.[].name' | grep "^$PREFIX" || true)
  [ -z "$stale" ] || { log "destroying stale projects: $stale"; for p in $stale; do destroy "$p"; done; }
  [ -n "$SSH_HOST" ] && inventory "" >"$WORK/box.before" 2>&1
  sleep_knob_on

  for id in $IDS; do
    while [ "$(jobs -rp | wc -l | tr -d ' ')" -ge "$JOBS" ]; do sleep 2; done
    log "$id: start"
    sweep_one "$id" >"$WORK/$id.log" 2>&1 &
    sleep 3
  done
  wait
}

main() {
  case "${1:-run}" in
    inventory) inventory "${2:-}" ;;
    cleanup) cleanup_projects; leftovers "$PREFIX" ;;
    reset) reset_box "${2:-}" ;;
    run) run_sweep ;;
    *) sed -n '2,40p' "$0" >&2; exit 2 ;;
  esac
}

# One line, so editing this file during a run cannot change what the run does.
main "$@"; exit $?
