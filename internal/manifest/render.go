package manifest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RenderConfig writes m as a readable tiffin.config.ts: defaults are left
// out, keys come in a fixed order and short objects stay on one line.
// Evaluating the result and parsing it gives back m exactly (same canonical
// JSON). note, if set, becomes a comment above the config.
//
// The output is also valid JavaScript (no type annotations), so it can be
// saved as tiffin.config.js or .mjs too.
func RenderConfig(m *Manifest, note string) []byte {
	var b bytes.Buffer
	b.WriteString("import { defineConfig } from \"tiffin-sdk\";\n\n")
	for _, line := range strings.Split(strings.TrimSpace(note), "\n") {
		if strings.TrimSpace(line) != "" {
			b.WriteString("// " + strings.TrimSpace(line) + "\n")
		}
	}
	b.WriteString("export default defineConfig(")
	writeNode(&b, configNode(m), 0)
	b.WriteString(");\n")
	return b.Bytes()
}

// node is an ordered JS value: an object (keys in order), an array or a
// scalar literal.
type node struct {
	keys   []string
	vals   []*node
	items  []*node
	lit    string
	isObj  bool
	isList bool
}

func obj() *node { return &node{isObj: true} }

func (n *node) set(k string, v *node) {
	if v != nil {
		n.keys, n.vals = append(n.keys, k), append(n.vals, v)
	}
}

func str(s string) *node {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return &node{lit: strings.TrimRight(b.String(), "\n")}
}

func num(i int) *node { return &node{lit: strconv.Itoa(i)} }
func boolean(v bool) *node {
	if v {
		return &node{lit: "true"}
	}
	return &node{lit: "false"}
}

func strs(ss []string) *node {
	n := &node{isList: true}
	for _, s := range ss {
		n.items = append(n.items, str(s))
	}
	return n
}

func strMap(m map[string]string) *node {
	n := obj()
	for _, k := range sortedKeys(m) {
		n.set(k, str(m[k]))
	}
	return n
}

// configNode is m with every default left out.
func configNode(m *Manifest) *node {
	root := obj()
	root.set("project", str(m.Project))
	if r := m.Resources; r != nil {
		n := obj()
		if r.MemoryMB != 0 {
			n.set("memoryMB", num(r.MemoryMB))
		}
		if r.CPUs != 0 {
			n.set("cpus", &node{lit: strconv.FormatFloat(r.CPUs, 'f', -1, 64)})
		}
		if r.MaxSharePercent != 0 {
			n.set("maxSharePercent", num(r.MaxSharePercent))
		}
		root.set("resources", n)
	}
	if len(m.Apps) > 0 {
		apps := obj()
		for _, name := range sortedKeys(m.Apps) {
			apps.set(name, appNode(name, m.Apps[name], impliedRoute(m, name)))
		}
		root.set("apps", apps)
	}
	if svc := servicesNode(m.Services); len(svc.keys) > 0 {
		root.set("services", svc)
	}
	if len(m.Env) > 0 {
		root.set("env", strMap(m.Env))
	}
	if len(m.Queues) > 0 {
		qs := obj()
		for _, name := range sortedKeys(m.Queues) {
			qs.set(name, queueNode(name, m.Queues[name]))
		}
		root.set("queues", qs)
	}
	if len(m.Topics) > 0 {
		ts := obj()
		for _, name := range sortedKeys(m.Topics) {
			t := obj()
			if len(m.Topics[name].Subscribers) > 0 {
				t.set("subscribers", strs(m.Topics[name].Subscribers))
			}
			ts.set(name, t)
		}
		root.set("topics", ts)
	}
	if len(m.Crons) > 0 {
		cs := obj()
		for _, name := range sortedKeys(m.Crons) {
			c := m.Crons[name]
			n := obj()
			n.set("schedule", str(c.Schedule))
			n.set("app", str(c.App))
			if c.Path != DefaultCronPathPrefix+name {
				n.set("path", str(c.Path))
			}
			if c.Timezone != "" {
				n.set("timezone", str(c.Timezone))
			}
			if c.Overlap {
				n.set("overlap", boolean(true))
			}
			cs.set(name, n)
		}
		root.set("crons", cs)
	}
	if len(m.Domains) > 0 {
		ds := obj()
		for _, name := range sortedKeys(m.Domains) {
			d := obj()
			if w := m.Domains[name].WWW; w != "" {
				d.set("www", str(w))
			}
			ds.set(name, d)
		}
		root.set("domains", ds)
	}
	return root
}

