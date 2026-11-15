package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// Next.js security releases: a deploy whose installed next is older than
// the latest security release of its major line gets a warning (never a
// failure). The table is data (nextsecurity.json), dated: past
// nextSecurityStale a warning says the list may be behind, and the box never
// says a version is safe, so an old table can only miss a warning.

//go:embed nextsecurity.json
var nextSecurityJSON []byte

// nextSecurityStale is how old the table may get before the box says it
// may be behind (Next.js publishes security releases about monthly).
const nextSecurityStale = 60 * 24 * time.Hour

type nextSecurityLine struct {
	Major         int    `json:"major"`
	Patched       string `json:"patched"`
	SecurityUntil string `json:"securityUntil,omitempty"`
}

type nextSecurityTable struct {
	AsOf    string             `json:"asOf"`
	Release string             `json:"release"`
	URL     string             `json:"url"`
	Lines   []nextSecurityLine `json:"lines"`
	// ProxyFraming: the versions that fixed GHSA-pc58-hwjq-p385, per major.
	ProxyFraming struct {
		Advisory string `json:"advisory"`
		Fixed    []struct {
			Major   int    `json:"major"`
			Version string `json:"version"`
		} `json:"fixed"`
	} `json:"proxyFraming"`
}

func loadNextSecurity(raw []byte) (nextSecurityTable, error) {
	var t nextSecurityTable
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	if _, err := time.Parse(time.DateOnly, t.AsOf); err != nil {
		return t, fmt.Errorf("asOf: %w", err)
	}
	if len(t.Lines) == 0 {
		return t, fmt.Errorf("no lines")
	}
	for _, l := range t.Lines {
		v, err := semver.StrictNewVersion(l.Patched)
		if err != nil || v.Major() != uint64(l.Major) {
			return t, fmt.Errorf("line %d: patched %q", l.Major, l.Patched)
		}
		if l.SecurityUntil != "" {
			if _, err := time.Parse(time.DateOnly, l.SecurityUntil); err != nil {
				return t, fmt.Errorf("line %d: securityUntil: %w", l.Major, err)
			}
		}
	}
	if len(t.ProxyFraming.Fixed) == 0 {
		return t, fmt.Errorf("proxyFraming: no fixed versions")
	}
	for _, f := range t.ProxyFraming.Fixed {
		v, err := semver.StrictNewVersion(f.Version)
		if err != nil || v.Major() != uint64(f.Major) {
			return t, fmt.Errorf("proxyFraming: %d %q", f.Major, f.Version)
		}
	}
	return t, nil
}

// nextFreshConns reports whether instances of a build with Next.js version
// (as installed; "" when unknown) get a fresh connection for every request:
// a version that can send a fetched Response's body with the framing of
// the compressed original (GHSA-pc58-hwjq-p385). On a reused keep-alive
// connection the bytes past the declared length are read as the next
// request's response, another client's. Unknown versions keep their pool.
func nextFreshConns(version string) bool {
	t, err := loadNextSecurity(nextSecurityJSON)
	if err != nil || version == "" {
		return false
	}
	return t.framingUnsafe(version)
}

func (t nextSecurityTable) framingUnsafe(version string) bool {
	v, err := semver.NewVersion(version)
	if err != nil {
		return false
	}
	lowest := t.ProxyFraming.Fixed[0].Major
	for _, f := range t.ProxyFraming.Fixed {
		if uint64(f.Major) == v.Major() {
			return v.LessThan(semver.MustParse(f.Version))
		}
		lowest = min(lowest, f.Major)
	}
	return v.Major() < uint64(lowest)
}

