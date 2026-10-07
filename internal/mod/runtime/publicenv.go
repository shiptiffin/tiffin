package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/platform"
)

// Env that frameworks write into browser code at build time
// (NEXT_PUBLIC_*, VITE_*, PUBLIC_*; see manifest.BuildInlined) is public by
// definition, so builds get it from every source, secrets included. A new
// value needs a new build: when it changes, the box rebuilds the live
// version from its kept source instead of only restarting it.

// sourceFile is where a deploy keeps its source archive while its build is
// kept, for rebuilds.
const sourceFile = "source.tgz"

// nextPublicAliases are box values Next.js apps also get under a
// NEXT_PUBLIC_ name, so browser code can read them.
var nextPublicAliases = map[string]string{
	"NEXT_PUBLIC_SENTRY_DSN": "TIFFIN_PUBLIC_SENTRY_DSN",
	"NEXT_PUBLIC_TIFFIN_URL": "TIFFIN_URL",
}

// addNextAliases sets the NEXT_PUBLIC_ aliases of box values in env for a
// Next.js app, unless the app sets them itself.
func addNextAliases(env map[string]string, spec *manifest.App) {
	if spec.Framework != manifest.FrameworkNext {
		return
	}
	for alias, from := range nextPublicAliases {
		if env[alias] == "" && env[from] != "" {
			env[alias] = env[from]
		}
	}
}

// publicEnv is the browser-visible env a build of an app environment gets:
// every build-inlined variable of the app's env (secrets included) and, for
// Next.js, the aliases of the box's public values. all is ProjectEnv.
func (r *rt) publicEnv(project, app, preview string, spec *manifest.App, all map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range all {
		if manifest.BuildInlined(k) {
			env[k] = v
		}
	}
	if spec.Framework == manifest.FrameworkNext {
		box := map[string]string{"TIFFIN_PUBLIC_SENTRY_DSN": all["TIFFIN_PUBLIC_SENTRY_DSN"], "TIFFIN_URL": r.deployURL(&Deploy{Project: project, App: app, Preview: preview}, spec)}
		for alias, from := range nextPublicAliases {
			if env[alias] == "" && box[from] != "" {
				env[alias] = box[from]
			}
		}
	}
	return env
}