// impliedRoute is the route Normalize would give app name if it set no
// routes, beside the other apps' routes as they are ("" for a worker).
func impliedRoute(m *Manifest, name string) string {
	c := &Manifest{Project: m.Project, Apps: make(map[string]App, len(m.Apps))}
	for n, a := range m.Apps {
		c.Apps[n] = a
	}
	a := c.Apps[name]
	a.Routes = nil
	c.Apps[name] = a
	defaultRoutes(c, nil)
	if r := c.Apps[name].Routes; len(r) == 1 {
		return r[0]
	}
	return ""
}

// appNode renders app name; implied is the route it gets when it sets none.
func appNode(name string, a App, implied string) *node {
	n := obj()
	if a.Framework != DefaultFramework {
		n.set("framework", str(string(a.Framework)))
	}
	if a.Path != DefaultAppPath {
		n.set("path", str(a.Path))
	}
	if a.Role != DefaultRole {
		n.set("role", str(string(a.Role)))
	}
	// What Normalize would fill in for this app if the field were absent.
	var defRoutes []string
	defHealth := ""
	if a.Role == RoleWeb {
		defRoutes = []string{implied}
		if a.Framework != FrameworkStatic {
			defHealth = DefaultHealthcheck
		}
	}
	if !slices.Equal(a.Routes, defRoutes) && len(a.Routes) > 0 {
		n.set("routes", strs(a.Routes))
	}
	if a.Instances != DefaultInstances {
		n.set("instances", num(a.Instances))
	}
	if a.MemoryMB != 0 {
		n.set("memoryMB", num(a.MemoryMB))
	}
	if a.Healthcheck != defHealth {
		n.set("healthcheck", str(a.Healthcheck))
	}
	if a.Command != "" {
		n.set("command", str(a.Command))
	}
	if a.Release != "" {
		n.set("release", str(a.Release))
	}
	if len(a.Packages) > 0 {
		n.set("packages", strs(a.Packages))
	}
	if len(a.Disk) > 0 {
		n.set("disk", strs(a.Disk))
	}
	if len(a.Env) > 0 {
		n.set("env", strMap(a.Env))
	}
	if g := a.Git; g != nil {
		gn := obj()
		gn.set("repo", str(g.Repo))
		if g.Branch != DefaultGitBranch {
			gn.set("branch", str(g.Branch))
		}
		if g.Path != "" {
			gn.set("path", str(g.Path))
		}
		if g.Previews != PreviewsSameRepo {
			gn.set("previews", str(string(g.Previews)))
		}
		n.set("git", gn)
	}
	if as := a.Assets; as != nil {
		an := obj()
		an.set("dir", str(as.Dir))
		if as.Path != "" && as.Path != "/" {
			an.set("path", str(as.Path))
		}
		n.set("assets", an)
	}
	return n
}

func servicesNode(s Services) *node {
	n := obj()
	if pg := s.Postgres; pg != nil {
		p := obj()
		if len(pg.Extensions) > 0 {
			p.set("extensions", strs(pg.Extensions))
		}
		if pg.StatementTimeoutSeconds > 0 {
			p.set("statementTimeoutSeconds", num(pg.StatementTimeoutSeconds))
		}
		if pg.Previews == PreviewDBShared {
			p.set("previews", str(string(pg.Previews)))
		}
		n.set("postgres", p)
	}
	if v := s.Valkey; v != nil {
		p := obj()
		if v.MaxMemoryMB != DefaultValkeyMemMB {
			p.set("maxMemoryMB", num(v.MaxMemoryMB))
		}
		n.set("valkey", p)
	}
	if st := s.Storage; st != nil {
		p := obj()
		if len(st.Buckets) > 0 {
			bs := obj()
			for _, name := range sortedKeys(st.Buckets) {
				b := obj()
				if st.Buckets[name].Public {
					b.set("public", boolean(true))
				}
				bs.set(name, b)
			}
			p.set("buckets", bs)
		}
		n.set("storage", p)
	}
	if a := s.Auth; a != nil {
		p := obj()
		if !slices.Equal(a.Methods, DefaultAuthMethods) {
			p.set("methods", strs(a.Methods))
		}
		if !a.Organizations {
			p.set("organizations", boolean(false))
		}
		if a.EmailVerification != nil {
			p.set("emailVerification", boolean(*a.EmailVerification))
		}
		n.set("auth", p)
	}
	if e := s.Email; e != nil {
		p := obj()
		if e.From != "" {
			p.set("from", str(e.From))
		}
		n.set("email", p)
	}
	if a := s.Analytics; a != nil {
		p := obj()
		if a.RetentionDays != DefaultAnalyticsRetentionDays {
			p.set("retentionDays", num(a.RetentionDays))
		}
		n.set("analytics", p)
	}
	return n
}

