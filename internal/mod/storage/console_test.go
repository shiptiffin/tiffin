package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// put writes a file into a bucket's directory, as the gateway would.
func (r *frontRig) put(bucket, key, body string) {
	r.t.Helper()
	path := filepath.Join(dataDir(r.p.DataRoot), "shop--"+bucket, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *frontRig) has(bucket, key string) bool {
	_, err := os.Stat(filepath.Join(dataDir(r.p.DataRoot), "shop--"+bucket, filepath.FromSlash(key)))
	return err == nil
}

func status(err error) int {
	var pr *api.Problem
	if errors.As(err, &pr) {
		return pr.Status
	}
	if err != nil {
		return 500
	}
	return 200
}

// Renames, moves and deletes work on the files themselves; each can be
// undone once, and the undo redone, unless something changed since.
func TestFilesMoveDeleteUndo(t *testing.T) {
	r := newFrontRig(t)
	m, p, ctx := r.m, r.p, r.ctx
	r.put("media", "a/one.txt", "1")
	r.put("media", "a/b/two.txt", "22")
	r.put("media", "c.txt", "333")

	res, err := m.moveObjects(ctx, p, "shop", "media", []string{"c.txt"}, "", "a/c2.txt")
	if err != nil || res.Files != 1 || res.Bytes != 3 || res.Keys[0] != "a/c2.txt" || !r.has("media", "a/c2.txt") || r.has("media", "c.txt") {
		t.Fatalf("rename: %+v %v", res, err)
	}
	back, err := m.undoFiles(ctx, p, "shop", res.Undo)
	if err != nil || !r.has("media", "c.txt") || r.has("media", "a/c2.txt") {
		t.Fatalf("undo rename: %v", err)
	}
	if _, err := m.undoFiles(ctx, p, "shop", res.Undo); status(err) != 404 {
		t.Fatalf("undo twice: %v", err)
	}
	if _, err := m.undoFiles(ctx, p, "shop", back.Undo); err != nil || !r.has("media", "a/c2.txt") {
		t.Fatalf("redo: %v", err)
	}
	if _, err := m.moveObjects(ctx, p, "shop", "media", []string{"a/c2.txt"}, "", "c.txt"); err != nil {
		t.Fatal(err)
	}

	// A folder moves with everything in it; the old one is gone.
	res, err = m.moveObjects(ctx, p, "shop", "media", nil, "a/", "z/a/")
	if err != nil || res.Files != 2 || !r.has("media", "z/a/b/two.txt") || r.has("media", "a") {
		t.Fatalf("move folder: %+v %v", res, err)
	}
	if _, err := m.undoFiles(ctx, p, "shop", res.Undo); err != nil || !r.has("media", "a/b/two.txt") || r.has("media", "z") {
		t.Fatalf("undo folder move: %v", err)
	}

	// Nothing is overwritten, a folder can't go inside itself, keys stay inside the bucket.
	for what, c := range map[string]struct {
		keys       []string
		prefix, to string
		want       int
	}{
		"onto a file":    {[]string{"c.txt"}, "", "a/one.txt", 409},
		"into itself":    {nil, "a/", "a/b/", 422},
		"escape":         {[]string{"c.txt"}, "", "../x.txt", 422},
		"missing":        {[]string{"nope.txt"}, "", "x.txt", 404},
		"two to one":     {[]string{"c.txt", "a/one.txt"}, "", "x.txt", 422},
		"both":           {[]string{"c.txt"}, "a/", "x/", 422},
		"folder no dash": {nil, "a", "x/", 422},
	} {
		if _, err := m.moveObjects(ctx, p, "shop", "media", c.keys, c.prefix, c.to); status(err) != c.want {
			t.Errorf("%s: %v, want %d", what, err, c.want)
		}
	}

	// Deletes are held for an hour; undo puts them back, redo deletes again.
	res, err = m.deleteObjects(ctx, p, "shop", "media", []string{"a/one.txt"}, "")
	if err != nil || r.has("media", "a/one.txt") || res.Files != 1 {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(heldDir(p.DataRoot, res.Undo), "a", "one.txt")); err != nil {
		t.Fatalf("not held: %v", err)
	}
	back, err = m.undoFiles(ctx, p, "shop", res.Undo)
	if err != nil || !r.has("media", "a/one.txt") {
		t.Fatalf("undo delete: %v", err)
	}
	if _, err := m.undoFiles(ctx, p, "shop", back.Undo); err != nil || r.has("media", "a/one.txt") {
		t.Fatalf("redo delete: %v", err)
	}

	// A new file where the old one was: undo refuses rather than overwrite.
	res, err = m.deleteObjects(ctx, p, "shop", "media", nil, "a/")
	if err != nil || res.Files != 1 || r.has("media", "a") {
		t.Fatalf("delete folder: %+v %v", res, err)
	}
	r.put("media", "a/b/two.txt", "new")
	if _, err := m.undoFiles(ctx, p, "shop", res.Undo); status(err) != 409 || !strings.Contains(err.Error(), "a/b/two.txt") {
		t.Fatalf("undo over a new file: %v", err)
	}

	// After the hour, held files are gone for good.
	if n, err := purgeHeld(ctx, p, time.Now().Add(2*holdKeep)); err != nil || n == 0 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := os.Stat(heldDir(p.DataRoot, res.Undo)); !os.IsNotExist(err) {
		t.Fatalf("held files left: %v", err)
	}
	if _, err := m.undoFiles(ctx, p, "shop", res.Undo); status(err) != 404 {
		t.Fatalf("undo after purge: %v", err)
	}
	// Another project's undo id is not this one's.
	res, _ = m.deleteObjects(ctx, p, "shop", "media", []string{"c.txt"}, "")
	if _, err := m.undoFiles(ctx, p, "blog", res.Undo); status(err) != 404 {
		t.Fatalf("other project: %v", err)
	}
}

func TestFilesUnder(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{"photos/2026/a.jpg", "photos/2025/b.jpg", "photos/x.jpg", "pdfs/c.pdf", "top.txt", ".sgwtmp/multipart/part"} {
		path := objectPathOn(dir, k)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte("x"), 0o644)
	}
	keys := func(prefix string) string {
		fs, err := filesUnder(dir, prefix, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range fs {
			out = append(out, f.From)
		}
		return strings.Join(out, " ")
	}
	for prefix, want := range map[string]string{
		"":           "pdfs/c.pdf photos/2025/b.jpg photos/2026/a.jpg photos/x.jpg top.txt",
		"photos/":    "photos/2025/b.jpg photos/2026/a.jpg photos/x.jpg",
		"photos/202": "photos/2025/b.jpg photos/2026/a.jpg",
		"p":          "pdfs/c.pdf photos/2025/b.jpg photos/2026/a.jpg photos/x.jpg",
		"nope/":      "",
	} {
		if got := keys(prefix); got != want {
			t.Errorf("%q: %q, want %q", prefix, got, want)
		}
	}
}

// apiRig serves the real API with the package's module wired to the rig's
// fake gateway and front, and an owner token.
func apiRig(t *testing.T) (*frontRig, func(method, target, body string) (int, map[string]any, http.Header, []byte)) {
	r, h, owner, _ := apiServer(t)
	return r, func(method, target, body string) (int, map[string]any, http.Header, []byte) {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+owner)
		if body != "" && body[0] == '{' {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Header(), w.Body.Bytes()
	}
}

// apiServer is the API handler of apiRig, with the owner's token.
func apiServer(t *testing.T) (*frontRig, http.Handler, string, *tokens.Manager) {
	r := newFrontRig(t)
	tm := tokens.NewManager(r.p.DB)
	owner, _, err := tm.Bootstrap(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	r.p.Tokens = tm
	mod.mu.Lock()
	mod.gw, mod.front, mod.publisher = r.m.gw, r.f, r.pub
	mod.mu.Unlock()
	t.Cleanup(func() {
		mod.mu.Lock()
		mod.gw, mod.front, mod.publisher = nil, nil, nil
		mod.mu.Unlock()
	})
	go mod.runEvents(r.ctx, r.p)
	return r, api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: tm, Platform: r.p}).Handler(), owner, tm
}

