// Package passkeys lets people sign in to the dashboard with a passkey.
//
// Each person registers their own passkeys (discoverable, user verification
// required). Sign-in asks for any discoverable credential and finds the
// person from it. Agents never hold passkeys.
//
// (Passkeys used to approve plans agents asked for. That approval layer is
// gone: keys decide what an agent may change, the agent's client asks the
// person before destructive tools, and every change is recorded and can be
// undone. The approvals table stays in the state database, unused.)
package passkeys

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/shiptiffin/tiffin/internal/state"
)

// Errors.
var (
	ErrNotFound = errors.New("passkey not found")
	ErrNoPerson = errors.New("passkeys belong to a person: sign in to the dashboard to add or list them")
	ErrCloned   = errors.New("this passkey's signature counter went backwards, which suggests it was copied, so it was refused; remove it and add a new one")
)

// Manager stores passkeys and runs their ceremonies.
type Manager struct {
	db  *state.DB
	wa  *webauthn.WebAuthn
	now func() time.Time

	// loginKey signs sign-in challenges; spent holds the ones a passkey
	// signed, until they expire (see BeginLogin).
	loginKey [32]byte
	loginMu  sync.Mutex
	spent    map[string]time.Time
}

// New returns a manager. rpID is the dashboard host (e.g.
// "dashboard.tiffin.localhost"); origin its URL.
func New(db *state.DB, rpID, origin string) (*Manager, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Tiffin",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Discoverable (resident) credentials, so a passkey can sign in
			// without a username. Passkeys added before this was required
			// may not be discoverable; adding them again makes them sign in.
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, err
	}
	m := &Manager{db: db, wa: wa, now: time.Now, spent: map[string]time.Time{}}
	_, _ = rand.Read(m.loginKey[:])
	return m, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
