package storage

// Renames, moves and deletes from the console (storage-objects-move,
// storage-objects-delete) work on the files the gateway keeps on disk: one
// file per object, its type and ETag in extended attributes that a rename
// keeps. So moving a folder of 10,000 files is as quick as one, and Undo
// needs no copies. A delete moves the files into the trash
// (trash/storage/held/<undo id>) for an hour; undo moves them back if
// nothing has taken their place since. Undo records live in the box's
// state, so they survive a restart; storage-undo uses each once.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
)

const (
	// holdKeep is how long deleted files can be brought back.
	holdKeep = time.Hour
	// maxFileOp is the most files one move or delete touches.
	maxFileOp = 50_000
)

// FilesResult is what a move, delete or undo did.
type FilesResult struct {
	Files int      `json:"files" doc:"How many files it moved, deleted or put back"`
	Bytes int64    `json:"bytes"`
	Keys  []string `json:"keys" doc:"The keys it touched, the first 100 (moves: where they are now)"`
	Undo  string   `json:"undo,omitempty" doc:"Pass to storage-undo within an hour to put things back as they were"`
}

// fileMove is one file of an undo record: where it was, where it went
// ("" while held), and its size and modification time afterwards, to tell
// whether anything has written there since.
type fileMove struct {
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

// undoRecord is a move, delete or restore that can be undone.
type undoRecord struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"` // move, delete, restore
	Project string     `json:"project"`
	Bucket  string     `json:"bucket"`
	At      time.Time  `json:"at"`
	Files   []fileMove `json:"files"`
}

func undoKey(id string) string { return "undo/" + id }

// heldDir is where a delete keeps its files.
func heldDir(root, id string) string { return filepath.Join(trashDir(root), "held", id) }

func newFilesUndoID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "stu_" + hex.EncodeToString(b[:])
}

// checkKey refuses keys that are not one plain path inside a bucket.
func checkKey(key string) error {
	if key == "" || len(key) > 1024 || strings.HasSuffix(key, "/") || strings.HasPrefix(key, "/") || strings.Contains(key, "//") ||
		strings.Contains("/"+key+"/", "/../") || strings.Contains("/"+key+"/", "/./") || strings.ContainsRune(key, 0) || strings.HasPrefix(key, ".sgwtmp/") {
		return api.NewProblem(422, "validation", fmt.Sprintf("%q is not a file name: it must not start or end with /, or contain empty, . or .. parts", key))
	}
	return nil
}

// objectPathOn is key's file in a bucket directory.
func objectPathOn(dir, key string) string { return filepath.Join(dir, filepath.FromSlash(key)) }

// stamp is a file's size and modification time.
func stamp(path string) (int64, int64, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, 0, false
	}
	return fi.Size(), fi.ModTime().UnixNano(), true
}

// filesUnder lists the files of a bucket directory whose keys start with
// prefix (sorted), with their sizes. It stops after limit+1.
func filesUnder(dir, prefix string, limit int) ([]fileMove, error) {
	start := dir
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		start = objectPathOn(dir, prefix[:i])
	}
	var out []fileMove
	err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, dir+string(filepath.Separator)))
		if d.IsDir() {
			if path != start && (rel == ".sgwtmp" || !strings.HasPrefix(rel+"/", prefix) && !strings.HasPrefix(prefix, rel+"/")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasPrefix(rel, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, fileMove{From: rel, Size: info.Size()})
		if len(out) > limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out, err
}

// mkdirLike makes dir and any missing parents below root with root's
// owner and mode, so the gateway (its own user) can write and delete there.
func mkdirLike(root, dir string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	var missing []string
	for d := dir; d != root && len(d) > len(root); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append(missing, d)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], fi.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
			if err := os.Lchown(missing[i], int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
	}
	return nil
}

// pruneDirs removes the directories a file was in, up to root, while they
// are empty. A folder object (a directory with its own metadata) stays.
func pruneDirs(root, dir string) {
	for d := dir; d != root && len(d) > len(root); d = filepath.Dir(d) {
		if hasXattrs(d) || os.Remove(d) != nil {
			return
		}
	}
}

// moveFile renames one object file, making the parents it needs.
func moveFile(fromRoot, from, toRoot, to string) error {
	if err := mkdirLike(toRoot, filepath.Dir(to)); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	pruneDirs(fromRoot, filepath.Dir(from))
	return nil
}

func conflict(detail, hint string) error {
	pr := api.NewProblem(409, "conflict", detail)
	pr.Hint = hint
	return pr
}

// relocate moves files (From → To within the bucket, or From into the
// held directory when To is ""), all or none: a failure halfway moves the
// done ones back. It fills in each file's stamp afterwards.
func relocate(dir, held string, files []fileMove) error {
	done := 0
	undo := func() {
		for i := done - 1; i >= 0; i-- {
			f := files[i]
			src := objectPathOn(dir, f.To)
			root := dir
			if f.To == "" {
				src, root = objectPathOn(held, f.From), held
			}
			_ = moveFile(root, src, dir, objectPathOn(dir, f.From))
		}
	}
	for i := range files {
		f := &files[i]
		dst, root := objectPathOn(dir, f.To), dir
		if f.To == "" {
			dst, root = objectPathOn(held, f.From), held
		}
		if err := moveFile(dir, objectPathOn(dir, f.From), root, dst); err != nil {
			undo()
			return fmt.Errorf("move %s: %w", f.From, err)
		}
		f.Size, f.MTime, _ = stamp(dst)
		done++
	}
	return nil
}

