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

// TestNextBuildEnv deploys a Next.js app whose build reads the project's
// env, a secret and its services, and checks that nothing leaks:
//
//	the build connects to Postgres and Valkey as read-only users: it reads,
//	and every write fails, even after it turns off read-only transactions →
//	no secret value (the secret, the database, Valkey and S3 passwords the
//	instances get) is in any layer or the config of the built image, in the
//	Railpack plan or anywhere else in the deploy's work directory → the
//	client bundles carry NEXT_PUBLIC_* values and no server-only value → a
//	preview's build reads the preview's database branch.
func TestNextBuildEnv(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "nextenv", "bx")
	phase("up", start)

	app := filepath.Join(b.dir, "bx")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", "--exclude", ".next",
		filepath.Join(RepoRoot(), "templates", "hello-next")+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(app, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pub, secret := fmt.Sprintf("pubmark%x", time.Now().UnixNano()), fmt.Sprintf("srvmark%x", time.Now().UnixNano())
	write("tiffin.config.ts", `import { defineConfig } from "@shiptiffin/sdk";
export default defineConfig({
  project: "bx",
  apps: { web: { framework: "next", healthcheck: "/api/health", env: { NEXT_PUBLIC_MARK: "`+pub+`" } } },
  services: { postgres: {}, valkey: {}, storage: { buckets: { media: {} } } },
});
`)
	pkg, _ := os.ReadFile(filepath.Join(app, "package.json"))
	write("package.json", strings.Replace(string(pkg), `"build": "next build"`, `"build": "bun buildcheck.mjs && next build"`, 1))
	write("buildcheck.mjs", buildCheck)
	write("app/mark/page.tsx", markPage)
	write("app/mark/marks.tsx", markClient)

	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: build env")
	b.waitReady("app/web", "service/postgres", "service/valkey", "service/storage")
	b.ok("secrets", "set", "bx", "SERVER_MARK", "--value", secret)
	raw, _ := json.Marshal(map[string]any{"sql": "create table marks (v text); insert into marks values ('a'), ('b'), ('c')"})
	b.ok("sql", "bx", "--write", "--body", string(raw))

	p := time.Now()
	t.Cleanup(func() {
		if t.Failed() {
			ds, _ := b.ok("deploys", "list", "bx", "web")["deploys"].([]any)
			for _, x := range ds {
				if d, _ := x.(map[string]any); d["status"] == "failed" {
					log := b.ok("deploys", "build-log", "bx", "web", fmt.Sprint(d["id"]))
					t.Logf("build log of %s:\n%s", d["id"], tail(fmt.Sprint(log["text"]), 60))
				}
			}
		}
	})
	d := deployArgs(t, b, app, "--app", "web")
	phase("deploy", p)
	c := b.https()
	site := b.url("bx")
	check := func(u, wantDB string) {
		t.Helper()
		code, _, body := b.get(c, "GET", u+"/buildcheck.json", nil)
		var got map[string]any
		if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatalf("buildcheck.json: %d %s", code, body)
		}
		t.Logf("the build saw: %s", body)
		if got["user"] != "p_bx__read" || got["db"] != wantDB || fmt.Sprint(got["rows"]) != "3" || fmt.Sprint(got["secret"]) != fmt.Sprint(len(secret)) {
			t.Errorf("the build must read %s as the read-only role, and see the secret: %v", wantDB, got)
		}
		for _, k := range []string{"insert", "forced", "ddl"} {
			if v := fmt.Sprint(got[k]); !strings.HasPrefix(v, "refused") {
				t.Errorf("a build's %s must fail: %s", k, v)
			}
		}
		if v := fmt.Sprint(got["valkeyGet"]); v != "ok" {
			t.Errorf("a build reads Valkey: %s", v)
		}
		if v := fmt.Sprint(got["valkeySet"]); !strings.HasPrefix(v, "refused") {
			t.Errorf("a build's Valkey write must fail: %s", v)
		}
	}
	check(site, "p_bx")
	if code, _, body := b.get(c, "GET", site+"/mark", nil); code != 200 || !strings.Contains(body, pub) || strings.Contains(body, secret) {
		t.Errorf("/mark: %d, want the public mark and no secret\n%s", code, head(body))
	}

	// The values the app's instances get that must stay out of the image.
	env := b.inBox(`c=$(sudo nerdctl -n tiffin ps --format '{{.Names}}' | grep '^tf\.bx\.web\.prod\.' | head -1); sudo nerdctl -n tiffin inspect "$c" --format '{{json .Config.Env}}'`)
	var vars []string
	_ = json.Unmarshal([]byte(env), &vars)
	secrets := map[string]string{}
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "SERVER_MARK", "PGPASSWORD", "S3_SECRET_ACCESS_KEY":
			secrets[k] = v
		case "REDIS_URL":
			if i, j := strings.Index(v, "://"), strings.LastIndex(v, "@"); i >= 0 && j > i {
				if _, pw, ok := strings.Cut(v[i+3:j], ":"); ok {
					secrets["valkey password"] = pw
				}
			}
		}
	}
	if len(secrets) != 4 || secrets["SERVER_MARK"] != secret {
		t.Fatalf("instance env: found %d of 4 secret values in %s", len(secrets), env)
	}
	// grep -c prints a count per input; the sum is what matters.
	count := func(script string) int {
		t.Helper()
		n := 0
		for _, l := range strings.Fields(b.inBox(script)) {
			var x int
			fmt.Sscan(l, &x)
			n += x
		}
		return n
	}
	ref := "docker.io/tiffin/bx-web:" + strings.ToLower(d.ID)
	// Every blob of the saved image (layers, config, manifests), decompressed.
	b.inBox(`sudo bash -c 'set -e; d=/var/lib/tiffin/imgcheck; rm -rf $d; mkdir -p $d; nerdctl -n tiffin save -o $d/i.tar ` + ref + `'`)
	t.Cleanup(func() { b.inBox("sudo rm -rf /var/lib/tiffin/imgcheck") })
	imageCount := func(v string) int {
		t.Helper()
		return count(`sudo bash -c 'd=/var/lib/tiffin/imgcheck; for m in $(tar -tf $d/i.tar | grep -v /$); do tar -xOf $d/i.tar "$m" > $d/blob
  if gzip -t $d/blob 2>/dev/null; then gzip -dc $d/blob; else cat $d/blob; fi | grep -a -c -F "` + v + `" || true; done'`)
	}
	if n := imageCount(pub); n == 0 {
		t.Fatal("the image check sees nothing: the public mark, built into the client code, must be found")
	}
	work := "/var/lib/tiffin/runtime/deploys/bx/web/" + d.ID
	assets := "/var/lib/tiffin/runtime/assets/bx/web/prod/" + d.ID + "/www/_next/static"
	for name, v := range secrets {
		if n := imageCount(v); n != 0 {
			t.Errorf("%s is in the image (%d places)", name, n)
		}
		if n := count(`sudo grep -r -a -c -F '` + v + `' ` + work + ` | cut -d: -f2 || true`); n != 0 {
			t.Errorf("%s is in the deploy's work directory (Railpack plan, build log)", name)
		}
		if n := count(`sudo grep -r -a -c -F '` + v + `' ` + assets + ` | cut -d: -f2 || true`); n != 0 {
			t.Errorf("%s is in the client bundles", name)
		}
	}
	if n := count(`sudo grep -r -a -c -F '` + pub + `' ` + assets + ` | cut -d: -f2 || true`); n == 0 {
		t.Error("NEXT_PUBLIC_MARK is not in the client bundles")
	}
	if !strings.Contains(b.inBox("sudo ls "+work+"/plan"), "railpack-plan.json") {
		t.Error("no Railpack plan to check")
	}

	// A preview's build reads its own branch.
	p = time.Now()
	pv := deployArgs(t, b, app, "--app", "web", "--preview", "pr-1")
	phase("preview", p)
	check(pv.URL, "p_bx__pv_pr_1")
}

