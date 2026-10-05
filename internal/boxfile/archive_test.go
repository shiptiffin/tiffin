package boxfile

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func testManifest() *Manifest {
	return &Manifest{Kind: ManifestKind, Format: FormatVersion, TiffinVersion: "0.3.0", Schema: 3, CreatedAt: time.Now().UTC(),
		Projects: []string{"shop"}, Recipient: "age1test"}
}

// buildTree makes a small directory with every kind of entry WriteTree keeps.
func buildTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tree")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "data", "shop-media", "nested"), 0o750))
	must(os.WriteFile(filepath.Join(root, "data", "shop-media", "nested", "a.txt"), []byte("hello object"), 0o640))
	must(os.WriteFile(filepath.Join(root, "root.env"), []byte("ROOT_ACCESS_KEY=x\n"), 0o600))
	must(os.WriteFile(filepath.Join(root, "empty"), nil, 0o644))
	must(os.WriteFile(filepath.Join(root, "data", "photo@000001"), []byte("an object key with an at sign"), 0o644))
	must(os.Symlink("/var/lib/tiffin/runtime/static/shop/web/dep_1", filepath.Join(root, "live-prod")))
	must(os.Symlink("data/shop-media", filepath.Join(root, "rel-link")))
	must(os.WriteFile(filepath.Join(root, "skip.mmdb"), []byte("geo"), 0o644))
	must(os.WriteFile(filepath.Join(root, "app.db"), []byte("live, inconsistent"), 0o600))
	must(os.WriteFile(filepath.Join(root, "app.db-wal"), []byte("wal"), 0o600))
	return root
}

