package postgres

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/change"
)

// embedded starts a real Postgres (embedded, downloaded once into
// ~/.embedded-postgres-go) and returns a superuser connection to db
// "postgres" and a function connecting to another database.
func embedded(t *testing.T) (*pgx.Conn, func(db string) *pgx.Conn) {
	t.Helper()
	port := embeddedPort(t)
	connect := func(db string) *pgx.Conn {
		t.Helper()
		return connectAs(t, port, "tiffin", "tiffin", db)
	}
	return connect("postgres"), connect
}

// connectAs connects to the embedded Postgres on port as user.
func connectAs(t *testing.T, port int, user, password, db string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s", user, password, port, db))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// embeddedPort starts a real Postgres whose superuser is tiffin/tiffin and
// returns its port.
func embeddedPort(t *testing.T) int {
	t.Helper()
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
	t.Cleanup(func() { _ = pg.Stop() })
	return port
}

// TestMeasureTables counts tables and rows on a real Postgres.
func TestMeasureTables(t *testing.T) {
	c, _ := embedded(t)
	ctx := context.Background()

	tables, rows, approx, err := measureTables(ctx, c)
	if err != nil || tables != 0 || rows != 0 || approx {
		t.Fatalf("empty database: %d tables, %d rows, approx %v, err %v", tables, rows, approx, err)
	}
	for _, s := range []string{
		`CREATE SCHEMA tiffin_auth`,
		`CREATE TABLE orders (id int)`,
		`CREATE TABLE "Mixed Case" (id int)`,
		`CREATE TABLE tiffin_auth.users (id int)`,
		`INSERT INTO orders SELECT generate_series(1, 1180)`,
		`INSERT INTO "Mixed Case" VALUES (1), (2)`,
		`INSERT INTO tiffin_auth.users SELECT generate_series(1, 22)`,
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
