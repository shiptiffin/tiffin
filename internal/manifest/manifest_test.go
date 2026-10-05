package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// updateGolden reports whether to rewrite goldens (-update or UPDATE_GOLDEN=1).
func updateGolden() bool { return *update || os.Getenv("UPDATE_GOLDEN") != "" }

// TestGolden evaluates every testdata/<case>/tiffin.config.ts and compares the
// canonical JSON (want.json) or the error text (want.err). An optional
// env.json provides process.env.
func TestGolden(t *testing.T) {
	old := evalTimeout
	evalTimeout = 300 * time.Millisecond
	t.Cleanup(func() { evalTimeout = old })

	dirs, err := filepath.Glob("testdata/*/tiffin.config.ts")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no golden cases found: %v", err)
	}
	for _, cfg := range dirs {
		dir := filepath.Dir(cfg)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			testGoldenCase(t, dir)
		})
	}
}

func testGoldenCase(t *testing.T, dir string) {
	var env map[string]string
	if b, err := os.ReadFile(filepath.Join(dir, "env.json")); err == nil {
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("env.json: %v", err)
		}
	}
	_, canon, err := Load(filepath.Join(dir, "tiffin.config.ts"), env)

	wantJSON, wantErr := filepath.Join(dir, "want.json"), filepath.Join(dir, "want.err")
	var got []byte
	var path, other string
	if err != nil {
		got, path, other = []byte(err.Error()+"\n"), wantErr, wantJSON
	} else {
		got, path, other = canon, wantJSON, wantErr
	}
	if updateGolden() {
		if werr := os.WriteFile(path, got, 0o644); werr != nil {
			t.Fatal(werr)
		}
		_ = os.Remove(other)
		return
	}
	want, rerr := os.ReadFile(path)
	if rerr != nil {
		if err != nil {
			t.Fatalf("expected success but got error (no %s): %v", filepath.Base(path), err)
		}
		t.Fatalf("missing golden %s (run with -update): %v", path, rerr)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// TestExamples makes sure the shipped example configs stay valid.
func TestExamples(t *testing.T) {
	cfg := filepath.Join("..", "..", "examples", "hello", "tiffin.config.ts")
	_, canon, err := Load(cfg, nil)
	if err != nil {
		t.Fatalf("examples/hello: %v", err)
	}
	golden := filepath.Join("testdata", "examples-hello.want.json")
	if updateGolden() {
		if err := os.WriteFile(golden, canon, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if !bytes.Equal(canon, want) {
		t.Errorf("examples/hello mismatch\n--- got ---\n%s\n--- want ---\n%s", canon, want)
	}
}

func TestEvaluateJSONFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tiffin.config.json")
	if err := os.WriteFile(p, []byte(`{"project":"jsonly","apps":{"web":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Evaluate(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "jsonly" || m.Apps["web"].Routes[0] != "web" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestEvaluateMJSAndEnvOption(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tiffin.config.mjs")
	src := `import { defineConfig } from "tiffin-sdk";
export default defineConfig({ project: process.env.NAME });`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Evaluate(p, WithEnv(map[string]string{"NAME": "from-env"}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "from-env" {
		t.Fatalf("project = %q", m.Project)
	}
	// Without env, project is undefined -> required-field error, not a crash.
	_, err = Evaluate(p)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %T: %v", err, err)
	}
}

func TestEvaluateMissingFile(t *testing.T) {
	_, err := Evaluate(filepath.Join(t.TempDir(), "nope.ts"))
	var ee *EvalError
	if !errors.As(err, &ee) {
		t.Fatalf("want EvalError, got %T: %v", err, err)
	}
}

func TestFindConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindConfig(dir); err == nil {
		t.Fatal("expected error for empty dir")
	}
	for _, n := range []string{"tiffin.config.json", "tiffin.config.js"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindConfig(dir)
	if err != nil || filepath.Base(got) != "tiffin.config.js" {
		t.Fatalf("FindConfig = %q, %v", got, err)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	for _, raw := range []string{``, `{`, `[]`, `"x"`, `{"project":"a"} {}`} {
		_, err := Parse([]byte(raw))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("Parse(%q): want ValidationError, got %v", raw, err)
		}
	}
}

func TestValidationErrorShape(t *testing.T) {
	_, err := Parse([]byte(`{"project":"ok","apps":{"web":{"instances":99}}}`))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(ve.Errors) != 1 || ve.Errors[0].Path != "/apps/web/instances" {
		t.Fatalf("errors = %+v", ve.Errors)
	}
	if !strings.HasPrefix(ve.Error(), "/apps/web/instances: ") {
		t.Fatalf("Error() = %q", ve.Error())
	}
}

func TestNormalizeDefaults(t *testing.T) {
	m, err := Parse([]byte(`{
		"project":"p",
		"apps":{
			"web":{},
			"site":{"framework":"static"},
			"w":{"role":"worker"}
		},
		"services":{"valkey":{},"postgres":{"extensions":["b","a","b"]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	web := m.Apps["web"]
	want := App{Path: ".", Framework: FrameworkBun, Role: RoleWeb, Routes: []string{"web"}, Instances: 1, Healthcheck: "/"}
	if !reflect.DeepEqual(web, want) {
		t.Errorf("web = %+v\nwant  %+v", web, want)
	}
	if s := m.Apps["site"]; s.Healthcheck != "" || !reflect.DeepEqual(s.Routes, []string{"site"}) {
		t.Errorf("static app = %+v", s)
	}
	if w := m.Apps["w"]; w.Healthcheck != "" || w.Routes != nil {
		t.Errorf("worker = %+v", w)
	}
	if m.Version != 1 || m.Services.Valkey.MaxMemoryMB != 64 {
		t.Errorf("version/valkey: %+v", m)
	}
	if !reflect.DeepEqual(m.Services.Postgres.Extensions, []string{"a", "b"}) {
		t.Errorf("extensions = %v", m.Services.Postgres.Extensions)
	}
}

func TestNormalizePlatformDefaults(t *testing.T) {
	m, err := Parse([]byte(`{
		"project":"p",
		"apps":{"w":{"role":"worker"}},
		"services":{"auth":{"methods":["otp","email","otp"]},"email":{},"analytics":{}},
		"crons":{"tick":{"schedule":"@daily","app":"w"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Auth{Methods: []string{"email", "otp"}, Organizations: true}); !reflect.DeepEqual(*m.Services.Auth, want) {
		t.Errorf("auth = %+v, want %+v", *m.Services.Auth, want)
	}
	if got := *m.Services.Email; got != (Email{}) {
		t.Errorf("email = %+v", got)
	}
	if m.Services.Analytics.RetentionDays != 365 {
		t.Errorf("analytics = %+v", m.Services.Analytics)
	}
	if want := (Cron{Schedule: "@daily", App: "w", Path: "/cron/tick"}); m.Crons["tick"] != want {
		t.Errorf("cron = %+v, want %+v", m.Crons["tick"], want)
	}
	// Explicit organizations:false survives; absent means true.
	m, err = Parse([]byte(`{"project":"p","services":{"auth":{"organizations":false}}}`))
	if err != nil || m.Services.Auth.Organizations || !reflect.DeepEqual(m.Services.Auth.Methods, DefaultAuthMethods) {
		t.Errorf("auth organizations:false: %+v, %v", m.Services.Auth, err)
	}
}

func TestNormalizeQueuesTopics(t *testing.T) {
	m, err := Parse([]byte(`{
		"project":"p",
		"apps":{"w":{"role":"worker"}},
		"queues":{
			"plain":{"app":"w"},
			"rated":{"app":"w","path":"/x","rateLimit":10,"concurrency":2},
			"slow":{"app":"w","rateLimit":1,"ratePeriodSeconds":3600,"maxAttempts":2,"leaseSeconds":5}
		},
		"topics":{"a.b":{"subscribers":["slow","plain","slow"]},"empty":{"subscribers":[]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Queue{
		"plain": {App: "w", Path: "/queues/plain", MaxAttempts: 8, LeaseSeconds: 60},
		"rated": {App: "w", Path: "/x", Concurrency: 2, RateLimit: 10, RatePeriodSeconds: 60, MaxAttempts: 8, LeaseSeconds: 60},
		"slow":  {App: "w", Path: "/queues/slow", RateLimit: 1, RatePeriodSeconds: 3600, MaxAttempts: 2, LeaseSeconds: 5},
	}
	if !reflect.DeepEqual(m.Queues, want) {
		t.Errorf("queues = %+v\nwant    %+v", m.Queues, want)
	}
	if got := m.Topics["a.b"].Subscribers; !reflect.DeepEqual(got, []string{"plain", "slow"}) {
		t.Errorf("subscribers = %v", got)
	}
	if got := m.Topics["empty"]; got.Subscribers != nil {
		t.Errorf("empty topic = %+v", got)
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	m, err := Parse([]byte(`{"project":"p","apps":{"a":{"routes":["Foo.com/x/"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Canonical(m)
	Normalize(m)
	after, _ := Canonical(m)
	if !bytes.Equal(before, after) {
		t.Fatalf("Normalize not idempotent:\n%s\n%s", before, after)
	}
}

func TestSchemaEmbedded(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(Schema(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["$id"] != SchemaID || doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("bad $id/$schema: %v %v", doc["$id"], doc["$schema"])
	}
	disk, err := os.ReadFile("schema.json")
	if err != nil || !bytes.Equal(disk, Schema()) {
		t.Fatalf("schema.json on disk differs from embedded copy: %v", err)
	}
	if _, err := schema(); err != nil {
		t.Fatalf("schema does not compile: %v", err)
	}
}

// TestSchemaMatchesTypes guards against drift between manifest.go and
// schema.json: every JSON field of every manifest type must be a schema
// property with a description, and vice versa.
func TestSchemaMatchesTypes(t *testing.T) {
	var doc map[string]any
	_ = json.Unmarshal(Schema(), &doc)
	defs := doc["$defs"].(map[string]any)
	root := doc

	storage := get(defs, "services", "properties", "storage")
	checks := []struct {
		typ    reflect.Type
		schema map[string]any
	}{
		{reflect.TypeOf(Manifest{}), root},
		{reflect.TypeOf(App{}), defs["app"].(map[string]any)},
		{reflect.TypeOf(Resources{}), defs["resources"].(map[string]any)},
		{reflect.TypeOf(Services{}), defs["services"].(map[string]any)},
		{reflect.TypeOf(Postgres{}), get(defs, "services", "properties", "postgres")},
		{reflect.TypeOf(Valkey{}), get(defs, "services", "properties", "valkey")},
		{reflect.TypeOf(Storage{}), storage},
		{reflect.TypeOf(Bucket{}), get(storage, "properties", "buckets", "additionalProperties")},
		{reflect.TypeOf(Auth{}), get(defs, "services", "properties", "auth")},
		{reflect.TypeOf(Email{}), get(defs, "services", "properties", "email")},
		{reflect.TypeOf(Analytics{}), get(defs, "services", "properties", "analytics")},
		{reflect.TypeOf(Cron{}), defs["cron"].(map[string]any)},
		{reflect.TypeOf(Queue{}), defs["queue"].(map[string]any)},
		{reflect.TypeOf(Topic{}), defs["topic"].(map[string]any)},
	}
	for _, c := range checks {
		props := c.schema["properties"].(map[string]any)
		seen := map[string]bool{}
		for i := range c.typ.NumField() {
			name, _, _ := strings.Cut(c.typ.Field(i).Tag.Get("json"), ",")
			seen[name] = true
			p, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s.%s missing from schema", c.typ.Name(), name)
				continue
			}
			if p["description"] == nil {
				t.Errorf("%s.%s has no description in schema", c.typ.Name(), name)
			}
		}
		for name := range props {
			if !seen[name] {
				t.Errorf("schema property %s.%s has no Go field", c.typ.Name(), name)
			}
		}
	}
}

func get(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		m = m[p].(map[string]any)
	}
	return m
}

// TestCanonicalRoundTrip checks Canonical(Parse(Canonical(x))) == Canonical(x)
// for randomly generated valid manifests.
func TestCanonicalRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 300 {
		m := randomManifest(r)
		Normalize(m)
		if err := Validate(m); err != nil {
			t.Fatalf("generated manifest %d invalid: %v\n%+v", i, err, m)
		}
		c1, err := Canonical(m)
		if err != nil {
			t.Fatal(err)
		}
		m2, err := Parse(c1)
		if err != nil {
			t.Fatalf("case %d: reparse: %v\n%s", i, err, c1)
		}
		c2, err := Canonical(m2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(c1, c2) {
			t.Fatalf("case %d unstable:\n%s\n---\n%s", i, c1, c2)
		}
	}
}

func FuzzParseCanonicalStable(f *testing.F) {
	f.Add([]byte(`{"project":"a"}`))
	f.Add([]byte(`{"project":"a","apps":{"w":{"role":"worker"},"x":{"routes":["X.com/"]}},"services":{"postgres":{"extensions":["b","a"]}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Parse(raw)
		if err != nil {
			return
		}
		c1, err := Canonical(m)
		if err != nil {
			t.Fatal(err)
		}
		m2, err := Parse(c1)
		if err != nil {
			t.Fatalf("canonical output does not reparse: %v\n%s", err, c1)
		}
		c2, _ := Canonical(m2)
		if !bytes.Equal(c1, c2) {
			t.Fatalf("unstable:\n%s\n%s", c1, c2)
		}
	})
}

func randomManifest(r *rand.Rand) *Manifest {
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	m := &Manifest{Project: pick("a", "shop", "my-app-2", "z9")}
	if r.IntN(2) == 0 {
		m.Version = 1
	}
	if r.IntN(3) == 0 {
		m.Resources = &Resources{}
		if r.IntN(2) == 0 {
			m.Resources.MemoryMB = 128 + r.IntN(16000)
		}
		if r.IntN(2) == 0 {
			m.Resources.CPUs = float64(1+r.IntN(64)) / 4
		}
		if r.IntN(2) == 0 {
			m.Resources.MaxSharePercent = 5 + r.IntN(96)
		}
	}
	names := []string{"web", "api", "jobs", "docs", "admin", "x1"}
	r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	m.Apps = map[string]App{}
	usedRoutes := map[string]bool{}
	for _, n := range names[:r.IntN(len(names)+1)] {
		a := App{}
		if r.IntN(2) == 0 {
			a.Path = pick("apps/"+n, ".", "services/x")
		}
		if r.IntN(2) == 0 {
			a.Framework = Framework(pick("next", "hono", "bun", "static"))
		}
		if r.IntN(4) == 0 {
			a.Role = RoleWorker
		} else if r.IntN(2) == 0 {
			a.Role = RoleWeb
			for range r.IntN(3) {
				rt := pick("h1", "h2", "H3.example.com", "ex.com/api", "ex.com/v1/", "t.dev") + "-" + n
				if !usedRoutes[normalizeRoute(rt)] {
					usedRoutes[normalizeRoute(rt)] = true
					a.Routes = append(a.Routes, rt)
				}
			}
		}
		if r.IntN(2) == 0 {
			a.Instances = 1 + r.IntN(16)
		}
		if r.IntN(2) == 0 {
			a.MemoryMB = 64 + r.IntN(8129)
		}
		if r.IntN(3) == 0 {
			a.Healthcheck = pick("/", "/healthz", "/a/b")
		}
		if r.IntN(2) == 0 {
			a.Env = map[string]string{pick("FOO", "_BAR", "A1"): pick("", "x", "<&>", "é\n")}
		}
		m.Apps[n] = a
	}
	if r.IntN(2) == 0 {
		m.Env = map[string]string{"LOG": pick("info", "debug"), "Z_9": "1"}
	}
	if r.IntN(2) == 0 {
		m.Services.Postgres = &Postgres{}
		for range r.IntN(4) {
			m.Services.Postgres.Extensions = append(m.Services.Postgres.Extensions, pick("vector", "pg_cron", "uuid-ossp", "citext"))
		}
		if r.IntN(3) == 0 {
			m.Services.Postgres.StatementTimeoutSeconds = 1 + r.IntN(3600)
		}
	}
	if r.IntN(2) == 0 {
		m.Services.Valkey = &Valkey{}
		if r.IntN(2) == 0 {
			m.Services.Valkey.MaxMemoryMB = 1 + r.IntN(1000)
		}
	}
	if r.IntN(2) == 0 {
		m.Services.Storage = &Storage{}
		if r.IntN(2) == 0 {
			m.Services.Storage.Buckets = map[string]Bucket{"up": {}, "pub": {Public: true}}
		}
	}
	if r.IntN(2) == 0 {
		m.Services.Auth = &Auth{Organizations: r.IntN(2) == 0}
		for range r.IntN(4) {
			m.Services.Auth.Methods = append(m.Services.Auth.Methods, pick(AuthMethods...))
		}
	}
	if r.IntN(2) == 0 {
		m.Services.Email = &Email{From: pick("", "hi@example.com", "a.b+c@mail.example.org")}
	}
	if r.IntN(2) == 0 {
		m.Services.Analytics = &Analytics{}
		if r.IntN(2) == 0 {
			m.Services.Analytics.RetentionDays = 1 + r.IntN(3650)
		}
	}
	if len(m.Apps) > 0 && r.IntN(2) == 0 {
		m.Crons = map[string]Cron{}
		apps := sortedKeys(m.Apps)
		for range 1 + r.IntN(3) {
			c := Cron{Schedule: pick("@hourly", "@daily", "*/5 * * * *", "0 3 * * mon"), App: apps[r.IntN(len(apps))]}
			if r.IntN(2) == 0 {
				c.Path = pick("/tick", "/a/b")
			}
			m.Crons[pick("tick", "nightly", "c-1")] = c
		}
	}
	if len(m.Apps) > 0 && r.IntN(2) == 0 {
		m.Queues = map[string]Queue{}
		apps := sortedKeys(m.Apps)
		for _, n := range []string{"emails", "orders", "q-1"}[:1+r.IntN(3)] {
			q := Queue{App: apps[r.IntN(len(apps))]}
			if r.IntN(2) == 0 {
				q.Path = pick("/jobs/x", "/a/b")
			}
			if r.IntN(2) == 0 {
				q.Concurrency, q.KeyConcurrency = r.IntN(1001), r.IntN(1001)
			}
			if r.IntN(2) == 0 {
				q.RateLimit = 1 + r.IntN(10000)
				if r.IntN(2) == 0 {
					q.RatePeriodSeconds = 1 + r.IntN(86400)
				}
			}
			if r.IntN(2) == 0 {
				q.MaxAttempts = 1 + r.IntN(100)
			}
			if r.IntN(2) == 0 {
				q.LeaseSeconds = 5 + r.IntN(3596)
			}
			m.Queues[n] = q
		}
		if r.IntN(2) == 0 {
			m.Topics = map[string]Topic{}
			qs := sortedKeys(m.Queues)
			for _, n := range []string{"order.created", "user", "a-b.c"}[:1+r.IntN(3)] {
				t := Topic{}
				for range r.IntN(4) {
					t.Subscribers = append(t.Subscribers, qs[r.IntN(len(qs))])
				}
				m.Topics[n] = t
			}
		}
	}
	return m
}

func TestWarnings(t *testing.T) {
	m, err := Parse([]byte(`{"project":"p","env":{"LOG_LEVEL":"debug","DATABASE_URL":"x"},
		"apps":{"web":{"env":{"S3_PUBLIC_ENDPOINT":"y"}}},"services":{"postgres":{},"auth":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	w := Warnings(m)
	if len(w) != 3 || !strings.Contains(w[0], "add `email: {}`") || !strings.Contains(w[1], "env sets DATABASE_URL") ||
		!strings.Contains(w[2], "apps.web.env sets S3_PUBLIC_ENDPOINT") {
		t.Fatalf("warnings = %q", w)
	}
	m, _ = Parse([]byte(`{"project":"p","services":{"postgres":{},"email":{},"auth":{"methods":["passkey"]}}}`))
	if w := Warnings(m); len(w) != 0 {
		t.Fatalf("clean manifest warned: %q", w)
	}
}

// auth.emailVerification: unset means automatic (kept unset), an explicit
// value survives parsing and rendering, and false warns in plans.
func TestAuthEmailVerification(t *testing.T) {
	m, err := Parse([]byte(`{"project":"p","services":{"postgres":{},"email":{},"auth":{}}}`))
	if err != nil || m.Services.Auth.EmailVerification != nil || len(Warnings(m)) != 0 {
		t.Fatalf("unset: %+v %v %v", m.Services.Auth, err, Warnings(m))
	}
	m, err = Parse([]byte(`{"project":"p","services":{"postgres":{},"email":{},"auth":{"emailVerification":false}}}`))
	if err != nil || m.Services.Auth.EmailVerification == nil || *m.Services.Auth.EmailVerification {
		t.Fatalf("false: %+v %v", m.Services.Auth, err)
	}
	if w := Warnings(m); len(w) != 1 || !strings.Contains(w[0], "emailVerification is false") {
		t.Fatalf("warnings: %v", w)
	}
	if src := string(RenderConfig(m, "")); !strings.Contains(src, "emailVerification: false") {
		t.Fatalf("render: %s", src)
	}
}
