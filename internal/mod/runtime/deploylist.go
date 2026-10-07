package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// versionMu keeps two deploys of one app from taking the same version:
// newDeploy holds it from nextVersion until the deploy is stored.
var versionMu sync.Mutex

// nextVersion is the version a new production deploy of the app takes: one
// more than the highest so far. Callers hold versionMu.
func (s store) nextVersion(ctx context.Context, project, app string) (int, error) {
	ds, err := s.listDeploys(ctx, project, app, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		n = max(n, d.Version)
	}
	return n + 1, nil
}

// listProjectDeploys is every deploy of every app in a project, newest first.
func (s store) listProjectDeploys(ctx context.Context, project string) ([]*Deploy, error) {
	// Project and app names are [a-z0-9-], so the prefix has no LIKE wildcards.
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT value FROM kv WHERE ns LIKE ?`, "runtime/deploys/"+project+"/%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var d Deploy
		if json.Unmarshal(b, &d) != nil || d.Project != project {
			continue
		}
		out = append(out, &d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ProjectDeployList is a page of a project's deploys.
type ProjectDeployList struct {
	Deploys []*Deploy `json:"deploys"`
	Next    string    `json:"next,omitempty" doc:"Pass as before to read the next (older) page"`
}

// deployStatuses are the statuses a status filter may name.
var deployStatuses = map[string]bool{StatusQueued: true, StatusBuilding: true, StatusStarting: true, StatusLive: true, StatusFailed: true, StatusSuperseded: true, StatusRolledBack: true, StatusStopped: true, StatusSkipped: true}

func (m *Module) registerProjectDeploys(a huma.API) {
	huma.Register(a, api.Untrusted(api.Op("project-deploys", http.MethodGet, "/v1/projects/{project}/deploys", "projects deploys", api.RiskRead, "List a project's deploys",
		"Deploys of every app in the project, newest first: production and previews, with status, version, source and timings. "+
			"Filter by app, env (production or preview), status (comma-separated) or branch; page with before.", "apps")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			App     string `query:"app" doc:"Only this app's deploys"`
			Env     string `query:"env" enum:"all,production,preview" default:"all" doc:"production, preview or all"`
			Status  string `query:"status" doc:"Only these statuses, comma-separated (queued, building, starting, live, failed, superseded, rolled_back, stopped, skipped)"`
			Branch  string `query:"branch" doc:"Only deploys of this branch or ref"`
			Before  string `query:"before" doc:"Only deploys older than this deploy ID (the next value of a previous page)"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum deploys to return"`
		}) (*struct{ Body ProjectDeployList }, error) {
			r, err := m.rt()
			if err != nil {
				return nil, unavailable(err)
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			want := map[string]bool{}
			for _, s := range strings.Split(in.Status, ",") {
				if s = strings.TrimSpace(s); s == "" {
					continue
				}
				if !deployStatuses[s] {
					return nil, problem(422, "validation", "unknown status "+s, "Use queued, building, starting, live, failed, superseded, rolled_back, stopped or skipped.")
				}
				want[s] = true
			}
			all, err := r.st.listProjectDeploys(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			out := []*Deploy{}
			next := ""
			for _, d := range all {
				if in.Before != "" && d.ID >= in.Before {
					continue
				}
				if in.App != "" && d.App != in.App {
					continue
				}
				if (in.Env == "production" && d.Preview != "") || (in.Env == "preview" && d.Preview == "") {
					continue
				}
				if len(want) > 0 && !want[d.Status] {
					continue
				}
				if in.Branch != "" && d.Ref != in.Branch {
					continue
				}
				if len(out) == in.Limit {
					next = out[len(out)-1].ID
					break
				}
				out = append(out, d)
			}
			return &struct{ Body ProjectDeployList }{ProjectDeployList{Deploys: out, Next: next}}, nil
		}))
}
