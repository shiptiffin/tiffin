package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestViaBox: the post-deploy check reaches a name this computer can't
// resolve yet through the box's own address, keeping the name for TLS.
func TestViaBox(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, _, _ := strings.Cut(r.Host, ":"); h != "example.com" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	tr := viaBox(srv.URL, srv.Client().Transport)
	if tr == nil {
		t.Fatal("no transport")
	}
	u, _ := url.Parse(srv.URL)
	port := u.Port()
	res, err := (&http.Client{Transport: tr}).Get("https://example.com:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	if viaBox("not a url\x00", nil) != nil {
		t.Fatal("bad base gave a transport")
	}
}
