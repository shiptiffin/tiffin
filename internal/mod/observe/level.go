package observe

import (
	"regexp"
	"strconv"
	"strings"
)

// Level inference for lines that carry no level field: plain-text app
// output and build output. The rule is conservative on purpose (a leading
// "error", "ERROR:" anywhere, a bracketed level), so "0 errors" or
// "error_count=0" stay unmarked.
//
// The dashboard mirrors this rule exactly (inferLevel in
// apps/dashboard/src/components/logs-query.ts): keep the two in step, so
// a line's colour and the Level chip agree.
var (
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	prefixRe = regexp.MustCompile(`^(?:#[0-9]+ (?:[0-9]+(?:\.[0-9]+)? )?|==> )`) // BuildKit "#8 12.3 ", tiffin's "==> "
	logfmtRe = regexp.MustCompile(`(?:^|[ \t])level=["']?([A-Za-z]+)`)

	errorRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:(?:npm|pnpm|yarn) )?\[?(?:error|fatal|panic|critical|err!)\]?(?:[ \t:]|$)`),
		regexp.MustCompile(`(?i)\[(?:error|fatal|panic|critical)\]`),
		regexp.MustCompile(`\b(?:PANIC|FATAL|ERROR):|\bFATAL\b|\bUnhandledPromiseRejection|^Uncaught\b|^(?:[A-Z][A-Za-z]*)?(?:Error|Exception): |^(?:[a-z_][a-z0-9_]*\.)+[A-Z]\w*: |^FAILED:|exited with code [1-9]`),
	}
	warnRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:(?:npm|pnpm|yarn) )?\[?(?:warn|warning)\]?(?:[ \t:]|$)`),
		regexp.MustCompile(`(?i)\[(?:warn|warning)\]`),
		regexp.MustCompile(`\bWARNING:`),
	}
)

// stripANSI removes terminal colour codes.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// levelName gives a level its usual name: error, warning, info or debug,
// pino's numbers included. Anything else comes back lowercased.
func levelName(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if n, err := strconv.Atoi(l); err == nil && n >= 10 && n <= 60 { // pino: 10 trace … 60 fatal
		switch {
		case n >= 50:
			return "error"
		case n >= 40:
			return "warning"
		case n >= 30:
			return "info"
		default:
			return "debug"
		}
	}
	switch l {
	case "error", "err", "fatal", "panic", "critical", "crit", "alert", "emerg":
		return "error"
	case "warn", "warning":
		return "warning"
	case "info", "notice":
		return "info"
	case "debug", "trace":
		return "debug"
	}
	return l
}

// inferLevel reads a level from a line's text: "error", "warning", a
// logfmt level, or "" when the line says nothing.
func inferLevel(msg string) string {
	s := strings.TrimLeft(stripANSI(msg), " \t")
	s = prefixRe.ReplaceAllString(s, "")
	if m := logfmtRe.FindStringSubmatch(s); m != nil {
		switch l := levelName(m[1]); l {
		case "error", "warning", "info", "debug":
			return l
		}
	}
	for _, re := range errorRes {
		if re.MatchString(s) {
			return "error"
		}
	}
	for _, re := range warnRes {
		if re.MatchString(s) {
			return "warning"
		}
	}
	return ""
}
