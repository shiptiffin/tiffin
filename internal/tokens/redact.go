package tokens

import (
	"bytes"
	"regexp"
	"strings"
)

// secretRe matches Tiffin's own credentials (API keys and session tokens,
// login codes, analytics keys), so logs, reported errors and traces never
// keep one, even when a process prints it.
var secretRe = regexp.MustCompile(`\b(tfn|tfl|tak)_[a-z0-9]{16,}`)

// Redact masks Tiffin credentials in s: tfn_[redacted].
func Redact(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	return secretRe.ReplaceAllString(s, "${1}_[redacted]")
}

// RedactBytes masks Tiffin credentials in text (JSON, log output) without
// changing its length: tfn_[redacted]****. It returns b itself when there
// is nothing to mask. Not for binary encodings: a match can run on into
// the bytes after a string.
func RedactBytes(b []byte) []byte {
	if !bytes.Contains(b, []byte("_")) {
		return b
	}
	locs := secretRe.FindAllIndex(b, -1)
	if locs == nil {
		return b
	}
	out := bytes.Clone(b)
	for _, l := range locs {
		rest := out[l[0]+4 : l[1]] // after "tfn_"; at least 16 bytes
		n := copy(rest, "[redacted]")
		for i := n; i < len(rest); i++ {
			rest[i] = '*'
		}
	}
	return out
}

// RedactSafeLen is how much of b, the start of a stream that goes on, can
// be masked and shown now: all of it, unless it ends in what may be the
// first part of a credential, which waits for the rest.
func RedactSafeLen(b []byte) int {
	i := len(b) - 1
	for i >= 0 && (b[i] >= 'a' && b[i] <= 'z' || b[i] >= '0' && b[i] <= '9' || b[i] == '_') {
		i--
	}
	// b[i+1:] is the trailing word: hold it if it starts like a credential
	// (or is a prefix of one's start).
	tail := b[i+1:]
	if len(tail) > 128 { // longer than any credential: nothing to wait for
		return len(b)
	}
	for _, p := range []string{"tfn_", "tfl_", "tak_"} {
		if bytes.HasPrefix(tail, []byte(p)) || (len(tail) < len(p) && len(tail) > 0 && strings.HasPrefix(p, string(tail))) {
			return i + 1
		}
	}
	return len(b)
}
