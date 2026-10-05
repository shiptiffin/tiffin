package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// A create sent twice with one Idempotency-Key is done once: the second
// request gets the first one's answer without its body being read, the key
// can be looked up, and failures are not kept.
func TestIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerP, _ := tm.Authenticate(ctx, owner)
	other, _, err := tm.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "other", Projects: tokens.Projects{tokens.AllProjects}, Access: tokens.LevelFull})
	if err != nil {
		t.Fatal(err)
	}
	a := New(Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: "test"})

	type thing struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	var runs atomic.Int32
	release := make(chan struct{})
	block := make(chan struct{}, 1)
	upload := op("thing-create", http.MethodPost, "/v1/things", "-", RiskWrite, "Create a thing", "A test create.", "system")
	upload.DefaultStatus = http.StatusAccepted
	huma.Register(a.api, Idempotent(upload), wrap(func(ctx context.Context, in *struct {
		Fail  bool `query:"fail"`
		Block bool `query:"block"`
	}) (*struct{ Body thing }, error) {
		if in.Block {
			block <- struct{}{}
			<-release
		}
		if in.Fail {
			return nil, NewProblem(422, "validation", "no")
		}
		return &struct{ Body thing }{thing{ID: int(runs.Add(1))}}, nil
	}))
	other2 := op("thing-other", http.MethodPost, "/v1/other-things", "-", RiskWrite, "Create another thing", "A test create.", "system")
	huma.Register(a.api, Idempotent(other2), wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body thing }, error) {
		return &struct{ Body thing }{thing{ID: int(runs.Add(1))}}, nil
	}))
	plain := op("thing-plain", http.MethodPost, "/v1/plain-things", "-", RiskWrite, "Create a plain thing", "Not idempotent.", "system")
	huma.Register(a.api, plain, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body thing }, error) {
		return &struct{ Body thing }{thing{ID: int(runs.Add(1))}}, nil
	}))
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	call := func(token, method, path, key, body string) (int, http.Header, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set(IdempotencyHeader, key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(raw)
	}
	lookup := func(token, key string) IdempotentRequest {
		t.Helper()
		code, _, raw := call(token, "GET", "/v1/idempotency-keys/"+key, "", "")
		if code != 200 {
			t.Fatalf("lookup %s: %d %s", key, code, raw)
		}
		var st IdempotentRequest
		_ = json.Unmarshal([]byte(raw), &st)
		return st
	}

	const k1 = "key-0000000000000001"
	if st := lookup(owner, k1); st.Status != "none" {
		t.Fatalf("a key never sent: %+v", st)
	}
	code, h, first := call(owner, "POST", "/v1/things", k1, "{}")
	if code != 202 || runs.Load() != 1 || h.Get(IdempotencyStatusHeader) != "" {
		t.Fatalf("first: %d %v %s", code, h, first)
	}
	// The body is not read: a resend that broke off is still answered.
	code, h, again := call(owner, "POST", "/v1/things", k1, "not even JSON")
	if code != 202 || again != first || runs.Load() != 1 || h.Get(IdempotencyStatusHeader) != "replayed" || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("resend: %d %v %s (first %s, runs %d)", code, h, again, first, runs.Load())
	}
	st := lookup(owner, k1)
	var stored, want thing
	got, _ := json.Marshal(st.Response)
	_ = json.Unmarshal(got, &stored)
	_ = json.Unmarshal([]byte(first), &want)
	if st.Status != "done" || st.Method != "POST" || st.Path != "/v1/things" || st.ResponseStatus != 202 || stored != want || want.ID != 1 {
		t.Fatalf("lookup: %+v (%s)", st, got)
	}
	// Keys belong to their token.
	if st := lookup(other, k1); st.Status != "none" {
		t.Fatalf("another token sees the key: %+v", st)
	}
	if code, _, _ := call(other, "POST", "/v1/things", k1, "{}"); code != 202 || runs.Load() != 2 {
		t.Fatalf("another token's request with the same key: %d, runs %d", code, runs.Load())
	}
	// One key, one request.
	if code, _, raw := call(owner, "POST", "/v1/things?fail=true", k1, "{}"); code != 202 {
		t.Fatalf("same path, other query: %d %s", code, raw) // the path names the request; the stored answer wins
	}
	if code, _, raw := call(owner, "POST", "/v1/plain-things", k1, "{}"); code != 200 || runs.Load() != 3 {
		t.Fatalf("an operation without keys ignores them: %d %s", code, raw)
	}
	if code, _, raw := call(owner, "POST", "/v1/things", "short", "{}"); code != 422 {
		t.Fatalf("a bad key: %d %s", code, raw)
	}
	// A failure is not kept: the same key may try again.
	const k2 = "key-0000000000000002"
	if code, _, _ := call(owner, "POST", "/v1/things?fail=true", k2, "{}"); code != 422 {
		t.Fatalf("failing: %d", code)
	}
	if st := lookup(owner, k2); st.Status != "none" {
		t.Fatalf("a failure was kept: %+v", st)
	}
	if code, _, _ := call(owner, "POST", "/v1/things", k2, "{}"); code != 202 || runs.Load() != 4 {
		t.Fatalf("after a failure: %d, runs %d", code, runs.Load())
	}
	// While the first request runs, a second one is told so.
	const k3 = "key-0000000000000003"
	done := make(chan string)
	go func() {
		_, _, raw := call(owner, "POST", "/v1/things?block=true", k3, "{}")
		done <- raw
	}()
	<-block
	if st := lookup(owner, k3); st.Status != "running" {
		t.Fatalf("lookup while running: %+v", st)
	}
	code, h, raw := call(owner, "POST", "/v1/things?block=true", k3, "{}")
	if code != 409 || h.Get(IdempotencyStatusHeader) != "running" || h.Get("Retry-After") == "" {
		t.Fatalf("while running: %d %v %s", code, h, raw)
	}
	close(release)
	firstRaw := <-done
	if code, _, raw := call(owner, "POST", "/v1/things?block=true", k3, "{}"); code != 202 || raw != firstRaw || runs.Load() != 5 {
		t.Fatalf("after it ran: %d %s (first %s), runs %d", code, raw, firstRaw, runs.Load())
	}
	// A key used for another request is refused.
	if code, _, raw := call(owner, "POST", "/v1/other-things", k1, "{}"); code != 422 || !strings.Contains(raw, "another request") || runs.Load() != 5 {
		t.Fatalf("a key reused elsewhere: %d %s", code, raw)
	}
}
