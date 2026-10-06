package runtime

import (
	"net/http"
	"regexp"
	"testing"
)

func TestTraceparentFromRequestID(t *testing.T) {
	h := http.Header{"X-Request-Id": {"0B7C4E2A-9F1D-4C3B-8A6E-2D5F7E9A1B3C"}}
	tp := traceparent(h)
	if !regexp.MustCompile(`^00-0b7c4e2a9f1d4c3b8a6e2d5f7e9a1b3c-[0-9a-f]{16}-01$`).MatchString(tp) {
		t.Fatalf("traceparent %q", tp)
	}
	h.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	if traceparent(h) != "" {
		t.Fatal("a request's own trace context is kept")
	}
	for _, id := range []string{"", "abc", "00000000-0000-0000-0000-000000000000", "zz7c4e2a-9f1d-4c3b-8a6e-2d5f7e9a1b3c"} {
		if tp := traceparent(http.Header{"X-Request-Id": {id}}); tp != "" {
			t.Fatalf("request ID %q gave %q", id, tp)
		}
	}
}
