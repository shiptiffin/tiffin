package email

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Delivery events: the relay's provider tells the box what happened after
// it accepted a message (delivered, bounced, marked as spam...). Each
// provider POSTs to a public address on the box, and every request is
// checked before anything is read:
//
//   - SendGrid signs the timestamp and body with ECDSA P-256; the box keeps
//     the public verification key.
//   - Resend signs with Svix: HMAC-SHA256 over "id.timestamp.body" with the
//     webhook's signing secret.
//   - Postmark has no signatures, so the box makes a long random password
//     that goes in the webhook address (HTTP basic auth).
//
// Keys and secrets are stored encrypted with the box's other secrets and
// never shown again. Replays are refused by timestamp where the provider
// signs one, and every event is recorded once by its provider ID, so a
// replayed request inside the window changes nothing.

// Event types, in the box's own words.
const (
	EventDelivered    = "delivered"
	EventDeferred     = "deferred"
	EventBounced      = "bounced"
	EventDropped      = "dropped"
	EventComplained   = "complained"
	EventUnsubscribed = "unsubscribed"
	EventOpened       = "opened"
	EventClicked      = "clicked"
	EventFailed       = "failed"
)

// Statuses events can move a relayed message to.
const (
	StatusDelivered  = "delivered"
	StatusBounced    = "bounced"
	StatusComplained = "complained"
)

const (
	// maxEventBody is the largest webhook request read (SendGrid batches).
	maxEventBody = 5 << 20
	// svixTolerance is how far a Svix timestamp may be from now (Svix's advice).
	svixTolerance = 5 * time.Minute
	// sendgridMaxAge: SendGrid retries for up to 24 hours and does not say it
	// re-signs, so its signed timestamp may be that old. Events are deduped
	// for longer than this (eventKeep), so a replay inside it is a no-op.
	sendgridMaxAge = 25 * time.Hour
	// futureSkew is how far ahead of the box's clock a timestamp may be.
	futureSkew = 5 * time.Minute
	// eventKeep is how long events (and so their dedupe keys) are kept.
	eventKeep = 35 * 24 * time.Hour
	// postmarkUser is the user name in the Postmark webhook address.
	postmarkUser = "tiffin"
)

var errSignature = errors.New("signature does not match")

// Event is one thing that happened to a relayed message.
type Event struct {
	Type      string    `json:"type" enum:"delivered,deferred,bounced,dropped,complained,unsubscribed,opened,clicked,failed"`
	Recipient string    `json:"recipient,omitempty"`
	Detail    string    `json:"detail,omitempty" doc:"What the provider or the receiving server said"`
	Hard      bool      `json:"hard,omitempty" doc:"For bounces: permanent (the address does not take mail)"`
	Provider  string    `json:"provider" doc:"Who reported it"`
	At        time.Time `json:"at"`
}

// inEvent is a parsed event before it is matched to a message.
type inEvent struct {
	Event
	Key        string // the provider's ID for this event, for dedupe
	TiffinID   string // our message ID, when the provider echoes it
	HeaderID   string // the Message-ID header, when echoed
	ProviderID string // the provider's ID for the message
	Subject    string // for the last-resort match
	suppress   string // suppression reason this event implies ("" = none)
}

// ---- verification ----

// parseECDSAKey reads SendGrid's verification key: base64 DER (as the
// dashboard shows it) or PEM.
func parseECDSAKey(s string) (*ecdsa.PublicKey, error) {
	s = strings.TrimSpace(s)
	var der []byte
	if strings.HasPrefix(s, "-----BEGIN") {
		b, _ := pem.Decode([]byte(s))
		if b == nil {
			return nil, errors.New("not a PEM public key")
		}
		der = b.Bytes
	} else {
		var err error
		if der, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), "")); err != nil {
			return nil, errors.New("not base64")
		}
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("not a public key")
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 public key")
	}
	return ek, nil
}

// checkTimestamp refuses a signed timestamp (Unix seconds) that is too old
// or too far ahead.
func checkTimestamp(ts string, maxAge time.Duration, now time.Time) error {
	n, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil || n <= 0 {
		return errors.New("missing or malformed timestamp")
	}
	t := time.Unix(n, 0)
	if now.Sub(t) > maxAge {
		return fmt.Errorf("timestamp is %s old: refused as a replay", now.Sub(t).Round(time.Second))
	}
	if t.Sub(now) > futureSkew {
		return errors.New("timestamp is in the future")
	}
	return nil
}

