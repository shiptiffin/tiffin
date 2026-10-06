package manifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // cron time zones validate the same on every machine
)

// FieldError is one thing wrong with a manifest.
type FieldError struct {
	// Path is a JSON pointer to the offending value, e.g. "/apps/web/instances".
	// The empty string is the document root.
	Path string `json:"path"`
	// Message says what is wrong and, where possible, how to fix it.
	Message string `json:"message"`
}

// ValidationError lists every problem found in a manifest. Errors are sorted
// by path so output is deterministic.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

// Error joins all field errors, one per line, as "<pointer>: <message>".
func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		p := fe.Path
		if p == "" {
			p = "/"
		}
		lines[i] = p + ": " + fe.Message
	}
	return strings.Join(lines, "\n")
}

func newValidationError(errs []FieldError) *ValidationError {
	slices.SortStableFunc(errs, func(a, b FieldError) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Message, b.Message)
	})
	errs = slices.CompactFunc(errs, func(a, b FieldError) bool { return a == b })
	return &ValidationError{Errors: errs}
}

// Validate checks a manifest against the JSON Schema and the semantic rules
// the schema cannot express (workers have no routes, routes are unique
// across apps, the email sender is an address, crons and queues target real apps, topics subscribe real queues). It expects a normalized manifest: zero values of defaulted
// fields (instances 0, memoryMB 0) are reported as errors. It returns nil or
// a *ValidationError.
func Validate(m *Manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("manifest: marshal: %w", err)
	}
	doc, err := decodeAny(raw)
	if err != nil {
		return fmt.Errorf("manifest: re-decode: %w", err)
	}
	errs, err := validateSchema(doc)
	if err != nil {
		return err
	}
	errs = append(errs, keyErrors(doc)...)
	errs = append(errs, semanticErrors(m)...)
	if len(errs) == 0 {
		return nil
	}
	return newValidationError(errs)
}

