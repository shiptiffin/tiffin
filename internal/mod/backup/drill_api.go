package backup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

const drillWhat = "Restores the backup's Postgres part into a scratch directory on the data disk, starts a private temporary Postgres on it " +
	"(unix socket only, WAL archiving off), counts the rows of every table in every database and checks that every database and table " +
	"the box had when the backup was taken is there, then stops the temporary server and deletes the scratch copy. " +
	"Nothing live changes: the live cluster, its WAL archive and the backup repository are only read. "

const drillAsync = "It runs in the background and returns the drill at once (status running, with its phase); poll " +
	"GET /v1/backups/drills/{id} (`tiffin backups drills get <id>`) until status is passed or failed, or pass wait=true to wait up to 50 seconds. " +
	"Refused with 409 when a drill is already running or the data disk has less free space than the backup's size plus 20%. " +
	"Needs full access to all projects."

const drillFrom = "With from=offsite the drill restores the copy in the bucket instead: Postgres from pgBackRest repo2 (WAL from there too), " +
	"and every other part (Valkey, platform state, files) downloaded into the scratch directory, each chunk decrypted and checked, " +
	"the platform state opened and SQLite databases checked. Scheduled drills alternate between the two."

type drillStartOut struct{ Body *BackupDrill }

func orLocal(s string) string {
	if s == "" {
		return SourceLocal
	}
	return s
}

// lastCopied is the newest successful set with an off-box copy.
func lastCopied(list []Backup) *Backup {
	for i := range list {
		if list[i].Status == "ok" && list[i].Offsite != nil && list[i].Offsite.Status == "ok" {
			return &list[i]
		}
	}
	return nil
}

// drillRefused turns StartDrill's refusals into problems.
func drillRefused(err error) error {
	var de *DiskError
	switch {
	case errors.As(err, &de):
		p := api.NewProblem(409, "precondition", err.Error())
		p.Hint = "free space on the data disk (`tiffin box` shows what uses it), then try again"
		return p
	case errors.Is(err, ErrDrillRunning):
		p := api.NewProblem(409, "conflict", err.Error())
		p.Hint = "see it with `tiffin backups drills`; cancel it with `tiffin backups drills cancel <id>`"
		return p
	}
	return api.NewProblem(409, "precondition", err.Error())
}

func requireDrill(ctx context.Context) (*tokens.Principal, error) {
	pr := api.PrincipalFrom(ctx)
	if err := pr.Require(tokens.ScopeApplyReversible, ""); err != nil {
		return nil, err
	}
	if !pr.CanProject("*") {
		return nil, fmt.Errorf("%w: a restore drill reads every project's database; this needs a token for all projects", tokens.ErrForbidden)
	}
	return pr, nil
}

// startDrill starts a drill of b and, when asked, waits a little for it.
func startDrill(ctx context.Context, p *platform.Platform, pr *tokens.Principal, b *Backup, source string, wait bool) (*drillStartOut, error) {
	d, done, err := StartDrillFrom(ctx, p, b, "manual", source)
	if err != nil {
		return nil, drillRefused(err)
	}
	_ = p.DB.Audit(ctx, pr.TokenID, "backup.drill", d.ID, map[string]any{"session": pr.Session, "backup": b.ID, "from": source})
	if wait {
		select {
		case <-done:
		case <-time.After(drillWaitMax):
		case <-ctx.Done():
		}
		if live := runningDrill(d.ID); live != nil {
			d = live
		} else if got, err := getDrill(context.WithoutCancel(ctx), p, d.ID); err == nil {
			d = got
		}
	}
	return &drillStartOut{d}, nil
}

