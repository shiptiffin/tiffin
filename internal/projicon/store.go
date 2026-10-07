// Package projicon gives every project an icon, the way a hosting
// dashboard does: the icon its app serves (found after each production
// deploy), one uploaded for it, or its initials on its colour.
package projicon

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Modes say which icon a project shows.
const (
	// ModeAuto shows the app's own icon when the box found one, else the letters.
	ModeAuto = "auto"
	// ModeUpload shows the uploaded icon.
	ModeUpload = "upload"
	// ModeLetter always shows the letters.
	ModeLetter = "letter"
)

// KV is where icons are kept (the platform's state database).
type KV interface {
	KVGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	KVPut(ctx context.Context, ns, key string, value []byte) error
	KVDelete(ctx context.Context, ns, key string) error
}

const (
	ns   = "project-icon"
	idNS = "project-icon-id" // public id → project
)

// Stored is one kept icon.
type Stored struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
	// Raster is a PNG of an SVG icon, for email (nil for raster icons,
	// whose Data is already a PNG).
	Raster    []byte    `json:"raster,omitempty"`
	Hash      string    `json:"hash"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Tone      string    `json:"tone,omitempty"`
	Source    string    `json:"source"`
	App       string    `json:"app,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IconCheck is the last time the box looked for the app's icon.
type IconCheck struct {
	At     time.Time `json:"at"`
	App    string    `json:"app"`
	Deploy string    `json:"deploy,omitempty"`
	Found  bool      `json:"found"`
	Reason string    `json:"reason,omitempty"`
}

// Record is everything kept about a project's icon.
type Record struct {
	Mode     string     `json:"mode"`
	PublicID string     `json:"publicId,omitempty"`
	Upload   *Stored    `json:"upload,omitempty"`
	Inferred *Stored    `json:"inferred,omitempty"`
	Checked  *IconCheck `json:"checked,omitempty"`
}

// Showing is the icon the project shows now, nil for the letters.
func (r *Record) Showing() (*Stored, string) {
	switch {
	case r.Mode == ModeUpload && r.Upload != nil:
		return r.Upload, ModeUpload
	case r.Mode == ModeAuto && r.Inferred != nil:
		return r.Inferred, "inferred"
	}
	return nil, ModeLetter
}

// One writer at a time: a deploy's inference and an upload must not
// overwrite each other's changes to the same record.
var mu sync.Mutex

// Get reads a project's icon record (a fresh auto record when it has none).
func Get(ctx context.Context, kv KV, project string) (*Record, error) {
	raw, ok, err := kv.KVGet(ctx, ns, project)
	if err != nil {
		return nil, err
	}
	r := &Record{Mode: ModeAuto}
	if ok {
		if err := json.Unmarshal(raw, r); err != nil {
			return &Record{Mode: ModeAuto}, nil // unreadable: start over
		}
	}
	if r.Mode == "" {
		r.Mode = ModeAuto
	}
	return r, nil
}

// Update changes a project's record under the writers' lock and saves it.
func Update(ctx context.Context, kv KV, project string, fn func(*Record) error) (*Record, error) {
	mu.Lock()
	defer mu.Unlock()
	r, err := Get(ctx, kv, project)
	if err != nil {
		return nil, err
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return r, kv.KVPut(ctx, ns, project, raw)
}

// PublicID returns the project's public icon id, making one the first time.
// It names the icon in a URL anyone can open (for email) without naming
// the project, and can't be guessed from the name.
func PublicID(ctx context.Context, kv KV, project string) (string, error) {
	if r, err := Get(ctx, kv, project); err == nil && r.PublicID != "" {
		return r.PublicID, nil
	}
	var id string
	_, err := Update(ctx, kv, project, func(r *Record) error {
		if r.PublicID == "" {
			b := make([]byte, 15)
			_, _ = rand.Read(b)
			r.PublicID = strings.ToLower(base32.StdEncoding.EncodeToString(b))
			if err := kv.KVPut(ctx, idNS, r.PublicID, []byte(project)); err != nil {
				return err
			}
		}
		id = r.PublicID
		return nil
	})
	return id, err
}

// ByPublicID finds the project a public icon id belongs to ("" when none).
func ByPublicID(ctx context.Context, kv KV, id string) (string, error) {
	v, ok, err := kv.KVGet(ctx, idNS, id)
	if err != nil || !ok {
		return "", err
	}
	return string(v), nil
}

// Delete forgets a project's icon (the project was deleted).
func Delete(ctx context.Context, kv KV, project string) error {
	mu.Lock()
	defer mu.Unlock()
	r, err := Get(ctx, kv, project)
	if err != nil {
		return err
	}
	if r.PublicID != "" {
		_ = kv.KVDelete(ctx, idNS, r.PublicID)
	}
	return kv.KVDelete(ctx, ns, project)
}

// Stamp turns a normalised image into a kept icon.
func Stamp(img, raster *Image, source, app string, now time.Time) *Stored {
	s := &Stored{MIME: img.MIME, Data: img.Data, Hash: img.Hash(), Width: img.Width, Height: img.Height, Tone: img.Tone,
		Source: source, App: app, UpdatedAt: now.UTC()}
	if img.SVG() && raster != nil {
		s.Raster = raster.Data
		s.Tone = raster.Tone // the SVG's look, judged from its raster twin
	}
	return s
}

// SaveInferred records an inference. The icon is replaced only when the
// image changed, so an unchanged favicon keeps its time and URL.
func SaveInferred(ctx context.Context, kv KV, project, app, deploy string, res Result, now time.Time) (*Record, bool, error) {
	changed := false
	r, err := Update(ctx, kv, project, func(r *Record) error {
		r.Checked = &IconCheck{At: now.UTC(), App: app, Deploy: deploy, Found: res.Icon != nil, Reason: res.Reason}
		if res.Icon == nil {
			// An app that answered without an icon has none now; one that
			// didn't answer keeps the last icon found.
			if res.None && r.Inferred != nil {
				r.Inferred, changed = nil, true
			}
			return nil
		}
		next := Stamp(res.Icon, res.Raster, res.Source, app, now)
		if old := r.Inferred; old != nil && old.Hash == next.Hash && string(old.Raster) == string(next.Raster) {
			if old.Source != next.Source || old.App != next.App {
				old.Source, old.App = next.Source, next.App
			}
			return nil
		}
		r.Inferred = next
		changed = true
		return nil
	})
	return r, changed, err
}
