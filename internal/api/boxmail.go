package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/passkeys"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// The box's own mail to the people who use its dashboard: invites, sign-in
// links (sent by an admin, or asked for on the login page), a notice when
// someone signs in from a browser the box has not seen them use before, and
// one when they create an API key in the dashboard or add or remove a
// passkey. The
// email module sends it (SetBoxMailer): through the relay when the box has
// one, else into the box's dev inbox, like any other mail.

// Box mail kinds.
const (
	BoxMailInvite    = "invite"      // a new person's first sign-in link
	BoxMailLink      = "link"        // a fresh link an admin made for someone
	BoxMailSignIn    = "sign-in"     // a link the person asked for on the login page
	BoxMailNewDevice = "new-sign-in" // they signed in from a new browser
	BoxMailNewKey    = "new-key"     // they created an API key in the dashboard
	// BoxMailNewPasskey: a passkey was added to their sign-ins.
	BoxMailNewPasskey = "new-passkey"
	// BoxMailPasskeyRemoved: one of their passkeys was removed.
	BoxMailPasskeyRemoved = "passkey-removed"
)

// BoxMail is one message from the box to a person.
type BoxMail struct {
	Kind      string
	To        string // the person's address
	Name      string // the person's name
	Role      string
	URL       string    // the sign-in link (invite, link, sign-in)
	ExpiresAt time.Time // when the link stops working
	By        string    // who invited them or made the link
	Device    string    // new-sign-in, new-key, passkey notices: "Chrome on macOS"
	IP        string    // new-sign-in, new-key, passkey notices: the address it came from
	Where     string    // new-sign-in, new-key, passkey notices: the country that address is in ("United States"); empty when unknown
	Via       string    // new-sign-in: how they signed in: "Sign-in link", "Passkey", "Google", "GitHub"
	At        time.Time // new-sign-in, new-key, passkey notices: when
	Dashboard string    // the dashboard's address
	// new-key: the key (Device, IP, Where and At say where it was made from).
	Key *tokens.Key
	// new-passkey, passkey-removed: the passkey's name ("MacBook").
	Passkey string
}

// BoxMailResult says what happened to a box message.
type BoxMailResult struct {
	Delivery string `json:"delivery" enum:"relay,inbox,suppressed,failed" doc:"relay: sent through the box's mail service; inbox: no mail service is connected, so it waits in the box's dev inbox; suppressed: the address bounced before; failed: it could not be sent"`
	To       string `json:"to" doc:"The address it went to"`
	Detail   string `json:"detail,omitempty" doc:"Why, in plain words"`
}

// BoxMailer sends the box's own mail. The email module implements it.
type BoxMailer interface {
	SendBoxMail(ctx context.Context, p *platform.Platform, m BoxMail) (*BoxMailResult, error)
	// BoxMailRelayed reports whether box mail leaves the box now (a relay is set).
	BoxMailRelayed(ctx context.Context, p *platform.Platform) bool
}

var (
	mailerMu sync.RWMutex
	mailer   BoxMailer
)

// SetBoxMailer installs the box's mailer (nil turns box mail off).
func SetBoxMailer(m BoxMailer) {
	mailerMu.Lock()
	mailer = m
	mailerMu.Unlock()
}

var (
	locatorMu sync.RWMutex
	locator   func(ip string) string
)

// SetLocator installs a lookup from an IP address to the name of the country
// it is in ("" when unknown). The analytics module installs one when it has its
// country database; nil turns it off. Nothing leaves the box.
func SetLocator(f func(ip string) string) {
	locatorMu.Lock()
	locator = f
	locatorMu.Unlock()
}

func locate(ip string) string {
	locatorMu.RLock()
	f := locator
	locatorMu.RUnlock()
	if f == nil || ip == "" {
		return ""
	}
	return f(ip)
}

func boxMailer() BoxMailer {
	mailerMu.RLock()
	defer mailerMu.RUnlock()
	return mailer
}

