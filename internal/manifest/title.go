package manifest

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTitle is the longest project title, in characters.
const MaxTitle = 60

// CleanTitle is a title as it is stored: without the spaces around it.
// Empty means no title.
func CleanTitle(s string) string { return strings.TrimSpace(s) }

// TitleProblem says what is wrong with a title, or "" when it is fine. The
// empty title is fine: it clears it.
func TitleProblem(s string) string {
	s = CleanTitle(s)
	if n := utf8.RuneCountInString(s); n > MaxTitle {
		return fmt.Sprintf("must be at most %d characters (got %d)", MaxTitle, n)
	}
	if !utf8.ValidString(s) {
		return "must be valid UTF-8 text"
	}
	for _, r := range s {
		if unicode.IsControl(r) || bidiControl(r) || r == '\u2028' || r == '\u2029' {
			return fmt.Sprintf("must be one line of text, without control characters (found %U)", r)
		}
	}
	return ""
}

// bidiControl reports the characters that reorder the text around them, so
// a title could not show something other than it says.
func bidiControl(r rune) bool {
	return (r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069') || r == '\u200E' || r == '\u200F' || r == '\u061C'
}
