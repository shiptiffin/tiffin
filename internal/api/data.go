package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Delete all data. Every project always has a Database, KV and Files
// (manifest.AlwaysOn); they are never removed, only emptied. Emptying one
// is a change like any other (History, Undo): it makes the resource
// emptied/<part>, whose module saves the data and deletes it. Restoring
// (or undoing that change) deletes the resource, and the module puts the
// data back. The saved data is kept change.DataKeep.

// nsEmptied keeps when each part's data was deleted: "<project>/<part>" →
// emptiedAt. Written as the change commits, so a project's state can say
// what is restorable until when.
const nsEmptied = "api.emptied"

type emptiedAt struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

// RestorableData is data a "Delete all data" took that can still be put back.
type RestorableData struct {
	Part      string    `json:"part" enum:"postgres,valkey,storage" doc:"The part: postgres (Database), valkey (KV) or storage (Files)"`
	DeletedAt time.Time `json:"deletedAt" doc:"When the data was deleted"`
	Until     time.Time `json:"until" doc:"Restorable until then (7 days after the delete); after that it is gone for good"`
}

type dataBody struct {
	Confirm string `json:"confirm,omitempty" doc:"The hash of the plan you reviewed (or its first 8+ characters). Without it nothing changes and the plan comes back with status 428."`
	Intent  string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence (History shows it)."`
}

type dataIn struct {
	Project string    `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Part    string    `path:"part" enum:"postgres,valkey,storage" doc:"postgres (Database), valkey (KV) or storage (Files)"`
	Body    *dataBody `required:"false"`
}

var partWords = map[string]string{"postgres": "the database", "valkey": "KV", "storage": "Files"}

func (a *API) registerData() {
	api := a.api
	em := op("data-empty", http.MethodPost, "/v1/projects/{project}/data/{part}/empty", "data empty", RiskDestructive, "Delete all data in a part",
		"Deletes everything in one of the project's always-there parts: postgres (Database: every database, branches included, then an empty "+
			"main database with the same role and password), valkey (KV: every key) or storage (Files: every file in every bucket; the declared "+
			"buckets stay, empty). Apps keep their connection settings. The data is kept for 7 days: restore it with data restore (or undo the "+
			"change), after that it is gone for good. A second delete within 7 days replaces the first one's saved data. Without confirm you get "+
			"the plan with status 428; its op says exactly what goes (loss: rows and tables, keys, files and bytes).", "projects")
	em.Errors = append(em.Errors, 404, 409, 428)
	em.Extensions[ExtConfirm] = true
	huma.Register(api, em, wrap(func(ctx context.Context, in *dataIn) (*struct{ Body ApplyResult }, error) {
		return a.dataChange(ctx, in, true)
	}))
	re := op("data-restore", http.MethodPost, "/v1/projects/{project}/data/{part}/restore", "data restore", RiskDestructive,
		"Restore the data deleted from a part",
		"Puts back what the last data empty of this part deleted, within 7 days of it, in place of what the part holds now (a database's "+
			"current contents are snapshotted first, a bucket's go to the trash for 7 days). GET /v1/projects/{project} lists what is "+
			"restorable (restorable). Without confirm you get the plan with status 428.", "projects")
	re.Errors = append(re.Errors, 404, 409, 428)
	re.Extensions[ExtConfirm] = true
	huma.Register(api, re, wrap(func(ctx context.Context, in *dataIn) (*struct{ Body ApplyResult }, error) {
		return a.dataChange(ctx, in, false)
	}))
}

func (a *API) dataChange(ctx context.Context, in *dataIn, empty bool) (*struct{ Body ApplyResult }, error) {
	p := PrincipalFrom(ctx)
	if err := p.Require(tokens.ScopePlan, in.Project); err != nil {
		return nil, err
	}
	v, res, err := a.deps.DB.Load(ctx, in.Project)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, problem(404, "not_found", "project "+in.Project+" does not exist")
	}
	if _, ok := res[change.KindService+"/"+in.Part]; !ok {
		return nil, problem(409, "precondition", "project "+in.Project+" has no "+in.Part+" yet; it gets one with its next apply")
	}
	addr := change.EmptyAddress(in.Part)
	words := partWords[in.Part]
	var intent string
	var body dataBody
	if in.Body != nil {
		body = *in.Body
	}
	if empty {
		intent = "Delete all data in " + words + " of " + in.Project
	} else {
		if _, ok := res[addr]; !ok {
			return nil, problem(409, "precondition", "nothing to restore: no data of "+words+" was deleted in the last 7 days")
		}
		intent = "Restore the data deleted from " + words + " of " + in.Project
	}
	if body.Intent != "" {
		intent = body.Intent
	}
	plan, err := a.deps.Engine.PlanEdit(ctx, in.Project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		if empty {
			spec, _ := json.Marshal(change.EmptiedSpec{Version: v})
			cur[addr] = change.Resource{Address: addr, Spec: spec}
		} else {
			delete(cur, addr)
		}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	if err := a.checkRestore(ctx, in.Project, plan.Ops); err != nil {
		return nil, err
	}
	return a.apply(ctx, p, plan, body.Confirm, intent)
}

// checkRestore refuses a plan that would restore deleted data past its 7 days.
func (a *API) checkRestore(ctx context.Context, project string, ops []change.Op) error {
	return change.RestoreExpired(ops, func(part string, version int64) (time.Time, bool) {
		e, ok := a.emptied(ctx, project, part)
		return e.At, ok && e.Version == version
	}, time.Now())
}

func (a *API) emptied(ctx context.Context, project, part string) (emptiedAt, bool) {
	var e emptiedAt
	raw, ok, err := a.deps.DB.KVGet(ctx, nsEmptied, project+"/"+part)
	if err != nil || !ok || json.Unmarshal(raw, &e) != nil {
		return e, false
	}
	return e, true
}

// noteEmptied records when a committed change deleted a part's data, and
// forgets it when the change puts it back (or deletes the project).
func (a *API) noteEmptied(ctx context.Context, c *change.Change) {
	if c == nil {
		return
	}
	for _, o := range c.Plan.Ops {
		if change.Kind(o.Address) != change.KindEmptied {
			continue
		}
		key := c.Project + "/" + change.Name(o.Address)
		if o.Action == change.Delete {
			_ = a.deps.DB.KVDelete(ctx, nsEmptied, key)
			continue
		}
		var s change.EmptiedSpec
		if json.Unmarshal(o.After, &s) != nil {
			continue
		}
		raw, _ := json.Marshal(emptiedAt{Version: s.Version, At: c.At})
		_ = a.deps.DB.KVPut(ctx, nsEmptied, key, raw)
	}
}

// restorable lists the data of a project's parts that can still be put back.
func (a *API) restorable(ctx context.Context, project string, res map[string]change.Resource) []RestorableData {
	var out []RestorableData
	now := time.Now()
	for addr, r := range res {
		if change.Kind(addr) != change.KindEmptied {
			continue
		}
		var s change.EmptiedSpec
		if json.Unmarshal(r.Spec, &s) != nil {
			continue
		}
		e, ok := a.emptied(ctx, project, change.Name(addr))
		if !ok || e.Version != s.Version {
			continue
		}
		if until := e.At.Add(change.DataKeep); now.Before(until) {
			out = append(out, RestorableData{Part: change.Name(addr), DeletedAt: e.At, Until: until})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Part < out[j].Part })
	return out
}
