package boxfile

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Archive identity and the newest format this build reads and writes.
const (
	ManifestKind = "tiffin-box-export"
	// ProjectKind is one project's export (tiffin projects export).
	ProjectKind   = "tiffin-project-export"
	FormatVersion = 1
	// FileExt is the archive's file extension.
	FileExt = ".tiffin"
)

// Manifest is the archive's first entry: where it came from and what it holds.
type Manifest struct {
	Kind          string      `json:"kind" doc:"tiffin-box-export or tiffin-project-export"`
	Project       string      `json:"project,omitempty" doc:"The project, in a project export"`
	Format        int         `json:"format" doc:"Archive format version"`
	TiffinVersion string      `json:"tiffinVersion" doc:"Tiffin version of the box that made it"`
	Schema        int         `json:"schema" doc:"Platform state schema version of that box"`
	CreatedAt     time.Time   `json:"createdAt"`
	Source        Source      `json:"source"`
	Projects      []string    `json:"projects"`
	Databases     []string    `json:"databases" doc:"Postgres databases, dumped logically (pg_dump, plain SQL)"`
	Images        []string    `json:"images,omitempty" doc:"App images of live deploys (containerd refs)"`
	Parts         []string    `json:"parts" doc:"Parts in archive order"`
	IncludesKey   bool        `json:"includesKey" doc:"Whether the box key that decrypts secrets is inside"`
	Recipient     string      `json:"recipient" doc:"age public key the archive's secrets are encrypted to"`
	WithHistory   bool        `json:"withHistory" doc:"Whether logs, metrics and build logs are inside"`
	CAPEM         string      `json:"caPem,omitempty" doc:"The source box's HTTPS root certificate (restored with the box)"`
	Consistency   Consistency `json:"consistency"`
}

// Source describes the box an archive came from.
type Source struct {
	Domain    string `json:"domain"`
	PublicURL string `json:"publicUrl"`
	Hostname  string `json:"hostname"`
}

// Consistency says how the export got a consistent picture of a live box.
type Consistency struct {
	WritesPausedMs int64  `json:"writesPausedMs" doc:"How long app containers and the object store were paused"`
	AppsPaused     int    `json:"appsPaused" doc:"App containers paused during the snapshot"`
	Staged         bool   `json:"staged" doc:"File trees were copied (reflinks) inside the pause; false means they were read live"`
	Note           string `json:"note"`
}

// CheckCompatible refuses archives this box cannot restore: a newer archive
// format, a newer platform state schema or a newer Tiffin release.
func CheckCompatible(m *Manifest, schema int, version string) error {
	switch m.Kind {
	case ManifestKind:
	case ProjectKind:
		return fmt.Errorf("this is an export of project %s, not of a whole box: import it with `tiffin projects import`", m.Project)
	default:
		return fmt.Errorf("not a Tiffin box export (kind %q)", m.Kind)
	}
	return checkVersions(m, schema, version)
}

// CheckProject is CheckCompatible for project exports.
func CheckProject(m *Manifest, schema int, version string) error {
	switch m.Kind {
	case ProjectKind:
	case ManifestKind:
		return errors.New("this is an export of a whole box, not of one project: import it with `tiffin box import`")
	default:
		return fmt.Errorf("not a Tiffin project export (kind %q)", m.Kind)
	}
	return checkVersions(m, schema, version)
}

func checkVersions(m *Manifest, schema int, version string) error {
	if m.Format > FormatVersion {
		return fmt.Errorf("the archive was made by a newer Tiffin (archive format %d; this box reads up to %d): update this box first with `tiffin up`", m.Format, FormatVersion)
	}
	if m.Schema > schema {
		return fmt.Errorf("the archive was made by a newer Tiffin (state schema %d; this box has %d): update this box first with `tiffin up`", m.Schema, schema)
	}
	if newer(m.TiffinVersion, version) {
		return fmt.Errorf("the archive was made by Tiffin %s, newer than this box's %s: update this box first with `tiffin up`", m.TiffinVersion, version)
	}
	return nil
}

// newer reports whether release a is newer than b. Only release numbers
// (major.minor.patch) are compared; development builds ("dev") never count
// as newer, the schema check covers them.
func newer(a, b string) bool {
	va, oka := release(a)
	vb, okb := release(b)
	if !oka || !okb {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func release(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
