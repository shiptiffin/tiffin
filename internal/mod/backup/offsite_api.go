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

const passphraseNote = "Keep this passphrase somewhere safe, off this server (a password manager). It is shown only now. " +
	"The off-box copies are encrypted with it: if this server is lost, a new box needs it to restore them " +
	"(`tiffin backups offsite set ... --passphrase <it>`, then `tiffin restore latest --from offsite`). Without it they cannot be read."

// BackupOffsiteTest is the outcome of a destination test.
type BackupOffsiteTest struct {
	OK          bool        `json:"ok"`
	Destination string      `json:"destination"`
	Steps       []ProbeStep `json:"steps"`
}

func offsiteProblem(status int, code string, err error, hint string) error {
	pb := api.NewProblem(status, code, err.Error())
	pb.Hint = hint
	return pb
}

func registerOffsite(a huma.API, p *platform.Platform, tag string) {
	sh := api.Op("backups-offsite-show", http.MethodGet, "/v1/backups/offsite", "backups offsite show", api.RiskRead,
		"Show where backups are copied off the box",
		"The off-box destination (S3-compatible bucket; never its secret or passphrase), its state (off, active, foreign) and the newest copy: "+
			"when, what was sent, and any error.", tag)
	huma.Register(a, sh, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOffsite }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		return &struct{ Body *BackupOffsite }{offsiteView(ctx, p)}, nil
	}))

	st := api.Outbound(api.Op("backups-offsite-set", http.MethodPut, "/v1/backups/offsite", "backups offsite set", api.RiskWrite,
		"Copy backups off the box",
		"Sets an S3-compatible bucket (Cloudflare R2, AWS S3, Hetzner Object Storage, MinIO) where every backup set is copied, encrypted: "+
			"Postgres as a second pgBackRest repository (aes-256-cbc, WAL archived there too), everything else (Valkey, platform state, files) as "+
			"encrypted, deduplicated chunks. The destination is tested first (a test object is written, read and deleted). "+
			"For a new destination a passphrase is generated and returned once: keep it, a new box needs it to restore. "+
			"For a destination that already holds copies (restoring onto a new box), pass that passphrase. Box owner only.", tag))
	st.Errors = append(st.Errors, 409, 422)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct{ Body OffsiteInput }) (*struct{ Body *BackupOffsite }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: off-box copies hold every project's data; they need the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if !work.TryLock() {
			return nil, offsiteProblem(409, "conflict", errors.New("an off-box copy or restore is running"), "try again when it finishes (`tiffin backups offsite show`)")
		}
		defer work.Unlock()
		out, err := setOffsite(ctx, p, in.Body)
		if err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.set", out.Bucket, map[string]any{"session": pr.Session, "endpoint": out.Endpoint,
			"prefix": out.Prefix, "state": out.State, "accessKeyId": out.AccessKeyID})
		return &struct{ Body *BackupOffsite }{out}, nil
	}))

	ts := api.Outbound(api.Op("backups-offsite-test", http.MethodPost, "/v1/backups/offsite/test", "backups offsite test", api.RiskWrite,
		"Test the off-box destination",
		"Writes a small random object to the bucket, reads it back, deletes it, and has pgBackRest list its repository there. "+
			"Returns each step with its time; ok is false when one failed (the step says why).", tag))
	ts.Errors = append(ts.Errors, 409)
	huma.Register(a, ts, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOffsiteTest }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		c, s := current()
		if c == nil {
			return nil, offsiteProblem(409, "precondition", ErrOffsiteOff, "set one with `tiffin backups offsite set`")
		}
		out := &BackupOffsiteTest{Destination: c.where()}
		store, err := newS3(c, s.SecretAccessKey)
		if err == nil {
			out.Steps, err = probe(ctx, store, c.Prefix)
		}
		if err == nil {
			t := time.Now()
			_, perr := asUserEnv(ctx, "postgres", repo2Env(c, s), "pgbackrest", "--repo=2", "--log-level-console=warn", "repo-ls")
			step := ProbeStep{Name: "pgBackRest lists its repository (repo2)", OK: perr == nil, Ms: time.Since(t).Milliseconds()}
			if perr != nil {
				step.Detail, err = clean(perr), perr
			}
			out.Steps = append(out.Steps, step)
		}
		out.OK = err == nil
		return &struct{ Body *BackupOffsiteTest }{out}, nil
	}))

	of := api.Op("backups-offsite-off", http.MethodDelete, "/v1/backups/offsite", "backups offsite off", api.RiskWrite,
		"Stop copying backups off the box",
		"Forgets the destination and its credentials; WAL is archived locally only again. The copies already in the bucket stay there "+
			"(delete them in the bucket if you want them gone); keep the passphrase to restore them. Box owner only.", tag)
	of.Errors = append(of.Errors, 409)
	huma.Register(a, of, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOffsite }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: off-box copies need the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if !work.TryLock() {
			return nil, offsiteProblem(409, "conflict", errors.New("an off-box copy or restore is running"), "try again when it finishes")
		}
		defer work.Unlock()
		prev, _ := current()
		if err := writeRepo2Conf(nil, nil); err != nil {
			return nil, err
		}
		for _, k := range []string{"config", "status", "pruned"} {
			if err := p.DB.KVDelete(ctx, nsOffsite, k); err != nil {
				return nil, err
			}
		}
		remember(nil, nil)
		_ = os.RemoveAll(offsiteRoot)
		if prev != nil {
			_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.off", prev.Bucket, map[string]any{"session": pr.Session, "endpoint": prev.Endpoint, "prefix": prev.Prefix})
		}
		out := offsiteView(ctx, p)
		if prev != nil {
			out.Message = "Off: backups are only on this server again. The copies already in " + prev.where() + " stay there; keep the passphrase to restore them."
		}
		return &struct{ Body *BackupOffsite }{out}, nil
	}))

	cp := api.Outbound(api.Op("backups-offsite-copy", http.MethodPost, "/v1/backups/offsite/copy", "backups offsite copy", api.RiskWrite,
		"Copy a backup off the box now",
		"Copies a backup set (default: the newest successful one) to the bucket now: an incremental Postgres backup to pgBackRest repo2 "+
			"(a full one each week) and the set's other parts as encrypted chunks, only those the bucket lacks. Copies otherwise run by "+
			"themselves after every backup. Waits up to timeoutSeconds (default 50) and returns the copy; status running means it carries on "+
			"(see `tiffin backups offsite show`). Needs full access to all projects.", tag))
	cp.Errors = append(cp.Errors, 404, 409)
	huma.Register(a, cp, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Backup         string `json:"backup,omitempty" pattern:"^bk_[0-9A-Z]{26}$" doc:"Backup ID (default the newest successful one)"`
			TimeoutSeconds int    `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"How long to wait for the copy (default 50)"`
		}
	}) (*struct{ Body *BackupOffsiteCopy }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		if !pr.CanProject("*") {
			return nil, fmt.Errorf("%w: backups cover every project; this needs a token for all projects", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		c, _ := current()
		if c == nil || c.State != OffsiteActive {
			hint := "set a destination with `tiffin backups offsite set`"
			if c != nil {
				hint = foreignWords
			}
			return nil, offsiteProblem(409, "precondition", ErrOffsiteOff, hint)
		}
		var b *Backup
		if in.Body.Backup != "" {
			got, err := get(ctx, p, in.Body.Backup)
			if errors.Is(err, os.ErrNotExist) {
				return nil, api.NewProblem(404, "not_found", "no backup "+in.Body.Backup)
			}
			if err != nil {
				return nil, err
			}
			b = got
		} else {
			list, err := List(ctx, p)
			if err != nil {
				return nil, err
			}
			if b = lastOK(list, ""); b == nil {
				return nil, offsiteProblem(409, "precondition", errors.New("there is no successful backup to copy yet"), "take one with `tiffin backup`")
			}
		}
		if b.Status != "ok" {
			return nil, api.NewProblem(409, "precondition", "backup "+b.ID+" did not succeed ("+b.Status+")")
		}
		wait := time.Duration(in.Body.TimeoutSeconds) * time.Second
		if wait == 0 {
			wait = 50 * time.Second
		}
		asked := time.Now()
		done := make(chan *BackupOffsiteCopy, 1)
		go func() {
			res, _ := CopyOffsite(boxContext(), p, b)
			done <- res
		}()
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.copy", b.ID, map[string]any{"session": pr.Session})
		select {
		case res := <-done:
			if res == nil {
				return nil, offsiteProblem(409, "precondition", ErrOffsiteOff, "set a destination with `tiffin backups offsite set`")
			}
			return &struct{ Body *BackupOffsiteCopy }{res}, nil
		case <-time.After(wait):
		case <-ctx.Done():
		}
		return &struct{ Body *BackupOffsiteCopy }{&BackupOffsiteCopy{Backup: b.ID, Status: "running", StartedAt: asked.UTC()}}, nil
	}))

	ls := api.Outbound(api.Op("backups-offsite-list", http.MethodGet, "/v1/backups/offsite/sets", "backups offsite list", api.RiskRead,
		"List the backups in the bucket",
		"Backup sets copied to the off-box destination, newest first, read from the bucket (so a new box sees a lost box's sets): ID, when it "+
			"was taken and copied, which box took it, and whether its Postgres backup is still there (restorable). Restore one with "+
			"`tiffin restore <id> --from offsite`.", tag))
	ls.Errors = append(ls.Errors, 409)
	huma.Register(a, ls, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []BackupOffsiteSet }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, _, err := offsiteSets(ctx)
		if errors.Is(err, ErrOffsiteOff) {
			return nil, offsiteProblem(409, "precondition", err, "set one with `tiffin backups offsite set`")
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body []BackupOffsiteSet }{list}, nil
	}))
}

