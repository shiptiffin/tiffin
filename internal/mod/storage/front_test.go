package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// fakeGateway stands in for versitygw: it records what reaches it.
type fakeGateway struct {
	mu      sync.Mutex
	seen    []string // "METHOD /path?query"
	objects map[string]fakeObject
	parts   int64 // ListParts: each of two parts is this big
	status  int   // forced status for writes (0: 200)
}

type fakeObject struct {
	body []byte
	typ  string
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = append(g.seen, r.Method+" "+r.URL.RequestURI())
	q := r.URL.Query()
	w.Header().Set("X-Amz-Request-Id", fmt.Sprintf("REQ%d", len(g.seen)))
	switch {
	case r.Method == http.MethodGet && q.Has("uploadId"):
		fmt.Fprintf(w, "<ListPartsResult><Part><PartNumber>1</PartNumber><Size>%d</Size></Part><Part><PartNumber>2</PartNumber><Size>%d</Size></Part></ListPartsResult>", g.parts, g.parts)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		o, ok := g.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", o.typ)
		w.Header().Set("Content-Length", fmt.Sprint(len(o.body)))
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, len(o.body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.body)
		}
	case g.status != 0:
		w.WriteHeader(g.status)
	default:
		if r.Method == http.MethodPut && !q.Has("uploadId") {
			g.objects[r.URL.Path] = fakeObject{body, r.Header.Get("Content-Type")}
		}
		if r.Method == http.MethodPost && q.Has("uploadId") {
			g.objects[r.URL.Path] = fakeObject{bytes.Repeat([]byte("x"), int(2*g.parts)), "video/mp4"}
		}
		if r.Method == http.MethodDelete && !q.Has("uploadId") {
			delete(g.objects, r.URL.Path)
		}
		w.Header().Set("ETag", `"tag"`)
		if r.Method == http.MethodPost && q.Has("uploads") {
			_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>U1</UploadId></InitiateMultipartUploadResult>")
		}
	}
}

