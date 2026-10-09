package email

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// ---- test keys, made fresh for each run ----

type sgKeys struct {
	priv   *ecdsa.PrivateKey
	pubB64 string
}

func newSGKeys(t *testing.T) sgKeys {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return sgKeys{k, base64.StdEncoding.EncodeToString(der)}
}

func (k sgKeys) sign(t *testing.T, ts string, body []byte) string {
	h := sha256.Sum256(append([]byte(ts), body...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func newSvixSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

func svixSign(secret, id, ts string, body []byte) string {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func TestVerifySendGrid(t *testing.T) {
	k := newSGKeys(t)
	now := time.Now()
	body := []byte(`[{"email":"a@inbox.dev","event":"delivered"}]`)
	ts := unix(now)
	sig := k.sign(t, ts, body)
	if err := verifySendGrid(k.pubB64, body, sig, ts, now); err != nil {
		t.Fatalf("valid: %v", err)
	}
	// PEM works too.
	der, _ := base64.StdEncoding.DecodeString(k.pubB64)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err := verifySendGrid(pemKey, body, sig, ts, now); err != nil {
		t.Fatalf("pem: %v", err)
	}
	if err := verifySendGrid(k.pubB64, []byte(`[{"email":"a@inbox.dev","event":"bounce"}]`), sig, ts, now); err != errSignature {
		t.Fatalf("tampered body: %v", err)
	}
	if err := verifySendGrid(k.pubB64, body, sig, unix(now.Add(time.Second)), now); err != errSignature {
		t.Fatalf("tampered timestamp: %v", err)
	}
	other := newSGKeys(t)
	if err := verifySendGrid(other.pubB64, body, sig, ts, now); err != errSignature {
		t.Fatalf("wrong key: %v", err)
	}
	if err := verifySendGrid(k.pubB64, body, "not base64!", ts, now); err != errSignature {
		t.Fatalf("garbage signature: %v", err)
	}
	if err := verifySendGrid(k.pubB64, body, "", "", now); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned: %v", err)
	}
	// Replays: a correctly signed but old request is refused.
	old := unix(now.Add(-26 * time.Hour))
	if err := verifySendGrid(k.pubB64, body, k.sign(t, old, body), old, now); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("old timestamp: %v", err)
	}
	// SendGrid retries for up to a day: a day-old signature still passes (dedupe makes it a no-op).
	day := unix(now.Add(-23 * time.Hour))
	if err := verifySendGrid(k.pubB64, body, k.sign(t, day, body), day, now); err != nil {
		t.Fatalf("retry within a day: %v", err)
	}
	future := unix(now.Add(10 * time.Minute))
	if err := verifySendGrid(k.pubB64, body, k.sign(t, future, body), future, now); err == nil {
		t.Fatal("future timestamp accepted")
	}
	// Keys are checked before they are saved.
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	d384, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	for _, bad := range []string{"", "hello", base64.StdEncoding.EncodeToString([]byte("not a key")), base64.StdEncoding.EncodeToString(d384)} {
		if checkKey(ProviderSendGrid, bad) == nil {
			t.Errorf("accepted key %q", bad)
		}
	}
	if err := checkKey(ProviderSendGrid, k.pubB64); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySvix(t *testing.T) {
	secret := newSvixSecret()
	now := time.Now()
	body := []byte(`{"type":"email.delivered","data":{}}`)
	id, ts := "msg_2abc", unix(now)
	sig := svixSign(secret, id, ts, body)
	if err := verifySvix(secret, body, id, ts, sig, now); err != nil {
		t.Fatalf("valid: %v", err)
	}
	// Several signatures (secret rotation): any match passes.
	if err := verifySvix(secret, body, id, ts, "v1,AAAA "+sig+" v2,zzz", now); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := verifySvix(newSvixSecret(), body, id, ts, sig, now); err != errSignature {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := verifySvix(secret, []byte(`{"type":"email.bounced"}`), id, ts, sig, now); err != errSignature {
		t.Fatalf("tampered: %v", err)
	}
	if err := verifySvix(secret, body, "msg_other", ts, sig, now); err != errSignature {
		t.Fatalf("other id: %v", err)
	}
	if err := verifySvix(secret, body, id, ts, strings.Replace(sig, "v1,", "v0,", 1), now); err != errSignature {
		t.Fatalf("unknown version: %v", err)
	}
	if err := verifySvix(secret, body, "", "", "", now); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned: %v", err)
	}
	old := unix(now.Add(-6 * time.Minute))
	if err := verifySvix(secret, body, id, old, svixSign(secret, id, old, body), now); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("old: %v", err)
	}
	future := unix(now.Add(6 * time.Minute))
	if err := verifySvix(secret, body, id, future, svixSign(secret, id, future, body), now); err == nil {
		t.Fatal("future accepted")
	}
	if checkKey(ProviderResend, "re_123") == nil || checkKey(ProviderResend, "whsec_!!!") == nil {
		t.Fatal("bad secrets accepted")
	}
}

func TestVerifyBasic(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	if verifyBasic("tok", req) == nil {
		t.Fatal("no credentials accepted")
	}
	req.SetBasicAuth("tiffin", "tok")
	if err := verifyBasic("tok", req); err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("tiffin", "tok2")
	if verifyBasic("tok", req) == nil {
		t.Fatal("wrong password accepted")
	}
	req.SetBasicAuth("admin", "tok")
	if verifyBasic("tok", req) == nil {
		t.Fatal("wrong user accepted")
	}
}

func TestMarkForRelay(t *testing.T) {
	raw := []byte("From: a@shop.test\r\nSubject: hi\r\n there\r\nMessage-ID: <abc@shop.test>\r\n\r\nbody\r\n")
	out, mid := markForRelay(raw, "msg_01ABCDEFGHJKMNPQRSTVWXYZ00", "box.test", ProviderSendGrid)
	if mid != "<abc@shop.test>" {
		t.Fatalf("message id: %q", mid)
	}
	s := string(out)
	if !strings.Contains(s, `X-SMTPAPI: {"unique_args":{"tiffin_id":"msg_01ABCDEFGHJKMNPQRSTVWXYZ00"}}`+"\r\n") ||
		!strings.Contains(s, "X-Tiffin-Message-Id: msg_01ABCDEFGHJKMNPQRSTVWXYZ00\r\n") ||
		!strings.Contains(s, "Subject: hi\r\n there\r\n") || !strings.HasSuffix(s, "\r\n\r\nbody\r\n") || strings.Count(s, "Message-ID") != 1 {
		t.Fatalf("sendgrid: %s", s)
	}
	// The app's own X-SMTPAPI is kept and merged.
	raw = []byte("Subject: x\r\nX-SMTPAPI: {\"category\": [\"welcome\"],\r\n \"unique_args\": {\"user\": \"7\"}}\r\n\r\nb")
	out, mid = markForRelay(raw, "msg_1", "box.test", ProviderSendGrid)
	if mid != "<msg_1@box.test>" || strings.Count(string(out), "X-SMTPAPI") != 1 {
		t.Fatalf("merge: %s", out)
	}
	fields, _ := splitHeader(out)
	var args struct {
		Category   []string          `json:"category"`
		UniqueArgs map[string]string `json:"unique_args"`
	}
	for _, f := range fields {
		if f.name == "X-Smtpapi" {
			_ = json.Unmarshal([]byte(f.value()), &args)
		}
	}
	if args.UniqueArgs["user"] != "7" || args.UniqueArgs[tiffinArg] != "msg_1" || len(args.Category) != 1 {
		t.Fatalf("merged args: %+v", args)
	}
	out, _ = markForRelay([]byte("Subject: x\r\n\r\nb"), "msg_1", "box.test", ProviderPostmark)
	if !strings.Contains(string(out), "X-PM-Metadata-tiffin-id: msg_1\r\n") {
		t.Fatalf("postmark: %s", out)
	}
	out, _ = markForRelay([]byte("Subject: x\r\n\r\nb"), "msg_1", "box.test", ProviderResend)
	if strings.Contains(string(out), "X-SMTPAPI") || !strings.Contains(string(out), "Message-ID: <msg_1@box.test>") {
		t.Fatalf("resend: %s", out)
	}
	if providerIDFromReply("2.0.0 Ok: queued as 7uQ3pXcGQhW2") != "7uQ3pXcGQhW2" ||
		providerIDFromReply("OK 56761188-7520-42d8-8898-ff6fc54ce618") != "56761188-7520-42d8-8898-ff6fc54ce618" || providerIDFromReply("OK") != "" {
		t.Fatal("provider id from reply")
	}
}

func TestParseEvents(t *testing.T) {
	sg := `[
	 {"email":"A@Inbox.dev","timestamp":1760000000,"event":"bounce","type":"bounce","bounce_classification":"Invalid Address","reason":"550 5.1.1 no such user","status":"5.1.1","sg_event_id":"e1","sg_message_id":"Q1.filter0001.1","smtp-id":"<x@y>","tiffin_id":"msg_1"},
	 {"email":"b@inbox.dev","timestamp":1760000000,"event":"bounce","type":"blocked","reason":"421 try later","sg_event_id":"e2"},
	 {"email":"c@inbox.dev","timestamp":1760000000,"event":"processed","sg_event_id":"e3"},
	 {"email":"d@inbox.dev","timestamp":1760000000,"event":"dropped","reason":"Spam Reporting Address","sg_event_id":"e4"},
	 {"email":"e@inbox.dev","timestamp":1760000000,"event":"spamreport","sg_event_id":"e5"},
	 {"email":"f@inbox.dev","timestamp":1760000000,"event":"click","url":"https://example.com/x","sg_event_id":"e6"}]`
	evs, err := parseSendGrid([]byte(sg))
	if err != nil || len(evs) != 5 {
		t.Fatalf("sendgrid: %d %v", len(evs), err)
	}
	if e := evs[0]; e.Type != EventBounced || !e.Hard || e.suppress != "bounce" || e.Recipient != "a@inbox.dev" || e.TiffinID != "msg_1" ||
		e.HeaderID != "<x@y>" || e.ProviderID != "Q1" || e.Key != "e1" || !strings.Contains(e.Detail, "Invalid Address") {
		t.Fatalf("hard bounce: %+v", e)
	}
	if e := evs[1]; e.Type != EventBounced || e.Hard || e.suppress != "" {
		t.Fatalf("soft bounce: %+v", e)
	}
	if evs[2].Type != EventDropped || evs[2].suppress != "complaint" || evs[3].Type != EventComplained || evs[4].Type != EventClicked {
		t.Fatalf("others: %+v", evs[2:])
	}
	if _, err := parseSendGrid([]byte(`{"not":"an array"}`)); err == nil {
		t.Fatal("object accepted")
	}

	rs := `{"type":"email.bounced","created_at":"2026-10-06T10:00:00Z","data":{"email_id":"56761188-7520-42d8-8898-ff6fc54ce618","message_id":"x@y",
	  "to":["A@inbox.dev","b@inbox.dev"],"subject":"Hi","bounce":{"type":"Permanent","subType":"General","message":"mailbox unavailable"}}}`
	evs, err = parseResend([]byte(rs), "msg_svix1")
	if err != nil || len(evs) != 2 || !evs[0].Hard || evs[0].HeaderID != "<x@y>" || evs[0].Key != "msg_svix1/a@inbox.dev" || evs[1].Recipient != "b@inbox.dev" {
		t.Fatalf("resend: %+v %v", evs, err)
	}
	evs, _ = parseResend([]byte(`{"type":"email.bounced","data":{"to":["a@x.com"],"bounce":{"type":"Temporary"}}}`), "s2")
	if evs[0].Hard || evs[0].suppress != "" {
		t.Fatalf("temporary: %+v", evs[0])
	}
	if evs, _ := parseResend([]byte(`{"type":"email.sent","data":{"to":["a@x.com"]}}`), "s3"); len(evs) != 0 {
		t.Fatal("email.sent should be skipped")
	}

	pm := `{"RecordType":"Bounce","ID":692560173,"Type":"HardBounce","TypeCode":1,"MessageID":"883953f4-6105-42a2-a16a-77a8eac79483",
	  "Metadata":{"tiffin-id":"msg_9"},"Email":"M@nasa.com","BouncedAt":"2026-10-05T16:33:54.9070259Z","Description":"Unknown user","Inactive":true}`
	evs, err = parsePostmark([]byte(pm))
	if err != nil || len(evs) != 1 || !evs[0].Hard || evs[0].TiffinID != "msg_9" || evs[0].Key != "Bounce/692560173" || evs[0].Recipient != "m@nasa.com" {
		t.Fatalf("postmark: %+v %v", evs, err)
	}
	evs, _ = parsePostmark([]byte(`{"RecordType":"Delivery","MessageID":"m","Recipient":"a@x.com","DeliveredAt":"2026-10-05T16:33:54Z","Metadata":{"tiffin-id":"msg_9"}}`))
	if evs[0].Type != EventDelivered || evs[0].Key == "" {
		t.Fatalf("postmark delivery: %+v", evs)
	}
}

// ---- end to end: relay, events, statuses, suppressions ----

type hookRig struct {
	*rig
	owner, agent string
	a            *api.API
	sink         *sink
}

func newHookRig(t *testing.T) *hookRig {
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
	return &hookRig{rig: r, owner: owner, agent: agent, a: api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p}), sink: newSink(t)}
}

