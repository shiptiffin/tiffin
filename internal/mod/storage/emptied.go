package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// "Delete all data" of a project's Files (resource emptied/storage). Making
// the resource moves every bucket of the project to the trash (kept
// TrashRetention, like a deleted bucket, but tagged so that making the
// bucket again does not bring it back), then makes the declared buckets
// again, empty. Deleting the resource (Restore, or undoing that change)
// moves what the buckets hold by then to the trash too and puts the tagged
// ones back. The project's key is untouched: apps keep their S3 settings.

func emptiedKey(project string) string { return "emptied/" + project }

type emptiedRecord struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
	Done    bool      `json:"done"` // the declared buckets were made again
}

func (m *Module) reconcileEmptied(ctx context.Context, p *platform.Platform, gw *gateway, project string, spec json.RawMessage) error {
	var rec emptiedRecord
	raw, have, err := p.DB.KVGet(ctx, kvNS, emptiedKey(project))
	if err != nil {
		return err
	}
	if have {
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unreadable record of the deleted files: %w", err)
		}
	}
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil {
		return err
	}
	if spec == nil {
		if !have {
			return nil
		}
		if !on {
			// The project is being deleted: its tagged buckets stay in the
			// trash as plain deleted buckets, purged with the rest.
			if err := m.untagTrash(ctx, p, project, rec.Version, false); err != nil {
				return err
			}
			return p.DB.KVDelete(ctx, kvNS, emptiedKey(project))
		}
		back, err := m.takeBack(ctx, p, project, rec.Version, buckets)
		if err != nil {
			return err
		}
		c, _, err := credsFor(ctx, p, project, true)
		if err != nil {
			return err
		}
		for _, name := range back {
			if err := gw.changeOwner(ctx, S3Name(project, name), c.AccessKey); err != nil {
				return err
			}
		}
		if err := m.makeBuckets(ctx, p, project, buckets); err != nil {
			return err
		}
		p.Log.Info("storage: restored the deleted files", "project", project, "buckets", len(back))
		return p.DB.KVDelete(ctx, kvNS, emptiedKey(project))
	}
	var s change.EmptiedSpec
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("emptied spec: %w", err)
	}
	if have && rec.Version == s.Version && rec.Done {
		return nil
	}
	if !on {
		return errors.New("the project has no files to empty")
	}
	if !have || rec.Version != s.Version {
		if have {
			// An earlier delete's buckets: replaced by this one.
			if err := m.untagTrash(ctx, p, project, rec.Version, true); err != nil {
				return err
			}
		}
		rec = emptiedRecord{Version: s.Version, At: time.Now().UTC()}
		if err := m.putEmptied(ctx, p, project, rec); err != nil {
			return err
		}
	}
	n, err := m.trashAll(ctx, p, project, s.Version)
	if err != nil {
		return err
	}
	if err := m.makeBuckets(ctx, p, project, buckets); err != nil {
		return err
	}
	rec.Done = true
	p.Log.Info("storage: deleted all files", "project", project, "buckets", n)
	return m.putEmptied(ctx, p, project, rec)
}

func (m *Module) putEmptied(ctx context.Context, p *platform.Platform, project string, rec emptiedRecord) error {
	raw, _ := json.Marshal(rec)
	return p.DB.KVPut(ctx, kvNS, emptiedKey(project), raw)
}

// projectBuckets are the names of every bucket the project has on disk
// ("<project>--<bucket>": project names have no "--").
func projectBuckets(_ context.Context, p *platform.Platform, project string) ([]string, error) {
	entries, err := os.ReadDir(dataDir(p.DataRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutPrefix(e.Name(), project+"--"); ok && e.IsDir() && projectOf(e.Name()) == project {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// trashAll moves every bucket of the project to the trash, tagged with version.
func (m *Module) trashAll(ctx context.Context, p *platform.Platform, project string, version int64) (int, error) {
	names, err := projectBuckets(ctx, p, project)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		if err := m.trashBucketFor(ctx, p, project, name, version); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

// takeBack puts the buckets tagged with version back, each in place of what
// it holds now (moved to the trash, untagged). A bucket the project no
// longer declares stays in the trash, untagged. It returns the buckets put back.
func (m *Module) takeBack(ctx context.Context, p *platform.Platform, project string, version int64, declared map[string]manifest.Bucket) ([]string, error) {
	entries, err := trashEntries(ctx, p)
	if err != nil {
		return nil, err
	}
	var back []string
	for _, e := range entries {
		if e.Project != project || e.Emptied != version {
			continue
		}
		if _, ok := declared[e.Bucket]; !ok {
			if err := retag(ctx, p, e, 0); err != nil {
				return back, err
			}
			continue
		}
		if _, err := os.Stat(e.Path); err != nil {
			_ = p.DB.KVDelete(ctx, kvNS, trashKey(e.ID))
			continue
		}
		if err := m.trashBucket(ctx, p, project, e.Bucket); err != nil {
			return back, err
		}
		if err := os.Rename(e.Path, filepath.Join(dataDir(p.DataRoot), e.S3Name)); err != nil {
			return back, fmt.Errorf("restore bucket %s from the trash: %w", e.S3Name, err)
		}
		if err := p.DB.KVDelete(ctx, kvNS, trashKey(e.ID)); err != nil {
			return back, err
		}
		back = append(back, e.Bucket)
	}
	sort.Strings(back)
	return back, nil
}

// untagTrash makes the buckets tagged with version plain deleted buckets,
// or (purge) deletes them for good.
func (m *Module) untagTrash(ctx context.Context, p *platform.Platform, project string, version int64, purge bool) error {
	entries, err := trashEntries(ctx, p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Project != project || e.Emptied != version {
			continue
		}
		if purge {
			err = purgeEntry(ctx, p, e)
		} else {
			err = retag(ctx, p, e, 0)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func retag(ctx context.Context, p *platform.Platform, e TrashEntry, version int64) error {
	e.Emptied = version
	raw, _ := json.Marshal(e)
	return p.DB.KVPut(ctx, kvNS, trashKey(e.ID), raw)
}

// makeBuckets converges the declared buckets: missing ones are made, empty.
func (m *Module) makeBuckets(ctx context.Context, p *platform.Platform, project string, declared map[string]manifest.Bucket) error {
	for _, name := range sortedNames(declared) {
		spec, _ := json.Marshal(declared[name])
		if err := m.Reconcile(ctx, p, project, change.KindBucket+"/"+name, spec); err != nil {
			return err
		}
	}
	return nil
}

func sortedNames(m map[string]manifest.Bucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