// nextSecurityCheck says what to tell a deploy of Next.js version (as
// installed): a warning when it is older than its line's latest security
// release or its major no longer gets security fixes, and a note for the
// log when the table may be behind. now is the check's time.
func nextSecurityCheck(t nextSecurityTable, version string, now time.Time) (warn, note string) {
	v, err := semver.NewVersion(version)
	if err != nil {
		return "", ""
	}
	asOf, _ := time.Parse(time.DateOnly, t.AsOf)
	stale := now.Sub(asOf) > nextSecurityStale
	behind := ""
	if stale {
		behind = fmt.Sprintf(" (The box's list of Next.js security releases is from %s, so newer ones may exist: see https://nextjs.org/blog.)", t.AsOf)
	}
	newest, oldest := t.Lines[0], t.Lines[0]
	for _, l := range t.Lines {
		if l.Major > newest.Major {
			newest = l
		}
		if l.Major < oldest.Major {
			oldest = l
		}
	}
	src := fmt.Sprintf("as of %s, %s", t.AsOf, t.URL)
	for _, l := range t.Lines {
		if uint64(l.Major) != v.Major() {
			continue
		}
		if until, err := time.Parse(time.DateOnly, l.SecurityUntil); err == nil && now.After(until.Add(24*time.Hour)) {
			return fmt.Sprintf("Next.js %s: Next.js %d stopped getting security fixes on %s. Upgrade next to %s or later.%s",
				version, l.Major, l.SecurityUntil, newest.Patched, behind), ""
		}
		// Prereleases sort before their release: 16.4.5-canary.1 is not patched.
		if v.LessThan(semver.MustParse(l.Patched)) {
			return fmt.Sprintf("Next.js %s is older than %s, the latest security release for Next.js %d (%s). Upgrade next to %s or later.%s",
				version, l.Patched, l.Major, src, l.Patched, behind), ""
		}
		if stale {
			return "", strings.TrimSpace(behind)
		}
		return "", ""
	}
	if v.Major() < uint64(oldest.Major) {
		return fmt.Sprintf("Next.js %s: Next.js %d no longer gets security fixes (%s lists %s and later). Upgrade next to %s or later.%s",
			version, v.Major(), t.URL, oldest.Patched, newest.Patched, behind), ""
	}
	// A major newer than the table knows (or a line it skips): nothing to
	// warn about, and nothing to vouch for either.
	if stale {
		return "", strings.TrimSpace(behind)
	}
	return "", ""
}

