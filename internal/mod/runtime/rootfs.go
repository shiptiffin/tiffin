package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// Rootfs manifests: the gate between an image built a new way and the one
// today's builder makes. A manifest has a line per path of the image's root
// filesystem: its type, mode, owner, size, the sha256 of a file's bytes, a
// link's target and the extended attributes, sorted by path. Times are left
// out: a layer reused from the build cache keeps the times of the build that
// made it, and nothing an app reads changes with them. Two images with the
// same manifest give an app the same files.

// rootfsManifest walks root (a mounted image) into its manifest.
func rootfsManifest(root string) ([]string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if volatilePath(filepath.ToSlash(rel)) {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line, err := manifestLine(p, filepath.ToSlash(rel), fi)
		if err != nil {
			return err
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	return lines, err
}

// volatilePath reports whether a path holds what a package manager writes
// about its own run (logs, ldconfig's cache of file times): today's builder
// already writes new ones whenever its apt step runs again, and no app reads
// them, so like times they are left out.
func volatilePath(rel string) bool {
	return strings.HasPrefix(rel, "var/log/") || rel == "var/cache/ldconfig/aux-cache"
}

// manifestLine is one path's line: path, type, mode, uid:gid, size,
// sha256 (files) or target (links) or device (devices), xattrs.
func manifestLine(p, rel string, fi fs.FileInfo) (string, error) {
	uid, gid, dev := uint32(0), uint32(0), uint64(0)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		uid, gid, dev = st.Uid, st.Gid, uint64(st.Rdev)
	}
	m := fi.Mode()
	kind, size, what := "?", int64(0), ""
	switch {
	case m.IsRegular():
		kind, size = "f", fi.Size()
		sum, err := fileSHA256(p)
		if err != nil {
			return "", err
		}
		what = sum
	case m.IsDir():
		kind = "d"
	case m&fs.ModeSymlink != 0:
		kind = "l"
		t, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		what = t
	case m&fs.ModeCharDevice != 0:
		kind, what = "c", fmt.Sprint(dev)
	case m&fs.ModeDevice != 0:
		kind, what = "b", fmt.Sprint(dev)
	case m&fs.ModeNamedPipe != 0:
		kind = "p"
	case m&fs.ModeSocket != 0:
		kind = "s"
	}
	x, err := xattrs(p)
	if err != nil {
		return "", err
	}
	// Permission and special bits only: the type is the second field.
	mode := m.Perm() | m&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)
	return fmt.Sprintf("%s\t%s\t%o\t%d:%d\t%d\t%s\t%s", rel, kind, uint32(mode), uid, gid, size, what, x), nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// manifestDiff compares two manifests: how many paths differ, and the
// first few differences, each as "path: what" for the build log.
func manifestDiff(a, b []string) (int, []string) {
	split := func(lines []string) map[string]string {
		m := make(map[string]string, len(lines))
		for _, l := range lines {
			path, rest, _ := strings.Cut(l, "\t")
			m[path] = rest
		}
		return m
	}
	ma, mb := split(a), split(b)
	var diffs []string
	for p, ra := range ma {
		rb, ok := mb[p]
		switch {
		case !ok:
			diffs = append(diffs, p+": only in the new image")
		case ra != rb && strings.HasPrefix(p, configPrefix):
			diffs = append(diffs, p+": "+ra+" vs "+rb)
		case ra != rb:
			diffs = append(diffs, p+": "+fieldsDiffer(ra, rb))
		}
	}
	for p := range mb {
		if _, ok := ma[p]; !ok {
			diffs = append(diffs, p+": only in today's image")
		}
	}
	sort.Strings(diffs)
	n := len(diffs)
	if len(diffs) > 5 {
		diffs = diffs[:5]
	}
	return n, diffs
}

// manifestFields names the fields of a manifest line after its path.
var manifestFields = []string{"type", "mode", "owner", "size", "content", "xattrs"}

// fieldsDiffer names the fields in which two lines (path cut off) differ.
func fieldsDiffer(a, b string) string {
	fa, fb := strings.Split(a, "\t"), strings.Split(b, "\t")
	var out []string
	for i, name := range manifestFields {
		if i < len(fa) && i < len(fb) && fa[i] != fb[i] {
			out = append(out, name)
		}
	}
	return strings.Join(out, ", ") + " differ"
}

// gateImage reports whether candidate gives an app the same files as
// legacy, the image today's builder made from the same source; why says
// what differs when it does not (for the build log and the deploy record).
func gateImage(ctx context.Context, eng Engine, candidate, legacy string) (same bool, why string, err error) {
	a, err := eng.ImageManifest(ctx, candidate)
	if err != nil {
		return false, "", err
	}
	b, err := eng.ImageManifest(ctx, legacy)
	if err != nil {
		return false, "", err
	}
	n, diffs := manifestDiff(a, b)
	if n == 0 {
		return true, "", nil
	}
	why = fmt.Sprintf("%d paths differ: %s", n, strings.Join(diffs, "; "))
	if n > len(diffs) {
		why += "; ..."
	}
	return false, why, nil
}

// ImageManifest mounts ref read-only (ctr images mount) and returns its
// rootfs manifest.
func (n *nerdctl) ImageManifest(ctx context.Context, ref string) ([]string, error) {
	tmp := filepath.Join(DataDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tmp, "rootfs-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(dir)
	ctr := filepath.Join(filepath.Dir(n.bin), "ctr")
	run := func(ctx context.Context, args ...string) error {
		c := exec.CommandContext(ctx, ctr, append([]string{"--namespace", Namespace}, args...)...)
		c.Env = toolEnv()
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("ctr %s: %w: %s", args[1], err, strings.TrimSpace(lastLines(string(out), 3)))
		}
		return nil
	}
	if err := run(ctx, "images", "mount", dockerName(ref), dir); err != nil {
		return nil, err
	}
	// --rm drops the snapshot the mount made (the image's own stay).
	defer func() {
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = run(cctx, "images", "unmount", "--rm", dir)
	}()
	lines, err := rootfsManifest(dir)
	if err != nil {
		return nil, err
	}
	out, err := n.run(ctx, "image", "inspect", "--format", "{{json .Config}}", dockerName(ref))
	if err != nil {
		return nil, err
	}
	cfg, err := configLines(out)
	return append(lines, cfg...), err
}

// configLines are the manifest lines of an image's config (what the
// container runs with): #config/<field> and its value, env sorted.
func configLines(raw string) ([]string, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("image config: %w", err)
	}
	var lines []string
	for k, v := range cfg {
		if k == "Image" {
			continue // nerdctl's own: the name inspected, not what runs
		}
		val := string(v)
		if k == "Env" {
			var env []string
			if json.Unmarshal(v, &env) == nil {
				env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, builtAtEnv+"=") })
				sort.Strings(env)
				b, _ := json.Marshal(env)
				val = string(b)
			}
		}
		lines = append(lines, configPrefix+k+"\t"+val)
	}
	sort.Strings(lines)
	return lines, nil
}

// builtAtEnv is the build's time, which Railpack sets in every image's env:
// like file times, it is left out.
const builtAtEnv = "RAILPACK_BUILT_AT"

// configPrefix starts the manifest lines of the image config.
const configPrefix = "#config/"