// publicEnvHash identifies a public env (never empty, so a deploy built
// with none is told apart from one built before the box kept track).
func publicEnvHash(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, env[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// publicEnvNames lists a public env's names, for logs.
func publicEnvNames(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// rebuildIfStale starts a rebuild of the live deploy d when the
// browser-visible env it was built with has changed, from the source it
// kept. It reports whether a rebuild (or another deploy that will carry
// the new values) is under way, in which case the caller does not restart.
func (r *rt) rebuildIfStale(ctx context.Context, d *Deploy, spec *manifest.App) bool {
	if d.PublicEnv == "" || spec.Role == manifest.RoleWorker {
		return false // built before the box kept track, or no browser code
	}
	all, err := r.p.ProjectEnv(ctx, d.Project, d.App)
	if err != nil {
		return false
	}
	pub := r.publicEnv(d.Project, d.App, d.Preview, spec, all)
	want := publicEnvHash(pub)
	if want == d.PublicEnv {
		return false
	}
	ds, err := r.st.listDeploys(ctx, d.Project, d.App, d.Preview)
	if err != nil {
		return false
	}
	for _, x := range ds {
		if x.ID <= d.ID {
			break
		}
		if !x.Terminal() {
			return true // a newer deploy is on its way and builds with the new values
		}
		if x.PublicEnv == want {
			return false // a rebuild for these values failed: restart, and the next deploy carries them
		}
	}
	src := filepath.Join(r.workDir(d), sourceFile)
	if d.Source == SourcePrebuilt || !exists(src) {
		r.p.Log.Warn("runtime: browser-visible env changed, but the live version has no source to rebuild from: it restarts, and its browser code keeps the old values until the next deploy",
			"project", d.Project, "app", d.App, "preview", d.Preview, "deploy", d.ID)
		return false
	}
	nd, err := r.newDeploy(ctx, d.Project, d.App, d.Preview, d.Source, "box", spec)
	if err != nil {
		return false
	}
	nd.Commit, nd.Repo, nd.Ref, nd.Message, nd.Author, nd.PullRequest, nd.Template, nd.Dir = d.Commit, d.Repo, d.Ref, d.Message, d.Author, d.PullRequest, d.Template, d.Dir
	nd.Trigger, nd.PublicEnv, nd.SourceBytes = "env", want, d.SourceBytes
	dst := filepath.Join(r.workDir(nd), sourceFile)
	if err := linkOrCopy(src, dst); err != nil {
		r.fail(ctx, nd, fmt.Errorf("copy the source of %s: %w", d.ID, err), "", io.Discard)
		return false
	}
	_ = r.st.putDeploy(ctx, nd)
	if log, err := r.openBuildLog(nd); err == nil {
		fmt.Fprintf(log, "==> rebuild of %s: env built into browser code changed (%s)\n", d.ID, publicEnvNames(pub))
		log.Close()
	}
	r.p.Log.Info("rebuilding: browser-visible env changed", "project", d.Project, "app", d.App, "preview", d.Preview, "from", d.ID, "deploy", nd.ID)
	r.start(nd, dst, d.Source)
	return true
}

// linkOrCopy hard-links src to dst, copying when it cannot.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// PlanWarnings implements platform.PlanWarner: what the box can tell about
// a plan from the apps it runs. Env built into browser code changing means
// rebuilds, not restarts; a connection budget the apps' pools would
// overrun means refused connections.
func (m *Module) PlanWarnings(ctx context.Context, p *platform.Platform, project string, plan *change.Plan, desired map[string]change.Resource) []string {
	apps := map[string]manifest.App{}
	env := map[string]string{}
	for addr, rs := range desired {
		switch change.Kind(addr) {
		case change.KindApp:
			var a manifest.App
			if json.Unmarshal(rs.Spec, &a) == nil {
				apps[change.Name(addr)] = a
			}
		case change.KindEnv:
			var v string
			if json.Unmarshal(rs.Spec, &v) == nil {
				env[change.Name(addr)] = v
			}
		}
	}
	var out []string
	if w := inlinedChanges(plan, apps); w != "" {
		out = append(out, w)
	}
	if _, ok := desired[change.KindService+"/postgres"]; ok {
		var secrets map[string]string
		if p != nil && p.Secrets != nil {
			secrets, _ = p.Secrets.All(ctx, project)
		}
		if w := poolWarning(project, postgres.PoolerClientLimit, apps, env, secrets); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// inlinedChanges says which web apps a plan rebuilds because env they build
// into browser code changes.
func inlinedChanges(plan *change.Plan, apps map[string]manifest.App) string {
	if plan == nil {
		return ""
	}
	names, hit := map[string]bool{}, map[string]bool{}
	for _, op := range plan.Ops {
		switch change.Kind(op.Address) {
		case change.KindEnv:
			if n := change.Name(op.Address); manifest.BuildInlined(n) {
				names[n] = true
				for a, spec := range apps {
					if spec.Role != manifest.RoleWorker {
						hit[a] = true
					}
				}
			}
		case change.KindApp:
			if op.Action != change.Update {
				continue
			}
			var before, after manifest.App
			_ = json.Unmarshal(op.Before, &before)
			_ = json.Unmarshal(op.After, &after)
			for _, k := range append(sortedKeys(before.Env), sortedKeys(after.Env)...) {
				if manifest.BuildInlined(k) && before.Env[k] != after.Env[k] && after.Role != manifest.RoleWorker {
					names[k] = true
					hit[change.Name(op.Address)] = true
				}
			}
		}
	}
	if len(hit) == 0 {
		return ""
	}
	return fmt.Sprintf("%s changes: it is built into browser code, so %s rebuild from their live source to pick it up (a build, not just a restart). "+
		"A version deployed with --prebuilt keeps the old value until its next deploy",
		strings.Join(sortedKeys(names), ", "), strings.Join(sortedKeys(hit), ", "))
}

// poolWarning warns when the apps' connection pools could open more
// client connections than the pooler lets one project hold (limit).
func poolWarning(project string, limit int, apps map[string]manifest.App, env, secrets map[string]string) string {
	instances := projectInstances(apps)
	if instances == 0 || limit <= 0 {
		return ""
	}
	box := poolMax(instances, false)
	peak := 0
	var parts []string
	for _, name := range sortedKeys(apps) {
		a := apps[name]
		if a.Framework == manifest.FrameworkStatic {
			continue
		}
		n := max(1, a.Instances)
		pool, own := box, ""
		for _, src := range []map[string]string{env, a.Env, secrets} {
			if v := src["DATABASE_POOL_MAX"]; v != "" {
				own = v
			}
		}
		if own != "" {
			_, _ = fmt.Sscan(own, &pool)
		}
		peak += 2 * n * pool
		parts = append(parts, fmt.Sprintf("%s %d × %d", name, n, pool))
	}
	if peak > limit {
		return fmt.Sprintf("database connections: during a deploy, old and new instances together can open %d (%s, doubled) and project %s may hold %d "+
			"at the connection pooler; connections past that are refused. Lower instances or DATABASE_POOL_MAX", peak, strings.Join(parts, ", "), project, limit)
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