// setOffsite checks and stores a destination; the caller holds work.
func setOffsite(ctx context.Context, p *platform.Platform, in OffsiteInput) (*BackupOffsite, error) {
	prev, prevSec := current()
	c, err := normalize(in, prev)
	if err != nil {
		return nil, api.NewProblem(422, "validation", err.Error())
	}
	same := prev != nil && prev.dest() == c.dest()
	secret := in.SecretAccessKey
	if secret == "" {
		if !same || prev.AccessKeyID != c.AccessKeyID {
			return nil, api.NewProblem(422, "validation", "secretAccessKey is required for a new destination or access key")
		}
		secret = prevSec.SecretAccessKey
	}
	store, err := newS3(c, secret)
	if err != nil {
		return nil, api.NewProblem(422, "validation", err.Error())
	}
	if _, err := probe(ctx, store, c.Prefix); err != nil {
		return nil, offsiteProblem(422, "validation", fmt.Errorf("the destination did not work: %w", err),
			"check the endpoint, region, bucket (it must exist) and the key's permissions (read, write, list and delete objects)")
	}
	v := &vault{st: store, prefix: vaultPrefix(c.Prefix)}
	raw, err := store.Get(ctx, v.keyKey())
	pass, reveal := in.Passphrase, ""
	var keys *offsiteKeys
	switch {
	case err == nil:
		if pass == "" && same {
			pass = prevSec.Passphrase
		}
		if pass == "" {
			return nil, offsiteProblem(409, "precondition", errors.New("this bucket prefix already holds Tiffin's encrypted off-box copies"),
				"pass the passphrase shown when they were set up (--passphrase) to use them, for example to restore a lost box; or choose another prefix")
		}
		if keys, err = openKeys(raw, pass); err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
	case errors.Is(err, errNoObject):
		if pass == "" {
			pass, reveal = newPassphrase(), "yes"
		} else if len(pass) < 12 {
			return nil, api.NewProblem(422, "validation", "a passphrase of your own must be at least 12 characters (or leave it out to have one generated)")
		}
		if keys, err = newKeys(hostname()); err != nil {
			return nil, err
		}
		sealed, err := sealKeys(keys, pass)
		if err != nil {
			return nil, err
		}
		if err := store.Put(ctx, v.keyKey(), sealed); err != nil {
			return nil, offsiteProblem(422, "validation", fmt.Errorf("writing the key bundle: %w", err), "")
		}
	default:
		return nil, offsiteProblem(422, "validation", fmt.Errorf("reading the bucket: %w", err), "")
	}
	sec := &offsiteSecrets{SecretAccessKey: secret, Passphrase: pass, Keys: keys}
	c.SetAt = time.Now().UTC()
	if same {
		c.SetAt = prev.SetAt
	}
	// pgBackRest: create (or check) the stanza in repo2 with these settings.
	if err := writeCA(c); err != nil {
		return nil, err
	}
	remember(c, sec)
	state, err := activate(ctx)
	if err != nil {
		remember(prev, prevSec)
		return nil, offsiteProblem(422, "validation", err, "the objects test passed, so check region and uriStyle (pgBackRest signs requests itself)")
	}
	c.State = state
	if state == OffsiteActive {
		err = writeRepo2Conf(c, sec)
	} else {
		err = os.Remove(offsiteConfPath)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		remember(prev, prevSec)
		return nil, err
	}
	if err := saveOffsite(ctx, p, c, sec); err != nil {
		remember(prev, prevSec)
		return nil, err
	}
	if !same {
		_ = p.DB.KVDelete(ctx, nsOffsite, "status")
		_ = p.DB.KVDelete(ctx, nsOffsite, "pruned")
		_ = os.RemoveAll(offsiteRoot)
	}
	pokeOffsite()
	out := offsiteView(ctx, p)
	if reveal != "" {
		out.Passphrase, out.PassphraseNote = pass, passphraseNote
	}
	return out, nil
}

// boxContext is the box's lifetime context (background work outlives requests).
func boxContext() context.Context {
	drillState.mu.Lock()
	defer drillState.mu.Unlock()
	if drillState.ctx == nil {
		return context.Background()
	}
	return drillState.ctx
}
