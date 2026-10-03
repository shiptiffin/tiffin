package postgres

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// TestMeasureTables counts tables and rows on a real Postgres (embedded,
// downloaded once into ~/.embedded-postgres-go).
func TestMeasureTables(t *testing.T) {
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

	tables, rows, approx, err := measureTables(ctx, c)
	if err != nil || tables != 0 || rows != 0 || approx {
		t.Fatalf("empty database: %d tables, %d rows, approx %v, err %v", tables, rows, approx, err)
	}
	for _, s := range []string{
		`CREATE SCHEMA auth`,
		`CREATE TABLE orders (id int)`,
		`CREATE TABLE "Mixed Case" (id int)`,
		`CREATE TABLE auth.users (id int)`,
		`INSERT INTO orders SELECT generate_series(1, 1180)`,
		`INSERT INTO "Mixed Case" VALUES (1), (2)`,
		`INSERT INTO auth.users SELECT generate_series(1, 22)`,
	} {
		if _, err := c.Exec(ctx, s); err != nil {
			t.Fatal(s, err)
		}
	}
	// Counted exactly, whatever the statistics say yet.
	tables, rows, approx, err = measureTables(ctx, c)
	if err != nil || tables != 3 || rows != 1204 || approx {
		t.Fatalf("got %d tables, %d rows, approx %v, err %v; want 3, 1204, exact", tables, rows, approx, err)
	}
	l2 := (&change.Loss{Bytes: 8 << 20, Counts: []change.LossCount{{N: rows, Unit: "row"}, {N: tables, Unit: "table"}}}).Summarize()
	if l2.Summary != "1,204 rows in 3 tables · 8 MB" {
		t.Fatalf("summary %q", l2.Summary)
	}
}
