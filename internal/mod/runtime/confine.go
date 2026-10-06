package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Builds and images run the app's own code, so whatever they leave on the
// host is untrusted: a link planted in it would make the edge (root) serve,
// or the runtime write through to, any file on the box.

// plainTree refuses dir if it, or anything under it, is not a plain folder
// or file. Copies out of an image are dereferenced (cp -L), so a link there
// was planted by the image's own tools.
func plainTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && !e.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			return fmt.Errorf("%s in the copied files is not a plain file or folder", rel)
		}
		return nil
	})
}

// confinedDir resolves dir, which must be a folder inside root, and checks
// that every link under it stays inside it. It returns dir's real path.
func confinedDir(root, dir string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !within(realRoot, real) {
		return "", fmt.Errorf("%s leads outside the source", filepath.Base(dir))
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", filepath.Base(dir))
	}
	err = filepath.WalkDir(real, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.Type()&fs.ModeSymlink == 0 {
			return err
		}
		rel, _ := filepath.Rel(real, p)
		t, err := filepath.EvalSymlinks(p)
		if err != nil || !within(real, t) {
			return fmt.Errorf("the link %s leads outside the site's folder", rel)
		}
		return nil
	})
	return real, err
}

func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// noLinks refuses rel (a path under root) if it, or a folder on the way to
// it, is a link: the host would follow it wherever it points.
func noLinks(root, rel string) error {
	p := root
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/") {
		if part == "" || part == "." {
			continue
		}
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link, which a disk folder may not be or be inside", strings.TrimPrefix(p, root+string(filepath.Separator)))
		}
	}
	return nil
}
