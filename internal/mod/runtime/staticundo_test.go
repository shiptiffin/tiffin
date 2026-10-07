package runtime

import (
	"os"
	"testing"
)

// A static deploy whose switch fails (the edge does not take it) leaves the
// site serving the release before it, not the failed one's files.
func TestFailedStaticPromotionKeepsTheLiveFiles(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy("site", "", map[string]string{"index.html": "v1"})
	if v1.Status != StatusLive {
		t.Fatalf("v1: %s %s", v1.Status, v1.Error)
	}
	link := h.r.staticLink("shop", "site", "")
	was, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	h.edge.down.Store(true)
	v2 := h.deploy("site", "", map[string]string{"index.html": "v2"})
	h.edge.down.Store(false)
	if v2.Status == StatusLive {
		t.Fatal("v2 went live with the edge down")
	}
	if now, _ := os.Readlink(link); now != was {
		t.Fatalf("the site serves %s after the failed deploy, want %s", now, was)
	}
}
