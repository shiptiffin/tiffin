package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Managed storage: a ShipTiffin managed box comes with off-box copies to a
// folder of its own (its box id) in ShipTiffin's backup bucket. The control
// plane hands the box temporary credentials that reach only that folder
// (platform.OffsiteGrant, renewed at check-ins while the subscription is
// active), and the box sets the destination up by itself the first time:
// the same copies as to any bucket, encrypted with a passphrase the box
// makes and only its owner sees (Settings › Backups shows it until the
// owner says it is saved). ShipTiffin never holds it.
//
// The owner's choice wins: a bucket of their own replaces the managed
// storage (and stays), and turning copies off keeps them off until
// `tiffin backups offsite managed`. When the credentials run out (the
// subscription ended, or the box stopped reaching shiptiffin.com) copies
// pause, and say why, until new ones arrive.

// setOffsite checks and stores a destination of the owner's; the caller holds work.
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
	return connect(ctx, p, c, &offsiteSecrets{SecretAccessKey: secret}, in.Passphrase, false)
}

// Tests replace these: pgBackRest's config file and its stanza in the bucket.
var (
	writeConf    = writeOffConf
	activateRepo = activate
)

// errHoldsCopies: the box would set up a destination that already holds
// copies, and has no passphrase for them.
var errHoldsCopies = errors.New("this bucket prefix already holds Tiffin's encrypted off-box copies")

