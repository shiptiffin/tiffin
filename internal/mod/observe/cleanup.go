package observe

import (
	"context"

	"github.com/btahir/tiffin/internal/platform"
)

// ProjectDeleted forgets a destroyed project's error issues, traces and
// ingest keys. Its logs and metrics age out with the normal retention (14
// and 30 days).
func (m *Module) ProjectDeleted(ctx context.Context, _ *platform.Platform, project string) error {
	if m.store == nil {
		return nil
	}
	if err := m.traces.DeleteProject(ctx, project); err != nil {
		return err
	}
	return m.store.DeleteProject(ctx, project)
}