// buildCheck runs before next build: what the build can read and write.
const buildCheck = `import { RedisClient, SQL } from "bun";
import { mkdirSync, writeFileSync } from "node:fs";

const out = { secret: process.env.SERVER_MARK?.length ?? 0 };
const why = (e) => "refused: " + String(e.errno ?? "") + " " + e.message;
const db = new SQL(process.env.DATABASE_URL);
const [who] = await db` + "`select current_user as user, current_database() as db, (select count(*)::int from marks) as rows`" + `;
Object.assign(out, who);
try { await db.unsafe("insert into marks values ('build')"); out.insert = "allowed"; } catch (e) { out.insert = why(e); }
try { await db.unsafe("create table build_was_here (x int)"); out.ddl = "allowed"; } catch (e) { out.ddl = why(e); }
const c = await db.reserve();
try {
  await c.unsafe("set default_transaction_read_only = off");
  await c.unsafe("insert into marks values ('build')");
  out.forced = "allowed";
} catch (e) { out.forced = why(e); } finally { c.release(); }
await db.close();
const r = new RedisClient(process.env.REDIS_URL);
const key = (process.env.VALKEY_PREFIX ?? "") + "buildcheck";
try { await r.get(key); out.valkeyGet = "ok"; } catch (e) { out.valkeyGet = why(e); }
try { await r.set(key, "x"); out.valkeySet = "allowed"; } catch (e) { out.valkeySet = why(e); }
r.close();
mkdirSync("public", { recursive: true });
writeFileSync("public/buildcheck.json", JSON.stringify(out));
console.log("buildcheck", JSON.stringify(out));
`

const markPage = `import { Marks } from "./marks";

// Prerendered at build: the server reads a secret, and renders only its length.
export default function Page() {
  return (
    <main>
      <p id="len">{process.env.SERVER_MARK?.length ?? 0}</p>
      <Marks />
    </main>
  );
}
`

// The client code reads both; only the NEXT_PUBLIC_ one may be built into it.
// (A server-only value it renders would be in the prerendered HTML, through
// the app's own doing: the click handler only runs in the browser.)
const markClient = `"use client";

export function Marks() {
  return (
    <p id="marks" onClick={() => alert(String(process.env.SERVER_MARK))}>
      {process.env.NEXT_PUBLIC_MARK}
    </p>
  );
}
`
