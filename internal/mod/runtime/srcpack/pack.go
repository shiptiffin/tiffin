package srcpack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Stats describes a packed archive.
type Stats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"` // uncompressed file bytes
}

// IgnoreFiles are read in every directory, in this order.
var IgnoreFiles = []string{".gitignore", ".tiffinignore"}

// Pack writes dir as a gzipped tar to w. Paths in the archive are relative
// to dir. Ignored paths (DefaultIgnore, .gitignore and .tiffinignore files at
// any depth) are skipped, as are sockets and devices. Symlinks are stored as
// symlinks; the box refuses ones that point outside the archive.
func Pack(dir string, w io.Writer) (Stats, error) {
	var st Stats
	root, err := filepath.Abs(dir)
	if err != nil {
		return st, err
	}
	fi, err := os.Stat(root)
	if err != nil {
		return st, err
	}
	if !fi.IsDir() {
		return st, fmt.Errorf("%s is not a directory", dir)
	}
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	m := NewMatcher()
	// Fixed times keep archives of identical trees identical.
	epoch := time.Unix(0, 0)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return addIgnoreFiles(m, "", p)
		}
		if m.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := addIgnoreFiles(m, rel, p); err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: rel + "/", Mode: 0o755, ModTime: epoch, Format: tar.FormatPAX})
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			st.Files++
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: rel, Linkname: filepath.ToSlash(target), Mode: 0o777, ModTime: epoch, Format: tar.FormatPAX})
		case info.Mode().IsRegular():
			mode := int64(0o644)
			if info.Mode()&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: rel, Size: info.Size(), Mode: mode, ModTime: epoch, Format: tar.FormatPAX}); err != nil {
				return err
			}
			n, err := io.Copy(tw, f)
			st.Files++
			st.Bytes += n
			return err
		default:
			return nil // sockets, devices, pipes
		}
	})
	if err != nil {
		return st, err
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	return st, gz.Close()
}

func addIgnoreFiles(m *Matcher, rel, dir string) error {
	for _, n := range IgnoreFiles {
		err := m.AddFile(rel, filepath.Join(dir, n))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Limits bound what Extract accepts.
type Limits struct {
	MaxBytes int64 // total uncompressed file bytes
	MaxFiles int
	// MaxEntries bounds everything Extract creates or reads past: files,
	// links, folders (named or implied by a path) and skipped entries.
	// 0: twice MaxFiles.
	MaxEntries int
}

// DefaultLimits are generous for app sources (images and fonts included).
var DefaultLimits = Limits{MaxBytes: 4 << 30, MaxFiles: 200_000, MaxEntries: 300_000}

// maxName is the longest path Extract takes (Linux's PATH_MAX).
const maxName = 4096

// ErrUnsafe is returned for archives with paths or links that escape the
// destination, or that exceed the limits.
var ErrUnsafe = errors.New("unsafe archive")

// Extract unpacks a gzipped tar into dir (created). Every write goes through
// an os.Root, so no entry or symlink chain can reach outside dir. It refuses
// absolute paths, "..", links that point outside dir (alone or via other
// links), entries at or under a path the archive made a symlink, and
// archives over the limits. Only regular files, directories and symlinks are
// created; modes are normalised to 0644/0755 and ownership is not restored.
func Extract(r io.Reader, dir string, lim Limits) (st Stats, err error) {
	if lim.MaxBytes == 0 {
		lim = DefaultLimits
	}
	if lim.MaxEntries == 0 {
		lim.MaxEntries = 2 * lim.MaxFiles
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return st, fmt.Errorf("not a gzipped tar: %w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return st, err
	}
	defer root.Close()
	links := map[string]bool{} // symlinks this archive created
	dirs := map[string]bool{}  // folders it made, named or implied
	entries := 0
	count := func(n int) error {
		if entries += n; entries > lim.MaxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrUnsafe, lim.MaxEntries)
		}
		return nil
	}
	// A link can look safe alone yet resolve outside through others
	// (t -> ., s -> t/t/..). Writes never follow it, but later readers of
	// the tree would, so such links are removed and fail the archive.
	defer func() {
		for l := range links {
			if _, serr := root.Stat(l); serr != nil && !errors.Is(serr, fs.ErrNotExist) {
				_ = root.Remove(l)
				if err == nil {
					err = fmt.Errorf("%w: symlink %q does not resolve inside the source", ErrUnsafe, l)
				}
			}
		}
	}()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, fmt.Errorf("read archive: %w", err)
		}
		if err := count(1); err != nil {
			return st, err
		}
		name, ok := cleanName(h.Name)
		if !ok || len(name) > maxName {
			return st, fmt.Errorf("%w: path %.200q", ErrUnsafe, h.Name)
		}
		if name == "" {
			continue
		}
		// Folders a path implies are made too: each counts.
		for p := path.Dir(name); p != "." && !dirs[p]; p = path.Dir(p) {
			dirs[p] = true
			if err := count(1); err != nil {
				return st, err
			}
		}
		if h.Typeflag == tar.TypeDir {
			dirs[name] = true
		}
		for p := name; p != "."; p = path.Dir(p) {
			if links[p] {
				return st, fmt.Errorf("%w: %q is written at or through the symlink %q", ErrUnsafe, h.Name, p)
			}
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return st, err
			}
		case tar.TypeReg, '\x00': // '\x00' is the pre-POSIX regular-file flag
			st.Files++
			if st.Files > lim.MaxFiles {
				return st, fmt.Errorf("%w: more than %d files", ErrUnsafe, lim.MaxFiles)
			}
			if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
				return st, err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return st, err
			}
			n, err := io.Copy(f, io.LimitReader(tr, lim.MaxBytes-st.Bytes+1))
			cerr := f.Close()
			st.Bytes += n
			if st.Bytes > lim.MaxBytes {
				return st, fmt.Errorf("%w: more than %d bytes", ErrUnsafe, lim.MaxBytes)
			}
			if err != nil {
				return st, err
			}
			if cerr != nil {
				return st, cerr
			}
		case tar.TypeSymlink:
			if path.IsAbs(h.Linkname) {
				return st, fmt.Errorf("%w: absolute symlink %q -> %q", ErrUnsafe, h.Name, h.Linkname)
			}
			if _, ok := cleanName(path.Join(path.Dir(name), h.Linkname)); !ok {
				return st, fmt.Errorf("%w: symlink %q points outside the source (%q)", ErrUnsafe, h.Name, h.Linkname)
			}
			if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
				return st, err
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return st, err
			}
			links[name] = true
			st.Files++
		default:
			// Hard links, devices, fifos: skipped. Pack never writes them.
		}
	}
	return st, nil
}

func cleanName(n string) (string, bool) {
	n = strings.ReplaceAll(n, `\`, "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, "\x00") {
		return "", false
	}
	c := path.Clean(n)
	if c == "." {
		return "", true
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}
