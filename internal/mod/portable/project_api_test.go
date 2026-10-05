package portable

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
)

// TestProjectAPI drives export → download → upload → import, duplicate,
// and stop/start through the HTTP API, with a key's limits.
func TestProjectAPI(t *testing.T) {
	ctx := context.Background()
	x := newTestBox(t, "a.example")
	x.shop("shop")
	x.p.Tokens = tokens.NewManager(x.p.DB)
	owner, _, err := x.p.Tokens.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerP, _ := x.p.Tokens.Authenticate(ctx, owner)
	reader, _, err := x.p.Tokens.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "reader", Projects: tokens.Projects{"shop"}, Access: tokens.LevelRead})
	if err != nil {
		t.Fatal(err)
	}
	// The registered module serves the API: give it this box's services.
	for _, mod := range platform.Modules() {
		if pm, ok := mod.(*Module); ok {
			pm.be = x.b
			t.Cleanup(func() { pm.be = nil })
		}
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: x.p.DB, Engine: x.p.Engine, Tokens: x.p.Tokens, Platform: x.p}).Handler())
	defer srv.Close()
	call := func(token, method, path string, body io.Reader, into any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if into != nil {
			if b, ok := into.(*[]byte); ok {
				*b = raw
			} else {
				_ = json.Unmarshal(raw, into)
			}
		}
		return res.StatusCode
	}
	waitJob := func(id string) *ProjectJob {
		t.Helper()
		for i := 0; i < 200; i++ {
			var j ProjectJob
			if code := call(owner, "GET", "/v1/project-jobs/"+id, nil, &j); code != 200 {
				t.Fatalf("job get: %d", code)
			}
			if j.Status == JobDone || j.Status == JobFailed {
				return &j
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("job did not finish")
		return nil
	}

	// A read key cannot export (it would hand out everything).
	if code := call(reader, "POST", "/v1/projects/shop/exports", strings.NewReader(`{}`), nil); code != 403 {
		t.Fatalf("read key export: %d", code)
	}
	var ex ProjectExport
	if code := call(owner, "POST", "/v1/projects/shop/exports", strings.NewReader(`{}`), &ex); code != 200 || ex.Status != ExportPending {
		t.Fatalf("export create: %d %+v", code, ex)
	}
	var archive []byte
	if code := call(owner, "GET", ex.Download, nil, &archive); code != 200 || len(archive) == 0 {
		t.Fatalf("download: %d", code)
	}
	if code := call(owner, "GET", "/v1/projects/shop/exports/"+ex.ID, nil, &ex); code != 200 || ex.Status != ExportDone || ex.SizeBytes != int64(len(archive)) ||
		len(ex.Apps) != 3 || ex.SecretsNote == "" {
		t.Fatalf("export record: %d %+v", code, ex)
	}
	if code := call(owner, "GET", ex.Download, nil, nil); code != 409 {
		t.Fatalf("a second download: %d", code)
	}

	// Upload, then import under another name.
	var up ProjectJob
	if code := call(owner, "POST", "/v1/project-imports", bytes.NewReader(archive), &up); code != 200 || up.Status != JobUploaded ||
		up.Source == nil || !up.Source.Taken || !up.Source.SecretsHere || up.From != "shop" {
		t.Fatalf("upload: %d %+v", code, up)
	}
	if code := call(owner, "POST", "/v1/project-imports/"+up.ID+"/apply", strings.NewReader(`{}`), nil); code != 409 {
		t.Fatalf("import over an existing project: %d", code)
	}
	if code := call(reader, "POST", "/v1/project-imports/"+up.ID+"/apply", strings.NewReader(`{"name":"shop-2"}`), nil); code != 404 {
		t.Fatalf("someone else's upload: %d", code)
	}
	var job ProjectJob
	if code := call(owner, "POST", "/v1/project-imports/"+up.ID+"/apply", strings.NewReader(`{"name":"shop-2"}`), &job); code != 202 {
		t.Fatalf("apply: %d %+v", code, job)
	}
	if j := waitJob(job.ID); j.Status != JobDone || !j.Healthy || !j.Created || j.Project != "shop-2" || j.Change == "" {
		t.Fatalf("import job: %+v", j)
	}

	// Duplicate.
	if code := call(owner, "POST", "/v1/projects/shop/duplicate", strings.NewReader(`{"name":"shop-2"}`), nil); code != 409 {
		t.Fatalf("duplicate onto an existing name: %d", code)
	}
	if code := call(owner, "POST", "/v1/projects/shop/duplicate", strings.NewReader(`{"name":"shop-copy"}`), &job); code != 202 || job.Kind != JobDuplicate {
		t.Fatalf("duplicate: %d %+v", code, job)
	}
	if j := waitJob(job.ID); j.Status != JobDone || j.Project != "shop-copy" || len(j.Apps) != 3 {
		t.Fatalf("duplicate job: %+v", j)
	}

	// Stop and start are changes in History.
	var ar api.ApplyResult
	if code := call(owner, "POST", "/v1/projects/shop/stop", strings.NewReader(`{"intent":"Moved to box b"}`), &ar); code != 200 || !ar.Applied || ar.Change.Intent != "Moved to box b" {
		t.Fatalf("stop: %d %+v", code, ar)
	}
	if _, res, _ := x.p.DB.Load(ctx, "shop"); res[change.KindStopped].Spec == nil {
		t.Fatal("not stopped")
	}
	if code := call(owner, "POST", "/v1/projects/shop/stop", nil, &ar); code != 200 || ar.Applied {
		t.Fatalf("stop twice: %d %+v", code, ar)
	}
	if code := call(owner, "POST", "/v1/projects/shop/start", nil, &ar); code != 200 || !ar.Applied {
		t.Fatalf("start: %d %+v", code, ar)
	}
	if code := call(reader, "POST", "/v1/projects/shop/stop", nil, nil); code != 403 {
		t.Fatalf("read key stop: %d", code)
	}
}
