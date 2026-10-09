package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Saved queries and the edit log over HTTP: names in the path reach the
// handler, reading needs read access, saving needs a key that can change things.
func TestSavedQueriesAPI(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Tokens: tokens.NewManager(db)}
	owner, _, err := p.Tokens.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerP, _ := p.Tokens.Authenticate(ctx, owner)
	reader, _, err := p.Tokens.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "reader", Projects: tokens.Projects{"shop"}, Access: tokens.LevelRead})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: db, Tokens: p.Tokens, Platform: p}).Handler())
	defer srv.Close()
	call := func(token, method, path, body string, into any) int {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if into != nil {
			_ = json.Unmarshal(raw, into)
		}
		return res.StatusCode
	}
	var q PGSavedQuery
	if code := call(owner, "PUT", "/v1/projects/shop/postgres/queries/Best%20sellers", `{"sql":"select 1"}`, &q); code != 200 || q.Name != "Best sellers" || q.SQL != "select 1" {
		t.Fatalf("save: %d %+v", code, q)
	}
	if code := call(reader, "PUT", "/v1/projects/shop/postgres/queries/x", `{"sql":"select 2"}`, nil); code != 403 {
		t.Fatalf("a read-only key saved a query: %d", code)
	}
	var list []PGSavedQuery
	if code := call(reader, "GET", "/v1/projects/shop/postgres/queries", "", &list); code != 200 || len(list) != 1 || list[0].Name != "Best sellers" {
		t.Fatalf("list: %d %+v", code, list)
	}
	if code := call(owner, "DELETE", "/v1/projects/shop/postgres/queries/Best%20sellers", "", nil); code != 204 && code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := call(owner, "DELETE", "/v1/projects/shop/postgres/queries/Best%20sellers", "", nil); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
	var edits []PGEdit
	if code := call(reader, "GET", "/v1/projects/shop/postgres/edits", "", &edits); code != 200 || len(edits) != 0 {
		t.Fatalf("edits: %d %v", code, edits)
	}
	if code := call(owner, "POST", "/v1/projects/shop/postgres/edits/edit_01J0000000000000000000NONE/undo", `{}`, nil); code != 404 {
		t.Fatalf("undo of an unknown edit: %d", code)
	}
}
