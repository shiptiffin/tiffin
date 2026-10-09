package postgres

import (
	"context"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// ProjectUsage measures a project's databases (branches included) and its
// open connections, for the project usage API.
func (*Module) ProjectUsage(ctx context.Context, p *platform.Platform, project string) (*platform.ServiceUsage, error) {
	if ok, err := HasService(ctx, p, project); err != nil || !ok {
		return nil, err
	}
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil || len(dbs) == 0 {
		return nil, err
	}
	names := make([]string, len(dbs))
	for i, d := range dbs {
		names[i] = d.Name
	}
	u := &platform.ServiceUsage{Service: "postgres", Disk: "database", Counts: map[string]int64{}}
	var conns int64
	if err := admin.QueryRow(ctx, `SELECT coalesce(sum(pg_database_size(datname)), 0)::bigint,
		(SELECT count(*) FROM pg_stat_activity WHERE datname = ANY($1))
		FROM pg_database WHERE datname = ANY($1)`, names).Scan(&u.Bytes, &conns); err != nil {
		return nil, err
	}
	u.Counts["connections"] = conns
	return u, nil
}
