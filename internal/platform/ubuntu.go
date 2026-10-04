package platform

import (
	"os"
	"strings"
)

// Ubuntu releases a box can run on, by version and codename.
var SupportedUbuntu = map[string]string{"24.04": "noble", "26.04": "resolute"}

// OSReleasePath is read for the running release (tests replace it).
var OSReleasePath = "/etc/os-release"

// Codename is the running Ubuntu release's codename ("noble", "resolute"),
// "noble" when it cannot be read.
func Codename() string {
	raw, _ := os.ReadFile(OSReleasePath)
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_CODENAME="); ok {
			if v = strings.Trim(v, `"`); v != "" {
				return v
			}
		}
	}
	return "noble"
}

// RepoCodename picks the apt suite for a third-party repository that only
// publishes some releases: the running one when it is listed, else the
// newest listed one (e.g. a repository without "resolute" yet gets "noble";
// its packages are built for the older release and run on the newer one).
func RepoCodename(has ...string) string {
	c := Codename()
	for _, h := range has {
		if h == c {
			return c
		}
	}
	if len(has) > 0 {
		return has[len(has)-1]
	}
	return c
}
