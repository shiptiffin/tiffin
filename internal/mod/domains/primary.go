package domains

// Primary domains: domains: { "example.com": { primary: true } } makes
// example.com its app's main address. Once it is live (its certificate is
// in), the app's other whole-host addresses (its <name>.<apps domain> and
// its other domains, with their www redirects) answer a permanent redirect
// to it, path and query kept, like Vercel's "Redirect to another domain".
//
// Rules:
//   - Only hosts the app serves whole, and nobody else serves any path of,
//     redirect: a host shared with another app (example.com/api on api)
//     keeps serving, so the other app's paths never move.
//   - Previews (<preview>--<name>) and deploy addresses (d-<id>--<name>)
//     are other hosts: they keep serving. So do the box's own names.
//   - Until the primary is live nothing redirects, so moving a site never
//     sends visitors to a name without HTTPS yet. If it stops being live
//     (its DNS moved away), the old addresses serve again.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// primary is one app's primary domain and the addresses that redirect to it.
type primary struct {
	Project, App, Host string
	// From are the app's other whole hosts, in route order: they redirect
	// to Host once it is live.
	From []string
	// WWW are From's www redirects (www.<from>), which go straight to Host.
	WWW []string
}

// primaries reads every project's primary domains.
func primaries(ctx context.Context, p *platform.Platform) []primary {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil
	}
	sort.Strings(projects)
	owners := map[string]map[string]bool{} // host → "project/app" with a route on it
	whole := map[string][]string{}         // "project/app" → the hosts it serves whole
	type want struct{ project, host string }
	var wants []want
	www := map[string]bool{} // "project/<domain>" with www: "redirect"
	for _, project := range projects {
		_, res, err := p.DB.Load(ctx, project)
		if err != nil {
			continue
		}
		addrs := make([]string, 0, len(res))
		for a := range res {
			addrs = append(addrs, a)
		}
		sort.Strings(addrs)
		for _, addr := range addrs {
			switch change.Kind(addr) {
			case change.KindApp:
				var a manifest.App
				if json.Unmarshal(res[addr].Spec, &a) != nil || a.Role == manifest.RoleWorker {
					continue
				}
				who := project + "/" + change.Name(addr)
				for _, rt := range a.Routes {
					host, path := splitRoute(rt)
					if !strings.Contains(host, ".") {
						host = p.Host(host)
					}
					if owners[host] == nil {
						owners[host] = map[string]bool{}
					}
					owners[host][who] = true
					if path == "/" && !slices.Contains(whole[who], host) {
						whole[who] = append(whole[who], host)
					}
				}
			case change.KindDomain:
				var d manifest.Domain
				if json.Unmarshal(res[addr].Spec, &d) != nil {
					continue
				}
				if d.Primary {
					wants = append(wants, want{project, change.Name(addr)})
				}
				if d.WWW == manifest.WWWRedirect {
					www[project+"/"+change.Name(addr)] = true
				}
			}
		}
	}
	var out []primary
	for _, w := range wants {
		who := ""
		for o := range owners[w.host] {
			if strings.HasPrefix(o, w.project+"/") && slices.Contains(whole[o], w.host) {
				who = o
			}
		}
		if who == "" {
			continue // the manifest check refuses this
		}
		pr := primary{Project: w.project, App: strings.TrimPrefix(who, w.project+"/"), Host: w.host}
		for _, h := range whole[who] {
			if h == w.host || len(owners[h]) != 1 || h == p.DashboardHost() {
				continue
			}
			pr.From = append(pr.From, h)
			if www[w.project+"/"+h] && !owners["www."+h][who] {
				pr.WWW = append(pr.WWW, "www."+h)
			}
		}
		out = append(out, pr)
	}
	return out
}

