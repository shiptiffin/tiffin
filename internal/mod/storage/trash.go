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

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/platform"
)

// TrashEntry is a deleted bucket kept for TrashRetention.
type TrashEntry struct {
	ID        string    `json:"id" doc:"Trash entry ID"`
	Project   string    `json:"project"`
	Bucket    string    `json:"bucket"`
	S3Name    string    `json:"s3Name"`
	Path      string    `json:"path" doc:"Where the bucket's files are kept on the box"`
	Bytes     int64     `json:"bytes"`
	Objects   int64     `json:"objects"`
	DeletedAt time.Time `json:"deletedAt"`
	ExpiresAt time.Time `json:"expiresAt" doc:"After this the files are deleted for good"`
}

func trashKey(id string) string { return "trash/" + id }

// trashBucket moves a bucket's directory into the trash. A missing bucket
// is not an error (already deleted).
func (m *Module) trashBucket(ctx context.Context, p *platform.Platform, project, name string) error {
	s3name := S3Name(project, name)
	src := filepath.Join(dataDir(p.DataRoot), s3name)
	if meta, err := getMeta(ctx, p, s3name); err != nil {
		return err
	} else if meta != nil && !meta.owns(project, name) {
		return fmt.Errorf("bucket %q: the S3 name %q belongs to bucket %q of project %q; leaving it alone", name, s3name, meta.Name, meta.Project)
	}
	_ = p.DB.KVDelete(ctx, kvNS, "bucket/"+s3name)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(trashDir(p.DataRoot), 0o700); err != nil {
		return err
	}
	u := scanBucket(src)
	now := time.Now().UTC()
	e := TrashEntry{ID: ids.New("trs"), Project: project, Bucket: name, S3Name: s3name, Bytes: u.Bytes, Objects: u.Objects,
		DeletedAt: now, ExpiresAt: now.Add(TrashRetention)}
	e.Path = filepath.Join(trashDir(p.DataRoot), e.ID)
	raw, _ := json.Marshal(e)
	// Record first: a crash after the record but before the move leaves a
	// stale entry (ignored on restore), never an untracked directory.
	if err := p.DB.KVPut(ctx, kvNS, trashKey(e.ID), raw); err != nil {
		return err
	}
	if err := os.Rename(src, e.Path); err != nil {
		_ = p.DB.KVDelete(ctx, kvNS, trashKey(e.ID))
		return fmt.Errorf("move bucket %s to the trash: %w", s3name, err)
	}
	p.Log.Info("storage: bucket moved to trash", "project", project, "bucket", name, "id", e.ID, "bytes", e.Bytes)
	return nil
}

// trashEntries lists trash entries, newest first.
func trashEntries(ctx context.Context, p *platform.Platform) ([]TrashEntry, error) {
	kv, err := p.DB.KVList(ctx, kvNS)
	if err != nil {
		return nil, err
	}
	out := []TrashEntry{}
	for k, v := range kv {
		if strings.HasPrefix(k, "trash/") {
			var e TrashEntry
			if json.Unmarshal(v, &e) == nil {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out, nil
}

// restoreBucket moves the newest restorable trash entry for a bucket back.
func (m *Module) restoreBucket(ctx context.Context, p *platform.Platform, project, name string) (bool, error) {
	entries, err := trashEntries(ctx, p)
	if err != nil {
		return false, err
	}
	dst := filepath.Join(dataDir(p.DataRoot), S3Name(project, name))
	for _, e := range entries {
		if e.Project != project || e.Bucket != name || time.Now().After(e.ExpiresAt) {
			continue
		}
		if _, err := os.Stat(e.Path); err != nil {
			_ = p.DB.KVDelete(ctx, kvNS, trashKey(e.ID))
			continue
		}
		if err := os.Rename(e.Path, dst); err != nil {
			return false, fmt.Errorf("restore bucket %s from the trash: %w", e.S3Name, err)
		}
		return true, p.DB.KVDelete(ctx, kvNS, trashKey(e.ID))
	}
	return false, nil
}

// purgeTrash deletes entries deleted before cutoff, for good.
func (m *Module) purgeTrash(ctx context.Context, p *platform.Platform, cutoff time.Time) (int, error) {
	entries, err := trashEntries(ctx, p)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.DeletedAt.After(cutoff) {
			continue
		}
		if err := purgeEntry(ctx, p, e); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func purgeEntry(ctx context.Context, p *platform.Platform, e TrashEntry) error {
	// Never follow a path outside the trash, whatever the record says.
	if filepath.Dir(e.Path) != trashDir(p.DataRoot) {
		return fmt.Errorf("trash entry %s points outside the trash", e.ID)
	}
	if err := os.RemoveAll(e.Path); err != nil {
		return err
	}
	return p.DB.KVDelete(ctx, kvNS, trashKey(e.ID))
}
