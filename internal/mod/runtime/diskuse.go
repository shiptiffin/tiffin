package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppsDisk is what one project's apps hold on the data disk, besides
// their disk folders (reported with files).
type AppsDisk struct {
	Images     int   `json:"images" doc:"Images in the box's store for its deploys: live builds, rollback targets, and a deleted app's live build kept for an undo"`
	ImageBytes int64 `json:"imageBytes" doc:"Their unpacked size. Layers an image shares with others (the same base) count in each, so projects can add up to more than the store holds"`
	BuildBytes int64 `json:"buildBytes" doc:"Deploy work folders (source archives kept for rebuilds, build logs), static sites' files, extracted client assets and image caches"`
	LogBytes   int64 `json:"logBytes" doc:"App container logs (5 MB × 3 files per instance, newest 10 deploys) and build output copies"`
}

// RuntimeDisk is the runtime's part of the disk report.
type RuntimeDisk struct {
	Projects map[string]*AppsDisk `json:"projects"`
	// ImagesBytes and BuildCacheBytes are the image store and BuildKit's
	// cache, shared layers counted once (nerdctl system df).
	ImagesBytes     int64 `json:"imagesBytes"`
	BuildCacheBytes int64 `json:"buildCacheBytes"`
	// BuildCacheCapBytes is what the hourly sweep prunes the build cache
	// back to (buildCacheCap of the data disk).
	BuildCacheCapBytes int64 `json:"buildCacheCapBytes"`
	// Unused: images the hourly sweep removes (nothing needs them).
	UnusedImages     int   `json:"unusedImages"`
	UnusedImageBytes int64 `json:"unusedImageBytes"`
}

// DiskUse measures what the runtime holds per project (images, builds,
// logs) and in the image store and build cache. It walks the projects'
// folders: callers cache it.
func (m *Module) DiskUse(ctx context.Context) (*RuntimeDisk, error) {
	r, err := m.rt()
	if err != nil {
		return nil, err
	}
	imgs, err := r.eng.Images(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := r.inventory(ctx)
	if err != nil {
		return nil, err
	}
	need := r.neededImages(ctx, inv)
	out := &RuntimeDisk{Projects: map[string]*AppsDisk{}}
	of := func(p string) *AppsDisk {
		if out.Projects[p] == nil {
			out.Projects[p] = &AppsDisk{}
		}
		return out.Projects[p]
	}
	for p := range inv.projects {
		of(p)
	}
	for _, im := range imgs {
		if !managedImage(im.Name) {
			continue
		}
		key := imageKey(im.Name)
		if d := inv.byImage[key]; d != nil {
			u := of(d.Project)
			u.Images++
			u.ImageBytes += im.Size
		}
		if need[key] == "" {
			out.UnusedImages++
			out.UnusedImageBytes += im.Size
		}
	}
	for _, root := range []string{"deploys", "static", "assets", "next-cache", buildCacheDir} {
		es, _ := os.ReadDir(filepath.Join(r.opt.DataDir, root))
		for _, e := range es {
			if e.IsDir() && projectName.MatchString(e.Name()) {
				of(e.Name()).BuildBytes += dirSize(filepath.Join(r.opt.DataDir, root, e.Name()))
			}
		}
	}
	es, _ := os.ReadDir(r.opt.LogDir)
	for _, e := range es {
		if e.IsDir() && projectName.MatchString(e.Name()) {
			of(e.Name()).LogBytes += dirSize(filepath.Join(r.opt.LogDir, e.Name()))
		}
	}
	out.ImagesBytes, out.BuildCacheBytes = r.storeSizes(ctx)
	out.BuildCacheCapBytes = buildCacheCap(diskBytes(r.opt.DataDir))
	return out, nil
}

// storeSizes reads the image store's and BuildKit cache's sizes from
// nerdctl system df (0 when it cannot say).
func (r *rt) storeSizes(ctx context.Context) (images, buildCache int64) {
	n, ok := r.eng.(*nerdctl)
	if !ok {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := n.run(ctx, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return 0, 0
	}
	return parseSystemDF(out)
}

func parseSystemDF(out string) (images, buildCache int64) {
	for _, line := range strings.Split(out, "\n") {
		var row struct{ Type, Size string }
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &row) != nil {
			continue
		}
		switch row.Type {
		case "Images":
			images = parseSize(row.Size)
		case "Build Cache":
			buildCache = parseSize(row.Size)
		}
	}
	return images, buildCache
}