// filesBucket is a converged bucket's directory, for the file operations.
func (m *Module) filesBucket(ctx context.Context, p *platform.Platform, project, bucket string) (string, *bucketMeta, error) {
	s3name, meta, err := m.readyBucket(ctx, p, project, bucket)
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(dataDir(p.DataRoot), s3name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", nil, conflict("bucket "+bucket+" has no files on the box yet", "")
	}
	return dir, meta, nil
}

// pick resolves keys and prefixes to the files they name.
func pick(dir string, keys []string, prefix string) ([]fileMove, error) {
	var files []fileMove
	seen := map[string]bool{}
	for _, k := range keys {
		if err := checkKey(k); err != nil {
			return nil, err
		}
		size, _, ok := stamp(objectPathOn(dir, k))
		if !ok {
			return nil, api.NewProblem(404, "not_found", "no file "+k)
		}
		if !seen[k] {
			seen[k] = true
			files = append(files, fileMove{From: k, Size: size})
		}
	}
	if prefix != "" {
		under, err := filesUnder(dir, prefix, maxFileOp)
		if err != nil {
			return nil, err
		}
		for _, f := range under {
			if !seen[f.From] {
				seen[f.From] = true
				files = append(files, f)
			}
		}
	}
	if len(files) > maxFileOp {
		return nil, api.NewProblem(422, "validation", fmt.Sprintf("that is more than %d files; do it in parts, a folder at a time", maxFileOp))
	}
	return files, nil
}

// moveObjects renames files: one key to a new key, keys into the folder
// to (ending in /) under their own names, or everything under prefix to
// under to instead (a folder renamed or moved). Nothing is overwritten.
func (m *Module) moveObjects(ctx context.Context, p *platform.Platform, project, bucket string, keys []string, prefix, to string) (*FilesResult, error) {
	dir, _, err := m.filesBucket(ctx, p, project, bucket)
	if err != nil {
		return nil, err
	}
	folder := strings.HasSuffix(to, "/") || to == ""
	switch {
	case len(keys) > 0 && prefix != "":
		return nil, api.NewProblem(422, "validation", "send keys or a prefix, not both")
	case prefix != "" && (!strings.HasSuffix(prefix, "/") || !folder):
		return nil, api.NewProblem(422, "validation", "moving a folder: prefix and to both end with /")
	case prefix != "" && strings.HasPrefix(to, prefix):
		return nil, api.NewProblem(422, "validation", "a folder can't move into itself")
	case !folder && len(keys) != 1:
		return nil, api.NewProblem(422, "validation", "to names one file; to move several, end it with / (a folder)")
	}
	m.filesMu.Lock()
	defer m.filesMu.Unlock()
	files, err := pick(dir, keys, prefix)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, api.NewProblem(404, "not_found", "nothing to move: no files under "+prefix)
	}
	dests := map[string]bool{}
	var moves []fileMove
	for _, f := range files {
		dst := to
		switch {
		case prefix != "":
			dst = to + strings.TrimPrefix(f.From, prefix)
		case folder:
			dst = to + f.From[strings.LastIndex(f.From, "/")+1:]
		}
		if dst == f.From {
			continue
		}
		if err := checkKey(dst); err != nil {
			return nil, err
		}
		if dests[dst] {
			return nil, conflict("two files would both become "+dst, "move them one at a time")
		}
		dests[dst] = true
		if _, err := os.Lstat(objectPathOn(dir, dst)); err == nil {
			return nil, conflict("there is already a file or folder at "+dst, "pick another name, or delete that one first")
		}
		moves = append(moves, fileMove{From: f.From, To: dst})
	}
	if len(moves) == 0 {
		return &FilesResult{Keys: []string{}}, nil
	}
	if err := relocate(dir, "", moves); err != nil {
		return nil, err
	}
	return m.record(ctx, p, &undoRecord{Kind: "move", Project: project, Bucket: bucket, Files: moves})
}

// deleteObjects holds files for an hour, then they are gone for good.
func (m *Module) deleteObjects(ctx context.Context, p *platform.Platform, project, bucket string, keys []string, prefix string) (*FilesResult, error) {
	dir, _, err := m.filesBucket(ctx, p, project, bucket)
	if err != nil {
		return nil, err
	}
	m.filesMu.Lock()
	defer m.filesMu.Unlock()
	files, err := pick(dir, keys, prefix)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, api.NewProblem(404, "not_found", "nothing to delete: no files under "+prefix)
	}
	rec := &undoRecord{ID: newFilesUndoID(), Kind: "delete", Project: project, Bucket: bucket, Files: files}
	held := heldDir(p.DataRoot, rec.ID)
	if err := os.MkdirAll(held, 0o700); err != nil {
		return nil, err
	}
	if err := relocate(dir, held, rec.Files); err != nil {
		_ = os.RemoveAll(held)
		return nil, err
	}
	m.tracker().invalidate()
	return m.record(ctx, p, rec)
}

