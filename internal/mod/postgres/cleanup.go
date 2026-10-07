package postgres

import (
	"context"

	"github.com/btahir/tiffin/internal/platform"
)

// ProjectDeleted runs once a destroyed project has no resources left. A
// destroy is final, so what the module keeps to undo things in the
// project's database goes with it:
//   - the undo record of the database: a new project under the same name
//     starts with an empty database, not the destroyed one's data (its auth
//     users included). The snapshot itself is kept for SnapshotKeep like
//     any other and pruned as usual.
//   - the row-edit log and the old rows it keeps for undo.
func (*Module) ProjectDeleted(ctx context.Context, p *platform.Platform, project string) error {
	editsMu.Lock()
	defer editsMu.Unlock()
	if list, err := listEdits(ctx, p, project); err == nil {
		for _, e := range list {
			_ = p.DB.KVDelete(ctx, nsEditRows, e.ID)
		}
	}
	if err := p.DB.KVDelete(ctx, nsEdits, project); err != nil {
		return err
	}
	return p.DB.KVDelete(ctx, nsDeleted, project)
}