// verifySendGrid checks SendGrid's Signed Event Webhook: ECDSA over
// sha256(timestamp + body), signature base64 ASN.1 DER.
func verifySendGrid(key string, body []byte, sig, ts string, now time.Time) error {
	pub, err := parseECDSAKey(key)
	if err != nil {
		return fmt.Errorf("the saved verification key is unusable: %w", err)
	}
	if sig == "" || ts == "" {
		return errors.New("unsigned request: no X-Twilio-Email-Event-Webhook-Signature or -Timestamp header")
	}
	if err := checkTimestamp(ts, sendgridMaxAge, now); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return errSignature
	}
	h := sha256.New()
	h.Write([]byte(ts))
	h.Write(body)
	if !ecdsa.VerifyASN1(pub, h.Sum(nil), raw) {
		return errSignature
	}
	return nil
}

// svixKey decodes a Svix signing secret ("whsec_" + base64).
func svixKey(secret string) ([]byte, error) {
	s := strings.TrimSpace(secret)
	s = strings.TrimPrefix(s, "whsec_")
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(k) < 16 {
		return nil, errors.New("not a signing secret (whsec_ followed by base64)")
	}
	return k, nil
}

// verifySvix checks a Svix-signed request (Resend): HMAC-SHA256 of
// "id.timestamp.body", base64, in a space-separated list of "v1,<sig>".
func verifySvix(secret string, body []byte, id, ts, sigs string, now time.Time) error {
	key, err := svixKey(secret)
	if err != nil {
		return fmt.Errorf("the saved signing secret is unusable: %w", err)
	}
	if id == "" || ts == "" || sigs == "" {
		return errors.New("unsigned request: no svix-id, svix-timestamp or svix-signature header")
	}
	if err := checkTimestamp(ts, svixTolerance, now); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, part := range strings.Fields(sigs) {
		ver, sig, ok := strings.Cut(part, ",")
		if !ok || ver != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return errSignature
}

// verifyBasic checks the Postmark webhook's user name and password.
func verifyBasic(token string, req *http.Request) error {
	user, pass, ok := req.BasicAuth()
	if !ok {
		return errors.New("no user name and password in the request")
	}
	u := subtle.ConstantTimeCompare([]byte(user), []byte(postmarkUser))
	p := subtle.ConstantTimeCompare([]byte(pass), []byte(token))
	if u&p != 1 {
		return errors.New("wrong user name or password")
	}
	return nil
}

// header returns the first present of several header names.
func header(req *http.Request, names ...string) string {
	for _, n := range names {
		if v := req.Header.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// verify checks a request for provider with its saved key.
func verify(provider, key string, req *http.Request, body []byte, now time.Time) error {
	switch provider {
	case ProviderSendGrid:
		return verifySendGrid(key, body, req.Header.Get("X-Twilio-Email-Event-Webhook-Signature"), req.Header.Get("X-Twilio-Email-Event-Webhook-Timestamp"), now)
	case ProviderResend:
		// Svix's headers; the same scheme also goes by Standard Webhooks' names.
		return verifySvix(key, body, header(req, "svix-id", "webhook-id"), header(req, "svix-timestamp", "webhook-timestamp"),
			header(req, "svix-signature", "webhook-signature"), now)
	case ProviderPostmark:
		return verifyBasic(key, req)
	}
	return errors.New("this provider does not send events")
}

// checkKey validates a key before it is saved.
func checkKey(provider, key string) error {
	switch provider {
	case ProviderSendGrid:
		if _, err := parseECDSAKey(key); err != nil {
			return invalid("key: that isn't SendGrid's verification key (%v). Copy it from the webhook once Signed Event Webhook is on", err)
		}
	case ProviderResend:
		if _, err := svixKey(key); err != nil {
			return invalid("key: that isn't a Resend signing secret. It starts with whsec_ and is on the webhook's page")
		}
	}
	return nil
}

// eventProviders are the providers whose events the box reads.
var eventProviders = []string{ProviderSendGrid, ProviderResend, ProviderPostmark}

func webhookSecretName(provider string) string { return "WEBHOOK_" + strings.ToUpper(provider) }
