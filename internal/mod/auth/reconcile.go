package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Kinds: the auth service. It is reconciled after every apply of its
// project, so app host changes reach the engine config too.
func (*Module) Kinds() []string { return []string{change.KindService + "/auth"} }

var reconcileMu sync.Mutex

// Reconcile makes the engine serve the project: write its config, reload,
// and create or upgrade the project's tiffin_auth schema. With spec nil (auth
// removed) the schema is dropped: every user, session and organization of
// the project is deleted. The plan marks that irreversible.
func (*Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	eng := defaultEngine
	if spec == nil {
		// The schema lives in the project's database: when that is going too
		// (the project is destroyed, or postgres removed with auth), it goes
		// with it, and the engine can't log in to drop it anyway.
		if postgresStays(ctx, p, project) {
			if err := eng.drop(ctx, project); err != nil && !gone(err) {
				return fmt.Errorf("delete the tiffin_auth schema: %w", err)
			}
		}
		_ = p.DB.KVDelete(ctx, nsSecret, project)
		if _, err := syncConfig(ctx, p, eng); err != nil {
			return err
		}
		return nil
	}
	errs, err := syncConfig(ctx, p, eng)
	if err != nil {
		return err
	}
	if e := errs[project]; e != nil {
		return e
	}
	// The platform reconciles services by address, so auth can run before
	// postgres has created the project's database. Wait for it: converge the
	// project again once this pass (which sets the database up) is done.
	if st, err := p.DB.ResourceStatuses(ctx, project); err == nil {
		if s, ok := st[change.KindService+"/postgres"]; !ok || s.State == platform.StatePending {
			p.ReconcileProject(project)
			return fmt.Errorf("waiting for the project's Postgres database; auth sets up right after it")
		}
	}
	r, err := eng.migrate(ctx, project)
	if err != nil {
		return fmt.Errorf("set up the tiffin_auth schema: %w", err)
	}
	if len(r.Created) > 0 || len(r.Added) > 0 {
		p.Log.Info("auth schema migrated", "project", project, "created", strings.Join(r.Created, ","), "added", strings.Join(r.Added, ","), "ms", r.Ms)
	}
	return nil
}

// syncConfig rewrites the engine config from platform state and reloads the
// engine when it changed. Returns per-project configuration errors.
func syncConfig(ctx context.Context, p *platform.Platform, eng *engine) (map[string]error, error) {
	c, errs, err := buildEngineConfig(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(c.Projects) == 0 && len(errs) == 0 {
		if _, err := os.Stat(ConfigPath(p)); err != nil {
			return errs, nil // auth never used on this box: leave the engine idle
		}
	}
	changed, err := writeEngineConfig(p, c)
	if err != nil {
		return nil, fmt.Errorf("write auth engine config: %w", err)
	}
	if changed {
		if err := eng.reload(ctx); err != nil {
			return nil, err
		}
	}
	return errs, nil
}

// postgresStays reports whether the project keeps its Postgres database
// after this change: it is still among its resources.
func postgresStays(ctx context.Context, p *platform.Platform, project string) bool {
	if p == nil || p.DB == nil {
		return true
	}
	v, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return true // can't tell: try, and let gone() judge the error
	}
	_, ok := res[change.KindService+"/postgres"]
	return v > 0 && ok
}

// gone reports errors that mean there is nothing left to delete: the
// schema, the project's entry or its database (whose role is deleted with
// it, so logging in fails) is already gone.
func gone(err error) bool {
	s := err.Error()
	for _, m := range []string{"does not exist", "auth isn't set up for project", "password authentication failed", "SASL authentication failed", "database removed"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// Start keeps the engine config fresh for changes that don't go through an
// apply: OAuth secrets set later, a database that became ready, a restarted
// engine. Cheap: it only reloads the engine when the file changed.
func (*Module) Start(ctx context.Context, p *platform.Platform) error {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			reconcileMu.Lock()
			_, err := syncConfig(ctx, p, defaultEngine)
			reconcileMu.Unlock()
			if err != nil && p.Log != nil {
				p.Log.Warn("auth config sync", "err", err)
			}
		}
	}()
	return nil
}
