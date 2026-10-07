package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/ids"
)

// memStore is an objectStore in memory.
type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts int
}

func newMem() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = append([]byte(nil), body...)
	m.puts++
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, errNoObject
	}
	return append([]byte(nil), b...), nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func (m *memStore) List(_ context.Context, prefix string, fn func(string, int64) error) error {
	m.mu.Lock()
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	m.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, int64(len(m.objs[k]))); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) count(prefix string) int {
	n := 0
	_ = m.List(context.Background(), prefix, func(string, int64) error { n++; return nil })
	return n
}

func testVault(t *testing.T, st objectStore) *vault {
	t.Helper()
	k, err := newKeys("test-box")
	if err != nil {
		t.Fatal(err)
	}
	v, err := newVault(st, "boxes/a", k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestKeysAndPassphrase(t *testing.T) {
	pass := newPassphrase()
	if !regexp.MustCompile(`^[a-z2-9]{5}(-[a-z2-9]{5}){5}$`).MatchString(pass) {
		t.Fatalf("passphrase %q", pass)
	}
	if newPassphrase() == pass {
		t.Fatal("passphrases repeat")
	}
	k, _ := newKeys("box-a")
	sealed, err := sealKeys(k, pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(k.Identity)) || bytes.Contains(sealed, k.MAC) {
		t.Fatal("the bundle holds the key in the clear")
	}
	got, err := openKeys(sealed, pass)
	if err != nil || got.Identity != k.Identity || !bytes.Equal(got.MAC, k.MAC) || got.Box != "box-a" {
		t.Fatalf("open: %v %+v", err, got)
	}
	if _, err := openKeys(sealed, "not-the-passphrase"); !errors.Is(err, errPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}

func TestSealOpen(t *testing.T) {
	v := testVault(t, newMem())
	for _, plain := range [][]byte{{}, []byte("hello, off-box world"), bytes.Repeat([]byte("compressible "), 100000), randBytes(1 << 20)} {
		ct, err := v.seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if len(plain) > 8 && bytes.Contains(ct, plain[:8]) {
			t.Fatal("ciphertext holds plaintext")
		}
		got, err := v.open(ct)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip of %d bytes: %v", len(plain), err)
		}
		if len(plain) > 0 {
			ct[len(ct)-1] ^= 1
			if _, err := v.open(ct); err == nil {
				t.Fatal("a tampered object opened")
			}
		}
	}
	// Another key cannot open it.
	ct, _ := v.seal([]byte("secret"))
	if _, err := testVault(t, newMem()).open(ct); err == nil {
		t.Fatal("another key opened it")
	}
	// Chunk IDs are keyed: the same content gets another ID under another key.
	if v.chunkID([]byte("x")) == testVault(t, newMem()).chunkID([]byte("x")) {
		t.Fatal("chunk IDs do not depend on the key")
	}
}

// writeTree makes a set directory: a 9 MiB file (three chunks), small
// files, an empty one, a nested directory, a symlink and odd modes.
func writeTree(t *testing.T, dir string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "files", "storage", "data", "shop-media", "notes"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "platform"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "valkey.rdb"), append([]byte("REDIS0011"), randBytes(9<<20)...), 0o600))
	must(os.WriteFile(filepath.Join(dir, "platform", "state.db"), bytes.Repeat([]byte("state "), 5000), 0o600))
	must(os.WriteFile(filepath.Join(dir, "files", "storage", "data", "shop-media", "notes", "a.txt"), []byte("written on box A"), 0o640))
	must(os.WriteFile(filepath.Join(dir, "files", "storage", "data", "shop-media", "empty"), nil, 0o644))
	must(os.WriteFile(filepath.Join(dir, "files", "storage", "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	must(os.Symlink("notes/a.txt", filepath.Join(dir, "files", "storage", "data", "shop-media", "link")))
	old := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	must(os.Chtimes(filepath.Join(dir, "files", "storage", "run.sh"), old, old))
}

func sameTree(t *testing.T, a, b string) {
	t.Helper()
	err := filepath.Walk(a, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(a, p)
		q := filepath.Join(b, rel)
		fb, err := os.Lstat(q)
		if err != nil {
			t.Errorf("%s missing: %v", rel, err)
			return nil
		}
		fa, _ := os.Lstat(p)
		if fa.Mode() != fb.Mode() {
			t.Errorf("%s: mode %v, want %v", rel, fb.Mode(), fa.Mode())
		}
		switch {
		case fa.Mode()&os.ModeSymlink != 0:
			la, _ := os.Readlink(p)
			lb, _ := os.Readlink(q)
			if la != lb {
				t.Errorf("%s: link %q, want %q", rel, lb, la)
			}
		case fa.Mode().IsRegular():
			ca, _ := os.ReadFile(p)
			cb, _ := os.ReadFile(q)
			if !bytes.Equal(ca, cb) {
				t.Errorf("%s: content differs", rel)
			}
			if !fa.ModTime().Equal(fb.ModTime()) {
				t.Errorf("%s: time %v, want %v", rel, fb.ModTime(), fa.ModTime())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTreeRoundTripAndDedup(t *testing.T) {
	ctx := context.Background()
	st := newMem()
	v := testVault(t, st)
	src := t.TempDir()
	writeTree(t, src)
	known := map[string]bool{}

	// 1. First upload: every chunk is new.
	rec := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	entries, err := v.putSet(ctx, rec, src, known)
	if err != nil {
		t.Fatal(err)
	}
	up := rec.Upload
	if up.Files != 5 || up.Chunks != 3+1+1+1 || up.NewChunks != 6 || up.SentBytes == 0 {
		t.Fatalf("first upload: %+v", up)
	}
	if n := st.count(v.prefix + "chunks/"); n != 6 {
		t.Fatalf("chunks stored: %d", n)
	}
	// Nothing in the bucket names or holds the content.
	for k, body := range st.objs {
		if strings.Contains(k, "a.txt") || strings.Contains(k, "shop") || bytes.Contains(body, []byte("written on box A")) || bytes.Contains(body, []byte("state state")) {
			t.Fatalf("%s leaks the content or its names", k)
		}
	}

	// 2. Download into an empty directory: the same tree.
	dst := filepath.Join(t.TempDir(), "restore")
	gs, err := v.getTree(ctx, entries, dst)
	if err != nil {
		t.Fatal(err)
	}
	if gs.Files != 5 || gs.Chunks != 6 {
		t.Fatalf("download: %+v", gs)
	}
	sameTree(t, src, dst)

	// 3. The same set again: no chunk is sent.
	rec2 := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	if _, err := v.putSet(ctx, rec2, src, known); err != nil {
		t.Fatal(err)
	}
	if u := rec2.Upload; u.NewChunks != 0 || u.Chunks != 6 || u.SentBytes != 0 {
		t.Fatalf("unchanged upload: %+v", u)
	}

	// 4. One small file changes: one chunk is sent.
	if err := os.WriteFile(filepath.Join(src, "files", "storage", "data", "shop-media", "notes", "a.txt"), []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	rec3 := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	if _, err := v.putSet(ctx, rec3, src, known); err != nil {
		t.Fatal(err)
	}
	if u := rec3.Upload; u.NewChunks != 1 {
		t.Fatalf("one file changed: %+v", u)
	}

	// 5. A fresh box (no cache) uploading the same big file with one chunk
	// changed: the file is read again, only the changed chunk is sent.
	rdb, _ := os.ReadFile(filepath.Join(src, "valkey.rdb"))
	copy(rdb[5<<20:], []byte("in the middle chunk"))
	if err := os.WriteFile(filepath.Join(src, "valkey.rdb"), rdb, 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := v.chunkIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec4 := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	if _, err := v.putSet(ctx, rec4, src, listed); err != nil {
		t.Fatal(err)
	}
	if u := rec4.Upload; u.NewChunks != 1 {
		t.Fatalf("one chunk of a big file changed: %+v", u)
	}

	// 6. Records list and read back; only part of a set can be fetched.
	idsIn, _ := v.setIDs(ctx)
	if len(idsIn) != 4 || idsIn[0] != rec.Backup.ID {
		t.Fatalf("sets: %v", idsIn)
	}
	got, err := v.getSet(ctx, rec4.Backup.ID)
	if err != nil || got.Upload.NewChunks != 1 {
		t.Fatalf("record: %v %+v", err, got)
	}
	e4, _ := v.getEntries(ctx, rec4.Backup.ID)
	part := filepath.Join(t.TempDir(), "part")
	if gs, err := v.getTree(ctx, e4, part, "files"); err != nil || gs.Files != 3 {
		t.Fatalf("files only: %+v %v", gs, err)
	}
	if _, err := os.Stat(filepath.Join(part, "valkey.rdb")); !os.IsNotExist(err) {
		t.Fatal("a part not asked for was downloaded")
	}

	// 7. A damaged or missing chunk fails the download, naming it.
	id := entries[0].Chunks
	for _, e := range entries {
		if e.Path == "platform/state.db" {
			id = e.Chunks
		}
	}
	key := v.chunkKey(id[0])
	good := st.objs[key]
	st.objs[key] = append(append([]byte(nil), good[:len(good)-1]...), good[len(good)-1]^1)
	if _, err := v.getTree(ctx, entries, filepath.Join(t.TempDir(), "bad")); err == nil || !strings.Contains(err.Error(), "cannot be decrypted") {
		t.Fatalf("damaged chunk: %v", err)
	}
	delete(st.objs, key)
	if _, err := v.getTree(ctx, entries, filepath.Join(t.TempDir(), "gone")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing chunk: %v", err)
	}
}

// A file whose content changes while its size and time stay the same (cp -p,
// rsync -t, touch -r, an archive unpacked again) is uploaded with its new
// content: size and time do not prove a file unchanged.
func TestTreeSameSizeAndTime(t *testing.T) {
	ctx := context.Background()
	v := testVault(t, newMem())
	src := t.TempDir()
	f := filepath.Join(src, "files", "data.txt")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	known := map[string]bool{}
	write("AAAA")
	if _, err := v.putSet(ctx, &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}, src, known); err != nil {
		t.Fatal(err)
	}
	write("BBBB")
	rec := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	entries, err := v.putSet(ctx, rec, src, known)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Upload.NewChunks != 1 {
		t.Fatalf("the changed file was not sent: %+v", rec.Upload)
	}
	dst := filepath.Join(t.TempDir(), "restore")
	if _, err := v.getTree(ctx, entries, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "files", "data.txt")); string(got) != "BBBB" {
		t.Fatalf("restored %q, want the new content", got)
	}
}

// idAt makes a set ID taken at t.
func idAt(t time.Time) string {
	id := []byte(ids.New("bk"))
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(t.UnixMilli())
	for i := 9; i >= 0; i-- {
		id[3+i] = alphabet[ms&31]
		ms >>= 5
	}
	return string(id)
}

func TestULIDTime(t *testing.T) {
	now := time.Now()
	got, ok := ulidTime(ids.New("bk"))
	if !ok || got.Sub(now).Abs() > time.Second {
		t.Fatalf("ulidTime: %v %v", got, ok)
	}
	at := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	if got, ok := ulidTime(idAt(at)); !ok || !got.Equal(at) {
		t.Fatalf("idAt: %v", got)
	}
	if _, ok := ulidTime("bk_short"); ok {
		t.Fatal("a bad ID parsed")
	}
}

func TestPruneRetention(t *testing.T) {
	ctx := context.Background()
	st := newMem()
	v := testVault(t, st)
	now := time.Now()
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "kept.txt"), []byte("in every set"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs := map[string][]string{}
	put := func(age time.Duration, extra string) string {
		dir := t.TempDir()
		data, _ := os.ReadFile(filepath.Join(shared, "kept.txt"))
		_ = os.WriteFile(filepath.Join(dir, "kept.txt"), data, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "own.txt"), []byte(extra), 0o600)
		rec := &offsiteSet{Backup: Backup{ID: idAt(now.Add(-age)), Status: "ok"}}
		e, err := v.putSet(ctx, rec, dir, map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		refs[rec.Backup.ID] = treeChunks(e)
		return rec.Backup.ID
	}
	old1 := put(40*24*time.Hour, "old one")
	old2 := put(35*24*time.Hour, "old two")
	recent := put(2*24*time.Hour, "recent")
	newest := put(time.Hour, "newest")
	if n := st.count(v.prefix + "chunks/"); n != 5 {
		t.Fatalf("chunks before: %d", n)
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	drop := func(id string) (bool, error) { at, _ := ulidTime(id); return at.Before(cutoff), nil }
	res, err := v.prune(ctx, drop, func(id string) ([]string, error) { return refs[id], nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Sets, ",") != old1+","+old2 || res.Chunks != 2 {
		t.Fatalf("pruned: %+v", res)
	}
	left, _ := v.setIDs(ctx)
	if strings.Join(left, ",") != recent+","+newest {
		t.Fatalf("left: %v", left)
	}
	// What the remaining sets use is all there.
	for _, id := range left {
		e, _ := v.getEntries(ctx, id)
		if _, err := v.getTree(ctx, e, filepath.Join(t.TempDir(), "x")); err != nil {
			t.Fatalf("%s after prune: %v", id, err)
		}
	}
	// The newest set is never dropped, even past the retention.
	res, err = v.prune(ctx, func(string) (bool, error) { return true, nil }, func(id string) ([]string, error) { return refs[id], nil })
	if err != nil || strings.Join(res.Sets, ",") != recent {
		t.Fatalf("drop all: %+v %v", res, err)
	}
	// A set whose chunk list cannot be read stops the prune before any chunk goes.
	before := st.count(v.prefix + "chunks/")
	if _, err := v.prune(ctx, func(string) (bool, error) { return false, nil }, func(string) ([]string, error) { return nil, errors.New("offline") }); err == nil {
		t.Fatal("prune went on without a set's chunk list")
	}
	if st.count(v.prefix+"chunks/") != before {
		t.Fatal("chunks deleted on a partial view")
	}
}

// flakyStore fails reading one key, as a store across the internet can.
type flakyStore struct {
	*memStore
	fail string
}

func (f *flakyStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == f.fail {
		return nil, errors.New("503 slow down")
	}
	return f.memStore.Get(ctx, key)
}

// A set whose record cannot be read during a prune is not taken for one
// whose Postgres backup expired: the prune stops and deletes nothing.
func TestPruneUnreadableRecord(t *testing.T) {
	ctx := context.Background()
	mem := newMem()
	st := &flakyStore{memStore: mem}
	v := testVault(t, st)
	now := time.Now()
	var setIDs []string
	for i, label := range []string{"20261001-000000F", "20261002-000000F_20261002-010000I", "20261002-000000F_20261002-020000I"} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "own.txt"), []byte(label), 0o600)
		rec := &offsiteSet{Backup: Backup{ID: idAt(now.Add(time.Duration(i-3) * time.Hour)), Status: "ok",
			Offsite: &BackupOffsiteCopy{Status: "ok", PostgresLabel: label}}}
		if _, err := v.putSet(ctx, rec, dir, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		setIDs = append(setIDs, rec.Backup.ID)
	}
	// repo2 still has every label; no local records (a restored box).
	info := []repoBackup{{Label: "20261001-000000F"}, {Label: "20261002-000000F_20261002-010000I"}, {Label: "20261002-000000F_20261002-020000I"}}
	noLocal := func(string) (*Backup, error) { return nil, os.ErrNotExist }
	refs := func(id string) ([]string, error) { e, err := v.getEntries(ctx, id); return treeChunks(e), err }
	cutoff := now.Add(-30 * 24 * time.Hour)

	st.fail = v.setKey(setIDs[0], "info")
	if _, err := v.prune(ctx, pruneDrop(ctx, v, noLocal, info, nil, cutoff), refs); err == nil {
		t.Fatal("the prune went on without the set's record")
	}
	if left, _ := v.setIDs(ctx); len(left) != 3 {
		t.Fatalf("sets deleted on a partial view: %v", left)
	}

	// Readable again: nothing is dropped (every label is in repo2); once
	// pgBackRest expires the first label, its set goes.
	st.fail = ""
	if res, err := v.prune(ctx, pruneDrop(ctx, v, noLocal, info, nil, cutoff), refs); err != nil || len(res.Sets) != 0 {
		t.Fatalf("prune: %+v %v", res, err)
	}
	if res, err := v.prune(ctx, pruneDrop(ctx, v, noLocal, info[1:], nil, cutoff), refs); err != nil || strings.Join(res.Sets, ",") != setIDs[0] {
		t.Fatalf("expired label: %+v %v", res, err)
	}
	// A local record that cannot be read is an error too.
	broken := func(string) (*Backup, error) { return nil, errors.New("database is locked") }
	if _, err := v.prune(ctx, pruneDrop(ctx, v, broken, info[2:], nil, cutoff), refs); err == nil {
		t.Fatal("the prune went on without the local record")
	}
	if left, _ := v.setIDs(ctx); len(left) != 2 {
		t.Fatalf("sets deleted on a partial view: %v", left)
	}
}

func TestProbe(t *testing.T) {
	st := newMem()
	steps, err := probe(context.Background(), st, "boxes/a")
	if err != nil || len(steps) != 3 || !steps[0].OK || !steps[2].OK {
		t.Fatalf("probe: %v %+v", err, steps)
	}
	if len(st.objs) != 0 {
		t.Fatalf("probe left %d objects", len(st.objs))
	}
	if st.puts != 1 || !strings.HasPrefix(steps[0].Name, "write boxes/a/tiffin/probe-") {
		t.Fatalf("probe wrote %d (%s)", st.puts, steps[0].Name)
	}
}