// connect tests destination c with credentials sec, opens (or, for a
// destination that holds no copies, makes) its keys with the passphrase,
// has pgBackRest take the repository, and stores it all. auto: the box sets
// the managed storage up by itself, so the passphrase it makes waits for
// the owner in the dashboard. The caller holds work.
func connect(ctx context.Context, p *platform.Platform, c *OffsiteConfig, sec *offsiteSecrets, pass string, auto bool) (*BackupOffsite, error) {
	prev, prevSec := current()
	same := prev != nil && prev.dest() == c.dest()
	store, err := newS3For(c, sec)
	if err != nil {
		return nil, api.NewProblem(422, "validation", err.Error())
	}
	if _, err := probe(ctx, store, c.Prefix); err != nil {
		return nil, offsiteProblem(422, "validation", fmt.Errorf("the destination did not work: %w", err),
			"check the endpoint, region, bucket (it must exist) and the key's permissions (read, write, list and delete objects)")
	}
	v := &vault{st: store, prefix: vaultPrefix(c.Prefix)}
	raw, err := store.Get(ctx, v.keyKey(), maxKeyObject)
	reveal := false
	var keys *offsiteKeys
	switch {
	case err == nil:
		if pass == "" && same {
			pass = prevSec.Passphrase
		}
		if pass == "" {
			if auto {
				return nil, errHoldsCopies
			}
			return nil, offsiteProblem(409, "precondition", errHoldsCopies,
				"pass the passphrase shown when they were set up (--passphrase) to use them, for example to restore a lost box; or choose another prefix")
		}
		if keys, err = openKeys(raw, pass); err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
	case errors.Is(err, errNoObject):
		if pass == "" {
			pass, reveal = newPassphrase(), true
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
	sec.Passphrase, sec.Keys = pass, keys
	c.SetAt = time.Now().UTC()
	if same {
		c.SetAt = prev.SetAt
		c.PassphraseUnsaved = prev.PassphraseUnsaved && pass == prevSec.Passphrase
	}
	if auto && reveal {
		c.PassphraseUnsaved = true
	}
	// pgBackRest: create (or check) the stanza in repo2 with these settings.
	undo := func() {
		remember(prev, prevSec)
		_ = writeConf(prev, prevSec)
	}
	if err := writeConf(c, sec); err != nil {
		undo()
		return nil, err
	}
	remember(c, sec)
	state, err := activateRepo(ctx)
	if err != nil {
		undo()
		return nil, offsiteProblem(422, "validation", err, "the objects test passed, so check region and uriStyle (pgBackRest signs requests itself)")
	}
	c.State = state
	if err := saveOffsite(ctx, p, c, sec); err != nil {
		undo()
		return nil, err
	}
	if !same {
		_ = p.DB.KVDelete(ctx, nsOffsite, "status")
		_ = p.DB.KVDelete(ctx, nsOffsite, "pruned")
		_ = os.RemoveAll(offsiteRoot)
	}
	pokeOffsite()
	out := offsiteView(ctx, p)
	if reveal && !auto {
		out.Passphrase, out.PassphraseNote = pass, passphraseNote
	}
	return out, nil
}

// ---- grants ----

// grantCA is the CA that signs the managed storage's certificate: empty
// (the system's) except in tests.
var grantCA string

// keepGrant stores a grant the control plane sent (sealed with the box
// key) and asks the loop to use it.
func keepGrant(ctx context.Context, p *platform.Platform, g *platform.OffsiteGrant) error {
	plain, _ := json.Marshal(g)
	sealed, err := p.Secrets.Seal(plain)
	if err != nil {
		return err
	}
	if err := p.DB.KVPut(ctx, nsOffsite, "grant", sealed); err != nil {
		return err
	}
	pokeOffsite()
	return nil
}

// loadGrant is the newest grant (nil when none came).
func loadGrant(ctx context.Context, p *platform.Platform) (*platform.OffsiteGrant, error) {
	raw, ok, err := p.DB.KVGet(ctx, nsOffsite, "grant")
	if err != nil || !ok {
		return nil, err
	}
	plain, err := p.Secrets.Unseal(raw)
	if err != nil {
		return nil, err
	}
	var g platform.OffsiteGrant
	if err := json.Unmarshal(plain, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// grantConfig is the destination a grant gives.
func grantConfig(g *platform.OffsiteGrant) (*OffsiteConfig, *offsiteSecrets, error) {
	c, err := normalize(OffsiteInput{Endpoint: g.Endpoint, Bucket: g.Bucket, Prefix: g.Prefix, AccessKeyID: g.AccessKeyID, RetentionDays: g.RetentionDays}, nil)
	if err != nil {
		return nil, nil, err
	}
	c.Region, c.CACert, c.Managed, c.ExpiresAt = "auto", grantCA, true, g.ExpiresAt.UTC()
	return c, &offsiteSecrets{SecretAccessKey: g.SecretAccessKey, SessionToken: g.SessionToken}, nil
}

var grantState struct {
	tried   time.Time // the expiry of the grant last tried for a set-up
	triedAt time.Time
	note    string // why the box did not set the managed storage up
}

func grantNote() string {
	off.mu.Lock()
	defer off.mu.Unlock()
	return grantState.note
}

// takeGrant puts the newest grant to use: it renews the managed storage's
// credentials, or sets the storage up when the box has no destination and
// its owner has not turned copies off. A bucket of the owner's stays.
func takeGrant(ctx context.Context, p *platform.Platform, now time.Time) {
	g, err := loadGrant(ctx, p)
	if err != nil {
		p.Log.Warn("backup: reading the off-site grant", "err", err)
		return
	}
	if g == nil || !g.ExpiresAt.After(now) || !work.TryLock() {
		return
	}
	defer work.Unlock()
	nc, nsec, err := grantConfig(g)
	if err != nil {
		p.Log.Warn("backup: the off-site grant", "err", err)
		return
	}
	c, s := current()
	switch {
	case c != nil && !c.Managed:
		return
	case c != nil:
		if c.dest() != nc.dest() {
			p.Log.Warn("backup: the managed off-site storage moved; keeping the current destination", "now", c.where(), "grant", nc.where())
			return
		}
		if c.AccessKeyID == g.AccessKeyID && s.SessionToken == g.SessionToken && c.ExpiresAt.Equal(nc.ExpiresAt) {
			return
		}
		if err := renew(ctx, p, c, s, nc, nsec); err != nil {
			p.Log.Warn("backup: renewing the off-site storage's credentials", "err", err)
			return
		}
		p.Log.Info("backup: off-site storage credentials renewed", "until", nc.ExpiresAt)
	default:
		if _, offed, _ := p.DB.KVGet(ctx, nsOffsite, "managedOff"); offed {
			return
		}
		off.mu.Lock()
		again := !grantState.tried.Equal(g.ExpiresAt) || now.Sub(grantState.triedAt) >= time.Hour
		if again {
			grantState.tried, grantState.triedAt = g.ExpiresAt, now
		}
		off.mu.Unlock()
		if !again {
			return
		}
		_, err := connect(ctx, p, nc, nsec, "", true)
		note := ""
		switch {
		case errors.Is(err, errHoldsCopies):
			note = "this box's folder in ShipTiffin's backup storage already holds encrypted copies (from an earlier server of this box). " +
				"Use them with `tiffin backups offsite managed --passphrase <the passphrase>`, then `tiffin restore latest --from offsite` restores them."
		case err != nil:
			note = "setting up ShipTiffin's backup storage failed, and is tried again within the hour: " + err.Error()
			p.Log.Warn("backup: setting up the managed off-site storage", "err", err)
		default:
			p.Log.Info("backup: copies off the box go to ShipTiffin's backup storage", "where", nc.where())
		}
		off.mu.Lock()
		grantState.note = note
		off.mu.Unlock()
	}
}

// renew swaps in a grant's credentials for the same destination.
func renew(ctx context.Context, p *platform.Platform, c *OffsiteConfig, s *offsiteSecrets, nc *OffsiteConfig, nsec *offsiteSecrets) error {
	n := *c
	n.AccessKeyID, n.ExpiresAt = nc.AccessKeyID, nc.ExpiresAt
	ns := *s
	ns.SecretAccessKey, ns.SessionToken = nsec.SecretAccessKey, nsec.SessionToken
	if err := writeConf(&n, &ns); err != nil {
		return err
	}
	return saveOffsite(ctx, p, &n, &ns)
}

// expired says why copies to the managed storage are paused ("" when they are not).
func expired(c *OffsiteConfig, now time.Time) string {
	if c == nil || !c.Managed || c.ExpiresAt.After(now) {
		return ""
	}
	return "ShipTiffin's backup storage comes with this box's subscription, and its credentials ran out " + ago(now.Sub(c.ExpiresAt)) +
		" ago (the subscription is not active, or the box has not reached shiptiffin.com since). Copies resume when a check-in brings new ones; " +
		"or copy to a bucket of your own with `tiffin backups offsite set`."
}

// ---- API ----

// BackupOffsitePassphrase is the passphrase of copies the box set up by itself.
type BackupOffsitePassphrase struct {
	Passphrase string `json:"passphrase"`
	Note       string `json:"note"`
}

func registerManaged(a huma.API, p *platform.Platform, tag string) {
	mg := api.Outbound(api.Op("backups-offsite-managed", http.MethodPost, "/v1/backups/offsite/managed", "backups offsite managed", api.RiskWrite,
		"Copy backups to ShipTiffin's backup storage",
		"On a ShipTiffin managed box: copies every backup set, encrypted, to the box's own folder in ShipTiffin's backup bucket, with temporary "+
			"credentials that the box's check-ins renew while the subscription is active. A box does this by itself unless its owner turned copies off "+
			"or set a bucket of their own; this turns it on again, or replaces that bucket. For a folder that already holds copies (this box's, from "+
			"an earlier server) pass their passphrase. A new passphrase is generated and returned once: keep it, only you hold it. Box owner only.", tag))
	mg.Errors = append(mg.Errors, 409, 422)
	huma.Register(a, mg, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Passphrase string `json:"passphrase,omitempty" maxLength:"200" doc:"The passphrase of copies already in the folder"`
		}
	}) (*struct{ Body *BackupOffsite }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: off-box copies hold every project's data; they need the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		g, err := loadGrant(ctx, p)
		if err != nil {
			return nil, err
		}
		if g == nil || !g.ExpiresAt.After(time.Now()) {
			return nil, offsiteProblem(409, "precondition", errors.New("this box has no ShipTiffin backup storage to use"),
				"it comes with a ShipTiffin managed box while its subscription is active (the next check-in brings it); or set a bucket of your own with `tiffin backups offsite set`")
		}
		if !work.TryLock() {
			return nil, offsiteProblem(409, "conflict", errors.New("an off-box copy or restore is running"), "try again when it finishes (`tiffin backups offsite show`)")
		}
		defer work.Unlock()
		c, sec, err := grantConfig(g)
		if err != nil {
			return nil, err
		}
		out, err := connect(ctx, p, c, sec, in.Body.Passphrase, false)
		if err != nil {
			return nil, err
		}
		_ = p.DB.KVDelete(ctx, nsOffsite, "managedOff")
		off.mu.Lock()
		grantState.note = ""
		off.mu.Unlock()
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.managed", out.Bucket, map[string]any{"session": pr.Session, "prefix": out.Prefix, "state": out.State})
		return &struct{ Body *BackupOffsite }{out}, nil
	}))

	ps := api.Op("backups-offsite-passphrase", http.MethodPost, "/v1/backups/offsite/passphrase", "backups offsite passphrase", api.RiskWrite,
		"Show the passphrase of copies the box set up",
		"When the box set up ShipTiffin's backup storage by itself, it made the passphrase that encrypts the copies. This shows it, until "+
			"`tiffin backups offsite passphrase-saved`; then never again. Keep it off this server: a new box needs it to restore the copies. Box owner only.", tag)
	ps.Errors = append(ps.Errors, 409)
	huma.Register(a, ps, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOffsitePassphrase }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: the passphrase opens every project's backups; it needs the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		c, s := current()
		if c == nil || !c.PassphraseUnsaved {
			return nil, offsiteProblem(409, "precondition", errors.New("there is no passphrase to show"),
				"a passphrase is shown once: when a destination is set, or, for one the box set up by itself, until you say it is saved")
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.passphrase", c.Bucket, map[string]any{"session": pr.Session})
		return &struct{ Body *BackupOffsitePassphrase }{&BackupOffsitePassphrase{Passphrase: s.Passphrase, Note: passphraseNote}}, nil
	}))

	sv := api.Op("backups-offsite-passphrase-saved", http.MethodPost, "/v1/backups/offsite/passphrase/saved", "backups offsite passphrase-saved", api.RiskWrite,
		"Say the passphrase is saved",
		"The box stops showing the passphrase of copies it set up by itself. Box owner only.", tag)
	sv.Errors = append(sv.Errors, 409)
	huma.Register(a, sv, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOffsite }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: it needs the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if !work.TryLock() {
			return nil, offsiteProblem(409, "conflict", errors.New("an off-box copy or restore is running"), "try again when it finishes")
		}
		defer work.Unlock()
		c, s := current()
		if c != nil && c.PassphraseUnsaved {
			n := *c
			n.PassphraseUnsaved = false
			if err := saveOffsite(ctx, p, &n, s); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "backup.offsite.passphrase.saved", c.Bucket, map[string]any{"session": pr.Session})
		}
		return &struct{ Body *BackupOffsite }{offsiteView(ctx, p)}, nil
	}))
}