// sendBoxMail sends m now, turning a failure into a "failed" result.
func (a *API) sendBoxMail(ctx context.Context, m BoxMail) *BoxMailResult {
	bm := boxMailer()
	if bm == nil || m.To == "" {
		return nil
	}
	if m.Dashboard == "" {
		m.Dashboard = strings.TrimRight(a.deps.PublicURL, "/")
	}
	res, err := bm.SendBoxMail(ctx, a.deps.Platform, m)
	if err != nil {
		return &BoxMailResult{Delivery: "failed", To: m.To, Detail: sentenceCase(err.Error())}
	}
	return res
}

// sendLater sends m in the background, so a sign-in never waits on mail.
func (a *API) sendLater(m BoxMail) {
	if boxMailer() == nil || m.To == "" {
		return
	}
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = a.sendBoxMail(ctx, m)
	}()
}

// WaitBackground waits for box mail sent in the background (tests).
func (a *API) WaitBackground() { a.bg.Wait() }

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---- "Email me a sign-in link" ----

// Limits for sign-in links by email: per client address, then per email
// address. Both apply before the box looks the address up, so they say
// nothing about whether it belongs to anyone.
const (
	EmailSignInPerIP      = 5 // links per IP every 15 minutes
	EmailSignInPerAddress = 3 // links per address every hour
)

// EmailSignInAnswer is the one answer to every sign-in-by-email request.
type EmailSignInAnswer struct {
	Detail           string `json:"detail"`
	ExpiresInMinutes int    `json:"expiresInMinutes"`
}

// EmailSignInStatus says whether the login page should offer sign-in by email.
type EmailSignInStatus struct {
	Available bool `json:"available" doc:"The box sends mail (a relay is connected), so it can email people a sign-in link"`
}

func emailSignInAnswer() EmailSignInAnswer {
	return EmailSignInAnswer{
		Detail:           "If that address belongs to someone on this box, a sign-in link is on its way. It works once, within 15 minutes.",
		ExpiresInMinutes: int(tokens.EmailLinkTTL / time.Minute),
	}
}

var emailShape = regexp.MustCompile(`^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$`)

func (a *API) registerBoxMail() {
	api := a.api
	ipLimit := newRateLimiter(EmailSignInPerIP, 15*time.Minute)
	addrLimit := newRateLimiter(EmailSignInPerAddress, time.Hour)

	st := op("session-email-status", http.MethodGet, "/v1/session/email", "-", RiskRead, "Can the box email a sign-in link?",
		"Whether the login page should offer \"Email me a sign-in link\": true when the box sends mail through a relay.", "system")
	st.Security = nil
	huma.Register(api, st, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body EmailSignInStatus }, error) {
		bm := boxMailer()
		return &struct{ Body EmailSignInStatus }{EmailSignInStatus{Available: bm != nil && bm.BoxMailRelayed(ctx, a.deps.Platform)}}, nil
	}))

	o := op("session-email", http.MethodPost, "/v1/session/email", "-", RiskWrite, "Email me a sign-in link",
		"Emails a one-time sign-in link (valid 15 minutes) to the person on this box with that address. The answer is the same whether "+
			"or not the address belongs to anyone. Limited per client address and per email address. Used by the dashboard's login page.", "system")
	o.Security = nil
	o.Errors = append(o.Errors, 429)
	o.Middlewares = huma.Middlewares{func(ctx huma.Context, next func(huma.Context)) {
		ip := callerIP(ctx)
		if !sameOrigin(ctx) {
			_ = huma.WriteErr(a.api, ctx, http.StatusForbidden, "This request came from another site.")
			return
		}
		if ok, wait := ipLimit.allow(ip); !ok {
			writeRateLimited(ctx, wait, "too many sign-in emails asked for from your address")
			return
		}
		next(huma.WithValue(ctx, clientIPKey{}, ip))
	}}
	huma.Register(api, o, wrap(func(ctx context.Context, in *struct {
		Body struct {
			Email string `json:"email" minLength:"3" maxLength:"254" doc:"Your email address on this box"`
		}
	}) (*struct {
		Status int
		Body   EmailSignInAnswer
	}, error) {
		addr := tokens.NormEmail(in.Body.Email)
		if a, err := mail.ParseAddress(addr); err != nil || a.Address != addr || !emailShape.MatchString(addr) {
			return nil, problem(422, "validation", "email: that isn't an email address")
		}
		if ok, wait := addrLimit.allow(addr); !ok {
			p := problem(http.StatusTooManyRequests, "rate_limited", "too many sign-in emails asked for this address")
			p.Hint = "wait " + strconv.Itoa(int(math.Ceil(wait.Minutes()))) + " minutes, then try again; links already sent still work"
			return nil, p
		}
		ip := clientIPFrom(ctx)
		// The lookup and the mail happen after the answer, so the answer and
		// its timing are the same for every address.
		a.bg.Add(1)
		go func() {
			defer a.bg.Done()
			bctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a.emailSignIn(bctx, addr, ip)
		}()
		return &struct {
			Status int
			Body   EmailSignInAnswer
		}{http.StatusAccepted, emailSignInAnswer()}, nil
	}))
}

