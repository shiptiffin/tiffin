package switchboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkProxy(b *testing.B) {
	body := strings.Repeat("x", 64<<10)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer app.Close()
	bd := New(nil, nil)
	bd.Set(Table{
		Hosts: map[string][]Route{"app.test": {{Env: "p/a"}}},
		Envs:  map[string]*Env{"p/a": {Project: "p", App: "a", Live: "d1", Instances: []Instance{{Name: "p-a-1", Port: portOf(b, app.Listener.Addr().String())}}}},
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.test"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		bd.ServeHTTP(rec, req)
	}
}
