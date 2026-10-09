package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// A destroyed project's database is not brought back into a new project of
// the same name: the destroy forgets the undo record (the snapshot stays)
// and the row-edit log with the old rows it kept.
func TestProjectDeletedForgetsUndoRecord(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	raw, _ := json.Marshal(deletedRecord{Snapshot: "snap_01J0000000000000000000SHOP", At: time.Now()})
	if err := db.KVPut(ctx, nsDeleted, "shop", raw); err != nil {
		t.Fatal(err)
	}
	if err := db.KVPut(ctx, nsDeleted, "other", raw); err != nil {
		t.Fatal(err)
	}
	if err := recordEdit(ctx, p, "shop", &PGEdit{Kind: "delete"}, &editRows{}); err != nil {
		t.Fatal(err)
	}
	edits, _ := listEdits(ctx, p, "shop")
	if len(edits) != 1 || !edits[0].Undoable {
		t.Fatalf("edit not recorded: %+v", edits)
	}
	var m platform.ProjectCleaner = &Module{}
	if err := m.ProjectDeleted(ctx, p, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.KVGet(ctx, nsDeleted, "shop"); ok {
		t.Fatal("the destroyed project's undo record must go")
	}
	if _, ok, _ := db.KVGet(ctx, nsDeleted, "other"); !ok {
		t.Fatal("other projects keep theirs")
	}
	if _, ok, _ := db.KVGet(ctx, nsEditRows, edits[0].ID); ok {
		t.Fatal("the edit's old rows must go")
	}
	if list, _ := listEdits(ctx, p, "shop"); len(list) != 0 {
		t.Fatalf("the edit log must go: %+v", list)
	}
	if err := restoreAfterUndo(ctx, p, "shop"); err != nil {
		t.Fatalf("a new project of the same name starts empty: %v", err)
	}
}
