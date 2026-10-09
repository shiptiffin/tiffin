#!/usr/bin/env bash
# A throwaway Lima box for the bench, made the way `tiffin up` makes the
# local box (internal/provider/lima/box.yaml, 2 vCPU / 4 GiB like a Hetzner
# cx23) plus a vzNAT address, so load can reach the box directly instead of
# through Lima's port forwarder. Everything lives in bench/.work: its own
# CLI config, never ~/.tiffin.
#
#   bench/box.sh up                      # build tiffin from this checkout, create the VM, install
#   bench/box.sh deploy [bun|node] [N]   # deploy the showcase (runtime, instances; default bun 2)
#   bench/box.sh env                     # BASE, VM, DIRECT_IP, CA for bench/run.ts
#   bench/box.sh tiffin <args...>        # the CLI against this box
#   bench/box.sh down                    # delete the VM and its disk
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
app="$(dirname "$here")"
repo="$(cd "$app/../.." && pwd)"
work="$here/.work"
mkdir -p "$work"
state="$work/box.env"
[ -f "$state" ] && . "$state"

cli() {
  env TIFFIN_CONFIG_DIR="$work/config" TIFFIN_HOME= TIFFIN_URL= TIFFIN_TOKEN= \
    TIFFIN_LIMA_INSTANCE="$VM" TIFFIN_LIMA_DISK="$DISK" TIFFIN_LIMA_PORT="$PORT" \
    CLAUDECODE= TIFFIN_AGENT= TIFFIN_SESSION= TIFFIN_MODEL= \
    "$work/tiffin" "$@"
}

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }

case "${1:-}" in
up)
  if [ -n "${VM:-}" ] && limactl list -q 2>/dev/null | grep -qx "$VM"; then
    echo "box $VM exists; bench/box.sh down first" >&2; exit 1
  fi
  VM="tiffin-bench-$(date +%s)"
  DISK="bnc$(openssl rand -hex 2)"
  PORT="$(free_port)"
  printf 'VM=%s\nDISK=%s\nPORT=%s\n' "$VM" "$DISK" "$PORT" > "$state"
  arch="$(go env GOARCH)"
  echo "==> building tiffin (host and linux/$arch) from $repo"
  (cd "$repo" && go build -trimpath -o "$work/tiffin" ./cmd/tiffin)
  (cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-X github.com/shiptiffin/tiffin/internal/version.Version=0.0.1-bench -X github.com/shiptiffin/tiffin/internal/version.Commit=$(git -C "$repo" rev-parse --short HEAD)" \
    -o "$work/tiffin-linux" ./cmd/tiffin)
  echo "==> creating $VM (disk $DISK, https on 127.0.0.1:$PORT)"
  limactl disk create "$DISK" --size 20GiB --format raw --tty=false
  limactl create --name "$VM" --tty=false \
    --set ".additionalDisks = [{\"name\": \"$DISK\", \"format\": true, \"fsType\": \"xfs\"}] | .portForwards[0].hostPort = $PORT | .networks = [{\"vzNAT\": true}]" \
    "$repo/internal/provider/lima/box.yaml"
  limactl start "$VM" --tty=false --timeout 14m
  echo "==> installing tiffin"
  cli up --binary "$work/tiffin-linux"
  # The default per-IP limit (300 requests / 10 s for pages and API calls)
  # would turn most of the load generator's requests into 429s.
  cli protect set --body '{"limits":{"app":{"requests":1000000,"windowSeconds":10}}}' >/dev/null
  ;;
deploy)
  runtime="${2:-bun}" n="${3:-2}"
  src="$work/app-$runtime-$n"
  rm -rf "$src" && mkdir -p "$src"
  rsync -a --exclude node_modules --exclude .next --exclude bench "$app/" "$src/"
  # The variant: instances, and for node a start command that runs Next.js
  # on Node.js (with Node installed in the image by Railpack).
  extra=""
  if [ "$runtime" = node ]; then
    extra=', command: "node node_modules/next/dist/bin/next start"'
    perl -0pi -e 's|  services: \{|  env: { RAILPACK_PACKAGES: "node\@24" },\n  services: {|' "$src/tiffin.config.ts"
  fi
  perl -0pi -e "s|instances: 2, memoryMB: 512, healthcheck: \"/api/health\"|instances: $n, memoryMB: 512, healthcheck: \"/api/health\"$extra|" "$src/tiffin.config.ts"
  grep -n "web:\|env:" "$src/tiffin.config.ts"
  hash="$(cli plan "$src" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("hash",""))')"
  if [ -n "$hash" ]; then cli apply "$src" --confirm "$hash" -m "bench: showcase $runtime x$n" >/dev/null || true; fi
  cli deploy "$src"
  ;;
env)
  ip="$(limactl shell --workdir / "$VM" -- ip -4 -o addr show lima0 2>/dev/null | awk '{print $4}' | cut -d/ -f1)"
  echo "export BASE=https://next-showcase.tiffin.localhost:$PORT"
  echo "export VM=$VM"
  echo "export DIRECT_IP=$ip"
  echo "export CA=$work/config/boxes/local/ca.crt"
  ;;
tiffin)
  shift
  cli "$@"
  ;;
run)
  # run a local script inside the VM as root: bench/box.sh run script.sh
  limactl shell --workdir / "$VM" -- sudo bash -s < "$2"
  ;;
down)
  [ -n "${VM:-}" ] || { echo "no box" >&2; exit 0; }
  limactl delete -f --tty=false "$VM" || true
  limactl disk delete -f --tty=false "$DISK" || true
  rm -f "$state"
  ;;
*)
  sed -n '2,13p' "$0"; exit 2
  ;;
esac