// primaryLive: the primary has its certificate (a name on the box always
// has one: the wildcard, or on demand).
func (m *Module) primaryLive(p *platform.Platform, host string) bool {
	if p.IsBoxHost(host) {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.recs[host]
	return r != nil && r.State == StateLive
}

// primaryRoutes are the redirects of the live primaries. www redirects of
// the old domains point at those domains; the edge takes them to the end
// of the chain in one hop.
func (m *Module) primaryRoutes(p *platform.Platform, prims []primary) []edge.Route {
	var out []edge.Route
	for _, pr := range prims {
		if !m.primaryLive(p, pr.Host) {
			continue
		}
		for _, h := range pr.From {
			out = append(out, edge.Route{Host: h, RedirectTo: pr.Host})
		}
	}
	return out
}

// primaryTick reports whether the primary redirects changed since the last
// call (a primary went live, say), so the edge reloads.
func (m *Module) primaryTick(ctx context.Context, p *platform.Platform) bool {
	var keys []string
	for _, r := range m.primaryRoutes(p, primaries(ctx, p)) {
		keys = append(keys, r.Host+">"+r.RedirectTo)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := !slices.Equal(keys, m.lastPrimary)
	m.lastPrimary = keys
	return changed
}

// RedirectTarget is the primary domain a host redirects to, or "" when it
// serves (no primary, or the primary is not live yet). The runtime uses it to
// name the primary as an app's address.
func RedirectTarget(host string) string { return std.redirectTarget(host) }

func (m *Module) redirectTarget(host string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.lastPrimary {
		if from, to, ok := strings.Cut(k, ">"); ok && from == host {
			return to
		}
	}
	return ""
}

// describePrimary adds the primary redirect to a domain's status.
func (m *Module) describePrimary(ctx context.Context, p *platform.Platform, d *Domain) {
	for _, pr := range primaries(ctx, p) {
		if pr.Project != d.Project {
			continue
		}
		live := m.primaryLive(p, pr.Host)
		if pr.Host == d.Domain {
			d.Primary, d.Redirecting = true, live
			d.RedirectFrom = append(append([]string{}, pr.From...), pr.WWW...)
			switch {
			case len(d.RedirectFrom) == 0:
				d.Summary += fmt.Sprintf(" It is the primary domain of app %s, which has no other address to redirect.", pr.App)
			case live:
				d.Summary += fmt.Sprintf(" Primary domain of app %s: %s redirect here (308).", pr.App, strings.Join(d.RedirectFrom, ", "))
			default:
				d.Summary += fmt.Sprintf(" Primary domain of app %s: once it is live, %s redirect here; until then they keep serving.", pr.App, strings.Join(d.RedirectFrom, ", "))
			}
			return
		}
		if slices.Contains(pr.From, d.Domain) || slices.Contains(pr.WWW, d.Domain) {
			d.PrimaryDomain, d.Redirecting = pr.Host, live
			if live {
				d.Summary += fmt.Sprintf(" It redirects to the primary domain %s (308).", pr.Host)
			} else {
				d.Summary += fmt.Sprintf(" It will redirect to the primary domain %s once that is live; until then it keeps serving.", pr.Host)
			}
			return
		}
	}
}

// wholeApp is the app that serves all of host (a route without a path).
func wholeApp(man *manifest.Manifest, host string) string {
	names := make([]string, 0, len(man.Apps))
	for n := range man.Apps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, rt := range man.Apps[n].Routes {
			if h, path := splitRoute(rt); h == host && path == "/" {
				return n
			}
		}
	}
	return ""
}

// setPrimary makes d (or stops it being) its app's primary domain; making
// it primary unsets the app's previous one.
func setPrimary(man *manifest.Manifest, d string, on bool) error {
	app := wholeApp(man, d)
	if on && app == "" {
		pr := api.NewProblem(422, "validation", "a primary domain is served whole by one app, and no app of "+man.Project+" serves all of "+d)
		pr.Hint = "add it without a path first: tiffin domains add " + man.Project + " --domain " + d + " --app <app> --primary"
		pr.Errors = append(pr.Errors, api.FieldError{Path: "/domains/" + d + "/primary", Message: "no app serves all of " + d})
		return pr
	}
	if man.Domains == nil {
		man.Domains = map[string]manifest.Domain{}
	}
	if on {
		for k, e := range man.Domains {
			if k != d && e.Primary && wholeApp(man, k) == app {
				e.Primary = false
				putDomain(man, k, e)
			}
		}
	}
	e := man.Domains[d]
	e.Primary = on
	putDomain(man, d, e)
	return nil
}

// putDomain stores a domain's options, dropping an entry left with none.
func putDomain(man *manifest.Manifest, d string, e manifest.Domain) {
	if e == (manifest.Domain{}) {
		delete(man.Domains, d)
		return
	}
	man.Domains[d] = e
}

type editBody struct {
	Primary *bool  `json:"primary,omitempty" doc:"Make it its app's primary domain: once it is live, the app's other addresses (<name>.<apps domain>, its other domains) redirect here (308, path and query kept), and the app's previous primary is unset. false (--primary=false) stops it: every address serves again."`
	WWW     *bool  `json:"www,omitempty" doc:"Also serve www.<domain> and redirect it here (point www at the box too). false (--www=false) stops it."`
	Confirm string `json:"confirm,omitempty" doc:"The plan hash (or its first 8+ characters) you reviewed. Without it nothing changes: you get status 428 with the plan."`
	Intent  string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence (shown in the activity timeline)."`
}

// registerEdit adds domains edit: a domain's options (primary, www).
func (m *Module) registerEdit(a huma.API, p *platform.Platform, ready func() error) {
	op := api.Op("domain-edit", http.MethodPost, "/v1/projects/{project}/domains/{domain}/edit", "domains edit", api.RiskWrite,
		"Change a domain's options",
		"Sets a domain's options in the manifest, through plan and apply: primary (the app's other addresses redirect here once it is live, like \"Redirect to another domain\"; "+
			"previews and deploy addresses keep serving) and www (www.<domain> redirects here). Without confirm you get status 428 with the plan.", "domains")
	op.Errors = append(op.Errors, 404, 428)
	op.Extensions[api.ExtConfirm] = true
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Domain  string `path:"domain" maxLength:"253" doc:"The domain, e.g. example.com"`
		Body    editBody
	}) (*struct{ Body DomainChange }, error) {
		if err := ready(); err != nil {
			return nil, err
		}
		if in.Body.Primary == nil && in.Body.WWW == nil {
			pr := api.NewProblem(422, "validation", "nothing to change")
			pr.Hint = "pass primary (--primary or --primary=false) or www (--www or --www=false)"
			return nil, pr
		}
		d := strings.TrimSuffix(strings.ToLower(in.Domain), ".")
		var what []string
		if in.Body.Primary != nil {
			what = append(what, map[bool]string{true: "make " + d + " the primary domain", false: d + " is no longer the primary domain"}[*in.Body.Primary])
		}
		if in.Body.WWW != nil {
			what = append(what, map[bool]string{true: "redirect www." + d + " to " + d, false: "stop redirecting www." + d}[*in.Body.WWW])
		}
		out, err := m.editManifest(ctx, p, in.Project, in.Body.Confirm, orDefault(in.Body.Intent, strings.Join(what, "; ")), func(man *manifest.Manifest) error {
			_, has := man.Domains[d]
			for _, app := range man.Apps {
				for _, rt := range app.Routes {
					if h, _ := splitRoute(rt); h == d {
						has = true
					}
				}
			}
			if !has {
				return api.NewProblem(404, "not_found", fmt.Sprintf("project %s does not serve %s", in.Project, d))
			}
			if in.Body.Primary != nil {
				if err := setPrimary(man, d, *in.Body.Primary); err != nil {
					return err
				}
			}
			if in.Body.WWW != nil {
				if man.Domains == nil {
					man.Domains = map[string]manifest.Domain{}
				}
				e := man.Domains[d]
				e.WWW = map[bool]string{true: manifest.WWWRedirect, false: ""}[*in.Body.WWW]
				putDomain(man, d, e)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if in.Body.WWW != nil && *in.Body.WWW {
			m.recheck(ctx, "www."+d)
		}
		for _, ds := range m.projectDomains(ctx, p, in.Project) {
			if ds.Domain == d {
				out.Domain = &ds
			}
		}
		return &struct{ Body DomainChange }{*out}, nil
	}))
}
