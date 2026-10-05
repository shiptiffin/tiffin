package manifest

import "strings"

// Default addresses.
//
// A web app that sets no routes is served at a name under the box's apps
// domain made from its project, so apps of different projects never share
// one and every address says which project it belongs to:
//
//   - the project's main app at <project>         (shop.<apps domain>)
//   - every other web app at <project>-<app>      (shop-docs.<apps domain>)
//   - workers at none
//
// The main app is the project's only web app; else the app named like the
// project; else the app named "web"; else the first web app the config
// declares (by name when the order is not known: canonical JSON sorts the
// apps). An app named like its project is always the main app, so it is
// served at just <project>.
//
// On a box, an app that sets no routes keeps the address it already has
// when that address is one a default gave it: its own app name (the default
// before addresses were named after the project) or <project> or
// <project>-<app>. So upgrading the box, or adding an app that becomes the
// main app, never moves a running app. A default another app already uses
// falls back to <project>-<app>.

// MainApp returns the project's main web app, the one served at the project's
// own name when it sets no routes, or "" when the project has no web app.
// order is the apps in the order the config declares them (nil: unknown).
func MainApp(project string, apps map[string]App, order []string) string {
	var web []string
	for _, name := range declared(apps, order) {
		if apps[name].Role != RoleWorker {
			web = append(web, name)
		}
	}
	switch {
	case len(web) == 0:
		return ""
	case len(web) == 1:
		return web[0]
	}
	for _, pick := range []string{project, "web"} {
		for _, name := range web {
			if name == pick {
				return name
			}
		}
	}
	return web[0]
}

// declared is apps' names in order, then any order leaves out by name.
func declared(apps map[string]App, order []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range order {
		if _, ok := apps[name]; ok && !seen[name] {
			out, seen[name] = append(out, name), true
		}
	}
	for _, name := range sortedKeys(apps) {
		if !seen[name] {
			out = append(out, name)
		}
	}
	return out
}

// DefaultName is the name under the apps domain an app gets when it sets no
// routes: the project for its main app (main), <project>-<app> for the
// others. Without a project it is the app's own name.
func DefaultName(project, app, main string) string {
	if project == "" || app == project || app == main {
		if project == "" {
			return app
		}
		return project
	}
	return project + "-" + app
}

// DefaultRoute is the route Normalize gives app when it sets none, before
// any address it already has on the box is taken into account: "" for a
// worker or an app not in m.
func DefaultRoute(m *Manifest, app string) string {
	a, ok := m.Apps[app]
	if !ok || a.Role == RoleWorker {
		return ""
	}
	return DefaultName(m.Project, app, MainApp(m.Project, m.Apps, m.appOrder))
}

// keptRoute is the address an app already has on the box (have) that it
// keeps when it sets no routes: a single name under the apps domain that a
// default gave it.
func keptRoute(project, app string, have []string) (string, bool) {
	if len(have) != 1 {
		return "", false
	}
	r := normalizeRoute(have[0])
	if strings.ContainsAny(r, "./") {
		return "", false
	}
	if r == app || (project != "" && (r == project || r == project+"-"+app)) {
		return r, true
	}
	return "", false
}

// defaultRoutes gives every web app without routes its address (see the
// package notes above). onBox holds each app's routes on the box now (nil
// for a new project). It returns the apps that keep an address from before
// addresses were named after the project.
func defaultRoutes(m *Manifest, onBox map[string][]string) (older []string) {
	main := MainApp(m.Project, m.Apps, m.appOrder)
	claimed := map[string]string{} // route → app
	var bare []string
	for _, name := range sortedKeys(m.Apps) {
		a := m.Apps[name]
		if a.Role == RoleWorker {
			continue
		}
		if len(a.Routes) == 0 {
			bare = append(bare, name)
			continue
		}
		for _, r := range a.Routes {
			if _, taken := claimed[normalizeRoute(r)]; !taken {
				claimed[normalizeRoute(r)] = name
			}
		}
	}
	set := func(name, route string) {
		a := m.Apps[name]
		a.Routes = []string{route}
		m.Apps[name] = a
		claimed[route] = name
	}
	var rest []string
	for _, name := range bare {
		if r, ok := keptRoute(m.Project, name, onBox[name]); ok {
			if _, taken := claimed[r]; !taken {
				set(name, r)
				if r != DefaultName(m.Project, name, main) && r == name {
					older = append(older, name)
				}
				continue
			}
		}
		rest = append(rest, name)
	}
	for _, name := range rest {
		r := DefaultName(m.Project, name, main)
		if _, taken := claimed[r]; taken && m.Project != "" {
			if alt := m.Project + "-" + name; alt != r {
				if _, taken := claimed[alt]; !taken {
					r = alt
				}
			}
		}
		set(name, r)
	}
	return older
}
