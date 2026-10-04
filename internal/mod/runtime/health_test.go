package runtime

import "testing"

// The default "/" passes on anything below 500 (no page at / is fine); a
// health path the app names must answer 2xx or 3xx.
func TestHealthOK(t *testing.T) {
	for _, c := range []struct {
		path   string
		status int
		ok     bool
	}{
		{"/", 200, true}, {"/", 302, true}, {"/", 404, true}, {"/", 499, true}, {"/", 500, false}, {"/", 503, false},
		{"/healthz", 200, true}, {"/healthz", 204, true}, {"/healthz", 301, true},
		{"/healthz", 404, false}, {"/healthz", 401, false}, {"/healthz", 500, false}, {"/healthz", 101, false},
	} {
		if got := healthOK(c.path, c.status); got != c.ok {
			t.Errorf("healthOK(%q, %d) = %v, want %v", c.path, c.status, got, c.ok)
		}
	}
	if healthWant("/") != " with a status below 500" || healthWant("/up") != " with a 2xx or 3xx status" {
		t.Error("hint wording")
	}
}
