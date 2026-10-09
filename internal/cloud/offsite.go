package cloud

import (
	"context"
	"encoding/json"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/sealbox"
)

// Off-site backups for managed boxes. Each box sends the public half of an
// X25519 key with its check-ins (the website keeps it, from check-ins that
// count only: the current licence, from the box's own address). Every few
// minutes the worker mints credentials for each box whose subscription is
// active and whose credentials expire within RenewBefore (or that has
// none): R2 temporary credentials limited to the box's folder, lasting
// TTL. It seals them to the box's key (sealbox, bound to the box) and
// stores them on the box's row; the website hands them over in the next
// check-in's answer and cannot read them. Boxes that may not have them
// (deleted, released, unpaid) lose them from the row at once, and what
// they hold expires within TTL: temporary credentials cannot be revoked
// one by one, so TTL is kept short.
//
// A deleted or released box's folder is emptied PurgeDays after it went
// (the account page says so before it is deleted).

// Offsite is the worker's off-site backup settings; a Worker with none
// (no CLOUD_R2_* settings) leaves boxes without.
type Offsite struct {
	R2 *R2
	// TTL of the credentials (default 48 hours), renewed when less than
	// RenewBefore is left (default 36 hours): with a check-in every 6
	// hours a box always holds credentials with a day or more to run.
	TTL         time.Duration
	RenewBefore time.Duration
	// RetentionDays the boxes keep copies (default 30).
	RetentionDays int
}

const (
	defaultOffsiteTTL  = 48 * time.Hour
	defaultRenewBefore = 36 * time.Hour
	defaultRetention   = 30
	offsiteEvery       = 5 * time.Minute
	offsiteBatch       = 20
	// PurgeDays: a deleted or released box's off-site copies go this many days after.
	PurgeDays = 7
)

func (o *Offsite) ttl() time.Duration {
	if o.TTL > 0 {
		return min(o.TTL, MaxTempTTL)
	}
	return defaultOffsiteTTL
}

func (o *Offsite) renewBefore() time.Duration {
	if o.RenewBefore > 0 {
		return o.RenewBefore
	}
	return defaultRenewBefore
}

func (o *Offsite) retention() int {
	if o.RetentionDays > 0 {
		return o.RetentionDays
	}
	return defaultRetention
}

// OffsiteBox is a box whose credentials are due.
type OffsiteBox struct {
	ID        string
	BackupKey string
}

// RefreshOffsite drops the credentials of boxes that may no longer have
// them, and mints fresh ones for active boxes whose are due. It says how
// many it minted.
func (w *Worker) RefreshOffsite(ctx context.Context) int {
	w.defaults()
	if w.Offsite == nil || w.Offsite.R2 == nil {
		return 0
	}
	if n, err := w.Store.DropOffsite(ctx); err != nil {
		w.Log.Warn("off-site: dropping credentials", "err", err)
	} else if n > 0 {
		w.Log.Info("off-site: dropped the credentials of boxes that may no longer have them", "boxes", n)
	}
	boxes, err := w.Store.OffsiteDue(ctx, w.Now().Add(w.Offsite.renewBefore()), offsiteBatch)
	if err != nil {
		w.Log.Warn("off-site: boxes due", "err", err)
		return 0
	}
	minted := 0
	for _, b := range boxes {
		if err := w.grant(ctx, b); err != nil {
			w.Log.Warn("off-site: credentials", "box", b.ID, "err", err)
			continue
		}
		minted++
	}
	return minted
}

func (w *Worker) grant(ctx context.Context, b OffsiteBox) error {
	pub, err := sealbox.ParsePublic(b.BackupKey)
	if err != nil {
		return err
	}
	r2 := w.Offsite.R2
	creds, err := r2.Mint(ctx, Folder(b.ID), "object-read-write", w.Offsite.ttl())
	if err != nil {
		return err
	}
	g := platform.OffsiteGrant{Endpoint: r2.S3Endpoint(), Bucket: r2.Bucket, Prefix: b.ID, AccessKeyID: creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken, ExpiresAt: creds.ExpiresAt, RetentionDays: w.Offsite.retention()}
	plain, _ := json.Marshal(g)
	sealed, err := sealbox.Seal(pub, plain, platform.OffsiteGrantAAD(b.ID))
	clear(plain)
	if err != nil {
		return err
	}
	_, err = w.Store.SetOffsite(ctx, b.ID, b.BackupKey, sealed, creds.ExpiresAt)
	return err
}

// PurgeOffsite empties the folders of boxes deleted or released PurgeDays ago.
func (w *Worker) PurgeOffsite(ctx context.Context) {
	w.defaults()
	if w.Offsite == nil || w.Offsite.R2 == nil {
		return
	}
	ids, err := w.Store.OffsitePurgeDue(ctx, w.Now(), 5)
	if err != nil {
		w.Log.Warn("off-site: folders due for deletion", "err", err)
		return
	}
	for _, id := range ids {
		n, err := w.Offsite.R2.EmptyFolder(ctx, Folder(id))
		if err != nil {
			w.Log.Warn("off-site: emptying a deleted box's folder", "box", id, "err", err)
			continue
		}
		if err := w.Store.OffsitePurged(ctx, id); err != nil {
			w.Log.Warn("off-site: recording a folder emptied", "box", id, "err", err)
			continue
		}
		w.Log.Info("off-site: emptied a deleted box's folder", "box", id, "objects", n)
	}
}