func writeArchive(t *testing.T, tree string, stream []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, testManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteJSON("postgres/meta.json", map[string]any{"roles": []string{"p_shop"}}); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "app.db.snapshot")
	if err := os.WriteFile(snap, []byte("consistent snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := w.WriteTree("files/storage", tree, TreeOptions{
		Skip:    func(rel string, _ fs.DirEntry) bool { return strings.HasSuffix(rel, ".mmdb") || rel == "app.db-wal" },
		Replace: map[string]string{"app.db": snap},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Part("storage", st)
	if _, err := w.WriteStream("postgres/db/p_shop.sql", bytes.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteStream("runtime/images.tar", bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	size, sum, tr, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(buf.Len()) {
		t.Fatalf("size %d, buffer %d", size, buf.Len())
	}
	h := sha256.Sum256(buf.Bytes())
	if sum != hex.EncodeToString(h[:]) {
		t.Fatal("sha256 of the written bytes differs")
	}
	if tr.Parts["storage"].Files != 5 { // a.txt, photo@000001, root.env, empty, app.db (+ no wal, no mmdb)
		t.Fatalf("storage files: %+v", tr.Parts["storage"])
	}
	return buf.Bytes(), sum
}

func TestRoundTrip(t *testing.T) {
	old := chunkSize
	chunkSize = 1000 // force several chunks
	defer func() { chunkSize = old }()
	tree := buildTree(t)
	stream := make([]byte, 3500)
	_, _ = rand.Read(stream)
	raw, _ := writeArchive(t, tree, stream)

	ar, err := NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	if ar.Manifest.Projects[0] != "shop" || ar.Manifest.Recipient != "age1test" {
		t.Fatalf("manifest: %+v", ar.Manifest)
	}
	dest := filepath.Join(t.TempDir(), "out")
	x, err := NewExtractor(dest)
	if err != nil {
		t.Fatal(err)
	}
	var gotStream, gotImages []byte
	sawImages := false
	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(e.Name, "files/storage"):
			rel := strings.TrimPrefix(strings.TrimPrefix(e.Name, "files/storage"), "/")
			if err := x.Add(rel, e.Header, e.Body); err != nil {
				t.Fatalf("%s: %v", e.Name, err)
			}
		case e.Name == "postgres/db/p_shop.sql":
			if !e.Stream {
				t.Fatal("dump is not a stream")
			}
			gotStream, _ = io.ReadAll(e.Body)
		case e.Name == "runtime/images.tar":
			sawImages = true
			gotImages, _ = io.ReadAll(e.Body)
		case e.Name == "postgres/meta.json":
			// left unread on purpose: Next drains it
		default:
			t.Fatalf("unexpected entry %s", e.Name)
		}
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if ar.Trailer == nil || ar.Trailer.Entries == 0 {
		t.Fatal("trailer not verified")
	}
	if !bytes.Equal(gotStream, stream) {
		t.Fatalf("stream: got %d bytes, want %d", len(gotStream), len(stream))
	}
	if !sawImages || len(gotImages) != 0 {
		t.Fatalf("empty stream: saw %v, %d bytes", sawImages, len(gotImages))
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if read("data/shop-media/nested/a.txt") != "hello object" || read("root.env") != "ROOT_ACCESS_KEY=x\n" || read("empty") != "" {
		t.Fatal("file contents")
	}
	if read("data/photo@000001") != "an object key with an at sign" {
		t.Fatal("a file named like a chunk")
	}
	if read("app.db") != "consistent snapshot" {
		t.Fatal("Replace must take the snapshot's content")
	}
	for _, gone := range []string{"skip.mmdb", "app.db-wal"} {
		if _, err := os.Lstat(filepath.Join(dest, gone)); err == nil {
			t.Fatalf("%s should have been skipped", gone)
		}
	}
	if l, _ := os.Readlink(filepath.Join(dest, "live-prod")); l != "/var/lib/tiffin/runtime/static/shop/web/dep_1" {
		t.Fatalf("absolute symlink: %q", l)
	}
	if l, _ := os.Readlink(filepath.Join(dest, "rel-link")); l != "data/shop-media" {
		t.Fatalf("relative symlink: %q", l)
	}
	if fi, _ := os.Stat(filepath.Join(dest, "root.env")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dest, "data")); fi.Mode().Perm() != 0o750 {
		t.Fatalf("dir mode: %v", fi.Mode())
	}
	src, _ := os.Stat(filepath.Join(tree, "data", "shop-media", "nested", "a.txt"))
	got, _ := os.Stat(filepath.Join(dest, "data", "shop-media", "nested", "a.txt"))
	if !src.ModTime().Truncate(time.Second).Equal(got.ModTime().Truncate(time.Second)) {
		t.Fatalf("mtime: %v vs %v", src.ModTime(), got.ModTime())
	}
	if m, tr, err := Verify(bytes.NewReader(raw)); err != nil || m.Kind != ManifestKind || tr.Digest == "" {
		t.Fatalf("verify: %v", err)
	}
}

// recompress decompresses an archive, lets f edit the tar bytes and
// compresses it again (valid zstd, altered content).
func recompress(t *testing.T, raw []byte, f func([]byte) []byte) []byte {
	t.Helper()
	zr, _ := zstd.NewReader(bytes.NewReader(raw))
	plain, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		t.Fatal(err)
	}
	plain = f(plain)
	var out bytes.Buffer
	zw, _ := zstd.NewWriter(&out)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	return out.Bytes()
}

func TestVerifyCatchesDamage(t *testing.T) {
	tree := buildTree(t)
	raw, _ := writeArchive(t, tree, []byte("CREATE TABLE t(); COPY t FROM stdin;\n"))

	// Truncated download.
	if _, _, err := Verify(bytes.NewReader(raw[:len(raw)*2/3])); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: %v", err)
	}
	// A flipped byte in the compressed stream (zstd checksum or digest).
	bad := append([]byte(nil), raw...)
	bad[len(bad)/2] ^= 0xff
	if _, _, err := Verify(bytes.NewReader(bad)); err == nil {
		t.Fatal("flipped byte accepted")
	}
	// Content changed and recompressed: only the digest can tell.
	tampered := recompress(t, raw, func(b []byte) []byte {
		return bytes.Replace(b, []byte("hello object"), []byte("HELLO OBJECT"), 1)
	})
	if _, _, err := Verify(bytes.NewReader(tampered)); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered: %v", err)
	}
	// Not an archive at all.
	if _, _, err := Verify(strings.NewReader("hello")); err == nil || errors.Is(err, ErrCorrupt) {
		t.Fatalf("garbage: %v", err)
	}
	// A zstd tar whose first entry is not the manifest.
	var buf bytes.Buffer
	zw, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: "x", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = zw.Close()
	if _, err := NewReader(&buf); err == nil || !strings.Contains(err.Error(), "first entry") {
		t.Fatalf("foreign tar: %v", err)
	}
}

