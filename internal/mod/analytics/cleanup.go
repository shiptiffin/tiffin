package analytics

import (
	"context"

	"github.com/btahir/tiffin/internal/platform"
)

// ProjectDeleted forgets everything analytics keeps about a destroyed
// project: events, daily rollups, vitals and its apps' collector keys.
// Removing services.analytics does the same, but a key is made for any app
// that asks for its snippet, and a failed delete must not leave data
// behind once the project is gone.
func (m *Module) ProjectDeleted(ctx context.Context, _ *platform.Platform, project string) error {
	if m.store == nil {
		return nil
	}
	if m.sites != nil {
		m.sites.Invalidate()
	}
	return m.deleteProject(ctx, project)
}
