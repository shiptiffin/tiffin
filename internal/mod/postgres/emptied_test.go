package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// Delete all data on a real Postgres: the project's database is snapshotted,
// dropped and made again empty for the same role and password; deleting the
// emptied resource loads the snapshot back. The test server has no pg_dump,
// so a snapshot here is a template copy of the database.
func TestEmptyAndRestore(t *testing.T) {
	tc := newTenantCluster(t)
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Secrets: sec, Home: home, Engine: change.NewEngine(db), Log: slog.New(slog.DiscardHandler)}
	set := func(res map[string]change.Resource) {
		t.Helper()
		plan, err := p.Engine.PlanEdit(ctx, "shop", func(map[string]change.Resource) (map[string]change.Resource, error) { return res, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "human", ID: "t"}}); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string]change.Resource{
		change.KindProject:               {Address: change.KindProject, Spec: json.RawMessage(`{}`)},
		change.KindService + "/postgres": {Address: change.KindService + "/postgres", Spec: json.RawMessage(`{}`)},
	}
	set(base)
	var m Module
	if err := m.Reconcile(ctx, p, "shop", change.KindService+"/postgres", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	pw, _, err := datakit.GetSecret(ctx, p, nsPassword, "shop")
	if err != nil {
		t.Fatal(err)
	}
	app, err := tc.as(t, "p_shop", pw, "p_shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `CREATE TABLE books (id int PRIMARY KEY, title text); INSERT INTO books SELECT i, 'b' || i FROM generate_series(1, 42) i`); err != nil {
		t.Fatal(err)
	}
	app.Close(ctx)

	// Snapshots are template copies, kept in a temp dir.
	snaps := map[string]*PGSnapshot{}
	n := 0
	oldSnap, oldLoad, oldRestore := snapshotDB, loadSnapshot, restoreDB
	t.Cleanup(func() { snapshotDB, loadSnapshot, restoreDB = oldSnap, oldLoad, oldRestore })
	snapshotDB = func(ctx context.Context, project, dbName, branch, reason string) (*PGSnapshot, error) {
		n++
		s := &PGSnapshot{ID: fmt.Sprintf("snap_%d", n), Project: project, Database: dbName, Branch: branch, Reason: reason, At: time.Now()}
		if _, err := tc.admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, s.ID, dbName)); err != nil {
			return nil, err
		}
		snaps[s.ID] = s
		return s, nil
	}
	loadSnapshot = func(project, id string) (*PGSnapshot, error) {
		if s, ok := snaps[id]; ok {
			return s, nil
		}
		return nil, os.ErrNotExist
	}
	restoreDB = func(ctx context.Context, _ *platform.Platform, s *PGSnapshot) (*PGSnapshot, error) {
		if _, err := tc.admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, s.Database)); err != nil {
			return nil, err
		}
		_, err := tc.admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s OWNER p_shop`, s.Database, s.ID))
		return nil, err
	}
	rows := func() (int, error) {
		c, err := tc.as(t, "p_shop", pw, "p_shop")
		if err != nil {
			return -1, err
		}
		defer c.Close(ctx)
		var n int
		err = c.QueryRow(ctx, `SELECT count(*) FROM books`).Scan(&n)
		return n, err
	}

	addr := change.EmptyAddress("postgres")
	spec := json.RawMessage(`{"version":2}`)
	emptied := map[string]change.Resource{addr: {Address: addr, Spec: spec}}
	for k, v := range base {
		emptied[k] = v
	}
	set(emptied)
	if err := m.Reconcile(ctx, p, "shop", addr, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := rows(); err == nil {
		t.Fatal("the table is still there after Delete all data")
	}
	// The same role and password still log in; a second pass changes nothing.
	if err := m.Reconcile(ctx, p, "shop", addr, spec); err != nil || n != 1 {
		t.Fatalf("a second pass: %v (%d snapshots)", err, n)
	}

	set(base) // Restore: the emptied resource goes.
	if err := m.Reconcile(ctx, p, "shop", addr, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := rows(); err != nil || got != 42 {
		t.Fatalf("after restore: %d rows, %v", got, err)
	}
	if _, ok, _ := db.KVGet(ctx, nsEmptied, "shop"); ok {
		t.Fatal("the record of the deleted data must go once it is back")
	}
	// Nothing recorded: deleting the resource again is a no-op.
	if err := m.Reconcile(ctx, p, "shop", addr, nil); err != nil {
		t.Fatal(err)
	}
}
