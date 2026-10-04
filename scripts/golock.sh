#!/usr/bin/env bash
# golock.sh — serialise go.mod/go.sum edits between processes (people, tools or
# agents) working in one checkout at the same time.
#   scripts/golock.sh go get example.com/pkg@v1.2.3
#   scripts/golock.sh go mod tidy
exec python3 -c '
import fcntl, os, subprocess, sys
fd = os.open("/tmp/tiffin-gomod.lock", os.O_CREAT | os.O_RDWR, 0o644)
fcntl.flock(fd, fcntl.LOCK_EX)
sys.exit(subprocess.call(sys.argv[1:]))
' "$@"
