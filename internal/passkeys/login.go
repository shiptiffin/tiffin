package passkeys

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Passkey sign-in: the dashboard asks for a discoverable credential (no
// username), the person picks a passkey, and the box finds who they are from
// the credential ID and user handle. The challenge is bound to the box's RP
// ID and origin (from PublicURL) like every other ceremony here, expires
// after LoginChallengeTTL and works once.
//
// The box keeps nothing per challenge it hands out: a challenge carries its
// own expiry and a MAC under a key only this process holds, so anyone may ask
// for as many as the rate limits allow without crowding out anyone else's.
// Only challenges a real passkey has signed are remembered (spent), until
// they expire, so a replayed assertion is refused.

// LoginChallengeTTL is how long a sign-in challenge stays valid.
const LoginChallengeTTL = 2 * time.Minute

// Sign-in errors. All mean "not signed in"; the text says what to do.
var (
	ErrLoginExpired   = errors.New("the passkey prompt expired or was already used; start again")
	ErrUnknownPasskey = errors.New("this passkey is not registered on this box. Sign in with a link (`tiffin login`), then add it in Settings › Passkeys; passkeys added before passkey sign-in may need adding again")
	ErrLoginFailed    = errors.New("the passkey check failed; try again")
)

// SignIn is who a passkey assertion proved to be.
type SignIn struct {
	Person      string // person ID (usr_...)
	PasskeyID   string // base64url credential ID
	PasskeyName string
}

// A challenge is expiry (8 bytes, Unix milliseconds) | nonce (16) | MAC (16).
const (
	challengeNonce = 16
	challengeMAC   = 16
	challengeLen   = 8 + challengeNonce + challengeMAC
)

func (m *Manager) challengeMAC(payload []byte) []byte {
	h := hmac.New(sha256.New, m.loginKey[:])
	h.Write(payload)
	return h.Sum(nil)[:challengeMAC]
}

// loginOptions makes assertion options (and their session data) for
// challenge, or a fresh challenge when it is nil.
func (m *Manager) loginOptions(challenge []byte) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	if challenge == nil {
		challenge = make([]byte, challengeLen)
		binary.BigEndian.PutUint64(challenge, uint64(m.now().Add(LoginChallengeTTL).UnixMilli()))
		_, _ = rand.Read(challenge[8 : 8+challengeNonce])
		copy(challenge[8+challengeNonce:], m.challengeMAC(challenge[:8+challengeNonce]))
	}
	return m.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithChallenge(challenge))
}

// BeginLogin returns assertion options for a discoverable passkey: no
// allowCredentials, user verification required, a fresh challenge.
func (m *Manager) BeginLogin(ctx context.Context) (*protocol.CredentialAssertion, error) {
	ca, _, err := m.loginOptions(nil)
	if err != nil {
		return nil, err
	}
	ca.Response.Timeout = int(LoginChallengeTTL.Milliseconds())
	return ca, nil
}

// openChallenge checks that challenge (base64url, from clientDataJSON) is one
// of ours, unexpired and not yet spent, and returns its bytes.
func (m *Manager) openChallenge(challenge string) ([]byte, time.Time, bool) {
	b, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(b) != challengeLen || !hmac.Equal(b[8+challengeNonce:], m.challengeMAC(b[:8+challengeNonce])) {
		return nil, time.Time{}, false
	}
	exp := time.UnixMilli(int64(binary.BigEndian.Uint64(b)))
	if !m.now().Before(exp) {
		return nil, time.Time{}, false
	}
	m.loginMu.Lock()
	_, spent := m.spent[challenge]
	m.loginMu.Unlock()
	return b, exp, !spent
}

// spend marks challenge used, once: false if it already was (a replay racing
// the first use). Expired entries are dropped as it goes.
func (m *Manager) spend(challenge string, exp time.Time) bool {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	if _, ok := m.spent[challenge]; ok {
		return false
	}
	now := m.now()
	for k, e := range m.spent {
		if !now.Before(e) {
			delete(m.spent, k)
		}
	}
	m.spent[challenge] = exp
	return true
}

// FinishLogin verifies a discoverable assertion against the stored passkeys
// and returns the person it belongs to. The caller must still check that the
// person may sign in (not removed) before minting a session.
func (m *Manager) FinishLogin(ctx context.Context, response json.RawMessage) (*SignIn, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoginFailed, err)
	}
	// The challenge in clientDataJSON must be one of ours (its MAC), live
	// and unspent; the signature over it is checked below.
	challenge := parsed.Response.CollectedClientData.Challenge
	raw, exp, ok := m.openChallenge(challenge)
	if !ok {
		return nil, ErrLoginExpired
	}
	_, s, err := m.loginOptions(raw)
	if err != nil {
		return nil, err
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
	if !m.spend(challenge, exp) {
		return nil, ErrLoginExpired
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if cred.Authenticator.CloneWarning {
		_ = m.db.Audit(ctx, found.person, "passkey.clone_warning", id, map[string]any{"ceremony": "sign-in"})
		return nil, ErrCloned
	}
	stored, _ := json.Marshal(cred)
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used = ? WHERE id = ?`, string(stored), ts(m.now().UTC()), cred.ID); err != nil {
		return nil, err
	}
	return &SignIn{Person: found.person, PasskeyID: id, PasskeyName: found.name}, nil
}
