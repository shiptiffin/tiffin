package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shipped table must load; a broken one would silently stop warnings.
func TestNextSecurityTableLoads(t *testing.T) {
	tab, err := loadNextSecurity(nextSecurityJSON)
	if err != nil {
		t.Fatal(err)
	}
	if tab.URL == "" || !strings.HasPrefix(tab.URL, "https://") {
		t.Errorf("url %q", tab.URL)
	}
	for _, bad := range []string{
		`{"asOf":"Nov 9","lines":[{"major":16,"patched":"16.4.5"}]}`,
		`{"asOf":"2026-11-09","lines":[]}`,
		`{"asOf":"2026-11-09","lines":[{"major":16,"patched":"15.5.29"}]}`,
		`{"asOf":"2026-11-09","lines":[{"major":15,"patched":"15.5.29","securityUntil":"soon"}]}`,
		`{"asOf":"2026-11-09","lines":[{"major":16,"patched":"16.4.5"}]}`, // no proxyFraming
		`{"asOf":"2026-11-09","lines":[{"major":16,"patched":"16.4.5"}],"proxyFraming":{"fixed":[{"major":16,"version":"15.5.29"}]}}`,
	} {
		if _, err := loadNextSecurity([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestNextSecurityCheck(t *testing.T) {
	tab, err := loadNextSecurity([]byte(`{"asOf":"2026-11-09","url":"https://nextjs.org/blog/x","lines":[
		{"major":16,"patched":"16.4.5"},{"major":15,"patched":"15.5.29","securityUntil":"2027-01-31"}],
		"proxyFraming":{"advisory":"GHSA-x","fixed":[{"major":16,"version":"16.4.5"},{"major":15,"version":"15.5.29"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	fresh := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		v    string
		warn string // substring; "" = no warning
	}{
		{"16.4.4", "older than 16.4.5"},
		{"16.3.9", "older than 16.4.5"}, // only the newest minor of a line gets patches
		{"16.0.0", "older than 16.4.5"},
		{"16.4.5-canary.2", "older than 16.4.5"},
		{"16.4.5", ""},
		{"16.4.6", ""},
		{"16.5.0-canary.46", ""},
		{"15.5.28", "older than 15.5.29"},
		{"15.1.0", "older than 15.5.29"},
		{"15.5.29", ""},
		{"14.2.35", "no longer gets security fixes"},
		{"13.5.11", "Upgrade next to 16.4.5"},
		{"17.0.0", ""}, // newer than the table: nothing to say either way
		{"latest", ""},
	} {
		warn, note := nextSecurityCheck(tab, c.v, fresh)
		if c.warn == "" && warn != "" || c.warn != "" && !strings.Contains(warn, c.warn) {
			t.Errorf("%s: warning %q, want %q", c.v, warn, c.warn)
		}
		if note != "" {
			t.Errorf("%s: a fresh table needs no note, got %q", c.v, note)
		}
		if warn != "" && !strings.Contains(warn, "2026-11-09") && !strings.Contains(warn, "nextjs.org") {
			t.Errorf("%s: a warning names its source: %q", c.v, warn)
		}
	}

	// Next.js 15 past the end of its security support.
	later := time.Date(2027, 2, 2, 0, 0, 0, 0, time.UTC)
	if w, _ := nextSecurityCheck(tab, "15.5.29", later); !strings.Contains(w, "stopped getting security fixes on 2027-01-31") {
		t.Errorf("15 after its end date: %q", w)
	}
	if w, _ := nextSecurityCheck(tab, "15.5.29", time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)); w != "" {
		t.Errorf("15 on its last day: %q", w)
	}

	// An old table never vouches for a version: what it does not flag gets a
	// note that it may be behind, and its warnings say so too.
	for _, v := range []string{"16.4.5", "17.1.0", "15.5.29"} {
		w, note := nextSecurityCheck(tab, v, later)
		if v == "15.5.29" {
			note = w
		}
		if !strings.Contains(note, "from 2026-11-09") || !strings.Contains(note, "newer ones may exist") {
			t.Errorf("%s with an old table: warning %q note %q", v, w, note)
		}
	}
	if w, _ := nextSecurityCheck(tab, "16.4.1", later); !strings.Contains(w, "newer ones may exist") {
		t.Errorf("an old table's warning says it may be behind: %q", w)
	}
}

func TestNextInstalledVersion(t *testing.T) {
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg := `{"dependencies":{"next":"^16.4.0","react":"19.3.0"}}`
	cases := []struct {
		name, lock, body, want string
	}{
		{"bun", "bun.lock", `{
  "lockfileVersion": 1,
  "workspaces": { "": { "dependencies": { "next": "^16.4.0" } } },
  "packages": {
    "@next/env": ["@next/env@16.4.3", "", {}, "sha512-x"],
    "next": ["next@16.4.3", "", { "dependencies": { "@next/env": "16.4.3" } }, "sha512-y"],
    "next-auth": ["next-auth@5.0.0", "", {}, "sha512-z"],
  }
}`, "16.4.3"},
		{"npm", "package-lock.json", `{"lockfileVersion":3,"packages":{"":{"dependencies":{"next":"^16.4.0"}},
"node_modules/next":{"version":"16.4.5"},"node_modules/@next/env":{"version":"16.4.5"}}}`, "16.4.5"},
		{"npm v1", "package-lock.json", `{"lockfileVersion":1,"dependencies":{"next":{"version":"15.5.2"}}}`, "15.5.2"},
		{"pnpm", "pnpm-lock.yaml", `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      next:
        specifier: ^16.4.0
        version: 16.4.2(react-dom@19.3.0(react@19.3.0))(react@19.3.0)
packages:
  '@next/env@16.4.2':
    resolution: {integrity: sha512-x}
  next@16.4.2:
    resolution: {integrity: sha512-y}
  next-auth@5.0.0:
    resolution: {integrity: sha512-z}
snapshots:
  next@16.4.2(react-dom@19.3.0(react@19.3.0))(react@19.3.0):
    dependencies:
      '@next/env': 16.4.2
`, "16.4.2"},
		{"yarn v1", "yarn.lock", `# yarn lockfile v1

"@next/env@16.4.1":
  version "16.4.1"

next-auth@^5:
  version "5.0.0"

next@^16.4.0:
  version "16.4.1"
  resolved "https://registry.yarnpkg.com/next/-/next-16.4.1.tgz"
`, "16.4.1"},
		{"yarn berry", "yarn.lock", `__metadata:
  version: 8

"next@npm:^16.4.0":
  version: 16.4.0
  resolution: "next@npm:16.4.0"
`, "16.4.0"},
		{"two versions, none tied to the app", "yarn.lock", `next@^15:
  version "15.5.2"

next@^16:
  version "16.4.1"
`, ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		write(dir, "package.json", pkg)
		write(dir, c.lock, c.body)
		if got, from := nextInstalledVersion(dir, dir); got != c.want || (got != "" && from != c.lock) {
			t.Errorf("%s: %q from %q, want %q", c.name, got, from, c.want)
		}
	}

	// A workspace: the lockfile at the top, the app in a folder.
	top := t.TempDir()
	app := filepath.Join(top, "apps", "web")
	write(app, "package.json", pkg)
	write(top, "package-lock.json", `{"packages":{"apps/web/node_modules/next":{"version":"16.3.1"},"node_modules/next":{"version":"16.4.5"}}}`)
	if got, _ := nextInstalledVersion(top, app); got != "16.3.1" {
		t.Errorf("workspace: %q", got)
	}
	// Never above the source.
	outside := t.TempDir()
	write(outside, "bun.lock", `{"packages":{"next":["next@14.0.0","",{},""]}}`)
	inner := filepath.Join(outside, "src")
	write(inner, "package.json", pkg)
	if got, _ := nextInstalledVersion(inner, inner); got != "" {
		t.Errorf("read a lockfile above the source: %q", got)
	}
	// No lockfile: only an exact version counts.
	exact := t.TempDir()
	write(exact, "package.json", `{"dependencies":{"next":"16.2.1"}}`)
	if got, from := nextInstalledVersion(exact, exact); got != "16.2.1" || from != "package.json" {
		t.Errorf("exact: %q %q", got, from)
	}
	if got, _ := nextInstalledVersion(inner, inner); got != "" {
		t.Errorf("range: %q", got)
	}
}

func TestPrepareNextWarnsOnOutdatedNext(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "package.json"), []byte(`{"dependencies":{"next":"16.4.2"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	d := &Deploy{ID: "dep_1"}
	prepareNext(BuildRequest{Deploy: d, SrcDir: src, Log: &log, Env: map[string]string{nextAdapterEnv: "./mine.js"}}, map[string]string{})
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "Next.js 16.4.2 is older than") || !strings.Contains(log.String(), "==> warning: Next.js 16.4.2") {
		t.Fatalf("warnings %q\nlog %s", d.Warnings, log.String())
	}
	// A patched version: no warning, and no claim that it is safe.
	if err := os.WriteFile(filepath.Join(src, "package.json"), []byte(`{"dependencies":{"next":"99.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	d = &Deploy{ID: "dep_2"}
	prepareNext(BuildRequest{Deploy: d, SrcDir: src, Log: &log, Env: map[string]string{nextAdapterEnv: "./mine.js"}}, map[string]string{})
	if len(d.Warnings) != 0 || strings.Contains(strings.ToLower(log.String()), "safe") || strings.Contains(log.String(), "up to date") {
		t.Fatalf("warnings %q\nlog %s", d.Warnings, log.String())
	}
}

// Next.js versions with GHSA-pc58-hwjq-p385 get a fresh connection per
// request; fixed and unknown versions keep the pool.
func TestNextFreshConns(t *testing.T) {
	for v, want := range map[string]bool{
		"16.4.4": true, "16.0.0": true, "16.3.9": true, "15.5.28": true, "15.1.0": true, "14.2.35": true, "13.5.11": true,
		"16.4.5": false, "16.4.6": false, "16.5.0-canary.46": false, "15.5.29": false, "17.0.0": false, "": false, "latest": false,
	} {
		if got := nextFreshConns(v); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
	// The build records the version, and says when the box works around it.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "package.json"), []byte(`{"dependencies":{"next":"16.4.4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	d := &Deploy{ID: "dep_1"}
	warnOutdatedNext(BuildRequest{Deploy: d, SrcDir: src, Log: &log}, &log, time.Now())
	if d.NextVersion != "16.4.4" || !strings.Contains(log.String(), "fresh connection for every request") {
		t.Fatalf("version %q, log %s", d.NextVersion, log.String())
	}
}
