#!/bin/sh
# Installs the tiffin command-line tool on macOS or Linux (Windows: run it in WSL).
#
#   curl -fsSL https://shiptiffin.com/install.sh | sh
#
# It reads the signed release list at releases.shiptiffin.com, downloads the
# build for this computer, checks its SHA-256 against the list (and the
# list's signature when minisign is installed), and puts `tiffin` in
# /usr/local/bin when that is writable, else ~/.local/bin.
#
# Settings: TIFFIN_CHANNEL (stable or edge, default stable),
# TIFFIN_INSTALL_DIR (where to put it).
set -eu

CHANNEL="${TIFFIN_CHANNEL:-stable}"
BASE="https://releases.shiptiffin.com"
PUBKEY="RWRGIoWQXtESNT2JTrrXUaPvYEST9ZGtgPmozFnqWvs5DhUojgetwlz0"

say() { printf '%s\n' "$*"; }
fail() { printf 'tiffin install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*) fail "on Windows, run this inside WSL (https://learn.microsoft.com/windows/wsl/install)." ;;
  *) fail "this computer ($(uname -s)) has no build yet: macOS and Linux only." ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "this processor ($(uname -m)) has no build yet: amd64 and arm64 only." ;;
esac
# A Mac with Apple Silicon running this shell under Rosetta still wants arm64.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -in sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then arch=arm64; fi
platform="$os/$arch"

command -v curl >/dev/null 2>&1 || fail "curl is needed."
if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else fail "sha256sum or shasum is needed to check the download."; fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL "$BASE/$CHANNEL/manifest.json" -o "$tmp/manifest.json" || fail "couldn't read $BASE/$CHANNEL/manifest.json"
if command -v minisign >/dev/null 2>&1; then
  curl -fsSL "$BASE/$CHANNEL/manifest.json.minisig" -o "$tmp/manifest.json.minisig" || fail "couldn't read the release list's signature"
  minisign -Vq -P "$PUBKEY" -m "$tmp/manifest.json" -x "$tmp/manifest.json.minisig" || fail "the release list's signature doesn't match: not installing."
  signed="signature and checksum checked"
else
  signed="checksum checked; install minisign to check the signature too"
fi

# The manifest is indented JSON: the artifact's block follows its "os/arch" key.
field() {
  awk -v p="\"$platform\"" -v f="\"$1\"" '
    index($0, p) { inside = 1; next }
    inside && index($0, "}") { exit }
    inside && index($0, f) { sub(/^[^:]*:[ ]*"/, ""); sub(/".*$/, ""); print; exit }
  ' "$tmp/manifest.json"
}
version="$(sed -n 's/^[ ]*"version":[ ]*"\([^"]*\)".*/\1/p' "$tmp/manifest.json" | head -n 1)"
url="$(field url)"
want="$(field sha256)"
[ -n "$url" ] && [ -n "$want" ] || fail "release $version has no build for $platform yet."

say "Downloading tiffin $version for $platform..."
curl -fSL --progress-bar "$url" -o "$tmp/tiffin" || fail "download failed: $url"
got="$(sha "$tmp/tiffin")"
[ "$got" = "$want" ] || fail "the download's checksum doesn't match the release list: not installing."
chmod +x "$tmp/tiffin"

dir="${TIFFIN_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
mv "$tmp/tiffin" "$dir/tiffin"
[ "$os" = darwin ] && xattr -d com.apple.quarantine "$dir/tiffin" 2>/dev/null || true

say "Installed tiffin $version to $dir/tiffin ($signed)."
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Add it to your PATH: export PATH=\"$dir:\$PATH\" (in ~/.zshrc or ~/.bashrc)." ;;
esac
say "Next: tiffin version, then https://shiptiffin.com/docs/quickstart.md to make a box or connect to yours."
