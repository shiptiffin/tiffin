package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type rig struct {
	t   *testing.T
	p   *platform.Platform
	ctx context.Context
	tm  *tokens.Manager
}

// newRig starts the registered module on a fresh in-memory box.
func newRig(t *testing.T) *rig {
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tokens.NewManager(db), Secrets: sec, DataRoot: t.TempDir(),
		Domain: "tiffin.localhost", PublicURL: "https://dashboard.tiffin.localhost:8443", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	*mod = Module{smtpAddr: freeAddr(t), backoff: func(int) time.Duration { return 50 * time.Millisecond }}
	if err := mod.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, p: p, ctx: ctx, tm: p.Tokens}
	r.apply(`{"project":"shop","services":{"email":{}}}`)
	return r
}

func (r *rig) apply(raw string) {
	r.t.Helper()
	mf, err := manifest.Parse([]byte(raw))
	if err != nil {
		r.t.Fatal(err)
	}
	desired, _ := change.Resources(mf)
	plan, err := r.p.Engine.Plan(r.ctx, mf.Project, desired)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.p.Engine.Apply(r.ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		r.t.Fatal(err)
	}
	for _, op := range plan.Ops {
		if op.Address != "service/email" {
			continue
		}
		var spec json.RawMessage
		if op.Action != change.Delete {
			spec = op.After
		}
		if err := mod.Reconcile(r.ctx, r.p, mf.Project, op.Address, spec); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) inbox(all bool) []*record {
	recs, err := listRecords(r.ctx, r.p.DB.SQL(), ListFilter{Project: "shop", All: all})
	if err != nil {
		r.t.Fatal(err)
	}
	return recs
}

// smtpSend submits a message to Tiffin's SMTP server as user.
func (r *rig) smtpSend(user, pass, from string, to []string, body string) error {
	c, err := smtp.Dial(mod.smtpAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
			return err
		}
	}
	return c.SendMail(from, to, strings.NewReader(body))
}

// sink is a test SMTP relay that records what it receives.
type sink struct {
	mu       sync.Mutex
	got      []string // raw messages
	rcpts    [][]string
	deferred map[string]int // address → times to answer 451 first
	addr     string
}

func newSink(t *testing.T) *sink {
	s := &sink{deferred: map[string]int{}}
	srv := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) { return &sinkSession{s: s}, nil }))
	srv.AllowInsecureAuth = true
	srv.Domain = "sink.test"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return s
}

type sinkSession struct {
	s    *sink
	rcpt []string
}