// nextLockfiles are the lockfiles the installed next version is read from,
// in the order a folder's are tried.
var nextLockfiles = []string{"bun.lock", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"}

var (
	bunLockNextRe  = regexp.MustCompile(`"next"\s*:\s*\[\s*"next@(\d[^"]*)"`)
	pnpmNextPkgRe  = regexp.MustCompile(`(?m)^\s+'?/?next@(\d+\.\d+\.\d+[^:'(\s]*)(?:\([^:]*\))?'?:\s*$`)
	yarnLockNextRe = regexp.MustCompile(`(?m)^"?next@[^\n]*:\n(?:[ \t]+[^\n]*\n)*?[ \t]+version:? "?(\d[^"\s]*)"?`)
	exactNextRe    = regexp.MustCompile(`^=?v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$`)
)

// nextInstalledVersion is the next version the build installs, read from
// the app's lockfile (in its folder, or a workspace's above it up to
// srcDir), else from an exact version in package.json; "" when it cannot
// tell (a range with no lockfile, bun.lockb). from says where it was read.
func nextInstalledVersion(srcDir, appDir string) (version, from string) {
	for dir := appDir; ; dir = filepath.Dir(dir) {
		for _, name := range nextLockfiles {
			raw, err := readSrc(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			appRel, _ := filepath.Rel(dir, appDir)
			if v := lockedNext(name, raw, filepath.ToSlash(appRel)); v != "" {
				return v, name
			}
		}
		// Up to the top of the source, never past it.
		if dir == srcDir || !strings.HasPrefix(dir, srcDir+string(filepath.Separator)) {
			break
		}
	}
	raw, err := readSrc(filepath.Join(appDir, "package.json"))
	if err != nil {
		return "", ""
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &pj) != nil {
		return "", ""
	}
	if m := exactNextRe.FindStringSubmatch(strings.TrimSpace(pj.Dependencies["next"])); m != nil {
		return m[1], "package.json"
	}
	return "", ""
}

// lockedNext reads next's version from one lockfile; appRel is the app's
// folder relative to the lockfile's ("." for the same one). A lockfile that
// holds several versions of next with none tied to the app gives "".
func lockedNext(name string, raw []byte, appRel string) string {
	one := func(found [][]string) string {
		seen := map[string]bool{}
		v := ""
		for _, m := range found {
			if !seen[m[1]] {
				seen[m[1]] = true
				v = m[1]
			}
		}
		if len(seen) == 1 {
			return v
		}
		return ""
	}
	switch name {
	case "bun.lock":
		// Workspace copies are keyed "<workspace>/next"; the top one is "next".
		return one(bunLockNextRe.FindAllStringSubmatch(string(raw), 2))
	case "package-lock.json":
		var pl struct {
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
			Dependencies map[string]struct {
				Version string `json:"version"`
			} `json:"dependencies"`
		}
		if json.Unmarshal(raw, &pl) != nil {
			return ""
		}
		if appRel != "." {
			if p, ok := pl.Packages[appRel+"/node_modules/next"]; ok {
				return p.Version
			}
		}
		if p, ok := pl.Packages["node_modules/next"]; ok {
			return p.Version
		}
		return pl.Dependencies["next"].Version
	case "pnpm-lock.yaml":
		return one(pnpmNextPkgRe.FindAllStringSubmatch(string(raw), -1))
	case "yarn.lock":
		return one(yarnLockNextRe.FindAllStringSubmatch(string(raw), -1))
	}
	return ""
}

// warnOutdatedNext checks the installed next against the table and puts
// what it finds on the deploy and in its log.
func warnOutdatedNext(req BuildRequest, log io.Writer, now time.Time) {
	t, err := loadNextSecurity(nextSecurityJSON)
	if err != nil {
		return // a broken table is caught by its test, not by deploys
	}
	v, from := nextInstalledVersion(req.SrcDir, req.appDir())
	if req.Deploy != nil {
		req.Deploy.NextVersion = v
	}
	if v == "" {
		fmt.Fprintf(log, "==> Next.js: no lockfile or exact version says which next installs, so the box could not check it against Next.js security releases\n")
		return
	}
	warn, note := nextSecurityCheck(t, v, now)
	if warn != "" {
		w := warn + " (version read from " + from + ")"
		fmt.Fprintf(log, "==> warning: %s\n", w)
		if req.Deploy != nil {
			req.Deploy.Warnings = append(req.Deploy.Warnings, w)
		}
	} else if note != "" {
		fmt.Fprintf(log, "==> Next.js %s: %s\n", v, note)
	}
	if w := nextOnOldBun(req, v); w != "" {
		fmt.Fprintf(log, "==> warning: %s\n", w)
		if req.Deploy != nil {
			req.Deploy.Warnings = append(req.Deploy.Warnings, w)
		}
	}
	if t.framingUnsafe(v) {
		fmt.Fprintf(log, "==> Next.js %s: its instances get a fresh connection for every request until next is upgraded (%s: a forwarded response could otherwise reach another client)\n", v, t.ProxyFraming.Advisory)
	}
}

// Bun before 1.4.0 fails `next build` for Next.js 16 apps: "Expected
// CommonJS module to have a function wrapper" loading next.config.ts
// (oven-sh/bun#26874, Bun 1.3.8 to 1.3.13) and a crash collecting page data
// with Next.js 16.3 (oven-sh/bun#36866, Bun 1.3.14); both fixed in 1.4.0.
// The box's own Bun is newer; an app can pin an older one.
const nextBunFixed = "1.4.0"

var (
	bunPinRe     = regexp.MustCompile(`(?m)^\s*bun\s*[= ]\s*"?v?(\d+\.\d+\.\d+)"?\s*$`)
	bunVersionRe = regexp.MustCompile(`^\s*(?:bun@)?[=v^~]?(\d+\.\d+\.\d+)\s*$`)
	bunEnginesRe = regexp.MustCompile(`^\s*(?:=|v|\^|~)?(\d+\.\d+\.\d+)\s*$`)
)

// pinnedBun is the exact Bun version an app pins (.bun-version,
// packageManager bun@x.y.z, engines.bun, mise.toml or .tool-versions), or "".
func pinnedBun(dir string) string {
	if raw, err := readSrc(filepath.Join(dir, ".bun-version")); err == nil {
		if m := bunVersionRe.FindStringSubmatch(strings.TrimSpace(string(raw))); m != nil {
			return m[1]
		}
	}
	if raw, err := readSrc(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			PackageManager string            `json:"packageManager"`
			Engines        map[string]string `json:"engines"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			if pm, ok := strings.CutPrefix(pkg.PackageManager, "bun@"); ok {
				if m := bunEnginesRe.FindStringSubmatch(strings.SplitN(pm, "+", 2)[0]); m != nil {
					return m[1]
				}
			}
			if m := bunEnginesRe.FindStringSubmatch(pkg.Engines["bun"]); m != nil {
				return m[1]
			}
		}
	}
	for _, f := range []string{"mise.toml", ".tool-versions"} {
		if raw, err := readSrc(filepath.Join(dir, f)); err == nil {
			if m := bunPinRe.FindStringSubmatch(string(raw)); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// nextOnOldBun warns about a Next.js 16 app built on a Bun it pins below
// nextBunFixed (not on runtime: "node", which builds with Node.js).
func nextOnOldBun(req BuildRequest, next string) string {
	if req.Spec.Runtime == manifest.RuntimeNode {
		return ""
	}
	nv, err := semver.NewVersion(next)
	if err != nil || nv.Major() < 16 {
		return ""
	}
	pin := pinnedBun(req.appDir())
	if pin == "" && req.SrcDir != req.appDir() {
		pin = pinnedBun(req.SrcDir)
	}
	bv, err := semver.NewVersion(pin)
	if err != nil || !bv.LessThan(semver.MustParse(nextBunFixed)) {
		return ""
	}
	return fmt.Sprintf("Bun %s, which the app pins, fails `next build` for Next.js 16 apps (oven-sh/bun#26874, oven-sh/bun#36866; fixed in Bun %s). "+
		"Pin Bun %s or later, drop the pin to build with the box's Bun, or set runtime: \"node\".", pin, nextBunFixed, nextBunFixed)
}