// emailSignIn sends a sign-in link to the person with addr, if there is one.
// The link counts as a strong sign-in (proof they read that inbox), so it is
// only made when the mail leaves the box: a link waiting in the box's dev
// inbox is readable by any admin, and is cancelled at once.
func (a *API) emailSignIn(ctx context.Context, addr, ip string) {
	person, err := a.deps.Tokens.PersonByEmail(ctx, addr)
	if err != nil {
		_ = a.deps.DB.Audit(ctx, "via:email", "session.email_unknown", "", map[string]any{"ip": ip})
		return
	}
	if bm := boxMailer(); bm == nil || !bm.BoxMailRelayed(ctx, a.deps.Platform) {
		_ = a.deps.DB.Audit(ctx, "via:email", "session.email_link", person.ID, map[string]any{"ip": ip, "delivery": "none"})
		return
	}
	code, exp, err := a.deps.Tokens.EmailLoginLink(ctx, person.ID, addr)
	if err != nil {
		return
	}
	res := a.sendBoxMail(ctx, BoxMail{Kind: BoxMailSignIn, To: person.Email, Name: person.Name, Role: person.Role,
		URL: strings.TrimRight(a.deps.PublicURL, "/") + "/login#" + code, ExpiresAt: exp, IP: ip})
	delivery := ""
	if res != nil {
		delivery = res.Delivery
	}
	if delivery != "relay" {
		_ = a.deps.Tokens.CancelEmailLinks(ctx, person.ID)
	}
	_ = a.deps.DB.Audit(ctx, "via:email", "session.email_link", person.ID, map[string]any{"ip": ip, "delivery": delivery})
}

func writeRateLimited(ctx huma.Context, wait time.Duration, detail string) {
	secs := int(math.Ceil(wait.Seconds()))
	p := problem(http.StatusTooManyRequests, "rate_limited", detail)
	p.Hint = "wait " + strconv.Itoa(secs) + "s and try again"
	ctx.SetHeader("Retry-After", strconv.Itoa(secs))
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(http.StatusTooManyRequests)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
}

// newRateLimiter allows burst calls per key, refilled evenly over every.
func newRateLimiter(burst int, every time.Duration) *ipLimiter {
	l := newIPLimiter(burst)
	l.perMin = float64(burst) * float64(time.Minute) / float64(every)
	l.capacity = float64(burst)
	return l
}

// ---- new API key notices ----

// keyCreated tells the person who made key in their dashboard session about
// it, by email, with the browser and address it came from: a key outlives
// the session, so a stolen session's key must not go unnoticed.
func (a *API) keyCreated(ctx context.Context, by *tokens.Principal, key *tokens.Key) {
	person, err := a.deps.Tokens.GetPerson(ctx, by.Person)
	if err != nil || person.Email == "" {
		return
	}
	ip := clientIPFrom(ctx)
	device := ""
	if c, err := a.deps.Tokens.SessionClient(ctx, by.TokenID); err == nil {
		device = c.Device
	}
	a.sendLater(BoxMail{Kind: BoxMailNewKey, To: person.Email, Name: person.Name, Role: person.Role,
		Device: device, IP: ip, Where: locate(ip), At: key.CreatedAt, Key: key})
}

// ---- passkey notices ----

