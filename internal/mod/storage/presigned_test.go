package storage

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Browser-style uploads against the real gateway: presigned URLs with the
// type, length and size cap signed in, multipart parts and completion, CORS
// from the app's origin, and object.created events.
func TestPresignedUploads(t *testing.T) {
	r := newRig(t)
	pub := &recPublisher{}
	r.m.publisher = pub
	r.apply(`{"project":"shop","apps":{"web":{}},"services":{"storage":{"buckets":{"media":{"maxFileSize":12582912,"allowedTypes":["image/*","video/*"]}}}}}`)
	c, _, _ := credsFor(r.ctx, r.p, "shop", false)
	const host = "s3.tiffin.localhost:8443"
	presign := func(method, key string, q url.Values, hdr map[string]string) string {
		u, err := PresignWith(method, "http://"+host, "shop--media", key, q, hdr, c, Region, time.Minute, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		return pu.RequestURI()
	}
	const app = "https://shop.tiffin.localhost:8443"
	origin := map[string]string{"Origin": app}

	// Single PUT: type and length are signed; another type fails the signature.
	put := presign("PUT", "a.png", nil, map[string]string{"content-type": "image/png", "content-length": "5"})
	if res, body := r.s3("PUT", host, put, []byte("12345"), nil, map[string]string{"Origin": app, "Content-Type": "image/png"}); res.StatusCode != 200 ||
		res.Header.Get("Access-Control-Allow-Origin") != app {
		t.Fatalf("presigned PUT: %d %s %v", res.StatusCode, body, res.Header)
	}
	if res, _ := r.s3("PUT", host, put, []byte("12345"), nil, map[string]string{"Content-Type": "image/gif"}); res.StatusCode != 403 {
		t.Fatalf("presigned PUT with another type: %d", res.StatusCode)
	}
	// The URL's cap cannot be stripped: the gateway rejects the signature.
	capped := presign("PUT", "b.png", url.Values{MaxSizeParam: {"3"}}, map[string]string{"content-type": "image/png"})
	if res, _ := r.s3("PUT", host, capped, []byte("12345"), nil, map[string]string{"Content-Type": "image/png"}); res.StatusCode != 413 {
		t.Fatalf("over the URL cap: %d", res.StatusCode)
	}
	stripped := strings.Replace(capped, "&"+MaxSizeParam+"=3", "", 1)
	if stripped == capped {
		t.Fatalf("no cap in %s", capped)
	}
	if res, _ := r.s3("PUT", host, stripped, []byte("12345"), nil, map[string]string{"Content-Type": "image/png"}); res.StatusCode != 403 {
		t.Fatalf("cap stripped from the URL: %d", res.StatusCode)
	}

	// Multipart: created with the project key, parts and completion presigned.
	res, body := r.s3("POST", r.front, "/shop--media/movie.mp4?uploads", nil, &c, map[string]string{"Content-Type": "video/mp4"})
	if res.StatusCode != 200 {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	uploadID := between(body, "<UploadId>", "</UploadId>")
	part := bytes.Repeat([]byte("p"), 5<<20)
	var etags []string
	for i, size := range []int{5 << 20, 1 << 20} {
		n := strconv.Itoa(i + 1)
		u := presign("PUT", "movie.mp4", url.Values{"partNumber": {n}, "uploadId": {uploadID}, MaxSizeParam: {"12582912"}}, map[string]string{"content-length": strconv.Itoa(size)})
		res, body := r.s3("PUT", host, u, part[:size], nil, origin)
		if res.StatusCode != 200 || res.Header.Get("ETag") == "" || !strings.Contains(res.Header.Get("Access-Control-Expose-Headers"), "ETag") {
			t.Fatalf("part %s: %d %s %v", n, res.StatusCode, body, res.Header)
		}
		etags = append(etags, res.Header.Get("ETag"))
	}
	list := presign("GET", "movie.mp4", url.Values{"uploadId": {uploadID}}, nil)
	if res, body := r.s3("GET", host, list, nil, nil, origin); res.StatusCode != 200 || strings.Count(body, "<Part>") != 2 {
		t.Fatalf("list parts: %d %s", res.StatusCode, body)
	}
	xmlBody := "<CompleteMultipartUpload>"
	for i, e := range etags {
		xmlBody += fmt.Sprintf("<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, e)
	}
	xmlBody += "</CompleteMultipartUpload>"
	done := presign("POST", "movie.mp4", url.Values{"uploadId": {uploadID}, MaxSizeParam: {"12582912"}}, nil)
	if res, body := r.s3("POST", host, done, []byte(xmlBody), nil, origin); res.StatusCode != 200 || !strings.Contains(body, "CompleteMultipartUploadResult") {
		t.Fatalf("complete: %d %s", res.StatusCode, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pub.events()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ev := pub.events()
	if len(ev) != 2 || ev[0].Key != "a.png" || ev[0].Size != 5 || ev[1].Key != "movie.mp4" || ev[1].Size != 6<<20 || ev[1].ContentType != "video/mp4" {
		t.Fatalf("events: %+v", ev)
	}

	// Over the cap at completion: refused, and the upload is aborted.
	_, body = r.s3("POST", r.front, "/shop--media/big.mp4?uploads", nil, &c, map[string]string{"Content-Type": "video/mp4"})
	uploadID = between(body, "<UploadId>", "</UploadId>")
	u := presign("PUT", "big.mp4", url.Values{"partNumber": {"1"}, "uploadId": {uploadID}}, nil)
	if res, _ := r.s3("PUT", host, u, part, nil, nil); res.StatusCode != 200 {
		t.Fatalf("part: %d", res.StatusCode)
	}
	done = presign("POST", "big.mp4", url.Values{"uploadId": {uploadID}, MaxSizeParam: {"1048576"}}, nil)
	if res, body := r.s3("POST", host, done, []byte("<CompleteMultipartUpload/>"), nil, nil); res.StatusCode != 413 || !strings.Contains(body, "aborted") {
		t.Fatalf("complete over the cap: %d %s", res.StatusCode, body)
	}
	gw, _ := r.m.gateway(r.p)
	if _, err := gw.partsSize(r.ctx, "shop--media", "big.mp4", uploadID); err == nil {
		t.Fatal("the upload should be aborted")
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	j := strings.Index(s, b)
	if i < 0 || j < i {
		return ""
	}
	return s[i+len(a) : j]
}
