#!/usr/bin/env bash
# Generates candidate illustrations with Codex's built-in image tool.
#
#   ART_SRC=<work folder> art/gen.sh <batch> <prompt-file> [reference.png ...]
#
# The prompt file holds the subject lines (one numbered image per line, each
# with the file name to save it as); this appends the mascot and style bibles
# (mascot.txt, style.txt, next to this script). Pass the mascot reference
# (mascot-base) for anything with the mascot in it. Candidates land in
# $ART_SRC/raw/<batch>/; pick, then run cut.py and export.py.
# Set HEAVY to a job limiter (e.g. research/heavy.sh) to queue behind other heavy work.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ART="${ART_SRC:?set ART_SRC to a work folder outside the repo}"
name=$1; pf=$2; shift 2
imgs=(); for r in "$@"; do imgs+=(-i "$r"); done
mkdir -p "$ART/raw/$name" "$ART/logs"
prompt="$(cat "$pf")

$(cat "$HERE/mascot.txt")

$(cat "$HERE/style.txt")

Use your built-in image generation tool only (no code, no scripts that draw). Generate each listed image as a separate generation. After generating, copy each PNG into $ART/raw/$name/ using the file name given for it in the list. Do not create any other files. Finally reply with the copied paths, one per line."
${HEAVY:-} codex exec --skip-git-repo-check -s workspace-write -C "$ART" ${imgs[@]+"${imgs[@]}"} \
  -o "$ART/logs/$name.last.txt" "$prompt" </dev/null >"$ART/logs/$name.log" 2>&1
echo "$name: $(ls "$ART/raw/$name" | wc -l | tr -d ' ') images"