func (h *hookRig) call(tok, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.a.Handler().ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (h *hookRig) post(path string, body []byte, hdr map[string]string, user, pass string) (int, map[string]any) {
	req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	w := httptest.NewRecorder()
	h.a.Handler().ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

// useRelay points the box at the test sink, as provider.
func (h *hookRig) useRelay(provider string) {
	host, port, _ := net.SplitHostPort(h.sink.addr)
	pn, _ := strconv.Atoi(port)
	pw := "relaypass"
	if err := setRelay(h.ctx, h.p, &Relay{Provider: provider, Host: host, Port: pn, Username: "relayuser", TLS: TLSNone}, &pw); err != nil {
		h.t.Fatal(err)
	}
}

// sendOne sends a message through the relay and waits until it is sent.
func (h *hookRig) sendOne(to, subject string) (string, string) {
	h.t.Helper()
	before := h.sink.count()
	res, err := Send(h.ctx, h.p, "shop", Message{To: []string{to}, Subject: subject, Text: "hello"})
	if err != nil {
		h.t.Fatal(err)
	}
	waitFor(h.t, "sent", func() bool {
		rec, _ := getRecord(h.ctx, h.p.DB.SQL(), "shop", res.ID)
		return rec.Status == StatusSent && h.sink.count() > before
	})
	h.sink.mu.Lock()
	raw := h.sink.got[len(h.sink.got)-1]
	h.sink.mu.Unlock()
	return res.ID, raw
}

func (h *hookRig) status(id string) string {
	rec, err := getRecord(h.ctx, h.p.DB.SQL(), "shop", id)
	if err != nil {
		h.t.Fatal(err)
	}
	return rec.Status
}

func (h *hookRig) suppressedAs(addr string) string {
	l, _ := listSuppressions(h.ctx, h.p.DB.SQL(), "shop")
	for _, s := range l {
		if s.Address == addr {
			return s.Reason
		}
	}
	return ""
}

func TestSendGridEvents(t *testing.T) {
	h := newHookRig(t)
	h.useRelay(ProviderSendGrid)
	k := newSGKeys(t)
	path := "/v1/email/events/sendgrid"

	// Saving the key: owner only, and it must be a real verification key.
	if code, _ := h.call(h.agent, "PUT", "/v1/email/webhooks/sendgrid", `{"key":"`+k.pubB64+`"}`); code != 403 {
		t.Fatalf("agent set webhook: %d", code)
	}
	if code, m := h.call(h.owner, "PUT", "/v1/email/webhooks/sendgrid", `{"key":"SG.not-a-public-key"}`); code != 422 {
		t.Fatalf("bad key: %d %v", code, m)
	}
	// Before a key is saved, requests are refused, and the refusal is shown.
	if code, _ := h.post(path, []byte(`[]`), nil, "", ""); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	code, m := h.call(h.owner, "PUT", "/v1/email/webhooks/sendgrid", `{"key":"`+k.pubB64+`"}`)
	if code != 200 || m["keySet"] != true || m["receiving"] != false || !strings.HasSuffix(m["url"].(string), path) || m["secretUrl"] != nil {
		t.Fatalf("set webhook: %d %v", code, m)
	}
	if strings.Contains(fmt.Sprint(m), k.pubB64) {
		t.Fatal("key echoed back")
	}

	id, raw := h.sendOne("ada@inbox.dev", "Welcome")
	if !strings.Contains(raw, `"tiffin_id":"`+id+`"`) {
		t.Fatalf("relayed without the unique argument: %s", raw)
	}
	send := func(events string, at time.Time) (int, map[string]any) {
		ts := unix(at)
		return h.post(path, []byte(events), map[string]string{
			"X-Twilio-Email-Event-Webhook-Signature": k.sign(t, ts, []byte(events)),
			"X-Twilio-Email-Event-Webhook-Timestamp": ts,
			"Content-Type":                           "application/json",
		}, "", "")
	}
	delivered := fmt.Sprintf(`[{"email":"ada@inbox.dev","timestamp":%d,"event":"delivered","response":"250 OK","sg_event_id":"ev-d1","tiffin_id":%q},
		{"email":"ada@inbox.dev","timestamp":%d,"event":"open","sg_event_id":"ev-o1","tiffin_id":%q},
		{"email":"zed@inbox.dev","timestamp":%d,"event":"delivered","sg_event_id":"ev-other"}]`, time.Now().Unix(), id, time.Now().Unix()+1, id, time.Now().Unix())
	code, m = send(delivered, time.Now())
	if code != 200 || m["matched"] != float64(2) || m["events"] != float64(3) {
		t.Fatalf("delivered: %d %v", code, m)
	}
	if h.status(id) != StatusDelivered {
		t.Fatalf("status: %s", h.status(id))
	}
	// A replay of the same signed request changes nothing.
	if code, m = send(delivered, time.Now()); code != 200 || m["matched"] != float64(0) {
		t.Fatalf("replay: %d %v", code, m)
	}
	evs, _ := messageEvents(h.ctx, h.p.DB.SQL(), id)
	if len(evs) != 2 || evs[0].Type != EventDelivered || evs[1].Type != EventOpened {
		t.Fatalf("events: %+v", evs)
	}
	// Unsigned, tampered and stale requests are refused and recorded.
	if code, _ := h.post(path, []byte(delivered), nil, "", ""); code != 401 {
		t.Fatalf("unsigned: %d", code)
	}
	ts := unix(time.Now())
	if code, _ := h.post(path, []byte(strings.Replace(delivered, "delivered", "bounce", 1)), map[string]string{
		"X-Twilio-Email-Event-Webhook-Signature": k.sign(t, ts, []byte(delivered)), "X-Twilio-Email-Event-Webhook-Timestamp": ts}, "", ""); code != 401 {
		t.Fatalf("tampered: %d", code)
	}
	if code, _ := send(`[]`, time.Now().Add(-26*time.Hour)); code != 401 {
		t.Fatalf("stale: %d", code)
	}

	// A hard bounce: bounced, and the address is suppressed with the reason.
	id2, _ := h.sendOne("gone@inbox.dev", "Receipt")
	code, _ = send(fmt.Sprintf(`[{"email":"gone@inbox.dev","timestamp":%d,"event":"bounce","type":"bounce","reason":"550 5.1.1 unknown user","status":"5.1.1","sg_event_id":"ev-b1","tiffin_id":%q}]`, time.Now().Unix(), id2), time.Now())
	if code != 200 || h.status(id2) != StatusBounced || h.suppressedAs("gone@inbox.dev") != "bounce" {
		t.Fatalf("bounce: %d %s %q", code, h.status(id2), h.suppressedAs("gone@inbox.dev"))
	}
	// A spam report beats delivered, and is never undone by a late "delivered".
	id3, raw3 := h.sendOne("grump@inbox.dev", "News")
	smtpID := ""
	for _, line := range strings.Split(raw3, "\r\n") {
		if k, v, ok := strings.Cut(line, ": "); ok && strings.EqualFold(k, "Message-ID") {
			smtpID = v
		}
	}
	// Matched by smtp-id (the Message-ID) alone.
	code, m = send(fmt.Sprintf(`[{"email":"grump@inbox.dev","timestamp":%d,"event":"spamreport","sg_event_id":"ev-s1","smtp-id":%q},
		{"email":"grump@inbox.dev","timestamp":%d,"event":"delivered","sg_event_id":"ev-s2","smtp-id":%q}]`, time.Now().Unix(), smtpID, time.Now().Unix(), smtpID), time.Now())
	if code != 200 || m["matched"] != float64(2) || h.status(id3) != StatusComplained || h.suppressedAs("grump@inbox.dev") != "complaint" {
		t.Fatalf("complaint: %d %v %s %q", code, m, h.status(id3), h.suppressedAs("grump@inbox.dev"))
	}

	// The status shows it is receiving, and the message detail has the timeline.
	code, m = h.call(h.owner, "GET", "/v1/email", "")
	hooks, _ := m["webhooks"].([]any)
	if code != 200 || len(hooks) != 1 || hooks[0].(map[string]any)["receiving"] != true || hooks[0].(map[string]any)["lastRejection"] == "" {
		t.Fatalf("status: %d %v", code, m)
	}
	code, m = h.call(h.agent, "GET", "/v1/projects/shop/email/messages/"+id, "")
	if evs, _ := m["events"].([]any); code != 200 || len(evs) != 2 || m["provider"] != "sendgrid" || m["status"] != "delivered" {
		t.Fatalf("detail: %d %v", code, m)
	}
	// Turning it off: requests are refused again.
	if code, _ := h.call(h.owner, "DELETE", "/v1/email/webhooks/sendgrid", ""); code != 204 && code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := send(delivered, time.Now()); code != 401 {
		t.Fatalf("after delete: %d", code)
	}
}

func TestResendEvents(t *testing.T) {
	h := newHookRig(t)
	h.useRelay(ProviderResend)
	secret := newSvixSecret()
	if code, m := h.call(h.owner, "PUT", "/v1/email/webhooks/resend", `{"key":"`+secret+`"}`); code != 200 || m["keySet"] != true {
		t.Fatalf("set: %d %v", code, m)
	}
	path := "/v1/email/events/resend"
	n := 0
	send := func(body string, at time.Time) (int, map[string]any) {
		n++
		id := fmt.Sprintf("msg_svix%d", n)
		ts := unix(at)
		return h.post(path, []byte(body), map[string]string{"svix-id": id, "svix-timestamp": ts, "svix-signature": svixSign(secret, id, ts, []byte(body))}, "", "")
	}
	id, raw := h.sendOne("ada@inbox.dev", "Welcome")
	mid := ""
	for _, line := range strings.Split(raw, "\r\n") {
		if k, v, ok := strings.Cut(line, ": "); ok && strings.EqualFold(k, "Message-ID") {
			mid = v
		}
	}
	ev := func(typ, messageID, to, subject, extra string) string {
		return fmt.Sprintf(`{"type":%q,"created_at":%q,"data":{"email_id":"56761188-7520-42d8-8898-ff6fc54ce618","message_id":%q,"to":[%q],"subject":%q%s}}`,
			typ, time.Now().UTC().Format(time.RFC3339Nano), messageID, to, subject, extra)
	}
	if code, m := send(ev("email.delivered", mid, "ada@inbox.dev", "Welcome", ""), time.Now()); code != 200 || m["matched"] != float64(1) || h.status(id) != StatusDelivered {
		t.Fatalf("delivered: %d %v %s", code, m, h.status(id))
	}
	// Later events match by Resend's email_id, learned from the first.
	if code, m := send(ev("email.complained", "", "ada@inbox.dev", "", ""), time.Now()); code != 200 || m["matched"] != float64(1) ||
		h.status(id) != StatusComplained || h.suppressedAs("ada@inbox.dev") != "complaint" {
		t.Fatalf("complained: %d %v %s", code, m, h.status(id))
	}
	// When Resend reports its own Message-ID, the recipient and subject still find the message.
	id2, _ := h.sendOne("bob@inbox.dev", "Your receipt")
	bounce := ev("email.bounced", "<0100019a-other@email.amazonses.com>", "bob@inbox.dev", "Your receipt",
		`,"bounce":{"type":"Permanent","subType":"General","message":"mailbox does not exist"}`)
	bounce = strings.Replace(bounce, "56761188-7520-42d8-8898-ff6fc54ce618", "11111111-2222-3333-4444-555555555555", 1)
	if code, m := send(bounce, time.Now()); code != 200 || m["matched"] != float64(1) || h.status(id2) != StatusBounced || h.suppressedAs("bob@inbox.dev") != "bounce" {
		t.Fatalf("bounced: %d %v %s", code, m, h.status(id2))
	}
	// An event for mail the box never sent is acknowledged and ignored.
	unknown := strings.Replace(ev("email.delivered", "<nope@x>", "who@inbox.dev", "Unknown", ""), "56761188-7520-42d8-8898-ff6fc54ce618", "00000000-0000-0000-0000-000000000000", 1)
	if code, m := send(unknown, time.Now()); code != 200 || m["matched"] != float64(0) {
		t.Fatalf("unknown: %d %v", code, m)
	}
	// Old timestamps (a replay) and bad signatures are refused.
	if code, _ := send(ev("email.delivered", mid, "ada@inbox.dev", "Welcome", ""), time.Now().Add(-10*time.Minute)); code != 401 {
		t.Fatalf("old: %d", code)
	}
	body := ev("email.delivered", mid, "ada@inbox.dev", "Welcome", "")
	if code, _ := h.post(path, []byte(body), map[string]string{"svix-id": "x", "svix-timestamp": unix(time.Now()), "svix-signature": svixSign(newSvixSecret(), "x", unix(time.Now()), []byte(body))}, "", ""); code != 401 {
		t.Fatalf("wrong secret: %d", code)
	}
	// The same delivery sent twice (Resend retries keep the svix-id) counts once.
	ts := unix(time.Now())
	id3, _ := h.sendOne("cy@inbox.dev", "Hello")
	_ = id3
	again := ev("email.opened", "", "cy@inbox.dev", "Hello", "")
	again = strings.Replace(again, "56761188-7520-42d8-8898-ff6fc54ce618", "99999999-2222-3333-4444-555555555555", 1)
	hdr := map[string]string{"svix-id": "msg_same", "svix-timestamp": ts, "svix-signature": svixSign(secret, "msg_same", ts, []byte(again))}
	_, m1 := h.post(path, []byte(again), hdr, "", "")
	_, m2 := h.post(path, []byte(again), hdr, "", "")
	if m1["matched"] != float64(1) || m2["matched"] != float64(0) {
		t.Fatalf("dedupe: %v %v", m1, m2)
	}
}

func TestPostmarkEvents(t *testing.T) {
	h := newHookRig(t)
	h.useRelay(ProviderPostmark)
	code, m := h.call(h.owner, "PUT", "/v1/email/webhooks/postmark", `{}`)
	secretURL, _ := m["secretUrl"].(string)
	if code != 200 || !strings.Contains(secretURL, "tiffin:") || !strings.HasSuffix(secretURL, "/v1/email/events/postmark") {
		t.Fatalf("set: %d %v", code, m)
	}
	pass := strings.TrimPrefix(strings.SplitN(strings.SplitN(secretURL, "@", 2)[0], "tiffin:", 2)[1], "")
	// The status never shows the password again.
	_, st := h.call(h.owner, "GET", "/v1/email", "")
	if strings.Contains(fmt.Sprint(st), pass) {
		t.Fatal("password shown in status")
	}
	id, raw := h.sendOne("m@inbox.dev", "Hi")
	if !strings.Contains(raw, "X-PM-Metadata-tiffin-id: "+id) {
		t.Fatalf("no metadata header: %s", raw)
	}
	body := fmt.Sprintf(`{"RecordType":"Bounce","ID":42,"Type":"HardBounce","TypeCode":1,"MessageID":"883953f4","Metadata":{"tiffin-id":%q},"Email":"m@inbox.dev","BouncedAt":%q,"Description":"Unknown user","Inactive":true}`,
		id, time.Now().UTC().Format(time.RFC3339))
	if code, _ := h.post("/v1/email/events/postmark", []byte(body), nil, "tiffin", "wrong"); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	if code, _ := h.post("/v1/email/events/postmark", []byte(body), nil, "", ""); code != 401 {
		t.Fatalf("no password: %d", code)
	}
	if code, m := h.post("/v1/email/events/postmark", []byte(body), nil, "tiffin", pass); code != 200 || m["matched"] != float64(1) {
		t.Fatalf("bounce: %d %v", code, m)
	}
	if h.status(id) != StatusBounced || h.suppressedAs("m@inbox.dev") != "bounce" {
		t.Fatalf("after bounce: %s %q", h.status(id), h.suppressedAs("m@inbox.dev"))
	}
	// Making a new password retires the old one.
	h.call(h.owner, "PUT", "/v1/email/webhooks/postmark", `{}`)
	if code, _ := h.post("/v1/email/events/postmark", []byte(body), nil, "tiffin", pass); code != 401 {
		t.Fatalf("old password after rotation: %d", code)
	}
}

func TestProvidersAPIAndRelayPresets(t *testing.T) {
	h := newHookRig(t)
	code, _ := h.call(h.agent, "GET", "/v1/email/providers", "")
	if code != 200 {
		t.Fatalf("providers: %d", code)
	}
	// A preset needs its key the first time.
	if code, m := h.call(h.owner, "PUT", "/v1/email/relay", `{"provider":"sendgrid"}`); code != 422 {
		t.Fatalf("no key: %d %v", code, m)
	}
	code, m := h.call(h.owner, "PUT", "/v1/email/relay", `{"provider":"sendgrid","password":"SG.test-key-not-real"}`)
	relay, _ := m["relay"].(map[string]any)
	if code != 200 || relay["host"] != "smtp.sendgrid.net" || relay["port"] != float64(587) || relay["username"] != "apikey" || relay["provider"] != "sendgrid" || relay["passwordSet"] != true {
		t.Fatalf("sendgrid relay: %d %v", code, m)
	}
	hooks, _ := m["webhooks"].([]any)
	if len(hooks) != 1 || hooks[0].(map[string]any)["provider"] != "sendgrid" || hooks[0].(map[string]any)["public"] != false {
		t.Fatalf("webhook setup shown: %v", m["webhooks"])
	}
	// Replacing it keeps the stored key when none is sent.
	code, m = h.call(h.owner, "PUT", "/v1/email/relay", `{"provider":"ses","region":"eu-west-2","username":"AKIAEXAMPLE"}`)
	relay, _ = m["relay"].(map[string]any)
	if code != 200 || relay["host"] != "email-smtp.eu-west-2.amazonaws.com" || relay["region"] != "eu-west-2" || relay["passwordSet"] != true {
		t.Fatalf("ses: %d %v", code, m)
	}
	if code, _ := h.call(h.owner, "PUT", "/v1/email/relay", `{"provider":"ses","region":"xx-1","username":"a"}`); code != 422 {
		t.Fatalf("bad region: %d", code)
	}
}

func TestPublicAddress(t *testing.T) {
	for u, want := range map[string]bool{
		"https://dashboard.shiptiffin.com": true, "https://dashboard.46-224-210-97.sslip.io": true, "https://dashboard.tiffin.localhost:8443": false,
		"http://127.0.0.1:7392": false, "https://10.0.0.5": false, "https://box.test": false, "": false, "https://95.216.1.2": true,
	} {
		if got := publicAddress(u); got != want {
			t.Errorf("%s: %v", u, got)
		}
	}
}
