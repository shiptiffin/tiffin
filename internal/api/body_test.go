package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

func bodyTestAPI(t *testing.T) (*API, *tokens.Manager, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return New(Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: "test"}), tm, owner
}

func post(t *testing.T, a *API, token, path, body string) (int, *Problem, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var p Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec.Code, &p, rec.Body.String()
}

// Numbers in free-form JSON (SQL params, job payloads) arrive exactly as
// sent: decoded as float64, 9007199254740993 became 9007199254740992.
func TestBodyNumbersKeepPrecision(t *testing.T) {
	a, _, owner := bodyTestAPI(t)
	var got []any
	huma.Register(a.api, op("echo-params", http.MethodPost, "/v1/echo", "-", RiskRead, "Echo", "Echo.", "system"),
		func(ctx context.Context, in *struct {
			Body struct {
				Params []any          `json:"params"`
				N      int64          `json:"n" maximum:"9223372036854775807"`
				Free   map[string]any `json:"free,omitempty"`
			}
		}) (*struct{ Body map[string]any }, error) {
			got = in.Body.Params
			return &struct{ Body map[string]any }{map[string]any{"params": in.Body.Params, "n": in.Body.N, "free": in.Body.Free}}, nil
		})
	code, _, raw := post(t, a, owner, "/v1/echo", `{"params":[9007199254740993,1.5,"x",true,null],"n":9007199254740993,"free":{"id":9007199254740993}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if n, ok := got[0].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Fatalf("params[0] = %#v", got[0])
	}
	if strings.Count(raw, "9007199254740993") != 3 {
		t.Fatalf("a number changed on the way: %s", raw)
	}
	// Trailing data is still refused, as json.Unmarshal does.
	if code, _, raw := post(t, a, owner, "/v1/echo", `{"params":[]} {}`); code != 400 {
		t.Fatalf("trailing data: %d %s", code, raw)
	}
}

// A small body of many invalid values is refused before huma validates each
// one (it keeps every error), and a problem lists a bounded number of errors.
func TestBodyValueBudget(t *testing.T) {
	a, _, owner := bodyTestAPI(t)
	var runs atomic.Int32
	o := op("fields-add", http.MethodPost, "/v1/fields", "-", RiskWrite, "Add", "Add.", "system")
	o.MaxBodyBytes = 8 << 20
	huma.Register(a.api, o, func(ctx context.Context, in *struct {
		Body struct {
			Fields []struct {
				Field string `json:"field"`
				Value string `json:"value"`
			} `json:"fields"`
		}
	}) (*struct{}, error) {
		runs.Add(1)
		return &struct{}{}, nil
	})
	flood := `{"fields":[` + strings.Repeat(`{},`, MaxJSONValues) + `{}]}`
	code, p, _ := post(t, a, owner, "/v1/fields", flood)
	if code != 400 || len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Message, "values") || runs.Load() != 0 {
		t.Fatalf("flood: %d %+v", code, p.Errors)
	}
	some := `{"fields":[` + strings.Repeat(`{},`, 999) + `{}]}`
	code, p, _ = post(t, a, owner, "/v1/fields", some)
	if code != 422 || len(p.Errors) != maxProblemErrors+1 || !strings.Contains(p.Errors[maxProblemErrors].Message, "more") {
		t.Fatalf("1000 bad items: %d, %d errors", code, len(p.Errors))
	}
	if n := jsonValues([]byte(`{"a":"[,{","b":[1,2,{"c":"\"],"}]}`), 100); n != 6 {
		t.Fatalf("jsonValues counted %d", n)
	}
}

// A key that cannot reach a project is refused before the body is read.
func TestProjectAccessBeforeBody(t *testing.T) {
	a, tm, owner := bodyTestAPI(t)
	ctx := context.Background()
	ownerP, _ := tm.Authenticate(ctx, owner)
	other, _, err := tm.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "shop-only", Projects: tokens.Projects{"shop"}, Access: tokens.LevelFull})
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	huma.Register(a.api, op("thing-put", http.MethodPost, "/v1/projects/{project}/things", "-", RiskWrite, "Put", "Put.", "system"),
		func(ctx context.Context, in *struct {
			Project string `path:"project"`
			Body    struct {
				X int `json:"x"`
			}
		}) (*struct{}, error) {
			runs.Add(1)
			return &struct{}{}, nil
		})
	if code, _, raw := post(t, a, other, "/v1/projects/shop/things", `{"x":1}`); code != 204 || runs.Load() != 1 {
		t.Fatalf("own project: %d %s", code, raw)
	}
	code, p, _ := post(t, a, other, "/v1/projects/blog/things", `not json at all`)
	if code != 403 || p.Code != "forbidden" || runs.Load() != 1 {
		t.Fatalf("other project: %d %+v", code, p)
	}
}

// A raw upload that stops sending is cut off, instead of holding its
// handler and connection forever; one that keeps sending is not, and work
// after the upload is not cut short by the deadline.
func TestUploadBodyStall(t *testing.T) {
	defer func(w time.Duration, n int64) { uploadWindow, uploadMinBytes = w, n }(uploadWindow, uploadMinBytes)
	uploadWindow, uploadMinBytes = 300*time.Millisecond, 4
	a, _, owner := bodyTestAPI(t)
	result := make(chan error, 4)
	o := op("raw-up", http.MethodPost, "/v1/raw", "-", RiskWrite, "Upload", "Upload.", "system")
	o.RequestBody = &huma.RequestBody{Content: map[string]*huma.MediaType{"application/octet-stream": {}}}
	huma.Register(a.api, o, func(ctx context.Context, in *rawUpload) (*struct{ Body string }, error) {
		_, err := io.Copy(io.Discard, in.body)
		if err == nil {
			time.Sleep(2 * uploadWindow) // work after the upload
			err = ctx.Err()
		}
		result <- err
		return &struct{ Body string }{"ok"}, nil
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	send := func(chunks int, gap time.Duration) {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < chunks; i++ {
				_, _ = pw.Write([]byte("chunk-of-bytes"))
				time.Sleep(gap)
			}
			pw.Close()
		}()
		req, _ := http.NewRequest("POST", srv.URL+"/v1/raw", pr)
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Content-Type", "application/octet-stream")
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}
	go send(1, 3*time.Second) // one chunk, then nothing for 10 windows
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a stalled upload was read to the end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled upload was not cut off")
	}
	send(6, uploadWindow/3) // slow but steady: 6 chunks over 2 windows
	if err := <-result; err != nil {
		t.Fatalf("a steady upload: %v", err)
	}
}

type rawUpload struct{ body io.Reader }

func (u *rawUpload) Resolve(ctx huma.Context) []error {
	u.body = UploadBody(ctx)
	return nil
}
