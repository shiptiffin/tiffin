package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// While the log store fails, what the batcher holds stays within its cap
// however many flushes fail, and it all goes out once the store is back.
func TestLogBatcherStaysWithinItsCap(t *testing.T) {
	var up atomic.Bool
	vl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer vl.Close()
	b := &LogBatcher{V: &Victoria{VL: vl.URL}}
	msg := strings.Repeat("x", 1000)
	held := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		n := 0
		for _, buf := range b.bufs {
			n += buf.Len()
		}
		return n
	}
	for round := 0; round < 4; round++ {
		for i := 0; i < 20000; i++ { // ~20 MiB per round, over two streams
			b.Add(Tenant(1+i%2), "app", map[string]any{"_msg": msg})
		}
		b.Flush(context.Background())
		if n := held(); n > 32<<20 {
			t.Fatalf("round %d: holding %d bytes, cap %d", round, n, 32<<20)
		}
	}
	if _, dropped := b.Stats(); dropped == 0 {
		t.Fatal("nothing counted as dropped")
	}
	up.Store(true)
	b.Flush(context.Background())
	if sent, _ := b.Stats(); sent == 0 || held() != 0 || false {
		t.Fatalf("after recovery: sent %d, holding %d (%d counted)", sent, held(), b.held)
	}
}
