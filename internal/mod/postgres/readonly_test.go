package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A hold makes every database of the project (branches too) refuse writes,
// closes its sessions so they pick that up, and lifting it reverses both.
// Other projects keep writing, and sizes are summed per project.
func TestReadOnlyHold(t *testing.T) {
	admin, connect := embedded(t)
	ctx := context.Background()
	for _, s := range []string{
		`CREATE DATABASE p_shop`,
		`CREATE DATABASE p_shop__pr_1`,
		`COMMENT ON DATABASE p_shop__pr_1 IS '{"tiffin":"branch","project":"shop","branch":"pr-1"}'`,
		`CREATE DATABASE p_blog`,
	} {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatal(s, err)
		}
	}
	shop, branch, blog := connect("p_shop"), connect("p_shop__pr_1"), connect("p_blog")
	for _, c := range []*pgx.Conn{shop, branch, blog} {
		if _, err := c.Exec(ctx, `CREATE TABLE t (id int)`); err != nil {
			t.Fatal(err)
		}
	}

	if err := setReadOnly(ctx, admin, "shop", true); err != nil {
		t.Fatal(err)
	}
	if _, err := shop.Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("shop's open session must be closed so it reconnects read-only")
	}
	for _, db := range []string{"p_shop", "p_shop__pr_1"} {
		_, err := connect(db).Exec(ctx, `INSERT INTO t VALUES (1)`)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "25006" {
			t.Fatalf("%s: a write must fail read-only, got %v", db, err)
		}
	}
	if _, err := blog.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatalf("blog keeps writing: %v", err)
	}
	if err := setReadOnly(ctx, admin, "shop", true); err != nil { // idempotent
		t.Fatal(err)
	}

	sizes, err := databaseSizes(ctx, admin, []string{"shop", "blog"})
	if err != nil || sizes["shop"] <= sizes["blog"] || sizes["blog"] == 0 || len(sizes) != 2 {
		t.Fatalf("sizes: %v %v (shop has a branch, so it is bigger)", sizes, err)
	}

	if err := setReadOnly(ctx, admin, "shop", false); err != nil {
		t.Fatal(err)
	}
	if _, err := connect("p_shop").Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatalf("lifted: %v", err)
	}
}

func TestHeldProblem(t *testing.T) {
	holds.Lock()
	holds.why["shop"] = "shop is read-only: free space"
	holds.Unlock()
	defer func() { holds.Lock(); delete(holds.why, "shop"); holds.Unlock() }()
	err := heldProblem("shop")
	if err == nil || !strings.Contains(err.Error(), "free space") || heldProblem("blog") != nil {
		t.Fatalf("held: %v", err)
	}
}
