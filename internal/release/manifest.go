package release

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Channels a box can follow.
var Channels = []string{"stable", "edge"}

// Artifact is one build a release ships.
type Artifact struct {
	Name   string `json:"name" doc:"File name, e.g. tiffin-linux-amd64"`
	URL    string `json:"url,omitempty" doc:"Where to download it: absolute, or relative to the manifest. Empty: the name, next to the manifest."`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest describes one release on one channel. It is published as
// manifest.json next to its signature, manifest.json.minisig.
type Manifest struct {
	Version string    `json:"version" doc:"Semantic version, e.g. 1.4.0"`
	Channel string    `json:"channel" enum:"stable,edge"`
	Date    time.Time `json:"date"`
	// MinVersion is the oldest version that may update to this one directly
	// (a step a release cannot skip, such as a state migration); older boxes
	// need the release in between first.
	MinVersion string `json:"minVersion,omitempty"`
	Notes      string `json:"notes,omitempty" doc:"Release notes URL"`
	// Rollout is the percentage of boxes that apply it automatically: each
	// box falls in a bucket from 0 to 99 by its ID and the version.
	Rollout int `json:"rollout"`
	// EdgeRestart says the edge's code changed: the box restarts
	// tiffin-edge after the update, so it runs the new build too.
	EdgeRestart bool `json:"edgeRestart,omitempty"`
	// Artifacts by GOOS/GOARCH ("linux/amd64").
	Artifacts map[string]Artifact `json:"artifacts"`
}

// TrustedComment is the comment a manifest's signature carries.
func (m *Manifest) TrustedComment() string {
	return "tiffin " + m.Version + " " + m.Channel
}

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate checks the manifest is complete and well formed.
func (m *Manifest) Validate() error {
	if _, err := ParseVersion(m.Version); err != nil {
		return err
	}
	if m.MinVersion != "" {
		if _, err := ParseVersion(m.MinVersion); err != nil {
			return fmt.Errorf("minVersion: %w", err)
		}
	}
	if !validChannel(m.Channel) {
		return fmt.Errorf("unknown channel %q", m.Channel)
	}
	if m.Rollout < 0 || m.Rollout > 100 {
		return fmt.Errorf("rollout %d is not a percentage", m.Rollout)
	}
	if len(m.Artifacts) == 0 {
		return errors.New("the manifest lists no artifacts")
	}
	for k, a := range m.Artifacts {
		if a.Name == "" || strings.ContainsAny(a.Name, "/\\") || !sha256RE.MatchString(a.SHA256) || a.Size <= 0 {
			return fmt.Errorf("artifact %s is incomplete", k)
		}
	}
	return nil
}

func validChannel(c string) bool {
	for _, x := range Channels {
		if c == x {
			return true
		}
	}
	return false
}

// Errors Allowed returns.
var (
	ErrNotRelease = errors.New("this build is not a release")
	ErrNotNewer   = errors.New("not newer than the running version")
)

// Allowed says whether a box running current may install m: m must be
// newer (never a downgrade, nor the same version again) and current at
// least m's minVersion.
func (m *Manifest) Allowed(current string) error {
	cur, err := ParseVersion(current)
	if err != nil {
		return fmt.Errorf("%w (%s): it updates with tiffin up", ErrNotRelease, current)
	}
	next, err := ParseVersion(m.Version)
	if err != nil {
		return err
	}
	if next.Compare(cur) <= 0 {
		return fmt.Errorf("%w: %s is available, %s runs", ErrNotNewer, m.Version, current)
	}
	if m.MinVersion != "" {
		if min, _ := ParseVersion(m.MinVersion); cur.Compare(min) < 0 {
			return fmt.Errorf("%s updates only from %s or newer; this box runs %s: install %s first (tiffin up)", m.Version, m.MinVersion, current, m.MinVersion)
		}
	}
	return nil
}

// Bucket is the box's place, 0 to 99, in the rollout of a version: a hash
// of both, so each release goes first to a different set of boxes.
func Bucket(boxID, version string) int {
	h := sha256.Sum256([]byte(boxID + "\x00" + version))
	return int(binary.BigEndian.Uint64(h[:8]) % 100)
}

// InRollout says whether the box applies m automatically now.
func (m *Manifest) InRollout(boxID string) bool { return Bucket(boxID, m.Version) < m.Rollout }

// ParseManifest decodes and validates a manifest.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the manifest is not valid JSON: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Version is a semantic version.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var (
	versionRE  = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	describeRE = regexp.MustCompile(`(^|-)\d+-g[0-9a-f]{7,}(-dirty)?$|(^|-)dirty$`)
)

// ParseVersion reads "1.4.0", "v1.4.0" or "1.5.0-rc.1". A development
// build ("dev", a commit, or git describe's "1.4.0-3-gabc1234") is not a
// version.
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil || describeRE.MatchString(m[4]) {
		return Version{}, fmt.Errorf("%q is not a release version", s)
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	return v, nil
}

// Compare orders versions by semver precedence: -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			return cmp.Compare(d[0], d[1])
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] == b[i] {
			continue
		}
		x, errA := strconv.Atoi(a[i])
		y, errB := strconv.Atoi(b[i])
		switch {
		case errA == nil && errB == nil:
			return cmp.Compare(x, y)
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		}
		return strings.Compare(a[i], b[i])
	}
	return cmp.Compare(len(a), len(b))
}
