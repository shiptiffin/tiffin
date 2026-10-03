package datakit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// TestPlatform returns a Platform with a real state DB and box key in a temp dir.
func testPlatform(t *testing.T) *platform.Platform {
	t.Helper()
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
	return &platform.Platform{DB: db, Secrets: sec, Home: home, Log: slog.Default()}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{"shop": "p_shop", "my-shop": "p_my_shop", "a1-b2-c3": "p_a1_b2_c3"} {
		if got := Ident(in); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPassword(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := Password()
		if !re.MatchString(p) {
			t.Fatalf("password %q is not 32 URL-safe characters", p)
		}
		if seen[p] {
			t.Fatal("duplicate password")
		}
		seen[p] = true
	}
}

func TestSecretsAreSealed(t *testing.T) {
	p := testPlatform(t)
	ctx := context.Background()
	v, err := EnsureSecret(ctx, p, "test.ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	again, err := EnsureSecret(ctx, p, "test.ns", "proj")
	if err != nil || again != v {
		t.Fatalf("EnsureSecret must be stable: %q then %q (%v)", v, again, err)
	}
	raw, ok, _ := p.DB.KVGet(ctx, "test.ns", "proj")
	if !ok || bytes.Contains(raw, []byte(v)) {
		t.Fatal("the stored value must be encrypted, not plain text")
	}
	if _, ok, _ := GetSecret(ctx, p, "test.ns", "missing"); ok {
		t.Fatal("missing secret reported as present")
	}
	// Never part of an app's env (that comes from p.Secrets only).
	all, _ := p.Secrets.All(ctx, "proj")
	if len(all) != 0 {
		t.Fatalf("module secrets leaked into app secrets: %v", all)
	}
}

func TestRequireConfirm(t *testing.T) {
	key := []string{"bk_1", "postgres"}
	err := RequireConfirm("", key, map[string]any{"n": 1})
	var cp *ConfirmProblem
	if !errors.As(err, &cp) || cp.Status != 428 || cp.Code != "confirm_required" || len(cp.Confirm) != 16 {
		t.Fatalf("want a 428 confirm_required with a hash, got %#v", err)
	}
	// The preview's live numbers may drift; the key decides.
	if err := RequireConfirm(cp.Confirm[:8], key, map[string]any{"n": 2}); err != nil {
		t.Fatalf("matching confirm prefix refused: %v", err)
	}
	if err := RequireConfirm(cp.Confirm[:7], key, nil); err == nil {
		t.Fatal("a confirm shorter than 8 characters must not count")
	}
	err = RequireConfirm(cp.Confirm, []string{"bk_2"}, nil)
	if !errors.As(err, &cp) || cp.Code != "plan_mismatch" {
		t.Fatalf("stale confirm must be plan_mismatch, got %v", err)
	}
}
