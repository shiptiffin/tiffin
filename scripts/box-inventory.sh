#!/usr/bin/env bash
# What the live box runs, for checking against new security advisories:
# kernel and pending OS updates, the CA store, Tiffin's own build and
# toolchain, and each app container's OS, runtimes and CA bundle.
# Read-only. Used by the weekly maintenance agent; run it by hand any time.
#
#   scripts/box-inventory.sh [box]      # default: shiptiffin
set -uo pipefail
cd "$(dirname "$0")/.."

BOX=${1:-shiptiffin}
srv=$(jq -r --arg b "$BOX" '.boxes[$b].server' ~/.tiffin/boxes.json)
[ "$srv" = null ] && { echo "no server box named $BOX in ~/.tiffin/boxes.json" >&2; exit 2; }
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=15
  -o UserKnownHostsFile="$(jq -r .knownHosts <<<"$srv")"
  -i "$(jq -r .identity <<<"$srv")" "$(jq -r .ssh <<<"$srv")")

echo "== this repo (what the next tiffin up installs)"
echo "go toolchain: $(sed -n 's/^go //p' go.mod)"
echo "caddy: $(awk '$1=="github.com/caddyserver/caddy/v2"{print $2}' go.mod)"
grep -E '^\s+(BunVersion|NodeVersion|RailpackVersion|NerdctlVersion)\s+=' internal/mod/runtime/versions.go | tr -s ' \t' ' '

"${SSH[@]}" 'bash -s' <<'EOF'
echo; echo "== box"
. /etc/os-release; echo "os: $PRETTY_NAME"
echo "kernel running: $(uname -r); installed: $(ls /boot/vmlinuz-* | sed 's|/boot/vmlinuz-||' | tr '\n' ' ')"
echo "uptime: $(uptime -p)"
[ -f /var/run/reboot-required ] && echo "REBOOT WAITING for: $(tr '\n' ' ' </var/run/reboot-required.pkgs 2>/dev/null)"
echo "tiffin build: $(readlink /usr/local/bin/tiffin | sed 's|.*/versions/||; s|/tiffin||')"
echo "ca-certificates: $(dpkg-query -W -f='${Version}' ca-certificates)"
echo "openssl: $(dpkg-query -W -f='${Version}' openssl) openssh-server: $(dpkg-query -W -f='${Version}' openssh-server)"
echo "postgres: $(dpkg-query -W -f='${db:Status-Abbrev}${Package} ${Version}\n' 'postgresql-[0-9]*' 2>/dev/null | sed -n 's/^ii *//p' | grep -v client | tr '\n' ' ')"
echo "valkey: $(valkey-server --version 2>/dev/null | grep -oE 'v=[0-9.]+')"
echo "php: $(command -v php php-fpm php-cgi 2>/dev/null | tr '\n' ' ' || true)$(command -v php >/dev/null || echo none)"
echo; echo "== pending OS updates"
apt-get update -qq >/dev/null 2>&1
apt list --upgradable 2>/dev/null | tail -n +2 | sed 's/ \[upgradable from: / from /; s/\]$//'
echo; echo "== app containers"
for c in $(nerdctl -n tiffin ps --format '{{.Names}}'); do
  printf '%s: ' "$c"
  nerdctl -n tiffin exec "$c" sh -c '. /etc/os-release 2>/dev/null; printf "%s" "$PRETTY_NAME"
    command -v node >/dev/null && printf "; node %s" "$(node --version)"
    command -v bun >/dev/null && printf "; bun %s" "$(bun --version)"
    command -v python3 >/dev/null && printf "; %s" "$(python3 --version)"
    command -v php >/dev/null && printf "; PHP %s" "$(php -r "echo PHP_VERSION;")"
    v=$(dpkg-query -W -f="\${Version}" ca-certificates 2>/dev/null || apk info -v ca-certificates 2>/dev/null | head -1)
    printf "; ca-certificates %s\n" "${v:-none}"' 2>&1 | head -1
done
echo; echo "== processes still on replaced libraries"
needrestart -b 2>/dev/null | grep -E 'NEEDRESTART-(SVC|KSTA)' || echo none
EOF
