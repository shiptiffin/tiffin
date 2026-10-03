// Package passkeytest is a software WebAuthn authenticator for tests: it
// answers the box's creation and assertion options the way a browser and a
// platform passkey would (ES256, "none" attestation, user verified), so
// tests drive the real go-webauthn checks end to end.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// Authenticator flags.
const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

// Authenticator holds passkeys for one origin.
type Authenticator struct {
	Origin string
	// NoUV makes the next assertions claim no user verification.
	NoUV bool
}

// Credential is one passkey. Tests may change Counter or UserHandle to
// simulate a cloned authenticator or a forged user handle.
type Credential struct {
	ID         []byte
	UserHandle []byte
	Counter    uint32
	key        *ecdsa.PrivateKey
}

// New returns an authenticator for origin (e.g. https://dashboard.tiffin.localhost).
func New(origin string) *Authenticator { return &Authenticator{Origin: origin} }

var b64 = base64.RawURLEncoding

func (a *Authenticator) clientData(typ string, challenge []byte) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": b64.EncodeToString(challenge), "origin": a.Origin, "crossOrigin": false})
	return b
}

func authData(rpID string, flags byte, counter uint32, attested []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, counter)
	return append(out, attested...)
}

// Create answers creation options (the JSON the box serves, {"publicKey":
// ...}) and returns the credential JSON the box expects back.
func (a *Authenticator) Create(options []byte) (json.RawMessage, *Credential, error) {
	var cc protocol.CredentialCreation
	if err := json.Unmarshal(options, &cc); err != nil {
		return nil, nil, fmt.Errorf("creation options: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	uh, _ := cc.Response.User.ID.(protocol.URLEncodedBase64)
	if uh == nil {
		// The user ID decodes as a string when it comes from JSON.
		s, _ := cc.Response.User.ID.(string)
		uh, _ = b64.DecodeString(s)
	}
	c := &Credential{ID: id, UserHandle: uh, key: key}
	pub, _ := key.PublicKey.ECDH()
	raw := pub.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256), XCoord: raw[1:33], YCoord: raw[33:65],
	})
	if err != nil {
		return nil, nil, err
	}
	att := make([]byte, 16) // AAGUID: zero
	att = binary.BigEndian.AppendUint16(att, uint16(len(id)))
	att = append(att, id...)
	att = append(att, cose...)
	ad := authData(cc.Response.RelyingParty.ID, flagUP|flagUV|flagAT, 0, att)
	obj, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		return nil, nil, err
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(id), "rawId": b64.EncodeToString(id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", cc.Response.Challenge)),
			"attestationObject": b64.EncodeToString(obj),
		},
	})
	return out, c, nil
}

// Get answers assertion options with credential c (as a discoverable
// passkey: the user handle is included) and returns the assertion JSON.
func (a *Authenticator) Get(options []byte, c *Credential) (json.RawMessage, error) {
	var ca protocol.CredentialAssertion
	if err := json.Unmarshal(options, &ca); err != nil {
		return nil, fmt.Errorf("assertion options: %w", err)
	}
	c.Counter++
	flags := byte(flagUP | flagUV)
	if a.NoUV {
		flags = flagUP
	}
	ad := authData(ca.Response.RelyingPartyID, flags, c.Counter, nil)
	cd := a.clientData("webauthn.get", ca.Response.Challenge)
	cdh := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.ID), "rawId": b64.EncodeToString(c.ID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(c.UserHandle),
		},
	})
	return out, nil
}
