package valkey

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// TestConnectionRevealNeedsFullAccess: the KV URL carries the
// password, so revealing it takes full access to that project (any key
// with it, not only the box owner's); read keys and keys for other
// projects are refused.
func TestConnectionRevealNeedsFullAccess(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerP, _ := tm.Authenticate(ctx, owner)
	key := func(access string, projects ...string) string {
		s, _, err := tm.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: access + "-" + projects[0], Projects: tokens.Projects(projects), Access: access})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	p := &platform.Platform{DB: db, Secrets: sec, Home: home, Log: slog.Default()}
	srv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Platform: p}).Handler())
	defer srv.Close()

	// Past the permission check, a project without KV answers 409.
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"owner", owner, 409},
		{"full key on shop", key(tokens.LevelFull, "shop"), 409},
		{"full key on every project", key(tokens.LevelFull, tokens.AllProjects), 409},
		{"read key on shop", key(tokens.LevelRead, "shop"), 403},
		{"full key on blog", key(tokens.LevelFull, "blog"), 403},
	} {
		req, _ := http.NewRequest("GET", srv.URL+"/v1/projects/shop/kv/connection?reveal=true", nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, res.StatusCode, c.want)
		}
	}
}
