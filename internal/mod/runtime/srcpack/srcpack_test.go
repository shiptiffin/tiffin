package srcpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestMatcher(t *testing.T) {
	m := NewMatcher()
	m.Add("", "*.log")
	m.Add("", "/build")
	m.Add("", "!keep.log")
	m.Add("", "docs/**/*.tmp")
	m.Add("web", "out/")
	cases := []struct {
		path  string
		dir   bool
		want  bool
		label string
	}{
		{"node_modules", true, true, "default dir"},
		{"a/b/node_modules", true, true, "default dir at depth"},
		{"node_modules", false, false, "dir-only rule ignores files of that name"},
		{".env", false, true, "local secrets"},
		{".env.production.local", false, true, "local secrets variant"},
		{".env.production", false, false, "committed env files stay"},
		{"server.log", false, true, "glob"},
		{"x/y/server.log", false, true, "glob at depth"},
		{"keep.log", false, false, "negation"},
		{"build", true, true, "anchored"},
		{"src/build", true, false, "anchored only at root"},
		{"docs/a/b/c.tmp", false, true, "double star"},
		{"docs/c.tmp", false, true, "double star matches zero dirs"},
		{"web/out", true, true, "nested base"},
		{"out", true, false, "nested base only below its dir"},
		{"index.ts", false, false, "plain file"},
	}
	for _, c := range cases {
		if got := m.Ignored(c.path, c.dir); got != c.want {
			t.Errorf("%s: Ignored(%q) = %v, want %v", c.label, c.path, got, c.want)
		}
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestPackExtractRoundTrip(t *testing.T) {
	src := t.TempDir()
	write(t, src, "package.json", `{"name":"x"}`)
	write(t, src, "index.ts", "export default {}")
	write(t, src, "node_modules/hono/index.js", "x")
	write(t, src, ".git/HEAD", "ref")
	write(t, src, ".env", "SECRET=1")
	write(t, src, ".gitignore", "dist/\n*.log\n")
	write(t, src, "dist/out.js", "built")
	write(t, src, "debug.log", "noise")
	write(t, src, "web/.gitignore", "cache\n")
	write(t, src, "web/cache", "c")
	write(t, src, "web/page.tsx", "p")
	write(t, src, ".tiffinignore", "secret-notes.md\n")
	write(t, src, "secret-notes.md", "s")
	if err := os.Symlink("index.ts", filepath.Join(src, "main.ts")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st, err := Pack(src, &buf)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Extract(&buf, dst, Limits{}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(list(t, dst), ",")
	want := ".gitignore,.tiffinignore,index.ts,main.ts,package.json,web/.gitignore,web/page.tsx"
	if got != want {
		t.Fatalf("files:\n got %s\nwant %s", got, want)
	}
	if st.Files != 7 {
		t.Errorf("stats %+v", st)
	}
	if l, _ := os.Readlink(filepath.Join(dst, "main.ts")); l != "index.ts" {
		t.Errorf("symlink = %q", l)
	}
}

type entry struct {
	h    tar.Header
	body string
}

func archive(t *testing.T, es ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := e.h
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

func TestExtractRefusesEscapes(t *testing.T) {
	cases := map[string][]entry{
		"dotdot":        {{h: tar.Header{Typeflag: tar.TypeReg, Name: "../evil"}, body: "x"}},
		"absolute":      {{h: tar.Header{Typeflag: tar.TypeReg, Name: "/etc/evil"}, body: "x"}},
		"symlink out":   {{h: tar.Header{Typeflag: tar.TypeSymlink, Name: "l", Linkname: "../../etc"}}},
		"symlink abs":   {{h: tar.Header{Typeflag: tar.TypeSymlink, Name: "l", Linkname: "/etc"}}},
		"write through": {{h: tar.Header{Typeflag: tar.TypeSymlink, Name: "d", Linkname: "."}}, {h: tar.Header{Typeflag: tar.TypeReg, Name: "d/x"}, body: "x"}},
	}
	for name, es := range cases {
		dst := t.TempDir()
		_, err := Extract(archive(t, es...), dst, Limits{})
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("%s: err = %v, want ErrUnsafe", name, err)
		}
	}
}

// Archives that try to reach a sentinel next to the destination through
// symlink chains or entries that reuse a link's path. Each must fail and
// leave the sentinel and no escaping link behind.
func TestExtractSymlinkChains(t *testing.T) {
	link := func(name, to string) entry {
		return entry{h: tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: to}}
	}
	file := func(name string) entry {
		return entry{h: tar.Header{Typeflag: tar.TypeReg, Name: name}, body: "CHANGED"}
	}
	cases := map[string][]entry{
		"chain":             {link("t", "."), link("s", "t/t/.."), file("s/outside.txt")},
		"chain only":        {link("t", "."), link("s", "t/t/../outside.txt")},
		"chain, link first": {link("s", "t/t/../outside.txt"), link("t", ".")},
		"file over link":    {link("a", "."), link("b", "a/../outside.txt"), file("b")},
		"link twice":        {link("b", "x"), link("b", "a/../outside.txt")},
		"dir over link":     {link("d", "."), {h: tar.Header{Typeflag: tar.TypeDir, Name: "d/"}}},
	}
	for name, es := range cases {
		parent := t.TempDir()
		sentinel := filepath.Join(parent, "outside.txt")
		write(t, parent, "outside.txt", "SAFE")
		dst := filepath.Join(parent, "src")
		_, err := Extract(archive(t, es...), dst, Limits{})
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("%s: err = %v, want ErrUnsafe", name, err)
		}
		if b, _ := os.ReadFile(sentinel); string(b) != "SAFE" {
			t.Errorf("%s: sentinel outside the root = %q", name, b)
		}
		real, _ := filepath.EvalSymlinks(dst)
		_ = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type()&fs.ModeSymlink != 0 {
				if r, err := filepath.EvalSymlinks(p); err == nil && r != real && !strings.HasPrefix(r, real+"/") {
					t.Errorf("%s: link %s left behind, resolves to %s", name, p, r)
				}
			}
			return nil
		})
	}
	// A link over an earlier file or directory is a plain error.
	for name, es := range map[string][]entry{
		"link over file": {file("f"), link("f", "g")},
		"link over dir":  {file("d/x"), link("d", ".")},
	} {
		if _, err := Extract(archive(t, es...), t.TempDir(), Limits{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// Ordinary relative and dangling links still extract.
	dst := t.TempDir()
	if _, err := Extract(archive(t, file("a/c/x"), link("a/b/l", "../c"), link("d", "missing")), dst, Limits{}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a/b/l/x")); err != nil || string(b) != "CHANGED" {
		t.Fatalf("relative link: %q %v", b, err)
	}
}

func TestExtractLimits(t *testing.T) {
	big := strings.Repeat("a", 1000)
	_, err := Extract(archive(t, entry{h: tar.Header{Typeflag: tar.TypeReg, Name: "a"}, body: big}), t.TempDir(), Limits{MaxBytes: 10, MaxFiles: 10})
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("size limit: %v", err)
	}
	_, err = Extract(archive(t,
		entry{h: tar.Header{Typeflag: tar.TypeReg, Name: "a"}, body: "1"},
		entry{h: tar.Header{Typeflag: tar.TypeReg, Name: "b"}, body: "2"}), t.TempDir(), Limits{MaxBytes: 100, MaxFiles: 1})
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("file limit: %v", err)
	}
	if _, err := Extract(strings.NewReader("not gzip"), t.TempDir(), Limits{}); err == nil {
		t.Fatal("garbage must fail")
	}
}
