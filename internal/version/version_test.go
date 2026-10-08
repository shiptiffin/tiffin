package version

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	got := String()
	for _, want := range []string{"tiffin", Version, Commit, Date} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestSourceURL(t *testing.T) {
	defer func(c string) { Commit = c }(Commit)
	Commit = "none"
	if got := SourceURL(); got != Source {
		t.Errorf("SourceURL() without a commit = %q, want %q", got, Source)
	}
	Commit = "abc1234"
	if got, want := SourceURL(), "https://github.com/shiptiffin/tiffin/tree/abc1234"; got != want {
		t.Errorf("SourceURL() = %q, want %q", got, want)
	}
}
