package runtime

import (
	"context"
	"fmt"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// ReportStatus says, for each app, whether production has a release. An
// app's stored state only says its config is applied, so an app whose first
// deploy failed read ready while it served nothing: it now reads failed,
// naming the deploy. A never deployed app stays ready (agents wait for
// ready before the first deploy), with release none.
func (m *Module) ReportStatus(ctx context.Context, p *platform.Platform, project string, st map[string]state.ResourceStatus) {
	s := store{db: p.DB}
	for addr, rs := range st {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		app := change.Name(addr)
		if env, err := s.getState(ctx, project, app, ""); err != nil {
			continue
		} else if env.Live != "" {
			rs.Release = "live"
			st[addr] = rs
			continue
		}
		deps, err := s.listDeploys(ctx, project, app, "")
		if err != nil {
			continue
		}
		rs.Release = "none"
		switch {
		case len(deps) == 0:
			if rs.Message == "" {
				rs.Message = "not deployed yet"
			}
		case deps[0].Status == StatusFailed:
			rs.Release = "failed"
			if rs.State != "failed" {
				rs.State = "failed"
				rs.Message = fmt.Sprintf("not live: its last deploy failed (%s); see tiffin deploys build-log %s %s %s", deps[0].ID, project, app, deps[0].ID)
			}
		case !deps[0].Terminal() && rs.Message == "":
			rs.Message = "its first deploy is under way (" + deps[0].ID + ")"
		}
		st[addr] = rs
	}
}

var _ platform.StatusReporter = (*Module)(nil)
