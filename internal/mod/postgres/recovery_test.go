package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// An undo whose snapshot is gone fails (it must not leave the service ready
// with an empty database) and keeps its record, marked failed; past the undo
// window the record just lapses.
func TestRestoreAfterUndoFailsVisibly(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	put := func(rec deletedRecord) {
		raw, _ := json.Marshal(rec)
		if err := db.KVPut(ctx, nsDeleted, "shop", raw); err != nil {
			t.Fatal(err)
		}
	}
	put(deletedRecord{Snapshot: "snap_01J0000000000000000000GONE", At: time.Now()})
	err = restoreAfterUndo(ctx, p, "shop")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing snapshot: %v", err)
	}
	raw, ok, _ := db.KVGet(ctx, nsDeleted, "shop")
	var rec deletedRecord
	if !ok || json.Unmarshal(raw, &rec) != nil || !rec.Failed {
		t.Fatalf("the record must stay, marked failed: %s", raw)
	}
	put(deletedRecord{Snapshot: "snap_old", At: time.Now().Add(-SnapshotKeep - time.Hour)})
	if err := restoreAfterUndo(ctx, p, "shop"); err != nil {
		t.Fatalf("expired record: %v", err)
	}
	if _, ok, _ := db.KVGet(ctx, nsDeleted, "shop"); ok {
		t.Fatal("an expired record lapses")
	}
	if err := restoreAfterUndo(ctx, p, "shop"); err != nil {
		t.Fatalf("no record: %v", err)
	}
}

// A database a crashed branch clone left refusing connections is reopened
// on start; databases that are not Tiffin's are left alone.
func TestReopenBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("needs an embedded Postgres")
	}
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "run")).DataPath(filepath.Join(dir, "data")).
		Username("tiffin").Password("tiffin").Database("postgres").
		StartParameters(map[string]string{"fsync": "off"}).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Stop() }()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, fmt.Sprintf("postgres://tiffin:tiffin@127.0.0.1:%d/postgres", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	old := dbAdmin
	dbAdmin = func(ctx context.Context, db string) (*pgx.Conn, error) {
		return pgx.Connect(ctx, fmt.Sprintf("postgres://tiffin:tiffin@127.0.0.1:%d/%s", port, db))
	}
	defer func() { dbAdmin = old }()
	for _, s := range []string{`CREATE ROLE p_shop`, `CREATE DATABASE p_shop OWNER p_shop`, `CREATE DATABASE p_shop__pr_1 OWNER p_shop`, `CREATE DATABASE other`,
		`ALTER DATABASE p_shop WITH ALLOW_CONNECTIONS false`, `ALTER DATABASE other WITH ALLOW_CONNECTIONS false`} {
		if _, err := c.Exec(ctx, s); err != nil {
			t.Fatal(s, err)
		}
	}
	if err := reopenBlocked(ctx, c, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	rows, _ := c.Query(ctx, `SELECT datname, datallowconn FROM pg_database`)
	for rows.Next() {
		var n string
		var ok bool
		_ = rows.Scan(&n, &ok)
		allowed[n] = ok
	}
	if !allowed["p_shop"] || !allowed["p_shop__pr_1"] || allowed["other"] || allowed["template0"] {
		t.Fatalf("after reopening: %v", allowed)
	}
}