func TestUnsafeNames(t *testing.T) {
	for _, n := range []string{"/etc/passwd", "files/../../etc", "a//b", "..", "files\\x"} {
		if safeName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
	for _, n := range []string{"files/storage/", "postgres/db/p_a.sql@000001", "files/storage/a..b"} {
		if err := safeName(n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
}

func TestExtractorStaysInside(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	outside := t.TempDir()
	x, err := NewExtractor(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	// A symlink pointing out, then a file "through" it: must not land outside.
	if err := x.Add("esc", &tar.Header{Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777}, nil); err != nil {
		t.Fatal(err)
	}
	err = x.Add("esc/pwned", &tar.Header{Typeflag: tar.TypeReg, Size: 1, Mode: 0o644}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("wrote through a symlink that leaves the destination")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatal("file escaped the destination")
	}
}

func TestCheckCompatible(t *testing.T) {
	m := testManifest()
	if err := CheckCompatible(m, 3, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompatible(m, 5, "dev"); err != nil {
		t.Fatalf("older archive, dev box: %v", err)
	}
	newer := *m
	newer.Format = FormatVersion + 1
	if err := CheckCompatible(&newer, 3, "0.3.0"); err == nil || !strings.Contains(err.Error(), "newer Tiffin") {
		t.Fatalf("newer format: %v", err)
	}
	newer = *m
	newer.Schema = 4
	if err := CheckCompatible(&newer, 3, "0.3.0"); err == nil || !strings.Contains(err.Error(), "state schema") {
		t.Fatalf("newer schema: %v", err)
	}
	newer = *m
	newer.TiffinVersion = "v0.4.0-3-gabc"
	if err := CheckCompatible(&newer, 3, "0.3.9"); err == nil || !strings.Contains(err.Error(), "0.4.0") {
		t.Fatalf("newer release: %v", err)
	}
	if err := CheckCompatible(&newer, 3, "dev"); err != nil {
		t.Fatalf("dev box only checks the schema: %v", err)
	}
	other := *m
	other.Kind = "something"
	if err := CheckCompatible(&other, 3, "0.3.0"); err == nil {
		t.Fatal("wrong kind accepted")
	}
}

func TestChunkNames(t *testing.T) {
	for in, want := range map[string]struct {
		base string
		n    int
		ok   bool
	}{
		"postgres/db/p_a.sql@000000": {"postgres/db/p_a.sql", 0, true},
		"runtime/images.tar@000012":  {"runtime/images.tar", 12, true},
		"files/storage/me@x.com":     {"", 0, false},
		"files/storage/a@12":         {"", 0, false},
	} {
		b, n, ok := chunkName(in)
		if b != want.base || n != want.n || ok != want.ok {
			t.Errorf("chunkName(%q) = %q %d %v", in, b, n, ok)
		}
	}
}

// TestReadHead: the first megabyte of a large archive holds the manifest
// and the next entry, so an import can be checked before the upload.
func TestReadHead(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, testManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteJSON("project.json", map[string]string{"project": "shop"}); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 16<<20)
	_, _ = rand.Read(big)
	if _, err := w.WriteStream("blob", bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ar, err := NewReader(bytes.NewReader(buf.Bytes()[:1<<20]))
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	e, err := ar.Next()
	if err != nil || e.Name != "project.json" {
		t.Fatalf("next: %v %+v", err, e)
	}
	if b, err := io.ReadAll(e.Body); err != nil || !strings.Contains(string(b), "shop") {
		t.Fatalf("project.json: %v %q", err, b)
	}
}
