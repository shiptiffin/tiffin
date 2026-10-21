package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Railpack hands an app's own Node.js choice (.nvmrc, .node-version,
// engines.node) to mise inside the build, and mise matches a range like "24"
// or "24.x" against a release list it caches across builds: a rebuild keeps
// the release the first build found and misses Node's security fixes. The
// box resolves the range itself, to the newest release that matches, and
// writes that exact version into the build's copy of the source (never the
// app's own files). mise.toml and .tool-versions are left as they are.

// nodeReleasesURL lists every Node.js release, newest first.
const nodeReleasesURL = "https://nodejs.org/dist/index.json"

// releaseList is a release index fetched at most once per ttl.
type releaseList struct {
	url    string
	ttl    time.Duration
	client *http.Client

	mu       sync.Mutex
	at       time.Time
	versions []*semver.Version
}

func newNodeReleases() *releaseList {
	return &releaseList{url: nodeReleasesURL, ttl: time.Hour, client: &http.Client{Timeout: 15 * time.Second}}
}

// get is the release list, refetched once it is older than ttl. A failed
// fetch keeps the last list it had, if any.
func (l *releaseList) get(ctx context.Context) ([]*semver.Version, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.versions != nil && time.Since(l.at) < l.ttl {
		return l.versions, nil
	}
	vs, err := l.fetch(ctx)
	if err != nil {
		if l.versions != nil {
			return l.versions, nil
		}
		return nil, err
	}
	l.versions, l.at = vs, time.Now()
	return vs, nil
}

func (l *releaseList) fetch(ctx context.Context) ([]*semver.Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", l.url, resp.Status)
	}
	var rows []struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rows); err != nil {
		return nil, fmt.Errorf("%s: %w", l.url, err)
	}
	var vs []*semver.Version
	for _, r := range rows {
		if v, err := semver.NewVersion(r.Version); err == nil && v.Prerelease() == "" {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("%s lists no releases", l.url)
	}
	return vs, nil
}

// exactVersion matches a version that names one release (22.11.0, v24.1.2).
var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// newestMatch is the newest release in vs that spec allows, or "" when spec
// names one release already, is an alias (lts/*, node) or allows none.
func newestMatch(spec string, vs []*semver.Version) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || exactVersion.MatchString(spec) {
		return ""
	}
	c, err := semver.NewConstraint(strings.TrimPrefix(spec, "v"))
	if err != nil {
		return ""
	}
	var best *semver.Version
	for _, v := range vs {
		if c.Check(v) && (best == nil || v.GreaterThan(best)) {
			best = v
		}
	}
	if best == nil {
		return ""
	}
	return best.String()
}

// enginesNode finds engines.node's value in a package.json, to replace it
// in place and keep the rest of the file byte for byte.
var enginesNode = regexp.MustCompile(`("engines"\s*:\s*\{[^{}]*?"node"\s*:\s*)"([^"]*)"`)

// pinNodeRange writes the newest release matching the app's own Node.js
// range into its version files and engines.node in srcDir, which is the
// build's copy of the source. It changes nothing when the app names no
// range, or when the release list cannot be had (mise then picks, as before).
func pinNodeRange(ctx context.Context, srcDir string, releases *releaseList, log io.Writer) {
	type found struct{ file, spec string }
	var specs []found
	for _, f := range []string{".nvmrc", ".node-version"} {
		if raw, err := readSrc(filepath.Join(srcDir, f)); err == nil {
			if s := strings.TrimSpace(string(raw)); s != "" && !exactVersion.MatchString(s) {
				specs = append(specs, found{f, s})
			}
		}
	}
	pkg, _ := readSrc(filepath.Join(srcDir, "package.json"))
	if m := enginesNode.FindSubmatch(pkg); m != nil && !exactVersion.MatchString(strings.TrimSpace(string(m[2]))) {
		specs = append(specs, found{"package.json", string(m[2])})
	}
	if len(specs) == 0 {
		return
	}
	vs, err := releases.get(ctx)
	if err != nil {
		fmt.Fprintf(log, "==> Node.js: could not read the release list (%v); the build picks from its own\n", err)
		return
	}
	for _, s := range specs {
		v := newestMatch(s.spec, vs)
		if v == "" {
			continue
		}
		p := filepath.Join(srcDir, s.file)
		var out []byte
		if s.file == "package.json" {
			loc := enginesNode.FindSubmatchIndex(pkg)
			out = append(append(append([]byte{}, pkg[:loc[4]]...), v...), pkg[loc[5]:]...)
		} else {
			out = []byte(v + "\n")
		}
		if err := os.WriteFile(p, out, 0o644); err != nil {
			fmt.Fprintf(log, "==> Node.js: could not pin %s: %v\n", s.file, err)
			continue
		}
		fmt.Fprintf(log, "==> Node.js: %s asks for %q; building with %s, the newest release that matches\n", s.file, s.spec, v)
	}
}
