//go:build e2e

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStorageUploads is the browser-uploads acceptance test on a fresh box.
// A Bun app (e2e/uploadsapp) hands out upload tickets; a real headless
// Chrome on the Mac loads the app's page at shop.<domain> and uploads,
// straight to s3.<domain> (CORS from the app origin): 5 MB in one PUT and
// 200 MB in parallel parts, both with progress → a file over the bucket's
// maxFileSize is refused by the box → each upload reaches the app's queue
// handler as an object.created event → an image in a public bucket is
// resized to WebP at files.<domain>, and the second request is a cache hit.
func TestStorageUploads(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "uploads", "shop")
	phase("up", start)

	p := time.Now()
	app := filepath.Join(b.dir, "uploadsapp")
	if out, err := exec.Command("cp", "-R", filepath.Join(RepoRoot(), "e2e", "uploadsapp"), app).CombinedOutput(); err != nil {
		t.Fatalf("copy app: %v %s", err, out)
	}
	bundle := func(entry, out, target string) {
		t.Helper()
		cmd := exec.Command("bun", "build", entry, "--target="+target, "--format=esm", "--outfile="+out)
		cmd.Dir = filepath.Join(RepoRoot(), "e2e", "uploadsapp")
		if o, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bun build %s: %v\n%s", entry, err, o)
		}
	}
	bundle("./sdk-entry.ts", filepath.Join(app, "sdk.gen.js"), "bun")
	bundle("./client-entry.ts", filepath.Join(app, "public", "client.js"), "browser")
	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: uploads")
	b.waitReady("app/web", "bucket/media", "bucket/small", "bucket/pics", "topic/storage.object.created")
	deployArgs(t, b, app)
	phase("deploy", p)

	// ---- the browser uploads ----
	p = time.Now()
	site := b.url("shop")
	var res struct {
		Single, Multipart, TooBig struct {
			OK        bool   `json:"ok"`
			Code      string `json:"code"`
			Status    int    `json:"status"`
			Message   string `json:"message"`
			Progress  int    `json:"progress"`
			Last      float64
			Monotonic bool
			MS        int `json:"ms"`
			Done      struct {
				Key  string `json:"key"`
				Size int64  `json:"size"`
				ETag string `json:"etag"`
			} `json:"done"`
		}
	}
	cmd := exec.Command("node", filepath.Join(app, "browser.mjs"), site+"/", filepath.Join(RepoRoot(), "apps", "dashboard", "package.json"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("browser: %v\n%s\n%s", err, out, stderr.String())
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("browser output: %s\n%s", out, stderr.String())
	}
	s, m, big := res.Single, res.Multipart, res.TooBig
	if !s.OK || s.Done.Size != 5<<20 || s.Last != 100 || !s.Monotonic || s.Progress < 2 {
		t.Fatalf("5 MB single PUT: %+v\n%s", s, stderr.String())
	}
	if !m.OK || m.Done.Size != 200<<20 || m.Last != 100 || !m.Monotonic || m.Progress < 25 || !strings.Contains(m.Done.ETag, "-") {
		t.Fatalf("200 MB multipart: %+v\n%s", m, stderr.String())
	}
	if big.OK || big.Code != "EntityTooLarge" || big.Status != 413 {
		t.Fatalf("over maxFileSize: %+v", big)
	}
	t.Logf("browser: 5 MB in %d ms (%d progress events), 200 MB multipart in %d ms (%d progress events, etag %s); 2 MB to a 1 MiB bucket: %s %q",
		s.MS, s.Progress, m.MS, m.Progress, m.Done.ETag, big.Code, big.Message)
	phase("browser uploads", p)

	// ---- object.created reaches the app's queue handler ----
	p = time.Now()
	c := b.https()
	var events []struct {
		Event, Bucket, Key, ContentType, ETag string
		Size                                  int64
	}
	deadline := time.Now().Add(time.Minute)
	for {
		_, _, body := b.get(c, "GET", site+"/events", nil)
		_ = json.Unmarshal([]byte(body), &events)
		if len(events) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	got := map[string]int64{}
	for _, e := range events {
		if e.Event != "object.created" || e.Bucket != "media" {
			t.Fatalf("event: %+v", e)
		}
		got[e.Key] = e.Size
	}
	if len(events) != 2 || got[s.Done.Key] != 5<<20 || got[m.Done.Key] != 200<<20 {
		t.Fatalf("object.created events: %+v (want %s and %s)", events, s.Done.Key, m.Done.Key)
	}
	phase("events", p)

	// ---- image transforms at files.<domain> ----
	p = time.Now()
	img := image.NewRGBA(image.Rect(0, 0, 1000, 600))
	for x := 0; x < 1000; x++ {
		for y := 0; y < 600; y++ {
			img.Set(x, y, color.RGBA{uint8(x / 4), uint8(y / 3), 128, 255})
		}
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	b.ok("storage", "objects", "put", "shop", "pics", "--key", "photos/wide.png", "--base64", base64.StdEncoding.EncodeToString(pngBuf.Bytes()), "--content-type", "image/png")
	u := b.url("files") + "/shop/pics/photos/wide.png?w=640&q=75&f=webp"
	get := func() (int, http.Header, string) { return b.get(c, "GET", u, nil) }
	code, hdr, body := get()
	if code != 200 || hdr.Get("Content-Type") != "image/webp" || hdr.Get("X-Tiffin-Cache") != "MISS" {
		t.Fatalf("transform: %d %v %.200s", code, hdr, body)
	}
	if w, h := webpSize([]byte(body)); w != 640 || h != 384 {
		t.Fatalf("webp is %dx%d, want 640x384", w, h)
	}
	code, hdr, body2 := get()
	if code != 200 || hdr.Get("X-Tiffin-Cache") != "HIT" || body2 != body {
		t.Fatalf("second request: %d %v", code, hdr)
	}
	t.Logf("transform: %d-byte PNG → %d-byte WebP 640x384, then a cache HIT", pngBuf.Len(), len(body))
	if code, _, body := b.get(c, "GET", b.url("files")+"/shop/pics/photos/wide.png?w=641", nil); code != 400 || !strings.Contains(body, "640") {
		t.Fatalf("width outside the allowlist: %d %s", code, body)
	}
	code, hdr, body = b.get(c, "GET", b.url("files")+"/shop/pics/photos/wide.png?w=256&f=avif", nil)
	if code != 200 || hdr.Get("Content-Type") != "image/avif" || !strings.Contains(body[:min(32, len(body))], "ftypavif") {
		t.Fatalf("avif: %d %v %q", code, hdr, body[:min(32, len(body))])
	}
	phase("image transforms", p)
	t.Logf("TOTAL uploads acceptance: %s", time.Since(start).Round(time.Second))
}

// webpSize reads a WebP's width and height (VP8, VP8L or VP8X).
func webpSize(b []byte) (int, int) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8 ":
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
	case "VP8L":
		v := binary.LittleEndian.Uint32(b[21:25])
		return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1
	}
	return 0, 0
}
