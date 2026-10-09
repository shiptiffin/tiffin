package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/mod/datakit"
)

// Files in a backed-up folder are the app's to name and fill. A name that
// reads as URI parameters must not reach SQLite as such (it ran PRAGMAs, so
// SQL, as root), and a file that only looks like a database must not fail
// the backup: it is kept as a plain copy and named in failed.
func TestSnapshotSQLiteHostileFiles(t *testing.T) {
	ctx := context.Background()
	live, dst := t.TempDir(), t.TempDir()
	hostile := filepath.Join(live, "x?_pragma=user_version(77)")
	db, err := sql.Open("sqlite3", datakit.SQLiteURI(hostile, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE marks (v TEXT); INSERT INTO marks VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	junk := append([]byte("SQLite format 3\x00"), make([]byte, 50)...)
	if err := os.WriteFile(filepath.Join(live, "junk.db"), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{filepath.Base(hostile), "junk.db"} {
		copyFile(t, filepath.Join(live, n), filepath.Join(dst, n))
	}

	failed, err := snapshotSQLite(ctx, live, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || !strings.HasPrefix(failed[0], "junk.db") {
		t.Fatalf("failed: %v", failed)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "junk.db")); string(b) != string(junk) {
		t.Fatal("the file that is no database keeps its plain copy")
	}
	if _, err := os.Stat(filepath.Join(live, "x")); err == nil {
		t.Fatal("the name's query part was read as parameters: SQLite opened x")
	}
	c, err := sql.Open("sqlite3", datakit.SQLiteURI(filepath.Join(dst, filepath.Base(hostile)), "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var v string
	var uv int
	if err := c.QueryRow(`SELECT v FROM marks`).Scan(&v); err != nil || v != "kept" {
		t.Fatalf("the snapshot of the oddly named database: %q %v", v, err)
	}
	if err := c.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != 0 {
		t.Fatalf("user_version %d %v: the name ran a PRAGMA", uv, err)
	}
}