// record stores an undo record and says what happened.
func (m *Module) record(ctx context.Context, p *platform.Platform, rec *undoRecord) (*FilesResult, error) {
	if rec.ID == "" {
		rec.ID = newFilesUndoID()
	}
	rec.At = time.Now().UTC()
	out := &FilesResult{Files: len(rec.Files), Keys: []string{}, Undo: rec.ID}
	for _, f := range rec.Files {
		out.Bytes += f.Size
		if len(out.Keys) < 100 {
			k := f.From
			if f.To != "" {
				k = f.To
			}
			out.Keys = append(out.Keys, k)
		}
	}
	raw, _ := json.Marshal(rec)
	if err := p.DB.KVPut(ctx, kvNS, undoKey(rec.ID), raw); err != nil {
		return nil, err
	}
	return out, nil
}

// undoFiles reverses a move, delete or restore, once, if nothing has
// written to its files since. Undoing a delete is a restore, and undoing
// that deletes again, so every undo can be redone.
func (m *Module) undoFiles(ctx context.Context, p *platform.Platform, project, id string) (*FilesResult, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, undoKey(id))
	if err != nil {
		return nil, err
	}
	var rec undoRecord
	if !ok || json.Unmarshal(raw, &rec) != nil || rec.Project != project || time.Since(rec.At) > holdKeep {
		pr := api.NewProblem(404, "not_found", "there is nothing to undo with "+id)
		pr.Hint = "a change to files can be undone for an hour, once"
		return nil, pr
	}
	dir, _, err := m.filesBucket(ctx, p, project, rec.Bucket)
	if err != nil {
		return nil, err
	}
	m.filesMu.Lock()
	defer m.filesMu.Unlock()
	held := heldDir(p.DataRoot, rec.ID)
	// Every file must be where the change left it, unchanged, and (but for
	// a restore) its old place still free.
	for _, f := range rec.Files {
		name, path := f.From, objectPathOn(dir, f.From)
		switch rec.Kind {
		case "move":
			name, path = f.To, objectPathOn(dir, f.To)
		case "delete":
			path = objectPathOn(held, f.From)
		}
		if size, mtime, ok := stamp(path); !ok || size != f.Size || mtime != f.MTime {
			return nil, conflict(name+" changed after that, so Undo would lose the newer file", "change it by hand")
		}
		if rec.Kind != "restore" {
			if _, err := os.Lstat(objectPathOn(dir, f.From)); err == nil {
				return nil, conflict("there is a new file at "+f.From+" now", "move or delete it, then undo again")
			}
		}
	}
	next := &undoRecord{ID: newFilesUndoID(), Project: project, Bucket: rec.Bucket}
	switch rec.Kind {
	case "move":
		next.Kind = "move"
		for _, f := range rec.Files {
			next.Files = append(next.Files, fileMove{From: f.To, To: f.From})
		}
		err = relocate(dir, "", next.Files)
	case "delete":
		next.Kind = "restore"
		for _, f := range rec.Files {
			dst := objectPathOn(dir, f.From)
			if err = moveFile(held, objectPathOn(held, f.From), dir, dst); err != nil {
				break
			}
			size, mtime, _ := stamp(dst)
			next.Files = append(next.Files, fileMove{From: f.From, Size: size, MTime: mtime})
		}
		if err == nil {
			_ = os.RemoveAll(held)
		}
	case "restore":
		next.Kind = "delete"
		for _, f := range rec.Files {
			next.Files = append(next.Files, fileMove{From: f.From})
		}
		if err = os.MkdirAll(heldDir(p.DataRoot, next.ID), 0o700); err == nil {
			err = relocate(dir, heldDir(p.DataRoot, next.ID), next.Files)
		}
	}
	if err != nil {
		return nil, err
	}
	_ = p.DB.KVDelete(ctx, kvNS, undoKey(id))
	m.tracker().invalidate()
	return m.record(ctx, p, next)
}

// purgeHeld drops undo records older than holdKeep, and the files deletes
// were holding: those are gone for good now.
func purgeHeld(ctx context.Context, p *platform.Platform, now time.Time) (int, error) {
	kv, err := p.DB.KVList(ctx, kvNS)
	if err != nil {
		return 0, err
	}
	n := 0
	for k, v := range kv {
		id, ok := strings.CutPrefix(k, "undo/")
		if !ok {
			continue
		}
		var rec undoRecord
		if json.Unmarshal(v, &rec) == nil && now.Sub(rec.At) <= holdKeep {
			continue
		}
		if rec.Kind == "delete" && strings.HasPrefix(id, "stu_") {
			if err := os.RemoveAll(heldDir(p.DataRoot, id)); err != nil {
				return n, err
			}
		}
		_ = p.DB.KVDelete(ctx, kvNS, k)
		n++
	}
	return n, nil
}
