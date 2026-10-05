package cli

import (
	"testing"
	"time"
)

// A query that asks the box to wait longer than a minute gets the CLI to wait too.
func TestRequestTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"": time.Minute, "x": time.Minute, "30": time.Minute, "120": 135 * time.Second} {
		if got := requestTimeout(in); got != want {
			t.Errorf("requestTimeout(%q) = %s, want %s", in, got, want)
		}
	}
}
