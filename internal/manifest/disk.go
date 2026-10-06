package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// DefaultDiskSize is the size of a disk folder that names none.
const DefaultDiskSize = "1GB"

// DefaultTimeout is how long a request to an app may take when its
// timeoutSeconds is unset.
const DefaultTimeout = 15 * time.Minute

// Timeout is the most one request to the app may take.
func (a *App) Timeout() time.Duration {
	if a.TimeoutSeconds > 0 {
		return time.Duration(a.TimeoutSeconds) * time.Second
	}
	return DefaultTimeout
}

// Disk is an app's persistent folders. Its JSON is a list of paths when
// every folder has the default size (["data"]), an object of path → size
// otherwise ({"data": "5GB"}).
type Disk []DiskFolder

// DiskFolder is one persistent folder.
type DiskFolder struct {
	Path string
	// Size is "<n>MB", "<n>GB" or "<n>TB" (1GB = 1024MB); "": DefaultDiskSize.
	Size string
}

var sizeRe = regexp.MustCompile(`^([1-9][0-9]{0,5})(MB|GB|TB)$`)

// ParseSize returns the bytes of a folder size ("" is DefaultDiskSize).
func ParseSize(s string) (int64, error) {
	if s == "" {
		s = DefaultDiskSize
	}
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a size like \"500MB\", \"5GB\" or \"1TB\"", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	shift := map[string]uint{"MB": 20, "GB": 30, "TB": 40}[m[2]]
	return n << shift, nil
}

// Bytes is the folder's size in bytes (0 for a size that does not parse,
// which validation refuses).
func (f DiskFolder) Bytes() int64 {
	n, _ := ParseSize(f.Size)
	return n
}

// Paths returns the folders' paths, in order.
func (d Disk) Paths() []string {
	out := make([]string, len(d))
	for i, f := range d {
		out[i] = f.Path
	}
	return out
}

// canonicalSize writes a size in its largest whole unit ("1024MB" is
// "1GB"), and the default as "".
func canonicalSize(s string) string {
	n, err := ParseSize(s)
	if err != nil {
		return s
	}
	def, _ := ParseSize(DefaultDiskSize)
	if n == def {
		return ""
	}
	for _, u := range []struct {
		name  string
		shift uint
	}{{"TB", 40}, {"GB", 30}} {
		if n%(1<<u.shift) == 0 {
			return strconv.FormatInt(n>>u.shift, 10) + u.name
		}
	}
	return strconv.FormatInt(n>>20, 10) + "MB"
}

func (d Disk) sized() bool {
	return slices.ContainsFunc(d, func(f DiskFolder) bool { return f.Size != "" })
}

// MarshalJSON writes the list form when every folder has the default size.
func (d Disk) MarshalJSON() ([]byte, error) {
	if !d.sized() {
		return json.Marshal(d.Paths())
	}
	m := make(map[string]string, len(d))
	for _, f := range d {
		m[f.Path] = f.Size
		if f.Size == "" {
			m[f.Path] = DefaultDiskSize
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads either form; the object's folders come sorted.
func (d *Disk) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		*d = make(Disk, 0, len(m))
		for _, p := range slices.Sorted(maps.Keys(m)) {
			*d = append(*d, DiskFolder{Path: p, Size: m[p]})
		}
		return nil
	}
	var paths []string
	if err := json.Unmarshal(b, &paths); err != nil {
		return err
	}
	*d = make(Disk, len(paths))
	for i, p := range paths {
		(*d)[i] = DiskFolder{Path: p}
	}
	return nil
}