func (g *fakeGateway) reached(prefix string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.seen {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

type recPublisher struct {
	mu   sync.Mutex
	got  []ObjectCreated
	keys []string
}

func (r *recPublisher) PublishEvent(_ context.Context, _ *platform.Platform, project, topic string, payload json.RawMessage, dedupe string) (bool, error) {
	var e ObjectCreated
	_ = json.Unmarshal(payload, &e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if topic != ObjectCreatedTopic || project != e.Project {
		return false, fmt.Errorf("topic %s project %s", topic, project)
	}
	r.got, r.keys = append(r.got, e), append(r.keys, dedupe)
	return true, nil
}

func (r *recPublisher) events() []ObjectCreated {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ObjectCreated(nil), r.got...)
}

type frontRig struct {
	t    *testing.T
	gw   *fakeGateway
	m    *Module
	p    *platform.Platform
	f    *frontServer
	pub  *recPublisher
	ctx  context.Context
	user Creds
}

// newFrontRig runs the front against a fake gateway, with project "shop"
// (app "web" on shop.<domain>) owning buckets media (rules) and pics (public).
func newFrontRig(t *testing.T) *frontRig {
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Secrets: sec, DataRoot: t.TempDir(), Domain: "tiffin.localhost",
		PublicURL: "https://dashboard.tiffin.localhost", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mf, err := manifest.Parse([]byte(`{"project":"shop","apps":{"web":{}},"services":{"storage":{"buckets":{"media":{},"pics":{"public":true}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	desired, _ := change.Resources(mf)
	plan, _ := p.Engine.Plan(ctx, "shop", desired)
	if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		t.Fatal(err)
	}
	_ = putMeta(ctx, p, "shop--media", &bucketMeta{Project: "shop", Name: "media", MaxFileSize: 100, AllowedTypes: []string{"image/*", "application/pdf"}})
	_ = putMeta(ctx, p, "shop--pics", &bucketMeta{Project: "shop", Name: "pics", Public: true})
	user, _, err := credsFor(ctx, p, "shop", true)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{objects: map[string]fakeObject{}}
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	pub := &recPublisher{}
	m := &Module{publisher: pub, imgEngine: fakeEngine(nil)}
	m.gw = &gateway{addr: strings.TrimPrefix(up.URL, "http://"), root: Creds{"ROOT", "rootsecret"}, region: Region, client: up.Client()}
	f := &frontServer{m: m, p: p}
	m.frontAddr = freeAddr(t)
	if err := f.start(ctx); err != nil {
		t.Fatal(err)
	}
	go m.runEvents(ctx, p)
	return &frontRig{t: t, gw: g, m: m, p: p, f: f, pub: pub, ctx: ctx, user: user}
}

// do sends a request through the front.
func (r *frontRig) do(method, host, target string, body []byte, hdr map[string]string) (*http.Response, string) {
	return r.send(method, host, target, body, hdr, nil)
}

// send is do, signed with c's key when c is set.
func (r *frontRig) send(method, host, target string, body []byte, hdr map[string]string, c *Creds) (*http.Response, string) {
	r.t.Helper()
	req := httptest.NewRequest(method, "http://"+host+target, bytes.NewReader(body))
	req.RequestURI = ""
	req.URL.Host = r.m.frontAddr
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c != nil {
		signRequest(req, *c, Region, sha256Hex(body), time.Now())
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

const s3Host = "s3.tiffin.localhost"

func TestFrontUploadRules(t *testing.T) {
	r := newFrontRig(t)
	png := map[string]string{"Content-Type": "image/png"}
	if res, body := r.do("PUT", s3Host, "/shop--media/a.png", make([]byte, 100), png); res.StatusCode != 200 {
		t.Fatalf("an upload within the rules: %d %s", res.StatusCode, body)
	}
	res, body := r.do("PUT", s3Host, "/shop--media/big.png", make([]byte, 101), png)
	if res.StatusCode != 413 || !strings.Contains(body, "EntityTooLarge") || r.gw.reached("PUT /shop--media/big.png") {
		t.Fatalf("over maxFileSize: %d %s", res.StatusCode, body)
	}
	res, body = r.do("PUT", s3Host, "/shop--media/a.txt", []byte("hi"), map[string]string{"Content-Type": "text/plain"})
	if res.StatusCode != 415 || !strings.Contains(body, "image/*, application/pdf") || r.gw.reached("PUT /shop--media/a.txt") {
		t.Fatalf("type not allowed: %d %s", res.StatusCode, body)
	}
	if res, _ := r.do("PUT", s3Host, "/shop--media/doc", []byte("%PDF"), map[string]string{"Content-Type": "application/pdf; charset=binary"}); res.StatusCode != 200 {
		t.Fatalf("exact type with parameters: %d", res.StatusCode)
	}
	if res, _ := r.do("PUT", s3Host, "/shop--media/none", []byte("x"), nil); res.StatusCode != 415 {
		t.Fatalf("no Content-Type: %d", res.StatusCode)
	}
	// A presigned URL's own cap (signed into the query) is smaller than the bucket's.
	if res, _ := r.do("PUT", s3Host, "/shop--media/t.png?"+MaxSizeParam+"=10", make([]byte, 11), png); res.StatusCode != 413 {
		t.Fatalf("URL cap: %d", res.StatusCode)
	}
	// Multipart: the type at creation, each part's size, and the total at completion.
	if res, _ := r.do("POST", s3Host, "/shop--media/v.bin?uploads", nil, map[string]string{"Content-Type": "application/zip"}); res.StatusCode != 415 {
		t.Fatalf("create with a type not allowed: %d", res.StatusCode)
	}
	if res, _ := r.do("POST", s3Host, "/shop--media/v.png?uploads", nil, png); res.StatusCode != 200 {
		t.Fatalf("create: %d", res.StatusCode)
	}
	if res, _ := r.do("PUT", s3Host, "/shop--media/v.png?partNumber=1&uploadId=U1", make([]byte, 101), nil); res.StatusCode != 413 {
		t.Fatalf("part over the cap: %d", res.StatusCode)
	}
	r.gw.parts = 60 // two parts of 60: 120 > 100
	// Unsigned (or signed by another key), the front neither lists the
	// parts nor aborts as root, whatever cap the query names: the gateway
	// answers (and refuses an unsigned request).
	res, _ = r.do("POST", s3Host, "/shop--media/v.png?uploadId=U1&"+MaxSizeParam+"=1", []byte("<CompleteMultipartUpload/>"), nil)
	if r.gw.reached("DELETE /shop--media/v.png") || r.gw.reached("GET /shop--media/v.png?uploadId") {
		t.Fatalf("an unsigned complete made the front act as root: %d %v", res.StatusCode, r.gw.seen)
	}
	other := Creds{"TFNOTHER", "othersecret"}
	r.send("POST", s3Host, "/shop--media/v.png?uploadId=U1", []byte("<CompleteMultipartUpload/>"), nil, &other)
	if r.gw.reached("DELETE /shop--media/v.png") {
		t.Fatalf("another key's complete aborted the upload: %v", r.gw.seen)
	}
	r.gw.mu.Lock()
	r.gw.seen = nil
	r.gw.mu.Unlock()
	res, body = r.send("POST", s3Host, "/shop--media/v.png?uploadId=U1", []byte("<CompleteMultipartUpload/>"), nil, &r.user)
	if res.StatusCode != 413 || !strings.Contains(body, "aborted") || !r.gw.reached("DELETE /shop--media/v.png?uploadId=U1") || r.gw.reached("POST /shop--media/v.png?uploadId") {
		t.Fatalf("complete over the cap: %d %s %v", res.StatusCode, body, r.gw.seen)
	}
	r.gw.parts = 40
	if res, _ := r.send("POST", s3Host, "/shop--media/v.png?uploadId=U2", []byte("<CompleteMultipartUpload/>"), nil, &r.user); res.StatusCode != 200 {
		t.Fatalf("complete within the cap: %d", res.StatusCode)
	}
	// Buckets without rules take anything; other requests pass untouched.
	if res, _ := r.do("PUT", s3Host, "/shop--pics/huge.bin", make([]byte, 5000), nil); res.StatusCode != 200 {
		t.Fatalf("no rules: %d", res.StatusCode)
	}
	if res, _ := r.do("GET", s3Host, "/shop--media?list-type=2", nil, nil); res.StatusCode != 404 && res.StatusCode != 200 {
		t.Fatalf("listing: %d", res.StatusCode)
	}
}

// Form (POST policy) uploads to /<bucket> count toward the storage limit
// like any other upload; buckets with rules refuse them.
func TestFrontPostQuota(t *testing.T) {
	r := newFrontRig(t)
	if err := r.p.DB.KVPut(r.ctx, kvNS, "quota/shop", []byte("50")); err != nil {
		t.Fatal(err)
	}
	form := map[string]string{"Content-Type": "multipart/form-data; boundary=x"}
	res, body := r.do("POST", s3Host, "/shop--pics", make([]byte, 80), form)
	if res.StatusCode != 403 || !strings.Contains(body, "QuotaExceeded") || r.gw.reached("POST /shop--pics") {
		t.Fatalf("form upload over the limit: %d %s", res.StatusCode, body)
	}
	if res, _ := r.do("POST", s3Host, "/shop--pics", make([]byte, 20), form); res.StatusCode != 200 {
		t.Fatalf("form upload within the limit: %d", res.StatusCode)
	}
	if res, _ := r.do("POST", s3Host, "/shop--pics?delete", make([]byte, 80), nil); res.StatusCode != 200 {
		t.Fatalf("DeleteObjects is not an upload: %d", res.StatusCode)
	}
	_ = r.p.DB.KVDelete(r.ctx, kvNS, "quota/shop")
	if res, body := r.do("POST", s3Host, "/shop--media", make([]byte, 20), form); res.StatusCode != 403 || !strings.Contains(body, "presigned PUT") {
		t.Fatalf("form upload to a bucket with rules: %d %s", res.StatusCode, body)
	}
	// The same holds for PUTs to keys.
	_ = r.p.DB.KVPut(r.ctx, kvNS, "quota/shop", []byte("50"))
	if res, _ := r.do("PUT", s3Host, "/shop--pics/x", make([]byte, 80), nil); res.StatusCode != 403 {
		t.Fatalf("PUT over the limit: %d", res.StatusCode)
	}
}

func TestFrontCORS(t *testing.T) {
	r := newFrontRig(t)
	pre := func(origin string) *http.Response {
		res, _ := r.do("OPTIONS", s3Host, "/shop--media/a.png", nil, map[string]string{"Origin": origin,
			"Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "content-type"})
		return res
	}
	for _, o := range []string{"https://shop.tiffin.localhost", "https://shop.tiffin.localhost:8443", "https://pr-7--shop.tiffin.localhost", "http://localhost:3000"} {
		res := pre(o)
		if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != o || !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "PUT") ||
			res.Header.Get("Access-Control-Allow-Headers") != "content-type" || r.gw.reached("OPTIONS") {
			t.Fatalf("preflight from %s: %d %v", o, res.StatusCode, res.Header)
		}
	}
	for _, o := range []string{"https://evil.example", "https://blog.tiffin.localhost", "https://shop.tiffin.localhost.evil.example"} {
		if res := pre(o); res.StatusCode != 403 || res.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("preflight from %s: %d %v", o, res.StatusCode, res.Header)
		}
	}
	res, _ := r.do("PUT", s3Host, "/shop--media/a.png", []byte("x"), map[string]string{"Origin": "https://shop.tiffin.localhost", "Content-Type": "image/png"})
	if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != "https://shop.tiffin.localhost" || !strings.Contains(res.Header.Get("Access-Control-Expose-Headers"), "ETag") {
		t.Fatalf("upload response: %d %v", res.StatusCode, res.Header)
	}
	// An explicit list replaces the default.
	_ = putMeta(r.ctx, r.p, "shop--media", &bucketMeta{Project: "shop", Name: "media", CORS: []string{"https://*.example.com"}})
	if pre("https://app.example.com").StatusCode != 204 || pre("https://shop.tiffin.localhost").StatusCode != 403 || pre("https://example.com").StatusCode != 403 {
		t.Fatal("explicit cors list")
	}
	if !matchOrigin([]string{"*"}, "https://x.y") || matchOrigin([]string{"https://a.com"}, "http://a.com") || !matchOrigin([]string{"https://a.com/"}, "https://A.com") {
		t.Fatal("matchOrigin")
	}
}

func TestObjectCreatedEvents(t *testing.T) {
	r := newFrontRig(t)
	r.do("PUT", s3Host, "/shop--pics/cat%20one.png", []byte("PNGDATA"), map[string]string{"Content-Type": "image/png"})
	r.do("PUT", s3Host, "/shop--pics/p?partNumber=1&uploadId=U", []byte("part"), nil) // a part: no event
	r.gw.parts = 5
	r.do("POST", s3Host, "/shop--pics/movie.mp4?uploadId=U", []byte("<CompleteMultipartUpload/>"), nil)
	r.gw.status = 403
	r.do("PUT", s3Host, "/shop--pics/denied.png", []byte("x"), nil) // refused by the gateway: no event
	deadline := time.Now().Add(5 * time.Second)
	for len(r.pub.events()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	ev := r.pub.events()
	if len(ev) != 2 {
		t.Fatalf("events: %+v", ev)
	}
	a, b := ev[0], ev[1]
	if a.Event != "object.created" || a.Bucket != "pics" || a.Key != "cat one.png" || a.Size != 7 || a.ContentType != "image/png" || a.ETag != "etag-7" ||
		a.URL != "https://files.tiffin.localhost/shop/pics/cat%20one.png" {
		t.Fatalf("put event: %+v", a)
	}
	if b.Key != "movie.mp4" || b.Size != 10 || b.ContentType != "video/mp4" {
		t.Fatalf("multipart event: %+v", b)
	}
	if r.pub.keys[0] == "" || r.pub.keys[0] == r.pub.keys[1] {
		t.Fatalf("dedupe keys: %v", r.pub.keys)
	}
}

func TestSignedFilesURL(t *testing.T) {
	r := newFrontRig(t)
	r.gw.objects["/shop--media/doc.pdf"] = fakeObject{[]byte("%PDF-1"), "application/pdf"}
	files := "files.tiffin.localhost"
	if res, _ := r.do("GET", files, "/shop/media/doc.pdf", nil, nil); res.StatusCode != 403 {
		t.Fatalf("private without a signature: %d", res.StatusCode)
	}
	exp := time.Now().Add(time.Minute).Unix()
	sig := FilesSignature(r.user.Secret, "shop", "media", "doc.pdf", exp)
	res, body := r.do("GET", files, fmt.Sprintf("/shop/media/doc.pdf?exp=%d&sig=%s", exp, sig), nil, nil)
	if res.StatusCode != 200 || body != "%PDF-1" || !strings.HasPrefix(res.Header.Get("Cache-Control"), "private, max-age=") {
		t.Fatalf("signed: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res, _ := r.do("GET", files, fmt.Sprintf("/shop/media/doc2.pdf?exp=%d&sig=%s", exp, sig), nil, nil); res.StatusCode != 403 {
		t.Fatalf("signature for another key: %d", res.StatusCode)
	}
	old := time.Now().Add(-time.Second).Unix()
	if res, body := r.do("GET", files, fmt.Sprintf("/shop/media/doc.pdf?exp=%d&sig=%s", old, FilesSignature(r.user.Secret, "shop", "media", "doc.pdf", old)), nil, nil); res.StatusCode != 403 || !strings.Contains(body, "expired") {
		t.Fatalf("expired: %d %s", res.StatusCode, body)
	}
}

func TestParseImageParams(t *testing.T) {
	for _, c := range []struct {
		q    string
		want imageParams
		ok   bool
		err  string
	}{
		{"", imageParams{0, 75, "original"}, false, ""},
		{"w=640", imageParams{640, 75, "original"}, true, ""},
		{"w=640&q=90&f=webp", imageParams{640, 90, "webp"}, true, ""},
		{"f=avif", imageParams{0, 75, "avif"}, true, ""},
		{"w=641", imageParams{}, true, "w must be one of 16, 32"},
		{"w=", imageParams{}, true, "w must be"},
		{"q=80", imageParams{}, true, "q must be one of 50, 75, 90, 100"},
		{"f=png", imageParams{}, true, "f must be webp, avif or original"},
	} {
		q, _ := url.ParseQuery(c.q)
		got, ok, err := parseImageParams(q)
		if ok != c.ok || (c.err == "" && (err != nil || got != c.want)) || (c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err))) {
			t.Errorf("%q: %+v %v %v", c.q, got, ok, err)
		}
	}
	a := imageParams{640, 75, "webp"}
	if a.cacheKey("b", "k", "e1") == a.cacheKey("b", "k", "e2") || a.cacheKey("b", "k", "e1") == (imageParams{750, 75, "webp"}).cacheKey("b", "k", "e1") {
		t.Fatal("cache key must change with the ETag and the params")
	}
	if a.outputExt("image/png") != "webp" || (imageParams{Format: "original"}).outputExt("image/jpeg") != "jpg" {
		t.Fatal("outputExt")
	}
}

// fakeEngine "transforms" by prefixing the params; calls counts runs.
func fakeEngine(calls *int) imageEngine {
	var mu sync.Mutex
	return func(_ context.Context, src []byte, srcType string, ip imageParams) ([]byte, error) {
		mu.Lock()
		if calls != nil {
			*calls++
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return []byte(fmt.Sprintf("%d/%d/%s:%s", ip.Width, ip.Quality, ip.outputExt(srcType), src)), nil
	}
}

func TestImageTransformCache(t *testing.T) {
	r := newFrontRig(t)
	calls := 0
	r.f.img.engine = fakeEngine(&calls)
	r.gw.objects["/shop--pics/a.png"] = fakeObject{[]byte("PNG"), "image/png"}
	r.gw.objects["/shop--pics/logo.svg"] = fakeObject{[]byte("<svg/>"), "image/svg+xml"}
	files := "files.tiffin.localhost"
	// Concurrent first requests share one transform.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.do("GET", files, "/shop/pics/a.png?w=640&q=75&f=webp", nil, nil)
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("transforms run: %d", calls)
	}
	res, body := r.do("GET", files, "/shop/pics/a.png?w=640&q=75&f=webp", nil, nil)
	if res.StatusCode != 200 || body != "640/75/webp:PNG" || res.Header.Get("Content-Type") != "image/webp" || res.Header.Get("X-Tiffin-Cache") != "HIT" || calls != 1 {
		t.Fatalf("cached: %d %q %v (calls %d)", res.StatusCode, body, res.Header, calls)
	}
	if res, _ := r.do("GET", files, "/shop/pics/a.png?w=640&q=75&f=webp", nil, map[string]string{"If-None-Match": res.Header.Get("ETag")}); res.StatusCode != 304 {
		t.Fatalf("revalidation: %d", res.StatusCode)
	}
	if res, body := r.do("GET", files, "/shop/pics/a.png?w=999", nil, nil); res.StatusCode != 400 || !strings.Contains(body, "3840") {
		t.Fatalf("width outside the allowlist: %d %s", res.StatusCode, body)
	}
	if res, body := r.do("GET", files, "/shop/pics/logo.svg?w=640&f=webp", nil, nil); res.StatusCode != 200 || body != "<svg/>" {
		t.Fatalf("not a raster image: served as stored: %d %q", res.StatusCode, body)
	}
	// A new version of the object is a new cache entry.
	r.gw.objects["/shop--pics/a.png"] = fakeObject{[]byte("PNG2"), "image/png"}
	if res, body := r.do("GET", files, "/shop/pics/a.png?w=640&q=75&f=webp", nil, nil); body != "640/75/webp:PNG2" || res.Header.Get("X-Tiffin-Cache") != "MISS" {
		t.Fatalf("after a change: %q %v", body, res.Header)
	}
	// Private buckets need a signature; w, q and f can be added to it.
	r.gw.objects["/shop--media/b.jpg"] = fakeObject{[]byte("JPG"), "image/jpeg"}
	if res, _ := r.do("GET", files, "/shop/media/b.jpg?w=64", nil, nil); res.StatusCode != 403 {
		t.Fatalf("private transform without a signature: %d", res.StatusCode)
	}
	exp := time.Now().Add(time.Hour).Unix()
	u := fmt.Sprintf("/shop/media/b.jpg?exp=%d&sig=%s&w=64&q=50", exp, FilesSignature(r.user.Secret, "shop", "media", "b.jpg", exp))
	if res, body := r.do("GET", files, u, nil, nil); res.StatusCode != 200 || body != "64/50/jpg:JPG" || !strings.HasPrefix(res.Header.Get("Cache-Control"), "private") {
		t.Fatalf("signed transform: %d %q %v", res.StatusCode, body, res.Header)
	}
}

func TestImageCacheEvicts(t *testing.T) {
	c := &imageCache{dir: t.TempDir(), max: 25}
	keys := []string{"aa01", "bb02", "cc03"}
	for _, k := range keys {
		if _, err := c.put(k, "webp", make([]byte, 10)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if k == "bb02" {
			c.get("aa01") // aa01 is now more recently used than bb02
		}
	}
	if _, ok := c.get("bb02"); ok {
		t.Fatal("the least recently used file should be gone")
	}
	if _, ok := c.get("aa01"); !ok {
		t.Fatal("aa01 was used recently")
	}
	if c.total != 20 {
		t.Fatalf("total %d", c.total)
	}
	// A restart reads the cache back from disk.
	d := &imageCache{dir: c.dir, max: 25}
	if _, ok := d.get("cc03"); !ok || d.total != 20 {
		t.Fatalf("reloaded: total %d", d.total)
	}
}

// A transform's output box is bounded whatever the source's dimensions
// (no width means the largest width, not the source's), it runs under a
// memory cap when prlimit is there, and what it writes is capped.
func TestVipsCommandBounds(t *testing.T) {
	name, args := vipsCommand("/usr/bin/vips", "", "image/png", imageParams{Quality: 75, Format: "avif"})
	line := strings.Join(args, " ")
	if name != "nice" || !strings.Contains(line, "[descriptor=0] .avif[Q=75,effort=1,keep=none] 3840 --height 10416 --size down") {
		t.Fatalf("no width: %s %s", name, line)
	}
	_, args = vipsCommand("/usr/bin/vips", "", "image/jpeg", imageParams{Width: 16, Quality: 75, Format: "original"})
	if line := strings.Join(args, " "); !strings.Contains(line, " 16 --height 100000 ") {
		t.Fatalf("narrow: %s", line)
	}
	name, args = vipsCommand("/usr/bin/vips", "/usr/bin/prlimit", "image/gif", imageParams{Width: 640, Quality: 75, Format: "webp"})
	if line := strings.Join(args, " "); name != "/usr/bin/prlimit" || !strings.HasPrefix(line, "--as=2147483648 nice -n 10 /usr/bin/vips thumbnail_source") || !strings.HasSuffix(line, "n=-1") {
		t.Fatalf("prlimit: %s %s", name, line)
	}
	b := &cappedBuffer{max: 4}
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := b.Write([]byte("de")); !errors.Is(err, errImageTooBig) || !b.over || b.Len() != 3 {
		t.Fatalf("over the cap: %v %v %d", err, b.over, b.Len())
	}
}

// A transform that fails is not run again for a while: the same request
// fails at once (another request still runs). Then it is tried again.
func TestImageTransformFailureRemembered(t *testing.T) {
	r := newFrontRig(t)
	var mu sync.Mutex
	calls := 0
	r.f.img.engine = func(_ context.Context, src []byte, _ string, _ imageParams) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if string(src) == "SLOW" {
			return nil, errors.New("the transform took longer than 30s")
		}
		return []byte("ok"), nil
	}
	r.gw.objects["/shop--pics/big.jpg"] = fakeObject{[]byte("SLOW"), "image/jpeg"}
	r.gw.objects["/shop--pics/a.png"] = fakeObject{[]byte("PNG"), "image/png"}
	files := "files.tiffin.localhost"
	for i := range 3 {
		if res, _ := r.do("GET", files, "/shop/pics/big.jpg?f=avif", nil, nil); res.StatusCode != 422 {
			t.Fatalf("request %d: %d", i, res.StatusCode)
		}
	}
	if calls != 1 {
		t.Fatalf("a failed transform ran %d times", calls)
	}
	if res, body := r.do("GET", files, "/shop/pics/a.png?f=avif", nil, nil); res.StatusCode != 200 || body != "ok" || calls != 2 {
		t.Fatalf("another image: %d %q (calls %d)", res.StatusCode, body, calls)
	}
	r.f.img.mu.Lock()
	for k, f := range r.f.img.failed {
		f.until = time.Now().Add(-time.Second)
		r.f.img.failed[k] = f
	}
	r.f.img.mu.Unlock()
	if res, _ := r.do("GET", files, "/shop/pics/big.jpg?f=avif", nil, nil); res.StatusCode != 422 || calls != 3 {
		t.Fatalf("after the failure expired: %d (calls %d)", res.StatusCode, calls)
	}
}

// One project never holds every transform slot: with its own slots taken,
// another project still gets one at once.
func TestImageTransformSlotsShared(t *testing.T) {
	iw := newImageWork(fakeEngine(nil), t.TempDir(), 1<<20)
	if cap(iw.slots) < 2 || iw.perProj >= cap(iw.slots) {
		t.Fatalf("slots %d, per project %d", cap(iw.slots), iw.perProj)
	}
	var releases []func()
	for range iw.perProj {
		rel, err := iw.acquire(context.Background(), "hog")
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, rel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := iw.acquire(ctx, "hog"); !errors.Is(err, errBusy) {
		t.Fatalf("the project over its share: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rel, err := iw.acquire(ctx, "other")
	if err != nil {
		t.Fatalf("another project: %v", err)
	}
	rel()
	for _, rel := range releases {
		rel()
	}
}
