package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