// passkeyNotice tells the person whose passkeys changed (kind is
// BoxMailNewPasskey or BoxMailPasskeyRemoved), with the browser and address
// it came from: a passkey signs in for good, so one added from a stolen
// session must not go unnoticed, and a removal is worth knowing about too.
func (a *API) passkeyNotice(ctx context.Context, by *tokens.Principal, kind string, pk *passkeys.Passkey) {
	if pk == nil {
		return
	}
	id := by.Person
	if id == "" {
		id = tokens.OwnerPerson // the owner's CLI token manages the owner's passkeys
	}
	person, err := a.deps.Tokens.GetPerson(ctx, id)
	if err != nil || person.Email == "" {
		return
	}
	ip := clientIPFrom(ctx)
	device := ""
	if by.IsSession() {
		if c, err := a.deps.Tokens.SessionClient(ctx, by.TokenID); err == nil {
			device = c.Device
		}
	}
	at := time.Now().UTC()
	if kind == BoxMailNewPasskey && !pk.CreatedAt.IsZero() {
		at = pk.CreatedAt
	}
	a.sendLater(BoxMail{Kind: kind, To: person.Email, Name: person.Name, Role: person.Role,
		Device: device, IP: ip, Where: locate(ip), At: at, Passkey: pk.Name})
}

// ---- new sign-in notices ----

// DeviceCookie remembers a browser so the box can tell a person when they
// sign in from one it has not seen them use. It holds a random ID, nothing else.
const DeviceCookie = "tiffin_device"

const devicesNS = "signin-devices"

type knownDevice struct {
	Hash  string    `json:"hash"` // sha256 of the cookie, shortened
	Label string    `json:"label"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

var deviceIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)

// signedIn records how and where session signed in (Settings › Sign-ins
// lists it), remembers the browser and, when the person has signed in before
// but never from this browser, emails them a notice. The first sign-in (an
// invite) is quiet. method is a tokens.Method*; via is the same in words for
// the notice ("Sign-in link", "Passkey", "Google"). It returns the device
// cookie to set.
func (a *API) signedIn(ctx context.Context, session, personID, cookie, ua, ip, method, via string) *http.Cookie {
	person, err := a.deps.Tokens.GetPerson(ctx, personID)
	if err != nil || a.deps.DB == nil {
		return nil
	}
	where := locate(ip)
	_ = a.deps.Tokens.SetClient(ctx, session, tokens.Client{Method: method, Device: deviceLabel(ua), IP: ip, Country: where})
	id := cookie
	if !deviceIDRE.MatchString(id) {
		var b [18]byte
		_, _ = rand.Read(b[:])
		id = base64.RawURLEncoding.EncodeToString(b[:])
	}
	sum := sha256.Sum256([]byte(id))
	h := hex.EncodeToString(sum[:12])
	now := time.Now().UTC()
	label := deviceLabel(ua)

	var list []knownDevice
	if raw, ok, _ := a.deps.DB.KVGet(ctx, devicesNS, personID); ok {
		_ = json.Unmarshal(raw, &list)
	}
	isNew := true
	for i := range list {
		if list[i].Hash == h {
			list[i].Last, list[i].Label, isNew = now, label, false
		}
	}
	notify := isNew && len(list) > 0
	if isNew {
		list = append(list, knownDevice{Hash: h, Label: label, First: now, Last: now})
		if len(list) > 20 { // forget the browsers used longest ago
			sort.Slice(list, func(i, j int) bool { return list[i].Last.After(list[j].Last) })
			list = list[:20]
		}
	}
	if raw, err := json.Marshal(list); err == nil {
		_ = a.deps.DB.KVPut(ctx, devicesNS, personID, raw)
	}
	if notify && person.Email != "" {
		a.sendLater(BoxMail{Kind: BoxMailNewDevice, To: person.Email, Name: person.Name, Role: person.Role,
			Device: label, IP: ip, Where: where, Via: via, At: now})
	}
	return &http.Cookie{Name: DeviceCookie, Value: id, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 400 * 24 * 60 * 60}
}

// deviceLabel names a browser in plain words from its User-Agent:
// "Chrome on macOS", "Safari on iPhone".
func deviceLabel(ua string) string {
	browser := "A browser"
	switch {
	case ua == "":
		return "An unknown browser"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(strings.ToLower(ua), "curl"):
		return "A script (curl)"
	}
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if os == "" {
		return browser
	}
	return browser + " on " + os
}

// withClientIP puts the caller's address in the context.
func withClientIP(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithValue(ctx, clientIPKey{}, callerIP(ctx)))
}
