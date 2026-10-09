package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shiptiffin/tiffin/internal/cli"
)

func runCLI(t *testing.T, env map[string]string, args ...string) (int, []byte, string) {
	t.Helper()
	var out, errb bytes.Buffer
	no := false
	code := cli.Execute(context.Background(), args, cli.IO{Out: &out, Err: &errb, TTY: &no, In: strings.NewReader(""),
		Env: func(k string) string { return env[k] }})
	return code, out.Bytes(), errb.String()
}

// Array flags whose items are not strings take JSON, and numbers reach the
// box (and come back) exactly as written: a comma-separated --members sent
// strings where the API wants objects, and float64 rounded 2^53+1.
func TestFlagsSendTheRightJSON(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9007199254740993}`))
	}))
	defer srv.Close()
	env := map[string]string{"TIFFIN_URL": srv.URL, "TIFFIN_TOKEN": "tfn_x", "TIFFIN_HOME": filepath.Join(t.TempDir(), "unused")}

	code, out, errs := runCLI(t, env, "kv", "zset", "add", "shop", "--key", "scores", "--members", `[{"member":"alice","score":1}]`)
	if code != cli.ExitOK || !strings.Contains(bodies["/v1/projects/shop/kv/zset/add"], `"members":[{"member":"alice","score":1}]`) {
		t.Fatalf("zset add: %d %s %s: sent %s", code, out, errs, bodies["/v1/projects/shop/kv/zset/add"])
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("the answer's number changed: %s", out)
	}
	if code, out, errs := runCLI(t, env, "kv", "zset", "add", "shop", "--key", "scores", "--members", "alice"); code != cli.ExitInvalid || !strings.Contains(string(out)+errs, "JSON array") {
		t.Fatalf("a non-JSON --members: %d %s %s", code, out, errs)
	}
	code, _, errs = runCLI(t, env, "sql", "shop", "select $1", "--params", `[9007199254740993,true,null]`)
	if sent := bodies["/v1/projects/shop/sql"]; code != cli.ExitOK || !strings.Contains(sent, `"params":[9007199254740993,true,null]`) {
		t.Fatalf("sql params: %d %s: sent %s", code, errs, sent)
	}
	code, _, errs = runCLI(t, env, "sql", "shop", "--body", `{"sql":"select $1","params":[9007199254740993]}`)
	if sent := bodies["/v1/projects/shop/sql"]; code != cli.ExitOK || !strings.Contains(sent, `[9007199254740993]`) {
		t.Fatalf("--body: %d %s: sent %s", code, errs, sent)
	}
	// String arrays stay comma-separated.
	code, _, errs = runCLI(t, env, "kv", "set", "add", "shop", "--key", "tags", "--members", "a,b")
	if sent := bodies["/v1/projects/shop/kv/set/add"]; code != cli.ExitOK || !strings.Contains(sent, `"members":["a","b"]`) {
		t.Fatalf("set add: %d %s: sent %s", code, errs, sent)
	}
}
