package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

// LiveHost is a host that production web apps are served on.
type LiveHost struct {
	Host    string
	Project string
	// App serves the host's root; empty when apps there only serve paths.
	App string
	// Deploys are the live deploys serving the host (sorted, joined): they
	// change when one of its apps deploys.
	Deploys string
}

// LiveHosts lists the hosts production web apps are served on, previews and
// deploy addresses aside (nil before the runtime starts). The search module
// watches it for new hosts and deploys.
func LiveHosts(ctx context.Context) ([]LiveHost, error) {
	r, err := std.rt()
	if err != nil {
		return nil, nil
	}
	return r.liveHosts(ctx)
}

func (r *rt) liveHosts(ctx context.Context) ([]LiveHost, error) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return nil, err
	}
	specs := map[string]map[string]*manifest.App{}
	byHost := map[string]*LiveHost{}
	deploys := map[string][]string{}
	for _, st := range states {
		if st.Live == "" || st.Stopped || st.Preview != "" {
			continue
		}
		if _, ok := specs[st.Project]; !ok {
			specs[st.Project] = map[string]*manifest.App{}
			_, res, err := r.p.DB.Load(ctx, st.Project)
			if err != nil {
				return nil, err
			}
			for addr, rs := range res {
				if change.Kind(addr) == change.KindApp {
					var a manifest.App
					if json.Unmarshal(rs.Spec, &a) == nil {
						specs[st.Project][change.Name(addr)] = &a
					}
				}
			}
		}
		spec := specs[st.Project][st.App]
		if spec == nil || spec.Role == manifest.RoleWorker {
			continue
		}
		for _, route := range appRoutes(st.Project, st.App, spec) {
			host, prefix := r.splitRoute(route)
			h := byHost[host]
			if h == nil {
				h = &LiveHost{Host: host, Project: st.Project}
				byHost[host] = h
			}
			if prefix == "" {
				h.App, h.Project = st.App, st.Project
			}
			deploys[host] = append(deploys[host], st.Live)
		}
	}
	out := make([]LiveHost, 0, len(byHost))
	for host, h := range byHost {
		ds := deploys[host]
		sort.Strings(ds)
		h.Deploys = strings.Join(ds, ",")
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}
