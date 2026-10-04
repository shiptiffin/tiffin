package ghapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxPayload is the largest webhook body GitHub sends (25 MB).
const MaxPayload = 25 << 20

// ErrSignature means a delivery's signature is missing or wrong.
var ErrSignature = errors.New("the webhook signature does not match")

// Verify checks a delivery's X-Hub-Signature-256 header ("sha256=<hex>")
// against an HMAC-SHA256 of the raw body with the webhook secret, in
// constant time.
func Verify(secret string, body []byte, header string) error {
	if secret == "" {
		return errors.New("no webhook secret is configured")
	}
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return ErrSignature
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return ErrSignature
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return ErrSignature
	}
	return nil
}

// Sign returns the X-Hub-Signature-256 value for body (for tests and fakes).
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

var deliveryRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// ValidDelivery reports whether s looks like an X-GitHub-Delivery GUID.
func ValidDelivery(s string) bool { return deliveryRe.MatchString(s) }

// Time is a timestamp GitHub sends either as unix seconds (push events'
// repository.pushed_at) or as RFC 3339.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		t.Time = time.Unix(n, 0).UTC()
		return nil
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

// EventRepo is the repository part of an event.
type EventRepo struct {
	ID            int64   `json:"id"`
	FullName      string  `json:"full_name"`
	DefaultBranch string  `json:"default_branch"`
	Private       bool    `json:"private"`
	PushedAt      Time    `json:"pushed_at"`
	Owner         Account `json:"owner"`
}

// EventInstallation names the installation a delivery is for.
type EventInstallation struct {
	ID int64 `json:"id"`
}

// PushEvent is a "push" delivery.
type PushEvent struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Created    bool   `json:"created"`
	Deleted    bool   `json:"deleted"`
	Forced     bool   `json:"forced"`
	HeadCommit *struct {
		ID        string `json:"id"`
		Message   string `json:"message"`
		Timestamp Time   `json:"timestamp"`
		Author    struct {
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"author"`
	} `json:"head_commit"`
	Repository   EventRepo         `json:"repository"`
	Sender       Account           `json:"sender"`
	Installation EventInstallation `json:"installation"`
}

// Branch is the pushed branch ("" for tags).
func (e *PushEvent) Branch() string {
	b, ok := strings.CutPrefix(e.Ref, "refs/heads/")
	if !ok {
		return ""
	}
	return b
}

// PullRequestEvent is a "pull_request" delivery.
type PullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		HTMLURL   string `json:"html_url"`
		State     string `json:"state"`
		Merged    bool   `json:"merged"`
		UpdatedAt Time   `json:"updated_at"`
		User      Account
		Head      struct {
			Ref  string     `json:"ref"`
			SHA  string     `json:"sha"`
			Repo *EventRepo `json:"repo"` // nil when the fork was deleted
		} `json:"head"`
		Base struct {
			Ref  string    `json:"ref"`
			SHA  string    `json:"sha"`
			Repo EventRepo `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository   EventRepo         `json:"repository"`
	Sender       Account           `json:"sender"`
	Installation EventInstallation `json:"installation"`
}

// FromFork reports whether the pull request's code comes from another
// repository than the one it targets.
func (e *PullRequestEvent) FromFork() bool {
	h := e.PullRequest.Head.Repo
	return h == nil || !strings.EqualFold(h.FullName, e.PullRequest.Base.Repo.FullName)
}

// InstallationEvent is an "installation" or "installation_repositories" delivery.
type InstallationEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64   `json:"id"`
		Account Account `json:"account"`
	} `json:"installation"`
	Sender Account `json:"sender"`
}

// Parse decodes a delivery body.
func Parse[T any](body []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidSHA reports whether s is a full commit SHA (SHA-1 or SHA-256).
func ValidSHA(s string) bool { return shaRe.MatchString(s) }