func (ss *sinkSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (ss *sinkSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, u, p string) error {
		if u != "relayuser" || p != "relaypass" {
			return errors.New("bad credentials")
		}
		return nil
	}), nil
}
func (ss *sinkSession) Mail(string, *smtp.MailOptions) error { return nil }
func (ss *sinkSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	ss.s.mu.Lock()
	defer ss.s.mu.Unlock()
	if strings.HasPrefix(to, "bounce") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	}
	if ss.s.deferred[to] > 0 {
		ss.s.deferred[to]--
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "try later"}
	}
	ss.rcpt = append(ss.rcpt, to)
	return nil
}
func (ss *sinkSession) Data(r io.Reader) error {
	b, _ := io.ReadAll(r)
	ss.s.mu.Lock()
	ss.s.got = append(ss.s.got, string(b))
	ss.s.rcpts = append(ss.s.rcpts, ss.rcpt)
	ss.s.mu.Unlock()
	return nil
}
func (ss *sinkSession) Reset()        { ss.rcpt = nil }
func (ss *sinkSession) Logout() error { return nil }

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInboxAPIAndSMTP(t *testing.T) {
	r := newRig(t)
	env, err := mod.Env(r.ctx, r.p, "shop", "web")
	if err != nil || env["EMAIL_FROM"] != "shop@tiffin.localhost" || !strings.HasPrefix(env["SMTP_URL"], "smtp://shop:") {
		t.Fatalf("env: %v %v", env, err)
	}
	_, h := mod.state()
	events, stop := h.subscribe("shop")
	defer stop()

	res, err := Send(r.ctx, r.p, "shop", Message{To: []string{"Ada <ada@inbox.dev>"}, Subject: "Verify your email",
		Text: "Open https://shop.tiffin.localhost/verify?t=abc to verify.",
		HTML: `<p>Hi <b>Ada</b></p><a href="https://shop.tiffin.localhost/verify?t=abc&amp;x=1">Verify</a><script>alert(1)</script><img src=x onerror=alert(2)>`})
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivery != DeliveryInbox || res.Status != StatusCaptured || !strings.Contains(res.Reason, "no SMTP relay") {
		t.Fatalf("send result: %+v", res)
	}
	select {
	case ev := <-events:
		if ev.ID != res.ID {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event")
	}
	d, err := mod.detail(r.ctx, r.p, "shop", res.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "Verify your email" || d.From != "shop@tiffin.localhost" ||
		strings.Contains(d.HTML, "script") || strings.Contains(d.HTML, "onerror") || !strings.Contains(d.HTML, "<b>Ada</b>") ||
		len(d.Links) != 2 || d.Links[0] != "https://shop.tiffin.localhost/verify?t=abc&x=1" || !strings.Contains(d.Text, "verify?t=abc") {
		b, _ := json.MarshalIndent(d, "", " ")
		t.Fatalf("detail: %s", b)
	}

	// SMTP submission with the env credentials; From is filled in.
	u, _ := url.Parse(env["SMTP_URL"])
	pw, _ := u.User.Password()
	if err := r.smtpSend("shop", pw, "app@shop.test", []string{"bob@inbox.dev"}, "To: bob@inbox.dev\r\nSubject: via smtp\r\n\r\nhello bob\r\n"); err != nil {
		t.Fatal(err)
	}
	box := r.inbox(false)
	if len(box) != 2 || box[0].Subject != "via smtp" || box[0].Source != "smtp" || !strings.Contains(box[0].From, "shop@tiffin.localhost") {
		t.Fatalf("inbox: %+v", box[0])
	}
	if err := r.smtpSend("shop", "wrong", "a@b.c", []string{"x@y.z"}, "Subject: x\r\n\r\nx"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := r.smtpSend("", "", "a@b.c", []string{"x@y.z"}, "Subject: x\r\n\r\nx"); err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("anonymous submission: %v", err)
	}
	// Search, then delete and clear.
	if l, _ := listRecords(r.ctx, r.p.DB.SQL(), ListFilter{Project: "shop", Query: "VERIFY"}); len(l) != 1 {
		t.Fatalf("search: %d", len(l))
	}
	if n, _ := deleteMessages(r.ctx, r.p, "shop", res.ID); n != 1 {
		t.Fatal("delete")
	}
	if n, _ := deleteMessages(r.ctx, r.p, "shop", ""); n != 1 || len(r.inbox(false)) != 0 {
		t.Fatal("clear")
	}

	// Validation.
	if _, err := Send(r.ctx, r.p, "shop", Message{To: []string{"not an address"}, Subject: "x", Text: "x"}); !errors.As(err, new(*ValidationError)) {
		t.Fatalf("bad address: %v", err)
	}
	if _, err := Send(r.ctx, r.p, "shop", Message{To: []string{"a@b.c"}, Subject: "x", Text: "x", Headers: map[string]string{"From": "x@y.z"}}); err == nil {
		t.Fatal("reserved header accepted")
	}
	if _, err := Send(r.ctx, r.p, "nope", Message{To: []string{"a@b.c"}, Subject: "x", Text: "x"}); !errors.Is(err, errNoProject) {
		t.Fatalf("no email service: %v", err)
	}
}

func TestSuppressionAndRateLimit(t *testing.T) {
	r := newRig(t)
	db := r.p.DB.SQL()
	if err := addSuppression(r.ctx, db, "shop", Suppression{Address: "Gone@Inbox.dev", Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	res, err := Send(r.ctx, r.p, "shop", Message{To: []string{"gone@inbox.dev", "here@inbox.dev"}, Subject: "s", Text: "t"})
	if err != nil || len(res.Suppressed) != 1 || len(res.Recipients) != 1 || res.Recipients[0] != "here@inbox.dev" {
		t.Fatalf("partial suppression: %+v %v", res, err)
	}
	res, err = Send(r.ctx, r.p, "shop", Message{To: []string{"gone@inbox.dev"}, Subject: "s", Text: "t"})
	if err != nil || res.Status != StatusSuppressed || res.Delivery != DeliverySuppressed {
		t.Fatalf("full suppression: %+v %v", res, err)
	}
	if len(r.inbox(false)) != 1 || len(r.inbox(true)) != 2 {
		t.Fatalf("suppressed message must not be in the inbox: %d/%d", len(r.inbox(false)), len(r.inbox(true)))
	}
	env, _ := mod.Env(r.ctx, r.p, "shop", "")
	// SMTP behaves like the API: accepted, logged as suppressed, not sent.
	if err := r.smtpSend("shop", env["SMTP_PASSWORD"], "a@shop.test", []string{"gone@inbox.dev"}, "Subject: x\r\n\r\nx"); err != nil {
		t.Fatalf("smtp suppressed rcpt: %v", err)
	}
	// Found by source: two messages in the same millisecond have no set order.
	all := r.inbox(true)
	i := slices.IndexFunc(all, func(m *record) bool { return m.Source == "smtp" })
	if len(all) != 3 || i < 0 || all[i].Status != StatusSuppressed || len(all[i].Suppressed) != 1 || len(r.inbox(false)) != 1 {
		t.Fatalf("smtp suppressed message: %+v", all)
	}
	if ok, _ := deleteSuppression(r.ctx, db, "shop", "GONE@inbox.dev"); !ok {
		t.Fatal("unsuppress")
	}

	// Rate limit: 2 per hour → the third is refused.
	if err := r.p.DB.KVPut(r.ctx, kvNS, "rate/shop", []byte("2")); err != nil {
		t.Fatal(err)
	}
	mod.limiter = newLimiter()
	for i := 0; i < 2; i++ {
		if _, err := Send(r.ctx, r.p, "shop", Message{To: []string{"a@b.c"}, Subject: "s", Text: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	var rl *RateLimitError
	if _, err := Send(r.ctx, r.p, "shop", Message{To: []string{"a@b.c"}, Subject: "s", Text: "t"}); !errors.As(err, &rl) || rl.RetryAfter < 10*time.Minute {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestRelayDeliveryRetriesAndBounces(t *testing.T) {
	r := newRig(t)
	s := newSink(t)
	host, port, _ := net.SplitHostPort(s.addr)
	pn := 0
	for _, c := range port {
		pn = pn*10 + int(c-'0')
	}
	pw := "relaypass"
	if err := setRelay(r.ctx, r.p, &Relay{Host: host, Port: pn, Username: "relayuser", TLS: TLSNone}, &pw); err != nil {
		t.Fatal(err)
	}
	s.deferred["slow@inbox.dev"] = 2
	res, err := Send(r.ctx, r.p, "shop", Message{To: []string{"ok@inbox.dev"}, Subject: "real mail", HTML: "<p>hi</p>"})
	if err != nil || res.Delivery != DeliveryRelay || res.Status != StatusQueued {
		t.Fatalf("send: %+v %v", res, err)
	}
	waitFor(t, "relay delivery", func() bool { return s.count() == 1 })
	if !strings.Contains(s.got[0], "Subject: real mail") || len(r.inbox(false)) != 0 {
		t.Fatalf("delivered: %q inbox=%d", s.got[0], len(r.inbox(false)))
	}
	// Reserved example and test domains never reach the relay: the dev inbox keeps them.
	for _, to := range []string{"invitee@example.com", "Kai <kai@shop.test>", "x@mail.example.org", "y@nowhere.invalid"} {
		res, err := Send(r.ctx, r.p, "shop", Message{To: []string{to}, Subject: "to a test address", Text: "t"})
		if err != nil || res.Delivery != DeliveryInbox || res.Status != StatusCaptured || !strings.Contains(res.Reason, "reserved") {
			t.Fatalf("%s: %+v %v", to, res, err)
		}
	}
	if s.count() != 1 || len(r.inbox(false)) != 4 {
		t.Fatalf("test addresses: relay got %d, inbox %d", s.count(), len(r.inbox(false)))
	}
	_, _ = deleteMessages(r.ctx, r.p, "shop", "")
	waitFor(t, "sent status", func() bool {
		rec, _ := getRecord(r.ctx, r.p.DB.SQL(), "shop", res.ID)
		return rec.Status == StatusSent
	})

	// Temporary failures are retried with backoff.
	res, _ = Send(r.ctx, r.p, "shop", Message{To: []string{"slow@inbox.dev"}, Subject: "slow", Text: "t"})
	waitFor(t, "retried delivery", func() bool {
		rec, _ := getRecord(r.ctx, r.p.DB.SQL(), "shop", res.ID)
		return rec.Status == StatusSent && rec.Attempts == 3
	})

	// A hard bounce suppresses the address; the message fails for good.
	res, _ = Send(r.ctx, r.p, "shop", Message{To: []string{"bounce@inbox.dev"}, Subject: "b", Text: "t"})
	waitFor(t, "bounce", func() bool {
		rec, _ := getRecord(r.ctx, r.p.DB.SQL(), "shop", res.ID)
		return rec.Status == StatusFailed
	})
	sups, _ := listSuppressions(r.ctx, r.p.DB.SQL(), "shop")
	if len(sups) != 1 || sups[0].Address != "bounce@inbox.dev" || sups[0].Reason != "bounce" {
		t.Fatalf("suppressions: %+v", sups)
	}
	before := s.count()
	res, _ = Send(r.ctx, r.p, "shop", Message{To: []string{"bounce@inbox.dev"}, Subject: "again", Text: "t"})
	if res.Status != StatusSuppressed {
		t.Fatalf("send to bounced: %+v", res)
	}

	// Preview deployments always land in the inbox, relay or not.
	env, _ := mod.Env(r.ctx, r.p, "shop", "")
	pu, _ := url.Parse(PreviewSMTPURL(env["SMTP_URL"], "feature-x"))
	ppw, _ := pu.User.Password()
	if pu.User.Username() != "shop+feature-x" {
		t.Fatalf("preview url: %s", pu)
	}
	if err := r.smtpSend(pu.User.Username(), ppw, "a@shop.test", []string{"someone@inbox.dev"}, "Subject: from preview\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	box := r.inbox(false)
	if len(box) != 1 || box[0].Subject != "from preview" || !strings.Contains(box[0].Reason, "preview feature-x") {
		t.Fatalf("preview inbox: %+v", box)
	}
	time.Sleep(200 * time.Millisecond)
	if s.count() != before {
		t.Fatal("preview mail reached the relay")
	}

	// Relay test send.
	tr, err := testRelay(r.ctx, r.p, "ok@inbox.dev", "")
	if err != nil || len(tr.Accepted) != 1 {
		t.Fatalf("relay test: %+v %v", tr, err)
	}
	// Wrong relay password: the attempt fails and is retried, not lost.
	bad := "nope"
	_ = setRelay(r.ctx, r.p, &Relay{Host: host, Port: pn, Username: "relayuser", TLS: TLSNone}, &bad)
	if _, err := testRelay(r.ctx, r.p, "ok@inbox.dev", ""); err == nil {
		t.Fatal("bad relay password accepted")
	}
}

func TestLimiterAndHelpers(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := 0; i < 60; i++ {
		if w := l.take("p", 3600, now); w != 0 {
			t.Fatalf("burst %d: %v", i, w)
		}
	}
	if w := l.take("p", 3600, now); w == 0 || w > 2*time.Second {
		t.Fatalf("after burst: %v", w)
	}
	if w := l.take("p", 3600, now.Add(2*time.Second)); w != 0 {
		t.Fatalf("after refill: %v", w)
	}
	if w := l.take("q", 0, now); w != 0 {
		t.Fatal("unlimited")
	}
	raw := fillHeaders([]byte("Subject: x\r\n\r\nbody"), "shop@box", "msg_1", "box")
	if !bytes.Contains(raw, []byte("From: shop@box\r\n")) || !bytes.Contains(raw, []byte("Message-ID: <msg_1@box>")) {
		t.Fatalf("fill: %s", raw)
	}
	raw = fillHeaders([]byte("From: a@b.c\r\nSubject: x\r\n\r\nbody"), "shop@box", "msg_1", "box")
	if bytes.Contains(raw, []byte("From: shop@box")) {
		t.Fatalf("fill kept From: %s", raw)
	}
}

// Read-only access sees mail's envelope and delivery, never what it says:
// app and sign-in mail carries reset links, magic links and codes, and
// reading them would turn read access to a project into its app's accounts.
func TestReadOnlyMailIsEnvelopeOnly(t *testing.T) {
	r := newRig(t)
	owner, _, err := r.tm.Bootstrap(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := r.tm.Authenticate(r.ctx, owner)
	viewer, _, err := r.tm.Create(r.ctx, op, tokens.CreateRequest{Name: "viewer", Kind: tokens.KindAgent, Scopes: []tokens.Scope{tokens.ScopeRead}, Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p})
	call := func(tok, path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	const secret = "reset-token-Zq81"
	send := `{"to":["admin@inbox.dev"],"subject":"Your code 482913","text":"Reset: https://shop.example.com/reset/` + secret + `"}`
	req := httptest.NewRequest("POST", "/v1/projects/shop/email/send", strings.NewReader(send))
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	var sent struct{ ID string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sent) != nil || sent.ID == "" {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}

	code, body := call(viewer, "/v1/projects/shop/email/messages?all=true")
	if code != 200 || !strings.Contains(body, sent.ID) || !strings.Contains(body, "admin@inbox.dev") || !strings.Contains(body, `"hidden":true`) {
		t.Fatalf("viewer list: %d %s", code, body)
	}
	if strings.Contains(body, "482913") || strings.Contains(body, secret) {
		t.Fatalf("viewer list shows what the message says: %s", body)
	}
	// Nor can the text be probed by searching it.
	for _, q := range []string{"482913", "Zq81"} {
		if code, body := call(viewer, "/v1/projects/shop/email/messages?all=true&q="+q); code != 200 || strings.Contains(body, sent.ID) {
			t.Fatalf("viewer search %q matched the message text: %d %s", q, code, body)
		}
	}
	if code, body := call(viewer, "/v1/projects/shop/email/messages?q=admin%40inbox"); code != 200 || !strings.Contains(body, sent.ID) {
		t.Fatalf("viewer search by recipient: %d %s", code, body)
	}
	code, body = call(viewer, "/v1/projects/shop/email/messages/"+sent.ID)
	if code != 200 || strings.Contains(body, secret) || strings.Contains(body, "482913") || !strings.Contains(body, `"hidden":true`) {
		t.Fatalf("viewer detail: %d %s", code, body)
	}
	for _, path := range []string{"/raw", "/attachments/0"} {
		if code, _ := call(viewer, "/v1/projects/shop/email/messages/"+sent.ID+path); code != 403 {
			t.Fatalf("viewer %s: %d", path, code)
		}
	}
	// Full access reads it all.
	if code, body := call(owner, "/v1/projects/shop/email/messages/"+sent.ID); code != 200 || !strings.Contains(body, secret) || strings.Contains(body, `"hidden":true`) {
		t.Fatalf("owner detail: %d %s", code, body)
	}
	if code, body := call(owner, "/v1/projects/shop/email/messages?q=482913"); code != 200 || !strings.Contains(body, sent.ID) {
		t.Fatalf("owner search: %d %s", code, body)
	}
}

func TestAPIScopes(t *testing.T) {
	r := newRig(t)
	owner, _, err := r.tm.Bootstrap(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := r.tm.Authenticate(r.ctx, owner)
	agent, _, err := r.tm.Create(r.ctx, op, tokens.CreateRequest{Name: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p})
	call := func(tok, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	send := `{"to":["x@inbox.dev"],"subject":"hi","text":"hello"}`
	if code, m := call(agent, "POST", "/v1/projects/shop/email/send", send); code != 200 || m["delivery"] != "inbox" {
		t.Fatalf("agent send to inbox: %d %v", code, m)
	}
	if code, m := call(agent, "GET", "/v1/projects/shop/email/messages", ""); code != 200 {
		t.Fatalf("list: %d %v", code, m)
	}
	if code, _ := call(agent, "PUT", "/v1/email/relay", `{"host":"127.0.0.1","port":2,"tls":"none"}`); code != 403 {
		t.Fatalf("agent set relay: %d", code)
	}
	if code, m := call(owner, "PUT", "/v1/email/relay", `{"host":"127.0.0.1","port":2,"tls":"none"}`); code != 200 || m["mode"] != "relay" {
		t.Fatalf("owner set relay: %d %v", code, m)
	}
	if code, m := call(agent, "POST", "/v1/projects/shop/email/send", send); code != 403 {
		t.Fatalf("agent real send must need apply:outbound: %d %v", code, m)
	}
	if code, _ := call(agent, "GET", "/v1/projects/shop/email/smtp", ""); code != 403 {
		t.Fatalf("agent read smtp creds: %d", code)
	}
	if code, m := call(owner, "GET", "/v1/projects/shop/email/smtp", ""); code != 200 || m["EMAIL_FROM"] != "shop@tiffin.localhost" {
		t.Fatalf("owner smtp creds: %d %v", code, m)
	}
	if code, m := call(owner, "GET", "/v1/email", ""); code != 200 || m["mode"] != "relay" {
		t.Fatalf("status: %d %v", code, m)
	}
	if code, _ := call(owner, "DELETE", "/v1/email/relay", ""); code != 200 {
		t.Fatalf("delete relay: %d", code)
	}
	if code, m := call(agent, "POST", "/v1/projects/shop/email/send", `{"to":["bad"],"subject":"x","text":"y"}`); code != 422 {
		t.Fatalf("validation: %d %v", code, m)
	}
	_ = http.MethodGet
}
