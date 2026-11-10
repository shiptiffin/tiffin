package auth

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The engine derives the same key (serverKey in packages/auth-engine/src/server.ts;
// its tests check this vector too).
func TestServerKey(t *testing.T) {
	const want = "tak_shop_b1b1d9fca2d3c0f71a9f19c5bd9674cffdfac5ef00097cc8"
	if got := ServerKey("shop", "test-secret-0123456789abcdef0123456789abcdef"); got != want {
		t.Fatalf("ServerKey = %s, want %s", got, want)
	}
	if ServerKey("blog", "test-secret-0123456789abcdef0123456789abcdef") == want || ServerKey("shop", "another-secret-0123456789abcdef0123456789") == want {
		t.Fatal("the key must depend on the project and its secret")
	}
}

func TestEnvServerKey(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, shop)
	apply(t, p, strings.ReplaceAll(shop, `"shop"`, `"blog"`))
	web, err := (&Module{}).Env(ctx, p, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := (&Module{}).Env(ctx, p, "shop", "admin")
	blog, _ := (&Module{}).Env(ctx, p, "blog", "web")
	key := web["TIFFIN_AUTH_KEY"]
	if !strings.HasPrefix(key, "tak_shop_") || admin["TIFFIN_AUTH_KEY"] != key {
		t.Fatalf("every app of the project gets the project's key: %q %q", key, admin["TIFFIN_AUTH_KEY"])
	}
	if blog["TIFFIN_AUTH_KEY"] == "" || blog["TIFFIN_AUTH_KEY"] == key {
		t.Fatalf("another project gets its own key: %q", blog["TIFFIN_AUTH_KEY"])
	}
	// The engine config carries the secret the key comes from.
	c, _, err := buildEngineConfig(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if ServerKey("shop", c.Projects["shop"].Secret) != key {
		t.Fatal("the key isn't the one the engine derives from its config")
	}
	// A preview keeps the project's key.
	PreviewEnv(web, "https://pr-3--shop.tiffin.localhost:8443")
	if web["TIFFIN_AUTH_KEY"] != key {
		t.Fatalf("preview key: %q", web["TIFFIN_AUTH_KEY"])
	}
}

// BetterAuthVersion follows the engine's pinned better-auth, and the
// embedded bundle was built from it (make auth-engine after a bump).
func TestBetterAuthVersionMatchesEngine(t *testing.T) {
	js, err := EngineBundle()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `"`+BetterAuthVersion+`"`) {
		t.Fatalf("the embedded engine bundle isn't Better Auth %s: run make auth-engine", BetterAuthVersion)
	}
	raw, err := os.ReadFile("../../../packages/auth-engine/package.json")
	if err != nil {
		t.Skip("packages/auth-engine not in this checkout")
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if got := pkg.Dependencies["better-auth"]; got != BetterAuthVersion {
		t.Fatalf("packages/auth-engine pins better-auth %q; BetterAuthVersion is %q: update it", got, BetterAuthVersion)
	}
}
