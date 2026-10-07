package runtime

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// A finished deploy's build log over the 4 MiB a read returns is not
// "done" until a read reaches its end: its last lines (often why it
// failed) are never silently cut off.
func TestBuildLogDoneMeansItsEnd(t *testing.T) {
	h := newHarness(t)
	call := h.apiCall(t)
	code, d := call("POST", "/v1/projects/shop/apps/api/deploys", `{"files":{"index.ts":"x"}}`)
	if code != 202 {
		t.Fatalf("deploy: %d %v", code, d)
	}
	got := h.wait("api", d["id"].(string))
	f, err := os.OpenFile(h.r.buildLogPath(got), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(strings.Repeat("noise noise noise\n", (5<<20)/18) + "THE CAUSE\ntf") // a last word held back as a possible credential
	f.Close()
	path := "/v1/projects/shop/apps/api/deploys/" + got.ID + "/build-log"
	code, first := call("GET", path, "")
	if code != 200 || first["done"] != false || strings.Contains(first["text"].(string), "THE CAUSE") {
		t.Fatalf("first read: %d done %v", code, first["done"])
	}
	off := strconv.FormatInt(int64(first["offset"].(float64)), 10)
	code, rest := call("GET", path+"?offset="+off, "")
	if code != 200 || rest["done"] != true || !strings.Contains(rest["text"].(string), "THE CAUSE") {
		t.Fatalf("second read: %d done %v", code, rest["done"])
	}
}
