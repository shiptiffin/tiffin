package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
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
	Deploys    []*Deploy `json:"deploys"`
	NextCursor string    `json:"nextCursor,omitempty" doc:"More deploys match: pass as cursor to read the next (older) page. Absent on the last page."`
}

// deployStatuses are the statuses a status filter may name.
var deployStatuses = map[string]bool{StatusQueued: true, StatusBuilding: true, StatusStarting: true, StatusLive: true, StatusFailed: true, StatusSuperseded: true, StatusRolledBack: true, StatusStopped: true, StatusSkipped: true}

var deployIDRe = regexp.MustCompile(deployPattern)

// Cursors are opaque to clients: the last deploy ID of a page, encoded.
func encodeCursor(id string) string {
	if id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("1:" + id))
}

func decodeCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	id, ok := strings.CutPrefix(string(b), "1:")
	if err != nil || !ok || !deployIDRe.MatchString(id) {
		return "", problem(422, "validation", "cursor is not one this box gave", "Pass nextCursor from the previous page as it is, or leave cursor out to start from the newest.")
	}
	return id, nil
}

// statusFilter parses a comma-separated status filter.
func statusFilter(in string) (map[string]bool, error) {
	want := map[string]bool{}
	for _, s := range strings.Split(in, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !deployStatuses[s] {
			return nil, problem(422, "validation", "unknown status "+s, "Use queued, building, starting, live, failed, superseded, rolled_back, stopped or skipped.")
		}
		want[s] = true
	}
	return want, nil
}

// pageDeploys reads deploys newest first (by ID: deploy IDs are ULIDs whose
// first part is the creation time, to the millisecond, and CreatedAt is
// that same time, so this is creation time then ID, both descending),
// starting after the deploy ID after ("": the newest), and returns the
// first limit that keep accepts and, when another one matches, the ID to
// continue after. app "" reads every app of the project. Both reads walk
// an index in order (the kv primary key for one app, kv_deploys for a
// project) and stop at the first deploy past the page.
func (s store) pageDeploys(ctx context.Context, project, app, after string, limit int, keep func(*Deploy) bool) ([]*Deploy, string, error) {
	if after == "" {
		after = "~" // after every ID ("dep_...")
	}
	q, args := `SELECT value FROM kv WHERE ns = ? AND key < ? ORDER BY key DESC`, []any{nsDeploys(project, app), after}
	if app == "" {
		q, args = `SELECT value FROM kv WHERE ns GLOB 'runtime/deploys/*' AND `+deployProjectExpr+` = ? AND key < ? ORDER BY key DESC`, []any{project, after}
	}
	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, "", err
		}
		var d Deploy
		if json.Unmarshal(b, &d) != nil || d.Project != project || (keep != nil && !keep(&d)) {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].ID, nil
		}
		out = append(out, &d)
	}
	return out, "", rows.Err()
}

// deployProjectExpr is the project part of a deploy record's namespace
// (runtime/deploys/<project>/<app>), as the kv_deploys index has it
// (internal/state): the query must spell it the same way to use it.
const deployProjectExpr = `substr(ns, 17, instr(substr(ns, 17), '/') - 1)`

func (m *Module) registerProjectDeploys(a huma.API) {
	huma.Register(a, api.Untrusted(api.Op("project-deploys", http.MethodGet, "/v1/projects/{project}/deploys", "projects deploys", api.RiskRead, "List a project's deploys",
		"Deploys of every app in the project, newest first: production and previews, with status, version, source, timings and each one's own address (url) "+
			"and whether its build is still kept (retention). Returns 50 by default (at most 200); when more match, nextCursor reads the next page. "+
			"Filter by app, env (production or preview), preview, status (comma-separated) or branch; filters apply before paging.", "apps")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			App     string `query:"app" doc:"Only this app's deploys"`
			Env     string `query:"env" enum:"all,production,preview" default:"all" doc:"production, preview or all"`
			Preview string `query:"preview" doc:"Only this preview's deploys"`
			Status  string `query:"status" doc:"Only these statuses, comma-separated (queued, building, starting, live, failed, superseded, rolled_back, stopped, skipped)"`
			Branch  string `query:"branch" doc:"Only deploys of this branch or ref"`
			Cursor  string `query:"cursor" doc:"Where to continue: nextCursor of the previous page (with the same filters). Absent: the newest."`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Deploys per page"`
		}) (*struct{ Body ProjectDeployList }, error) {
			r, err := m.rt()
			if err != nil {
				return nil, unavailable(err)
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			want, err := statusFilter(in.Status)
			if err != nil {
				return nil, err
			}
			after, err := decodeCursor(in.Cursor)
			if err != nil {
				return nil, err
			}
			keep := func(d *Deploy) bool {
				return (in.App == "" || d.App == in.App) &&
					!(in.Env == "production" && d.Preview != "") && !(in.Env == "preview" && d.Preview == "") &&
					(in.Preview == "" || d.Preview == in.Preview) &&
					(len(want) == 0 || want[d.Status]) &&
					(in.Branch == "" || d.Ref == in.Branch)
			}
			out, next, err := r.st.pageDeploys(ctx, in.Project, in.App, after, in.Limit, keep)
			if err != nil {
				return nil, err
			}
			r.presentAll(ctx, in.Project, out)
			return &struct{ Body ProjectDeployList }{ProjectDeployList{Deploys: out, NextCursor: encodeCursor(next)}}, nil
		}))
}
