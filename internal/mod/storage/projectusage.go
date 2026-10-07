package storage

import (
	"context"

	"github.com/btahir/tiffin/internal/platform"
)

// ProjectUsage reports a project's bucket files (from the periodic scan),
// for the project usage API.
func (m *Module) ProjectUsage(ctx context.Context, p *platform.Platform, project string) (*platform.ServiceUsage, error) {
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil || !on {
		return nil, err
	}
	t := m.tracker()
	u := &platform.ServiceUsage{Service: "storage", Disk: "files", Bytes: t.project(project),
		Counts: map[string]int64{"buckets": int64(len(buckets))}}
	var objects int64
	for name := range buckets {
		objects += t.bucket(S3Name(project, name)).Objects
	}
	u.Counts["objects"] = objects
	return u, nil
}