// semanticErrors implements the rules JSON Schema cannot express.
func semanticErrors(m *Manifest) []FieldError {
	var errs []FieldError
	owners := map[string]string{} // normalized route -> owning app
	for _, name := range sortedKeys(m.Apps) {
		app := m.Apps[name]
		base := "/apps/" + escapePointer(name)
		if g := app.Git; g != nil && g.Path != "" {
			for _, seg := range strings.Split(g.Path, "/") {
				if seg == ".." || seg == "." || seg == "" {
					errs = append(errs, FieldError{Path: base + "/git/path",
						Message: fmt.Sprintf("%q must be a folder inside the repository, like \"apps/web\" (no \"..\", \".\" or empty parts)", g.Path)})
					break
				}
			}
		}
		switch {
		case app.Command != "" && app.Framework == FrameworkStatic:
			errs = append(errs, FieldError{Path: base + "/command",
				Message: "static apps are files served by the edge and run no command; remove \"command\" or pick another framework"})
		case app.Command != "" && strings.TrimSpace(app.Command) == "":
			errs = append(errs, FieldError{Path: base + "/command", Message: "the command is blank; remove \"command\" to use the detected start command"})
		}
		switch {
		case app.Release != "" && app.Framework == FrameworkStatic:
			errs = append(errs, FieldError{Path: base + "/release",
				Message: "static apps are files served by the edge and run no release command; remove \"release\" or pick another framework"})
		case app.Release != "" && strings.TrimSpace(app.Release) == "":
			errs = append(errs, FieldError{Path: base + "/release", Message: "the release command is blank; remove \"release\" or name a command"})
		}
		if len(app.Packages) > 0 && app.Framework == FrameworkStatic {
			errs = append(errs, FieldError{Path: base + "/packages",
				Message: "static apps are files served by the edge and run no programs; remove \"packages\" or pick another framework"})
		}
		errs = append(errs, diskErrors(base, app)...)
		if app.TimeoutSeconds != 0 && app.Framework == FrameworkStatic {
			errs = append(errs, FieldError{Path: base + "/timeoutSeconds",
				Message: "static apps are files served by the edge, which has no time limit to set; remove \"timeoutSeconds\""})
		}
		if a := app.Assets; a != nil {
			for _, seg := range strings.Split(a.Dir, "/") {
				if seg == ".." || seg == "" {
					errs = append(errs, FieldError{Path: base + "/assets/dir",
						Message: fmt.Sprintf("%q must be a folder inside the app's build, like \"dist/client\" (no \"..\" or empty parts)", a.Dir)})
					break
				}
			}
		}
		if app.Role == RoleWorker && len(app.Routes) > 0 {
			errs = append(errs, FieldError{
				Path:    base + "/routes",
				Message: fmt.Sprintf("worker apps cannot have routes (got %s); remove \"routes\" or set role to \"web\"", quoteList(app.Routes)),
			})
			continue
		}
		for i, r := range app.Routes {
			key := normalizeRoute(r)
			if h, _, _ := strings.Cut(key, "/"); !strings.Contains(h, ".") && len(h) > 63 {
				errs = append(errs, FieldError{
					Path: fmt.Sprintf("%s/routes/%d", base, i),
					Message: fmt.Sprintf("address %q is longer than 63 characters, the most a name under the box domain can have; "+
						"set a shorter one in \"routes\" (an app that sets none is served at <project>-<app>)", h),
				})
				continue
			}
			if prev, dup := owners[key]; dup {
				who := fmt.Sprintf("app %q", prev)
				if prev == name {
					who = "this app"
				}
				errs = append(errs, FieldError{
					Path:    fmt.Sprintf("%s/routes/%d", base, i),
					Message: fmt.Sprintf("route %q is already used by %s; routes must be unique across apps", r, who),
				})
				continue
			}
			owners[key] = name
		}
	}
	if e := m.Services.Email; e != nil && e.From != "" {
		if !emailRe.MatchString(e.From) {
			errs = append(errs, FieldError{
				Path:    "/services/email/from",
				Message: fmt.Sprintf("%q is not a valid email address; use a bare address such as \"hello@example.com\", or remove \"from\" to use \"<project>@<box domain>\"", e.From),
			})
		}
	}
	for _, name := range sortedKeys(m.Crons) {
		c := m.Crons[name]
		if _, ok := m.Apps[c.App]; !ok && c.App != "" {
			msg := fmt.Sprintf("cron %q targets app %q, which is not defined in \"apps\"", name, c.App)
			if len(m.Apps) > 0 {
				msg += "; defined apps: " + quoteList(sortedKeys(m.Apps))
			} else {
				msg += "; add the app to \"apps\" first"
			}
			errs = append(errs, FieldError{Path: "/crons/" + escapePointer(name) + "/app", Message: msg})
		}
		if c.Timezone != "" {
			if _, err := time.LoadLocation(c.Timezone); err != nil || c.Timezone == "Local" {
				errs = append(errs, FieldError{Path: "/crons/" + escapePointer(name) + "/timezone",
					Message: fmt.Sprintf("%q is not an IANA time zone name; use one such as \"America/New_York\", \"Europe/London\" or \"UTC\"", c.Timezone)})
			}
		}
	}
	errs = append(errs, queueErrors(m)...)
	errs = append(errs, domainErrors(m)...)
	return errs
}

// queueErrors checks queues and topics against the rest of the manifest:
// queues target real apps, topics subscribe real queues, and no name is both
// a queue and a topic (a name with subscribers fans out, so the two would
// be ambiguous when an app sends to it).
func queueErrors(m *Manifest) []FieldError {
	var errs []FieldError
	for _, name := range sortedKeys(m.Queues) {
		q := m.Queues[name]
		base := "/queues/" + escapePointer(name)
		if _, ok := m.Apps[q.App]; !ok && q.App != "" {
			msg := fmt.Sprintf("queue %q targets app %q, which is not defined in \"apps\"", name, q.App)
			if len(m.Apps) > 0 {
				msg += "; defined apps: " + quoteList(sortedKeys(m.Apps))
			} else {
				msg += "; add the app to \"apps\" first"
			}
			errs = append(errs, FieldError{Path: base + "/app", Message: msg})
		}
		if q.RateLimit > 0 && q.RatePeriodSeconds == 0 {
			errs = append(errs, FieldError{Path: base + "/ratePeriodSeconds",
				Message: fmt.Sprintf("queue %q sets rateLimit %d without ratePeriodSeconds; add the window in seconds, e.g. 60 for \"%d per minute\"", name, q.RateLimit, q.RateLimit)})
		}
		if _, clash := m.Topics[name]; clash {
			errs = append(errs, FieldError{Path: base,
				Message: fmt.Sprintf("%q is both a queue and a topic; a name with subscribers fans out, so pick different names", name)})
		}
	}
	for _, name := range sortedKeys(m.Topics) {
		for i, sub := range m.Topics[name].Subscribers {
			if _, ok := m.Queues[sub]; ok {
				continue
			}
			msg := fmt.Sprintf("topic %q subscribes queue %q, which is not defined in \"queues\"", name, sub)
			if len(m.Queues) > 0 {
				msg += "; defined queues: " + quoteList(sortedKeys(m.Queues))
			} else {
				msg += "; declare the queue in \"queues\" first"
			}
			errs = append(errs, FieldError{Path: fmt.Sprintf("/topics/%s/subscribers/%d", escapePointer(name), i), Message: msg})
		}
	}
	return errs
}

