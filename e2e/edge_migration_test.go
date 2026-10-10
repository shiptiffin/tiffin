//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// layoutBefore is the last commit whose box ran the HTTPS edge inside the
// tiffin service (before tiffin-edge). TIFFIN_E2E_OLD_REF overrides it.
const layoutBefore = "989305e"

// TestEdgeMigration is the first update of a box from the old layout (the
// edge inside tiffin) to the edge's own service, under steady app load:
// a build that cannot run at all, then one that installs but fails its
// health check, each leave the box in the old layout, serving; then a good
// build moves it to the new layout. Failed requests and the longest gap
// are reported for each.
func TestEdgeMigration(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	ref := layoutBefore
	if r := os.Getenv("TIFFIN_E2E_OLD_REF"); r != "" {
		ref = r
	}

	// ---- builds: the old layout's CLI and box build, the new ones, two broken ones ----
	p := time.Now()
	dir := t.TempDir()
	src := filepath.Join(dir, "old-src")
	_ = os.MkdirAll(src, 0o755)
	archive := exec.Command("sh", "-c", "git archive "+ref+" | tar -x -C "+src)
	archive.Dir = RepoRoot()
	if out, err := archive.CombinedOutput(); err != nil {
		t.Fatalf("git archive %s: %v\n%s", ref, err, out)
	}
	oldDir, newDir := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	_ = os.MkdirAll(oldDir, 0o755)
	_ = os.MkdirAll(newDir, 0o755)
	oldCLI, oldBin := buildTiffinFrom(t, src, oldDir, "", ""), buildTiffinFrom(t, src, oldDir, "linux", "0.0.1-old")
	newCLI, newBin := buildTiffin(t, newDir, "", ""), buildTiffin(t, newDir, "linux", "0.0.2-new")
	// Cannot run at all: provisions "fine", fails everything else.
	dead := filepath.Join(dir, "dead")
	// Installs and runs the edge, but tiffin serve exits: unhealthy.
	unhealthy := filepath.Join(dir, "unhealthy")
	for f, body := range map[string]string{
		dead:      "#!/bin/sh\n[ \"$1\" = provision ] && exit 0\necho broken >&2\nexit 1\n",
		unhealthy: "#!/bin/sh\n[ \"$1\" = serve ] && { echo 'broken build' >&2; exit 1; }\nexec /opt/tiffin-real \"$@\"\n",
	} {
		if err := os.WriteFile(f, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	phase("builds", p)

	// ---- an old-layout box with an app ----
	p = time.Now()
	b := newCLIBoxFrom(t, dir, "steady", oldCLI, oldBin)
	b.inBox("sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt")
	app := filepath.Join(dir, "steady")
	_ = os.MkdirAll(app, 0o755)
	for f, body := range map[string]string{
		"index.ts":         strings.ReplaceAll(steadyApp, "VERSION", "v1"),
		"package.json":     `{"name":"steady","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`,
		"tiffin.config.ts": `export default { project: "steady", apps: { web: { instances: 2, routes: ["steady"] } } };` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(app, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan := b.ok("plan", app)
	b.ok("apply", app, "--confirm", plan["hash"].(string), "-m", "e2e migration")
	deploy(t, b, app)
	b.cli = newCLI // from here the new CLI drives the box, as after an upgrade
	installLoadgen(t, b)
	if out, err := exec.Command("limactl", "copy", newBin, b.instance+":/tmp/tiffin-real").CombinedOutput(); err != nil {
		t.Fatalf("copy the real build: %v\n%s", err, out)
	}
	b.inBox("sudo install -m 0755 /tmp/tiffin-real /opt/tiffin-real")
	phase("old box", p)

	oldLayout := func(when string) {
		t.Helper()
		unit := b.inBox("cat /etc/systemd/system/tiffin.service")
		if !strings.Contains(unit, " --edge ") || strings.Contains(unit, "--edge-external") {
			t.Fatalf("%s: tiffin.service is not the old one:\n%s", when, unit)
		}
		if got := b.inBox("ls /etc/systemd/system | grep -c tiffin-edge || true"); got != "0" {
			t.Fatalf("%s: edge units left behind (%s)", when, got)
		}
		if got := b.inBox("systemctl is-active tiffin || true"); got != "active" {
			t.Fatalf("%s: tiffin is %s", when, got)
		}
		if got := b.inBox("systemctl is-active tiffin-edge.socket || true"); got == "active" {
			t.Fatalf("%s: the edge socket is still active", when)
		}
		if v := b.ok("health")["version"]; v != "0.0.1-old" {
			t.Fatalf("%s: version %v", when, v)
		}
	}
	oldLayout("before")
	loads := &steadyLoads{t: t, b: b, phase: phase}
	loads.run("old layout baseline", func() { time.Sleep(5 * time.Second) })

	// ---- a build that cannot run: nothing changes ----
	r := loads.run("dead build", func() {
		if code, out := b.run("up", "--provider", "local", "--binary", dead); code == 0 {
			t.Fatalf("a dead build updated the box: %s", out)
		}
		b.inBox(healthyScript)
	})
	oldLayout("after the dead build")
	if r.Failed > 0 {
		t.Errorf("dead build: %d of %d requests failed", r.Failed, r.Requests)
	}

	// ---- a build that installs but fails its health check: rolled back to the old layout ----
	loads.run("unhealthy build", func() {
		code, out := b.run("up", "--provider", "local", "--binary", unhealthy)
		if code == 0 || !strings.Contains(out, "rolled back") {
			t.Fatalf("an unhealthy build must be rolled back: %d %s", code, out)
		}
		b.inBox(healthyScript)
	})
	oldLayout("after the rollback")
	if body := b.inBox(`curl -s --cacert /tmp/ca.crt https://steady.tiffin.localhost:8443/x`); !strings.Contains(body, "steady v1") {
		t.Fatalf("app after the rollback: %q", body)
	}

	// ---- the good build: the edge gets its own service ----
	r = loads.run("migration (tiffin up)", func() {
		b.ok("up", "--provider", "local", "--binary", newBin)
		b.inBox(healthyScript)
	})
	unit := b.inBox("cat /etc/systemd/system/tiffin.service")
	if !strings.Contains(unit, "--edge-external") {
		t.Fatalf("after the migration tiffin.service is:\n%s", unit)
	}
	if got := b.inBox(`systemctl is-active tiffin tiffin-edge tiffin-edge.socket | tr '\n' ' '`); got != "active active active" {
		t.Fatalf("after the migration: %q", got)
	}
	if v := b.ok("health")["version"]; v != "0.0.2-new" {
		t.Fatalf("after the migration: version %v", v)
	}
	if b.inBox("sudo test -e /var/lib/tiffin/platform/units-before && echo left || echo gone") != "gone" {
		t.Error("the unit backup outlived a good update")
	}
	if r.OK == 0 {
		t.Fatal("no request succeeded across the migration")
	}

	// ---- and now a restart of tiffin costs the app nothing ----
	r = loads.run("restart after migration", func() { b.inBox("sudo systemctl restart tiffin\n" + healthyScript) })
	if r.Failed > 0 {
		t.Errorf("restart after the migration: %d of %d requests failed", r.Failed, r.Requests)
	}
	for _, l := range loads.report {
		t.Logf("SUMMARY %s", l)
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