// countingReader counts what is read of a request body.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// The multipart upload is held to its body limit before the form is
// parsed (huma's form parser spools any size to disk), and a caller who
// may not write gets 403 before a byte of it is read.
func TestMultipartUploadLimits(t *testing.T) {
	r, h, owner, tm := apiServer(t)
	form := func(size int) (string, []byte) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		_ = mw.WriteField("key", "a.png")
		fw, _ := mw.CreateFormFile("file", "a.png")
		_, _ = fw.Write(bytes.Repeat([]byte("x"), size))
		_ = mw.Close()
		return mw.FormDataContentType(), b.Bytes()
	}
	send := func(token string, size int, chunked bool) (int, int64) {
		ct, body := form(size)
		cr := &countingReader{r: bytes.NewReader(body)}
		req := httptest.NewRequest("POST", "/v1/projects/shop/storage/buckets/pics/objects", cr)
		req.ContentLength = int64(len(body))
		if chunked {
			req.ContentLength = -1
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, cr.n
	}
	if code, _ := send(owner, 10, false); code != 200 {
		t.Fatalf("a small upload: %d", code)
	}
	if code, read := send(owner, MaxAPIUpload+2<<20, false); code != 413 || read != 0 {
		t.Fatalf("over the limit, length known: %d, read %d", code, read)
	}
	if code, read := send(owner, MaxAPIUpload+4<<20, true); code < 400 || read > MaxAPIUpload+2<<20 {
		t.Fatalf("over the limit, length unknown: %d, read %d", code, read)
	}
	op, _ := tm.Authenticate(r.ctx, owner)
	viewer, _, err := tm.Create(r.ctx, op, tokens.CreateRequest{Name: "viewer", Scopes: []tokens.Scope{tokens.ScopeRead}})
	if err != nil {
		t.Fatal(err)
	}
	if code, read := send(viewer, 1<<20, false); code != 403 || read != 0 {
		t.Fatalf("a viewer: %d, read %d", code, read)
	}
}