func queueNode(name string, q Queue) *node {
	n := obj()
	n.set("app", str(q.App))
	if q.Path != DefaultQueuePathPrefix+name {
		n.set("path", str(q.Path))
	}
	for _, f := range []struct {
		key      string
		val, def int
	}{
		{"concurrency", q.Concurrency, 0},
		{"keyConcurrency", q.KeyConcurrency, 0},
		{"rateLimit", q.RateLimit, 0},
	} {
		if f.val != f.def {
			n.set(f.key, num(f.val))
		}
	}
	defPeriod := 0
	if q.RateLimit > 0 {
		defPeriod = DefaultRatePeriodSecs
	}
	if q.RatePeriodSeconds != defPeriod {
		n.set("ratePeriodSeconds", num(q.RatePeriodSeconds))
	}
	if q.MaxAttempts != DefaultMaxAttempts {
		n.set("maxAttempts", num(q.MaxAttempts))
	}
	if q.LeaseSeconds != DefaultLeaseSeconds {
		n.set("leaseSeconds", num(q.LeaseSeconds))
	}
	return n
}

var jsIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func key(k string) string {
	if jsIdent.MatchString(k) {
		return k
	}
	return str(k).lit
}

// inline renders n on one line, or "" when it does not fit in width.
func inline(n *node, width int) string {
	var b strings.Builder
	var walk func(n *node) bool
	walk = func(n *node) bool {
		switch {
		case n.isObj:
			if len(n.keys) == 0 {
				b.WriteString("{}")
				return true
			}
			b.WriteString("{ ")
			for i, k := range n.keys {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(key(k) + ": ")
				if !walk(n.vals[i]) {
					return false
				}
			}
			b.WriteString(" }")
		case n.isList:
			b.WriteString("[")
			for i, it := range n.items {
				if i > 0 {
					b.WriteString(", ")
				}
				if !walk(it) {
					return false
				}
			}
			b.WriteString("]")
		default:
			b.WriteString(n.lit)
		}
		return b.Len() <= width
	}
	if !walk(n) {
		return ""
	}
	return b.String()
}

const lineWidth = 100

// nested reports whether an object or list holds objects or lists.
func (n *node) nested() bool {
	for _, v := range append(append([]*node{}, n.vals...), n.items...) {
		if v.isObj || v.isList {
			return true
		}
	}
	return false
}

// writeNode writes n at the given indent depth. Down to the second level
// (apps, services, ...), objects that hold objects always break across
// lines, so each app and service gets its own line.
func writeNode(b *bytes.Buffer, n *node, depth int) {
	pad := strings.Repeat("  ", depth)
	if depth >= 2 || !n.nested() {
		if s := inline(n, lineWidth-len(pad)); s != "" {
			b.WriteString(s)
			return
		}
	}
	if n.isList {
		b.WriteString("[\n")
		for _, it := range n.items {
			b.WriteString(pad + "  ")
			writeNode(b, it, depth+1)
			b.WriteString(",\n")
		}
		b.WriteString(pad + "]")
		return
	}
	if len(n.keys) == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{\n")
	for i, k := range n.keys {
		b.WriteString(pad + "  " + key(k) + ": ")
		writeNode(b, n.vals[i], depth+1)
		b.WriteString(",\n")
	}
	b.WriteString(pad + "}")
}
