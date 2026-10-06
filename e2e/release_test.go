//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRelease covers the database side of deploys on a fresh box:
//
//	a release command creates a table before the new version takes traffic
//	→ a release that fails stops the deploy and the live version keeps
//	serving → a preview gets its own database branch, migrates it without
//	touching production's schema, and the branch goes with the preview →
//	a Next.js app inlines NEXT_PUBLIC_ values, and changing one (a secret)
//	rebuilds it so the browser bundle has the new value.
func TestRelease(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "release", "relx")
	phase("up", start)

	root := filepath.Join(b.dir, "relx")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("api/package.json", `{"name":"relx-api","private":true,"scripts":{"start":"bun index.ts"}}`)
	write("api/index.ts", relApi("v1"))
	write("api/migrate.ts", relMigrate(`create table if not exists notes (id serial primary key, body text)`))
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", "--exclude", ".next", "--exclude", "tiffin.config.ts",
		filepath.Join(RepoRoot(), "templates", "hello-next")+"/", filepath.Join(root, "web")+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	write("web/app/greet/page.tsx", `"use client";

export default function Greet() {
  return <p id="greeting">{"greeting:" + process.env.NEXT_PUBLIC_GREETING + " url:" + process.env.NEXT_PUBLIC_TIFFIN_URL}</p>;
}
`)
	write("tiffin.config.json", `{"project":"relx","services":{"postgres":{}},"apps":{
	  "api":{"framework":"bun","path":"api","routes":["relx"],"release":"bun migrate.ts"},
	  "web":{"framework":"next","path":"web","routes":["relx-web"],"healthcheck":"/api/health","env":{"NEXT_PUBLIC_GREETING":"greeting-one-e2e"}}}}`)
	plan := b.ok("plan", root)
	hash, _ := plan["hash"].(string)
	b.ok("apply", root, "--confirm", hash, "-m", "e2e: release")
	b.waitReady("service/postgres", "app/api", "app/web")
	c := b.https()
	buildLog := func(app, id string) string { return fmt.Sprint(b.ok("deploys", "build-log", "relx", app, id)["text"]) }
	sql := func(query, branch string) string {
		t.Helper()
		body := map[string]any{"sql": query}
		if branch != "" {
			body["branch"] = branch
		}
		raw, _ := json.Marshal(body)
		res := b.ok("sql", "relx", "--body", string(raw))
		r, _ := res["results"].([]any)
		if len(r) == 0 {
			t.Fatalf("sql %q: %v", query, res)
		}
		out, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
		return string(out)
	}
	const notesTable = `select to_regclass('public.notes')::text`
	const previewTable = `select to_regclass('public.preview_only')::text`

	// ---- the release command migrates before the new version serves ----
	p := time.Now()
	d1 := deployArgs(t, b, root, "--app", "api")
	phase("api deploy", p)
	log := buildLog("api", d1.ID)
	t.Logf("api build log (release lines):\n%s", grepLines(log, "==> release", "migrated"))
	if !strings.Contains(log, "==> release: bun migrate.ts") || !strings.Contains(log, "migrated on p_relx") || !strings.Contains(log, "==> release done") ||
		strings.Index(log, "==> release done") > strings.Index(log, "==> starting") {
		t.Fatalf("the release must run before the instances start:\n%s", log)
	}
	if got := sql(notesTable, ""); got != `[["notes"]]` {
		t.Fatalf("the release did not create notes: %s", got)
	}
	if code, _, body := b.get(c, "GET", b.url("relx")+"/", nil); code != 200 || !strings.Contains(body, "v1 tables=1") ||
		!strings.Contains(body, "direct=true") || strings.Contains(body, "pool=undefined") {
		t.Fatalf("api: %d %s", code, body)
	}

	// ---- a failing release stops the deploy; v1 keeps serving ----
	p = time.Now()
	write("api/index.ts", relApi("v2"))
	write("api/migrate.ts", relMigrate(`create table notes (id int)`))
	code, out := b.run("deploy", root, "--app", "api")
	if code == 0 || !strings.Contains(out, "release command") {
		t.Fatalf("a failing release must fail the deploy: exit %d\n%s", code, out)
	}
	if code, _, body := b.get(c, "GET", b.url("relx")+"/", nil); code != 200 || !strings.Contains(body, "v1 ") {
		t.Fatalf("v1 must keep serving after a failed release: %d %s", code, body)
	}
	phase("failed release", p)

	// ---- a preview migrates its own branch, not production ----
	p = time.Now()
	write("api/index.ts", relApi("preview"))
	write("api/migrate.ts", relMigrate(`create table if not exists notes (id serial primary key, body text)`, `create table if not exists preview_only (id int)`))
	pv := deployArgs(t, b, root, "--app", "api", "--preview", "pr-1")
	phase("preview deploy", p)
	if log := buildLog("api", pv.ID); !strings.Contains(log, "gets branch pv-pr-1") || !strings.Contains(log, "migrated on p_relx__pv_pr_1") {
		t.Fatalf("preview build log:\n%s", log)
	}
	if code, _, body := b.get(c, "GET", b.url("pr-1--relx")+"/", nil); code != 200 || !strings.Contains(body, "preview tables=2") || !strings.Contains(body, "db=p_relx__pv_pr_1") {
		t.Fatalf("preview: %d %s", code, body)
	}
	if got := sql(previewTable, ""); got != `[[null]]` {
		t.Fatalf("the preview's migration reached production: %s", got)
	}
	if got := sql(previewTable, "pv-pr-1"); got != `[["preview_only"]]` {
		t.Fatalf("the preview's branch lacks its table: %s", got)
	}
	if _, out := b.run("branches", "list", "relx"); !strings.Contains(out, "pv-pr-1") {
		t.Fatalf("branches: %s", out)
	}
	b.ok("previews", "delete", "relx", "api", "pr-1")
	if _, out := b.run("branches", "list", "relx"); strings.Contains(out, "pv-pr-1") {
		t.Fatalf("the branch outlived its preview: %s", out)
	}
	if code, _, body := b.get(c, "GET", b.url("relx")+"/", nil); code != 200 || !strings.Contains(body, "v1 tables=1") {
		t.Fatalf("production after the preview: %d %s", code, body)
	}
	phase("preview branch", p)

	// ---- NEXT_PUBLIC_: inlined at build, a change rebuilds ----
	p = time.Now()
	w1 := deployArgs(t, b, root, "--app", "web")
	phase("next deploy", p)
	greet := func(want string) {
		t.Helper()
		code, _, page := b.get(c, "GET", b.url("relx-web")+"/greet", nil)
		if code != 200 {
			t.Fatalf("/greet: %d %.300s", code, page)
		}
		for _, u := range chunkURLs(page) {
			if _, _, js := b.get(c, "GET", b.url("relx-web")+u, nil); strings.Contains(js, want) {
				if !strings.Contains(js, "https://relx-web.tiffin.localhost") {
					t.Errorf("chunk %s has %q but not NEXT_PUBLIC_TIFFIN_URL", u, want)
				}
				return
			}
		}
		t.Fatalf("no client chunk of /greet has %q:\n%.2000s", want, page)
	}
	greet("greeting-one-e2e")
	p = time.Now()
	b.ok("secrets", "set", "relx", "NEXT_PUBLIC_GREETING", "--value", "greeting-two-e2e")
	rb := b.waitDeployWhere("relx", "web", 10*time.Minute, func(x map[string]any) bool { return x["trigger"] == "env" && x["id"].(string) > w1.ID })
	phase("next rebuild", p)
	if log := buildLog("web", rb["id"].(string)); !strings.Contains(log, "==> rebuild of "+w1.ID) || !strings.Contains(log, "NEXT_PUBLIC_GREETING") {
		t.Errorf("rebuild log:\n%s", grepLines(log, "==> "))
	}
	greet("greeting-two-e2e")
}

// relApi is the API: its version, how many tables its database has and
// what the box told it about connections.
func relApi(version string) string {
	return `import { sql } from "bun";

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch() {
    const [{ n }] = await sql` + "`select count(*)::int as n from information_schema.tables where table_schema = 'public'`" + `;
    const env = process.env;
    return new Response(` + "`" + version + " tables=${n} db=${env.PGDATABASE} pool=${env.DATABASE_POOL_MAX} direct=${env.DIRECT_DATABASE_URL === env.DATABASE_URL}`" + `);
  },
});
`
}

// relMigrate is a release command that runs statements in order.
func relMigrate(statements ...string) string {
	var s strings.Builder
	s.WriteString("import { sql } from \"bun\";\n\n")
	for _, st := range statements {
		s.WriteString("await sql.unsafe(" + fmt.Sprintf("%q", st) + ");\n")
	}
	s.WriteString("console.log(\"migrated on \" + process.env.PGDATABASE);\nawait sql.close();\n")
	return s.String()
}