func registerDrills(a huma.API, p *platform.Platform, tag string) {
	dl := api.Op("backup-drill", http.MethodPost, "/v1/backups/drill", "backups drill", api.RiskWrite,
		"Run a restore drill of the newest backup",
		"Proves the newest successful backup can be restored. "+drillWhat+drillAsync+
			" To drill an older backup use POST /v1/backups/{id}/drill (`tiffin backups drills start <id>`). "+drillFrom, tag)
	dl.Errors = append(dl.Errors, 409)
	huma.Register(a, dl, api.Wrap(func(ctx context.Context, in *struct {
		Wait bool   `query:"wait" doc:"Wait up to 50 seconds for the drill to finish before answering"`
		From string `query:"from" enum:"local,offsite," doc:"local (default): the copy on this box; offsite: the copy in the bucket"`
	}) (*drillStartOut, error) {
		pr, err := requireDrill(ctx)
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, err := List(ctx, p)
		if err != nil {
			return nil, err
		}
		source := orLocal(in.From)
		b := lastOK(list, "")
		if source == SourceOffsite {
			b = lastCopied(list)
		}
		if b == nil {
			pb := api.NewProblem(409, "precondition", "there is no successful backup to drill yet")
			pb.Hint = "take one with `tiffin backup`, then run `tiffin backups drill`"
			if source == SourceOffsite {
				pb.Detail, pb.Hint = "no backup has been copied off the box yet", "copy one with `tiffin backups offsite copy`"
			}
			return nil, pb
		}
		return startDrill(ctx, p, pr, b, source, in.Wait)
	}))

	ls := api.Op("backups-drills", http.MethodGet, "/v1/backups/drills", "backups drills", api.RiskRead,
		"List restore drills",
		"Restore drills, newest first (the last 30 are kept): which backup, status (running, passed, failed), phase, timings in seconds "+
			"(restore, start, verify, total), sizes, per-database table and row counts compared with the live database, and the outcome in plain words.", tag)
	huma.Register(a, ls, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []BackupDrill }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, err := ListDrills(ctx, p)
		if err != nil {
			return nil, err
		}
		for i := range list {
			if live := runningDrill(list[i].ID); live != nil {
				list[i] = *live
			}
		}
		return &struct{ Body []BackupDrill }{list}, nil
	}))

	gt := api.Op("backups-drills-get", http.MethodGet, "/v1/backups/drills/{id}", "backups drills get", api.RiskRead,
		"Show a restore drill",
		"One restore drill: status and phase (poll this while it runs), timings, the restored size, every database's tables and rows next to the "+
			"live database's, tables missing from the restored copy, problems, and the outcome in plain words.", tag)
	gt.Errors = append(gt.Errors, 404)
	huma.Register(a, gt, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^dr_[0-9A-Z]{26}$" doc:"Drill ID"`
	}) (*struct{ Body *BackupDrill }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if live := runningDrill(in.ID); live != nil {
			return &struct{ Body *BackupDrill }{live}, nil
		}
		d, err := getDrill(ctx, p, in.ID)
		if errors.Is(err, os.ErrNotExist) {
			pb := api.NewProblem(404, "not_found", "no restore drill "+in.ID)
			pb.Hint = "list them with `tiffin backups drills`"
			return nil, pb
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body *BackupDrill }{d}, nil
	}))

	st := api.Op("backups-drills-start", http.MethodPost, "/v1/backups/{id}/drill", "backups drills start", api.RiskWrite,
		"Run a restore drill of a backup",
		"Proves this backup can be restored. "+drillWhat+drillAsync+" "+drillFrom, tag)
	st.Errors = append(st.Errors, 404, 409)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct {
		ID   string `path:"id" pattern:"^bk_[0-9A-Z]{26}$" doc:"Backup ID"`
		Wait bool   `query:"wait" doc:"Wait up to 50 seconds for the drill to finish before answering"`
		From string `query:"from" enum:"local,offsite," doc:"local (default): the copy on this box; offsite: the copy in the bucket"`
	}) (*drillStartOut, error) {
		pr, err := requireDrill(ctx)
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		b, err := get(ctx, p, in.ID)
		if errors.Is(err, os.ErrNotExist) {
			pb := api.NewProblem(404, "not_found", "no backup "+in.ID)
			pb.Hint = "list them with `tiffin backups list`"
			return nil, pb
		}
		if err != nil {
			return nil, err
		}
		return startDrill(ctx, p, pr, b, orLocal(in.From), in.Wait)
	}))

	cn := api.Op("backups-drills-cancel", http.MethodPost, "/v1/backups/drills/{id}/cancel", "backups drills cancel", api.RiskWrite,
		"Cancel a running restore drill",
		"Stops a running restore drill: the temporary Postgres is stopped and the scratch copy deleted; the drill ends as failed (cancelled). "+
			"Returns the drill. 409 when it is not running. Needs full access to all projects.", tag)
	cn.Errors = append(cn.Errors, 404, 409)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^dr_[0-9A-Z]{26}$" doc:"Drill ID"`
	}) (*struct{ Body *BackupDrill }, error) {
		pr, err := requireDrill(ctx)
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		d, err := getDrill(ctx, p, in.ID)
		if errors.Is(err, os.ErrNotExist) {
			return nil, api.NewProblem(404, "not_found", "no restore drill "+in.ID)
		}
		if err != nil {
			return nil, err
		}
		drillState.mu.Lock()
		r := drillState.running
		drillState.mu.Unlock()
		if r == nil || r.rec.ID != in.ID || !CancelDrill(in.ID) {
			return nil, api.NewProblem(409, "conflict", "restore drill "+in.ID+" is not running ("+d.Status+")")
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.drill.cancel", in.ID, map[string]any{"session": pr.Session})
		select {
		case <-r.done:
		case <-time.After(drillWaitMax):
		case <-ctx.Done():
		}
		if live := runningDrill(in.ID); live != nil {
			return &struct{ Body *BackupDrill }{live}, nil
		}
		if got, err := getDrill(context.WithoutCancel(ctx), p, in.ID); err == nil {
			d = got
		}
		return &struct{ Body *BackupDrill }{d}, nil
	}))
}
