#!/usr/bin/env bash
# Publish a built and signed release (make release, make release-sign) to the
# releases bucket, then delete every other version from it.
#
#   scripts/release-publish.sh v0.27.0 [stable,edge]
#
# 1. Uploads dist/ (the binaries, checksums and licence files) to <version>/.
# 2. Uploads each channel's signed manifest, uncached, so boxes see it at once.
# 3. Deletes every v*/ folder but the newest two (this one and the one
#    before, a spare): the manifests only ever point at the newest, and a box
#    rolls back to its own previous binary.
#
# The R2 keys come from .env (CLOUDFLARE_ACCOUNT_ID, R2_ACCESS_KEY_ID,
# R2_SECRET_ACCESS_KEY) and are never printed. The bucket is always the
# releases bucket (RELEASE_BUCKET, default shiptiffin-releases), never .env's
# R2_BUCKET: that one is for backup tests.
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:?usage: scripts/release-publish.sh vX.Y.Z [channels]}
channels=${2:-stable,edge}
keep=2
[[ $version == v* ]] || { echo "version must start with v: $version" >&2; exit 1; }

bucket=${RELEASE_BUCKET:-shiptiffin-releases}
set -a; . ./.env; set +a
: "${CLOUDFLARE_ACCOUNT_ID:?}" "${R2_ACCESS_KEY_ID:?}" "${R2_SECRET_ACCESS_KEY:?}"
base="https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com/${bucket}"
echo "==> bucket $bucket"

r2() { # r2 METHOD KEY [curl args...] -> HTTP status on stdout
	local m=$1 key=$2; shift 2
	curl -sS -X "$m" --aws-sigv4 "aws:amz:auto:s3" --user "${R2_ACCESS_KEY_ID}:${R2_SECRET_ACCESS_KEY}" \
		-o /dev/null -w '%{http_code}' -m 300 "$base/$key" "$@"
}
put() { # put FILE KEY [curl args...]
	local f=$1 key=$2; shift 2
	local code; code=$(r2 PUT "$key" --data-binary "@$f" "$@")
	[[ $code == 200 ]] || { echo "upload $key: HTTP $code" >&2; exit 1; }
	echo "  $key"
}

for c in ${channels//,/ }; do
	grep -q "/${version}/" "dist/$c/manifest.json" ||
		{ echo "dist/$c/manifest.json is not for $version: run make release-sign first" >&2; exit 1; }
done

echo "==> uploading $version"
for f in dist/tiffin-* dist/checksums.txt dist/LICENSE dist/NOTICE dist/THIRD_PARTY_NOTICES; do
	put "$f" "$version/$(basename "$f")"
done

echo "==> uploading the manifests ($channels)"
for c in ${channels//,/ }; do
	for f in manifest.json manifest.json.minisig; do
		put "dist/$c/$f" "$c/$f" -H "Cache-Control: no-cache"
	done
done

echo "==> deleting all but the newest $keep versions"
all=$(curl -sS --aws-sigv4 "aws:amz:auto:s3" --user "${R2_ACCESS_KEY_ID}:${R2_SECRET_ACCESS_KEY}" -m 60 \
	"$base?list-type=2&max-keys=1000" | grep -o '<Key>v[^<]*' | sed 's/<Key>//' || true)
kept=$(printf '%s\n' "$all" | cut -d/ -f1 | sort -uV | tail -n "$keep")
grep -qx "$version" <<<"$kept" || { echo "$version is not among the newest $keep versions in the bucket: nothing deleted" >&2; exit 1; }
for key in $all; do
	grep -qx "${key%%/*}" <<<"$kept" && continue
	code=$(r2 DELETE "$key")
	[[ $code == 204 ]] || { echo "delete $key: HTTP $code" >&2; exit 1; }
	echo "  $key"
done
echo "==> $version published; the bucket holds" $kept