var (
	slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// topicRe is a topic name: like a slug but dots are allowed ("order.created").
	topicRe = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)
	// emailRe accepts a bare address: a local part, "@", and a dotted hostname.
	emailRe  = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	envKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// domainErrors checks the domains section against the apps' routes: every
// domain is served by an app, and a www redirect does not shadow a route.
func domainErrors(m *Manifest) []FieldError {
	var errs []FieldError
	hosts := map[string]string{} // route host → app
	for _, name := range sortedKeys(m.Apps) {
		for _, r := range m.Apps[name].Routes {
			h, _, _ := strings.Cut(normalizeRoute(r), "/")
			if _, ok := hosts[h]; !ok {
				hosts[h] = name
			}
		}
	}
	for _, d := range sortedKeys(m.Domains) {
		base := "/domains/" + escapePointer(d)
		if _, ok := hosts[d]; !ok {
			errs = append(errs, FieldError{Path: base, Message: fmt.Sprintf("no app serves %q; add it to an app's routes, e.g. \"routes\": [\"web\", %q]", d, d)})
		}
		if m.Domains[d].WWW == WWWRedirect {
			www := "www." + d
			if strings.HasPrefix(d, "www.") {
				errs = append(errs, FieldError{Path: base + "/www", Message: fmt.Sprintf("%q already starts with www.; set www: \"redirect\" on the bare domain instead", d)})
			} else if app, ok := hosts[www]; ok {
				errs = append(errs, FieldError{Path: base + "/www", Message: fmt.Sprintf("%s is a route of app %q, so it cannot also redirect; remove one of them", www, app)})
			}
		}
	}
	return errs
}

