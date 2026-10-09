package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A managed box is one ShipTiffin's control plane installed in the
// customer's own cloud account. It has this file; a box installed with
// `tiffin up` does not, and everything managed stays off on it.
//
// The file holds the box's licence (it proves which box checks in; it opens
// nothing on the box) and where to check in. Root only.
var ManagedConfigPath = "/etc/tiffin/managed.json"

// ManagedConfig is how a managed box reaches its control plane.
type ManagedConfig struct {
	// ControlPlane is the control plane's address, e.g. https://shiptiffin.com.
	ControlPlane string `json:"controlPlane"`
	BoxID        string `json:"boxID"`
	// Licence is the signed licence token (see internal/licence).
	Licence string `json:"licence"`
	// PublicKey checks the licence (base64 ed25519).
	PublicKey string `json:"publicKey"`
	// OwnerName is the name on the customer's ShipTiffin account, if it
	// had one: the box's owner starts with it instead of "Owner"
	// (tokens.Manager.NameOwner, once).
	OwnerName string `json:"ownerName,omitempty"`
}

// Validate checks the config before it is written to a box.
func (c *ManagedConfig) Validate() error {
	u, err := url.Parse(c.ControlPlane)
	if err != nil || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) || u.Host == "" {
		return fmt.Errorf("managed: control plane %q must be an https address", c.ControlPlane)
	}
	if c.BoxID == "" || !strings.HasPrefix(c.Licence, "tl1.") {
		return errors.New("managed: box id and licence are required")
	}
	return nil
}

// LoadManagedConfig reads the managed config: nil, nil when the box is not managed.
func LoadManagedConfig() (*ManagedConfig, error) {
	raw, err := os.ReadFile(ManagedConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var c ManagedConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", ManagedConfigPath, err)
	}
	return &c, nil
}

// ManagedStatePath is where the box keeps the control plane's last answer.
var ManagedStatePath = "/var/lib/tiffin/platform/managed-state.json"

// ManagedState is what the control plane said at the last check-in.
type ManagedState struct {
	CheckedAt time.Time `json:"checkedAt"`
	// Answered: the control plane answered (false: it could not be reached;
	// the fields below are from the last answer).
	Answered bool   `json:"answered"`
	Error    string `json:"error,omitempty"`
	// Active: the subscription is paid; the managed extras are on.
	Active bool `json:"active"`
	// Updates: the box may install Tiffin updates by itself.
	Updates bool `json:"updates"`
	// Message is for people ("Updates are paused: …").
	Message string `json:"message,omitempty"`
	// MemoryMB is the RAM the box last provisioned for (a resize retunes).
	MemoryMB int `json:"memoryMB,omitempty"`
	// LastAnswerAt is when the control plane last answered.
	LastAnswerAt time.Time `json:"lastAnswerAt"`
	// OffsiteSealed is the newest off-site grant (OffsiteGrant), sealed to
	// the box's key, and OffsiteExpiresAt when its credentials expire.
	OffsiteSealed    string    `json:"offsiteSealed,omitempty"`
	OffsiteExpiresAt time.Time `json:"offsiteExpiresAt,omitzero"`
}

// LoadManagedState reads the last state (zero when none).
func LoadManagedState() ManagedState {
	var s ManagedState
	if raw, err := os.ReadFile(ManagedStatePath); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// SaveManagedState writes the state.
func SaveManagedState(s ManagedState) error {
	raw, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(ManagedStatePath), 0o700); err != nil {
		return err
	}
	tmp := ManagedStatePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ManagedStatePath)
}

// ManagedUpdatesPaused says whether a managed box's control plane said not
// to install updates, and why. Only an explicit answer pauses them: a box
// that cannot reach the control plane, or is not managed, carries on.
func ManagedUpdatesPaused() (bool, string) {
	if c, err := LoadManagedConfig(); err != nil || c == nil {
		return false, ""
	}
	s := LoadManagedState()
	if s.LastAnswerAt.IsZero() || s.Updates {
		return false, ""
	}
	msg := s.Message
	if msg == "" {
		msg = "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account."
	}
	return true, msg
}

// OffsiteGrant is the off-site backup storage that comes with a managed
// box: a folder of its own in ShipTiffin's backup bucket, and temporary
// credentials that reach only that folder. The control plane mints them
// (Cloudflare R2 temporary credentials), seals them to the box's key and
// hands them over at check-ins, before they expire, while the subscription
// is active. The box encrypts what it copies there with a passphrase only
// its owner holds.
type OffsiteGrant struct {
	Endpoint        string    `json:"endpoint"` // https://<account>.r2.cloudflarestorage.com
	Bucket          string    `json:"bucket"`
	Prefix          string    `json:"prefix"` // the box's folder: its id
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey"`
	SessionToken    string    `json:"sessionToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
	RetentionDays   int       `json:"retentionDays"`
}

// OffsiteGrantAAD binds a sealed grant to its box.
func OffsiteGrantAAD(boxID string) string { return "offsite:" + boxID }

// Validate checks a grant before the box uses it.
func (g *OffsiteGrant) Validate(boxID string, now time.Time) error {
	u, err := url.Parse(g.Endpoint)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "":
		return fmt.Errorf("off-site grant: endpoint %q is not https", g.Endpoint)
	case g.Bucket == "" || g.AccessKeyID == "" || g.SecretAccessKey == "":
		return errors.New("off-site grant: bucket and credentials are required")
	case strings.Trim(g.Prefix, "/") != boxID:
		return fmt.Errorf("off-site grant: the folder %q is not this box's", g.Prefix)
	case !g.ExpiresAt.After(now):
		return errors.New("off-site grant: expired")
	}
	return nil
}

var offsiteGrants struct {
	sync.Mutex
	fn func(ctx context.Context, g *OffsiteGrant) error
}

// HandleOffsiteGrants sets what takes the grants a managed box receives
// (the backup module).
func HandleOffsiteGrants(fn func(ctx context.Context, g *OffsiteGrant) error) {
	offsiteGrants.Lock()
	offsiteGrants.fn = fn
	offsiteGrants.Unlock()
}

// GiveOffsiteGrant hands a grant from the control plane to the backup module.
func GiveOffsiteGrant(ctx context.Context, g *OffsiteGrant) error {
	offsiteGrants.Lock()
	fn := offsiteGrants.fn
	offsiteGrants.Unlock()
	if fn == nil {
		return errors.New("off-site grant: the backup module is not running")
	}
	return fn(ctx, g)
}
