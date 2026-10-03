package approvals

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkey sign-in: the dashboard asks for a discoverable credential (no
// username), the person picks a passkey, and the box finds who they are from
// the credential ID and user handle. The challenge lives in memory only: it
// is single use, expires after LoginChallengeTTL and is bound to the box's
// RP ID and origin (from PublicURL) like every other ceremony here.

// LoginChallengeTTL is how long a sign-in challenge stays valid.
const LoginChallengeTTL = 2 * time.Minute

// maxLoginCeremonies caps challenges waiting in memory, so unauthenticated
// callers cannot grow it without bound (the API also rate-limits per IP).
const maxLoginCeremonies = 2048

// Sign-in errors. All mean "not signed in"; the text says what to do.
var (
	ErrLoginExpired   = errors.New("the passkey prompt expired or was already used; start again")
	ErrLoginBusy      = errors.New("too many passkey sign-ins are in progress; try again in a minute")
	ErrUnknownPasskey = errors.New("this passkey is not registered on this box. Sign in with a link (`tiffin login`), then add it in Settings › Passkeys; passkeys added before passkey sign-in may need adding again")
	ErrLoginFailed    = errors.New("the passkey check failed; try again")
)

type loginCeremony struct {
	session webauthn.SessionData
	expires time.Time
}

// SignIn is who a passkey assertion proved to be.
type SignIn struct {
	Person      string // person ID (usr_...)
	PasskeyID   string // base64url credential ID
	PasskeyName string
}

// BeginLogin returns assertion options for a discoverable passkey: no
// allowCredentials, user verification required, a fresh challenge.
func (m *Manager) BeginLogin(ctx context.Context) (*protocol.CredentialAssertion, error) {
	ca, s, err := m.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	ca.Response.Timeout = int(LoginChallengeTTL.Milliseconds())
	now := m.now()
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	for k, c := range m.logins {
		if !now.Before(c.expires) {
			delete(m.logins, k)
		}
	}
	if len(m.logins) >= maxLoginCeremonies {
		return nil, ErrLoginBusy
	}
	m.logins[s.Challenge] = loginCeremony{session: *s, expires: now.Add(LoginChallengeTTL)}
	return ca, nil
}

// takeLogin removes and returns the ceremony for challenge (once).
func (m *Manager) takeLogin(challenge string) (*webauthn.SessionData, bool) {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	c, ok := m.logins[challenge]
	if !ok {
		return nil, false
	}
	delete(m.logins, challenge)
	if !m.now().Before(c.expires) {
		return nil, false
	}
	return &c.session, true
}

// FinishLogin verifies a discoverable assertion against the stored passkeys
// and returns the person it belongs to. The caller must still check that the
// person may sign in (not removed) before minting a session.
func (m *Manager) FinishLogin(ctx context.Context, response json.RawMessage) (*SignIn, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoginFailed, err)
	}
	// The challenge in clientDataJSON names the ceremony; the signature over
	// it is checked below, so a forged one finds nothing or fails.
	s, ok := m.takeLogin(parsed.Response.CollectedClientData.Challenge)
	if !ok {
		return nil, ErrLoginExpired
	}
	var found struct {
		person, name string
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		var person, name string
		err := m.db.SQL().QueryRowContext(ctx, `SELECT coalesce(person, ?), name FROM passkeys WHERE id = ?`, tokens.OwnerPerson, rawID).Scan(&person, &name)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !bytes.Equal([]byte(person), userHandle)) {
			return nil, ErrUnknownPasskey
		} else if err != nil {
			return nil, err
		}
		o, err := m.owner(ctx, person)
		if err != nil {
			return nil, err
		}
		found.person, found.name = person, name
		return o, nil
	}
	cred, err := m.wa.ValidateDiscoverableLogin(handler, *s, parsed)
	if err != nil {
		if errors.Is(err, ErrUnknownPasskey) || found.person == "" {
			return nil, ErrUnknownPasskey
		}
		return nil, fmt.Errorf("%w: %v", ErrLoginFailed, err)
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if cred.Authenticator.CloneWarning {
		_ = m.db.Audit(ctx, found.person, "passkey.clone_warning", id, map[string]any{"ceremony": "sign-in"})
		return nil, ErrCloned
	}
	raw, _ := json.Marshal(cred)
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used = ? WHERE id = ?`, string(raw), ts(m.now().UTC()), cred.ID); err != nil {
		return nil, err
	}
	return &SignIn{Person: found.person, PasskeyID: id, PasskeyName: found.name}, nil
}
