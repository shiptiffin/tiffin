package state

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/change/changetest"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConformance(t *testing.T) {
	changetest.Run(t, func(t *testing.T) change.Store { return openTemp(t) })
}

func TestConformanceInMemory(t *testing.T) {
	changetest.Run(t, func(t *testing.T) change.Store {
		db, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := changetest.Converge(t, change.NewEngine(db), changetest.M("shop", nil))
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ver, cur, err := db.Load(context.Background(), "shop")
	if err != nil || ver != 1 || len(cur) != 4 {
		t.Fatalf("after reopen: ver=%d n=%d err=%v", ver, len(cur), err)
	}
	got, err := db.GetChange(context.Background(), c.ID)
	if err != nil || got.Plan.Hash != c.Plan.Hash {
		t.Fatalf("change after reopen: %v", err)
	}
}

// Concurrent appliers racing on the same base version: exactly one wins,
// the rest get ErrConflict, and the version advances by exactly one.
func TestConcurrentApplyOneWinner(t *testing.T) {
	db := openTemp(t)
	e := change.NewEngine(db)
	changetest.Converge(t, e, changetest.M("shop", nil))
	ctx := context.Background()
	const n = 8
	plans := make([]*change.Plan, n)
	for i := range n {
		m := changetest.M("shop", func(m *manifest.Manifest) { m.Env["WORKER"] = string(rune('a' + i)) })
		r, _ := change.Resources(m)
		p, err := e.Plan(ctx, "shop", r)
		if err != nil {
			t.Fatal(err)
		}
		plans[i] = p
	}
	var wg sync.WaitGroup
	results := make(chan error, n)
	for _, p := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash, Actor: change.Actor{Kind: "agent", ID: "t"}})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch err {
		case nil:
			wins++
		case change.ErrConflict:
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	ver, _, _ := db.Load(ctx, "shop")
	if wins != 1 || ver != 2 {
		t.Fatalf("wins=%d version=%d, want 1 and 2", wins, ver)
	}
}

func TestAudit(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	for _, a := range []string{"token.create", "token.revoke"} {
		if err := db.Audit(ctx, "tok_owner", a, "tok_x", map[string]string{"name": "ci"}); err != nil {
			t.Fatal(err)
		}
	}
	log, err := db.AuditLog(ctx, 10, 0)
	if err != nil || len(log) != 2 || log[0].Action != "token.revoke" || log[0].At.IsZero() {
		t.Fatalf("audit log: %v %+v", err, log)
	}
	if older, _ := db.AuditLog(ctx, 10, log[0].Seq); len(older) != 1 || older[0].Action != "token.create" {
		t.Fatalf("the page after the newest: %+v", older)
	}
	// Retention: events from before the cut go.
	if err := db.PruneAudit(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.AuditLog(ctx, 10, 0); len(left) != 0 {
		t.Fatalf("after pruning: %+v", left)
	}
}

// Project secrets move from the secrets table into "secret/NAME" resources:
// for applied projects and for projects that only had secrets. Leftovers of
// destroyed projects and module secrets stay in the table.
func TestSecretsMigrateToResources(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.sql.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects(name, version) VALUES ('shop', 2), ('gone', 3)`)
	exec(`INSERT INTO resources(project, address, spec) VALUES ('shop', 'project', '{}')`)
	for _, p := range []string{"shop", "fresh", "gone", "_email"} {
		exec(`INSERT INTO secrets(project, name, ciphertext, updated_at, updated_by) VALUES (?, 'KEY', X'00FF', '2026-01-02T03:04:05Z', 'tok_1')`, p)
	}
	// The three statements that move secrets, run again on this data.
	from := slices.IndexFunc(migrations, func(m string) bool {
		return strings.HasPrefix(m, "INSERT INTO projects(name, version) SELECT DISTINCT")
	})
	for _, m := range migrations[from : from+3] {
		exec(m)
	}
	for _, p := range []string{"shop", "fresh"} {
		v, res, err := db.Load(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		r, ok := res["secret/KEY"]
		if !ok || v < 1 || !strings.Contains(string(r.Spec), `"sealed":"00ff"`) || !strings.Contains(string(r.Spec), `"updatedBy":"tok_1"`) {
			t.Fatalf("%s: version %d, resources %v", p, v, res)
		}
	}
	if _, res, _ := db.Load(ctx, "gone"); len(res) != 0 {
		t.Fatalf("a destroyed project came back: %v", res)
	}
	var left []string
	rows, _ := db.sql.QueryContext(ctx, `SELECT project FROM secrets ORDER BY project`)
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		left = append(left, p)
	}
	rows.Close()
	if strings.Join(left, ",") != "_email,gone" {
		t.Fatalf("left in the secrets table: %v", left)
	}
}
