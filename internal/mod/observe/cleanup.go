package observe

import (
	"context"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Committed drops the host map once a change commits, so requests to an
// app's new routes count for it at once rather than after the map's next
// refresh (up to 5s of traffic went to no app).
func (m *Module) Committed(context.Context, *platform.Platform, *change.Change) {
	if m.sites != nil {
		m.sites.Invalidate()
	}
}

// ProjectDeleted forgets a destroyed project's error issues, traces and
// ingest keys. Its logs and metrics age out with the normal retention (30
// days unless changed).
func (m *Module) ProjectDeleted(ctx context.Context, _ *platform.Platform, project string) error {
	if m.store == nil {
		return nil
	}
	if err := m.traces.DeleteProject(ctx, project); err != nil {
		return err
	}
	return m.store.DeleteProject(ctx, project)
}