// keyErrors checks the names used as map keys in a decoded JSON document: app
// and bucket names are slugs, env variable names are UPPER_SNAKE_CASE. It is
// lenient about shape; shape errors are the schema's job.
func keyErrors(doc any) []FieldError {
	var errs []FieldError
	slug := func(base string, obj any) {
		for key := range asMap(obj) {
			if !slugRe.MatchString(key) {
				errs = append(errs, FieldError{Path: base + "/" + escapePointer(key), Message: fmt.Sprintf("name %q %s", key, patternHints[slugRe.String()])})
			}
		}
	}
	env := func(base string, obj any) {
		for key := range asMap(obj) {
			if !envKeyRe.MatchString(key) {
				errs = append(errs, FieldError{Path: base + "/" + escapePointer(key), Message: fmt.Sprintf("variable name %q %s", key, patternHints[envKeyRe.String()])})
			}
		}
	}
	root := asMap(doc)
	env("/env", root["env"])
	apps := asMap(root["apps"])
	slug("/apps", apps)
	for name, app := range apps {
		env("/apps/"+escapePointer(name)+"/env", asMap(app)["env"])
	}
	slug("/crons", root["crons"])
	slug("/queues", root["queues"])
	for key := range asMap(root["topics"]) {
		if !topicRe.MatchString(key) {
			errs = append(errs, FieldError{Path: "/topics/" + escapePointer(key), Message: fmt.Sprintf("name %q %s", key, patternHints[topicRe.String()])})
		}
	}
	slug("/services/storage/buckets", asMap(asMap(asMap(root["services"])["storage"])["buckets"]))
	for key := range asMap(root["domains"]) {
		if !domainRe.MatchString(key) || len(key) > 253 {
			errs = append(errs, FieldError{Path: "/domains/" + escapePointer(key),
				Message: fmt.Sprintf("%q must be a full lowercase host name with at least one dot, such as \"example.com\" (no scheme, port, path or wildcard)", key)})
		}
	}
	return errs
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// boxEnv are variables the box sets for apps; prefixes end in "_".
var boxEnv = []string{"PORT", "DATABASE_URL", "DIRECT_DATABASE_URL", "DATABASE_POOL_MAX", "REDIS_URL", "VALKEY_PREFIX", "SMTP_URL", "EMAIL_FROM", "SENTRY_DSN",
	"S3_", "AWS_", "TIFFIN_", "OTEL_", "UPSTASH_REDIS_REST_", "KV_REST_API_", "NEXT_PUBLIC_SENTRY_DSN", "NEXT_PUBLIC_TIFFIN_"}

// buildInlined are prefixes of env names that frameworks write into an
// app's browser code at build time: Next.js (NEXT_PUBLIC_), Vite (VITE_),
// SvelteKit and Astro (PUBLIC_).
var buildInlined = []string{"NEXT_PUBLIC_", "VITE_", "PUBLIC_"}

// BuildInlined reports whether frameworks build an env var of this name
// into browser code, so a new value needs a new build, not a restart.
func BuildInlined(name string) bool {
	for _, p := range buildInlined {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// SetByBox reports whether the box gives apps an env var of this name, so
// a value of the project's own (env or a secret) would replace it.
func SetByBox(name string) bool {
	for _, b := range boxEnv {
		if name == b || (strings.HasSuffix(b, "_") && strings.HasPrefix(name, b)) {
			return true
		}
	}
	return false
}

// Warnings are things a valid manifest probably did not mean: plans show
// them so a person or agent can fix them before applying. They never block.
func Warnings(m *Manifest) []string {
	var out []string
	if a := m.Services.Auth; a != nil && m.Services.Email == nil {
		for _, meth := range a.Methods {
			if meth == AuthEmail || meth == AuthMagicLink || meth == AuthOTP {
				out = append(out, "services.auth sends verification, sign-in and reset emails through the project's email service, and there is none: "+
					"add `email: {}` to services (mail lands in the dev inbox until a relay is set), or new users can't confirm their address or sign in")
				break
			}
		}
	}
	if m.Services.Auth != nil {
		for _, name := range sortedKeys(m.Apps) {
			for _, r := range m.Apps[name].Routes {
				if i := strings.IndexByte(r, '/'); i >= 0 && (r[i:] == "/api/auth" || strings.HasPrefix(r[i:], "/api/auth/")) {
					out = append(out, fmt.Sprintf("apps.%s route %q is under /api/auth, which services.auth serves on every app host: the app never gets those requests. Pick another path", name, r))
				}
			}
		}
	}
	if a := m.Services.Auth; a != nil && a.EmailVerification != nil && !*a.EmailVerification {
		out = append(out, "services.auth.emailVerification is false: anyone can sign up with an address they don't own. "+
			"Fine for testing; before real users sign up, remove it (verification turns on by itself once the box has an SMTP relay) or set it to true")
	}
	over := func(where string, env map[string]string) {
		for _, k := range sortedKeys(env) {
			if SetByBox(k) {
				out = append(out, fmt.Sprintf("%s sets %s, which replaces the value the box gives apps; leave it out unless you mean to override the box", where, k))
			}
		}
	}
	over("env", m.Env)
	for _, name := range sortedKeys(m.Apps) {
		over("apps."+name+".env", m.Apps[name].Env)
	}
	if pg := m.Services.Postgres; pg != nil && pg.Previews == PreviewDBShared {
		for _, name := range sortedKeys(m.Apps) {
			if m.Apps[name].Release != "" {
				out = append(out, fmt.Sprintf("services.postgres.previews is \"shared\": previews use the production database, so they skip apps.%s.release, "+
					"and a preview whose code changes the schema on start still changes production's. Remove it (each preview then gets its own branch) unless previews need live data", name))
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

var diskPathRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// diskErrors checks an app's persistent folders: plain relative paths
// (no "." or ".." parts), none inside another, and no static apps.
func diskErrors(base string, app App) []FieldError {
	if len(app.Disk) == 0 {
		return nil
	}
	if app.Framework == FrameworkStatic {
		return []FieldError{{Path: base + "/disk",
			Message: "static apps are files served by the edge and write nothing; remove \"disk\" or pick another framework"}}
	}
	var errs []FieldError
	sized := app.Disk.sized()
	for i, f := range app.Disk {
		p := f.Path
		at := fmt.Sprintf("%s/disk/%d", base, i)
		if sized {
			at = base + "/disk/" + escapePointer(p)
		}
		if !diskPathRe.MatchString(p) || slices.ContainsFunc(strings.Split(p, "/"), func(s string) bool { return s == "." || s == ".." || s == "" }) {
			errs = append(errs, FieldError{Path: at,
				Message: fmt.Sprintf("%q must be a folder inside the app's working directory, like \"data\" (no \"..\", \".\" or empty parts)", p)})
			continue
		}
		for _, q := range app.Disk.Paths()[:i] {
			if p != q && (strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/")) {
				errs = append(errs, FieldError{Path: at, Message: fmt.Sprintf("%q and %q overlap; list only the outer folder", q, p)})
			}
		}
	}
	return errs
}