func TestFilesConsoleOps(t *testing.T) {
	r, call := apiRig(t)
	const b = "/v1/projects/shop/storage/buckets/"

	// Links: a public bucket's plain URL, a private one's signed URL that verifies.
	code, out, _, _ := call("POST", b+"pics/link", `{"key":"a b.jpg","w":640,"f":"webp"}`)
	if code != 200 || out["url"] != "https://files.tiffin.localhost/shop/pics/a%20b.jpg?w=640&f=webp" || out["expiresAt"] != nil {
		t.Fatalf("public link: %d %v", code, out)
	}
	code, out, _, _ = call("POST", b+"media/link", `{"key":"x.png","expiresIn":60}`)
	u, _ := url.Parse(out["url"].(string))
	if code != 200 || u.Path != "/shop/media/x.png" || out["expiresAt"] == nil {
		t.Fatalf("private link: %d %v", code, out)
	}
	if left, err := r.f.checkSigned(r.ctx, "shop", "media", "x.png", u.Query(), time.Now()); err != nil || left > 60 {
		t.Fatalf("signature: %d %v", left, err)
	}
	if code, _, _, _ := call("POST", b+"pics/link", `{"key":"a.jpg","w":641}`); code != 422 {
		t.Fatalf("bad width: %d", code)
	}

	// Files show on the dashboard's origin only as media, sandboxed, never sniffed.
	r.gw.objects["/shop--media/p.png"] = fakeObject{[]byte("PNGDATA"), "image/png"}
	r.gw.objects["/shop--media/page.html"] = fakeObject{[]byte("<script>"), "text/html"}
	code, _, h, body := call("GET", b+"media/file?key=p.png", "")
	if code != 200 || string(body) != "PNGDATA" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") ||
		h.Get("Content-Disposition") != "" || h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("image: %d %q %v", code, body, h)
	}
	if code, _, h, _ := call("GET", b+"media/file?key=page.html", ""); code != 200 || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("html must download: %d %v", code, h)
	}
	if code, _, h, _ := call("GET", b+"media/file?key=p.png&download=true", ""); code != 200 || !strings.Contains(h.Get("Content-Disposition"), "p.png") {
		t.Fatalf("download: %d %v", code, h)
	}
	if code, _, _, _ := call("GET", b+"media/file?key=gone.png", ""); code != 404 {
		t.Fatalf("missing file: %d", code)
	}

	// Large uploads: the bucket's rules apply before any byte moves (media takes images and PDFs up to 100 bytes).
	if code, out, _, _ := call("POST", b+"media/uploads", `{"key":"big.png","size":101,"contentType":"image/png"}`); code != 413 || !strings.Contains(out["detail"].(string), "up to 100 B") {
		t.Fatalf("too big: %d %v", code, out)
	}
	if code, _, _, _ := call("POST", b+"media/uploads", `{"key":"a.txt","size":10,"contentType":"text/plain"}`); code != 422 {
		t.Fatalf("wrong type: %d", code)
	}
	code, out, _, _ = call("POST", b+"media/uploads", `{"key":"v.png","size":80,"contentType":"image/png"}`)
	if code != 200 || out["uploadId"] != "U1" || out["partSize"] != float64(minPart) {
		t.Fatalf("start: %d %v", code, out)
	}
	code, out, _, _ = call("PUT", b+"media/uploads/U1/parts/1?key=v.png", strings.Repeat("x", 40))
	if code != 200 || out["etag"] != "tag" || out["size"] != float64(40) || !r.gw.reached("PUT /shop--media/v.png?partNumber=1&uploadId=U1") {
		t.Fatalf("part: %d %v", code, out)
	}
	if code, _, _, _ := call("PUT", b+"media/uploads/U1/parts/2?key=v.png", strings.Repeat("x", 101)); code != 413 {
		t.Fatalf("part over the bucket's limit: %d", code)
	}
	r.gw.parts = 40
	code, out, _, _ = call("POST", b+"media/uploads", `{"key":"v.png","size":80,"contentType":"image/png","uploadId":"U1"}`)
	if parts, _ := out["parts"].([]any); code != 200 || len(parts) != 2 {
		t.Fatalf("resume lists stored parts: %d %v", code, out)
	}
	code, out, _, _ = call("POST", b+"media/uploads/U1/complete", `{"key":"v.png"}`)
	if code != 200 || out["size"] != float64(80) || !r.gw.reached("POST /shop--media/v.png?uploadId=U1") {
		t.Fatalf("complete: %d %v", code, out)
	}
	r.gw.parts = 60
	if code, _, _, _ := call("POST", b+"media/uploads/U2/complete", `{"key":"w.png"}`); code != 413 || !r.gw.reached("DELETE /shop--media/w.png?uploadId=U2") {
		t.Fatalf("complete over the limit aborts: %d", code)
	}
	if code, _, _, _ := call("DELETE", b+"media/uploads/U3?key=x.png", ""); code != 204 && code != 200 {
		t.Fatalf("abort: %d", code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(r.pub.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ev := r.pub.events(); len(ev) != 1 || ev[0].Key != "v.png" {
		t.Fatalf("object.created: %+v", ev)
	}

	// A folder delete asks first with what goes; then it can be undone.
	r.put("media", "old/a.txt", "aa")
	r.put("media", "old/b/c.txt", "ccc")
	code, out, _, _ = call("POST", b+"media/delete", `{"prefix":"old/"}`)
	preview, _ := out["preview"].(map[string]any)
	if code != 428 || preview["count"] != float64(2) || preview["bytes"] != float64(5) || !r.has("media", "old/a.txt") {
		t.Fatalf("folder delete asks: %d %v", code, out)
	}
	code, out, _, _ = call("POST", b+"media/delete", `{"prefix":"old/","confirm":"`+out["confirm"].(string)+`"}`)
	if code != 200 || out["files"] != float64(2) || r.has("media", "old") {
		t.Fatalf("folder delete: %d %v", code, out)
	}
	if code, out, _, _ = call("POST", "/v1/projects/shop/storage/undo", `{"id":"`+out["undo"].(string)+`"}`); code != 200 || !r.has("media", "old/b/c.txt") {
		t.Fatalf("undo: %d %v", code, out)
	}
	if code, out, _, _ := call("POST", b+"media/move", `{"keys":["old/a.txt"],"to":"new.txt"}`); code != 200 || out["undo"] == "" || !r.has("media", "new.txt") {
		t.Fatalf("move: %d %v", code, out)
	}
}
