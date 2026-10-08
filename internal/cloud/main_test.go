package cloud

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// The store tests run against a real Postgres 18 (embedded-postgres, as the
// queue module's tests do). TIFFIN_CLOUD_TEST_PG points them at another.

var (
	pgBase string
	dbSeq  atomic.Int64
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	if v := os.Getenv("TIFFIN_CLOUD_TEST_PG"); v != "" {
		pgBase = v
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "tiffin-cloud-pg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "run")).DataPath(filepath.Join(dir, "data")).
		Username("tiffin").Password("tiffin").Database("postgres").
		StartParameters(map[string]string{"fsync": "off"}).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "embedded postgres:", err)
		return 1
	}
	defer pg.Stop()
	pgBase = fmt.Sprintf("postgres://tiffin:tiffin@127.0.0.1:%d/", port)
	return m.Run()
}

// newStore makes a fresh database with the schema applied.
func newStore(t *testing.T) *PG {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("c%d_%d", os.Getpid(), dbSeq.Add(1))
	c, err := pgx.Connect(ctx, pgBase+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	s, err := OpenPG(ctx, pgBase+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	// Applying again changes nothing.
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
