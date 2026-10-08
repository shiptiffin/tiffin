package auth

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/tokens"
)

// SolveAltcha solves the engine's proof-of-work challenge (SHA-256, cost 1):
// find the counter whose sha256(salt || nonce || uint32be(counter)) starts
// with keyPrefix. Returns the x-captcha-response header value.
func SolveAltcha(challenge json.RawMessage) (string, error) {
	var c struct {
		Parameters struct {
			Nonce     string `json:"nonce"`
			Salt      string `json:"salt"`
			KeyPrefix string `json:"keyPrefix"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(challenge, &c); err != nil {
		return "", err
	}
	nonce, _ := hex.DecodeString(c.Parameters.Nonce)
	salt, _ := hex.DecodeString(c.Parameters.Salt)
	buf := append(append([]byte{}, salt...), nonce...)
	buf = append(buf, 0, 0, 0, 0)
	for n := uint32(0); n < 10_000_000; n++ {
		binary.BigEndian.PutUint32(buf[len(buf)-4:], n)
		sum := sha256.Sum256(buf)
		h := hex.EncodeToString(sum[:])
		if strings.HasPrefix(h, c.Parameters.KeyPrefix) {
			payload, _ := json.Marshal(map[string]any{"challenge": challenge, "solution": map[string]any{"counter": n, "derivedKey": h}})
			return base64.StdEncoding.EncodeToString(payload), nil
		}
	}
	return "", fmt.Errorf("no solution")
}

// startEngine runs the real engine (bun) on a throwaway Postgres.
func startEngine(t *testing.T, configPath string) (dbURL string, port int, sock string) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed; the engine contract test needs it")
	}
	dir, _ := filepath.Abs("../../../packages/auth-engine")
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		t.Skip("packages/auth-engine dependencies not installed (bun install)")
	}
	sock = filepath.Join(os.TempDir(), fmt.Sprintf("tfa-%d.sock", time.Now().UnixNano()%1e9))
	cmd := exec.Command(bun, "test/fixture.ts", "--config", configPath, "--socket", sock)
	cmd.Dir = dir
	out, _ := cmd.StdoutPipe()
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		os.Remove(sock)
		if t.Failed() {
			t.Logf("engine stderr:\n%s", logs.String())
		}
	})
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "{\"databaseUrl\"") {
				line <- sc.Text()
			}
		}
	}()
	select {
	case l := <-line:
		var v struct {
			DatabaseURL string `json:"databaseUrl"`
			Port        int    `json:"port"`
		}
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatal(err)
		}
		return v.DatabaseURL, v.Port, sock
	case <-time.After(90 * time.Second):
		t.Fatalf("engine fixture didn't start:\n%s", logs.String())
	}
	return
}

// TestEngineContract drives the real engine through the module: reconcile
// writes the config and migrates; a person signs up over HTTP; the platform
// API lists, bans and deletes.
func TestEngineContract(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Postgres and the engine")
	}
	p := newPlatform(t)
	ctx := t.Context()
	// With a relay (real mail goes out) new users confirm their address.
	fakeRelay = true
	t.Cleanup(func() { fakeRelay = false })
	dbURL, port, sock := startEngine(t, ConfigPath(p))
	fakeDB["shop"] = dbURL
	t.Cleanup(func() { delete(fakeDB, "shop") })
	old := defaultEngine
	defaultEngine = newEngine(sock)
	t.Cleanup(func() { defaultEngine = old })

	plan := apply(t, p, shop)
	m := &Module{}
	// Auth waits for the database first.
	if err := m.Reconcile(ctx, p, "shop", "service/auth", json.RawMessage(`{"methods":["email"],"organizations":true}`)); err == nil || !strings.Contains(err.Error(), "waiting for the project's Postgres") {
		t.Fatalf("before postgres is ready: %v", err)
	}
	if err := p.DB.SetResourceStatus(ctx, "shop", "service/postgres", "ready", ""); err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Ops {
		if op.Address == "service/auth" {
			if err := m.Reconcile(ctx, p, "shop", op.Address, op.After); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
		}
	}
	// Idempotent.
	if err := m.Reconcile(ctx, p, "shop", "service/auth", json.RawMessage(`{"methods":["email","google","otp"],"organizations":true}`)); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if c := m.Checks(ctx, p); len(c) != 1 || !c[0].OK || c[0].Detail != "serving 1 project" {
		t.Fatalf("checks: %+v", c)
	}

	// A person signs up through the public endpoint (as the edge would send it).
	pub := fmt.Sprintf("http://127.0.0.1:%d/api/auth", port)
	do := func(method, path string, body any, hdr map[string]string) (int, []byte) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, pub+path, rd)
		req.Host = "shop.tiffin.localhost:8443" // the edge keeps the browser's Host
		req.Header.Set("Origin", "https://shop.tiffin.localhost:8443")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	st, ch := do("GET", "/altcha/challenge", nil, nil)
	if st != 200 {
		t.Fatalf("challenge: %d %s", st, ch)
	}
	sol, err := SolveAltcha(ch)
	if err != nil {
		t.Fatal(err)
	}
	st, body := do("POST", "/sign-up/email", map[string]any{"email": "ada@example.com", "password": "correct horse battery", "name": "Ada"}, map[string]string{"x-captcha-response": sol})
	if st != 200 {
		t.Fatalf("sign-up: %d %s", st, body)
	}
	// Removing sign-in would delete that one account; the plan says so.
	if l, err := m.EstimateLoss(ctx, p, "shop", change.Op{Action: change.Delete, Address: "service/auth"}); err != nil || l == nil ||
		l.Summarize().Summary != "1 user" {
		t.Fatalf("loss: %+v %v", l, err)
	}
	var mails []struct {
		To, Kind, Text string
	}
	if err := defaultEngine.do(ctx, "GET", "/outbox", nil, nil, &mails); err != nil || len(mails) == 0 {
		t.Fatalf("outbox: %v %v", mails, err)
	}
	link := regexp.MustCompile(`https://\S+`).FindString(mails[len(mails)-1].Text)
	if !strings.HasPrefix(link, "https://shop.tiffin.localhost:8443/api/auth/verify-email?token=") {
		t.Fatalf("verification link: %q", link)
	}

	// The platform API, as the dashboard and agents use it.
	tm := tokens.NewManager(p.DB)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: p.DB, Engine: p.Engine, Tokens: tm, Version: "test", Platform: p})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	call := func(method, path string, body any, out any) int {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
			}
		}
		return res.StatusCode
	}
	var ov Overview
	if st := call("GET", "/v1/projects/shop/auth", nil, &ov); st != 200 || ov.Stats.Users != 1 || ov.Endpoint != "https://shop.tiffin.localhost:8443/api/auth" {
		t.Fatalf("overview: %d %+v", st, ov)
	}
	if !ov.Organizations || ov.Social["google"] || len(ov.Hosts) != 3 {
		t.Fatalf("overview settings: %+v", ov)
	}
	var users UserList
	if st := call("GET", "/v1/projects/shop/auth/users?search=ADA", nil, &users); st != 200 || len(users.Items) != 1 || users.NextCursor != "" || users.Items[0].Email != "ada@example.com" {
		t.Fatalf("users: %d %+v", st, users)
	}
	id := users.Items[0].ID
	var detail UserDetail
	if st := call("GET", "/v1/projects/shop/auth/users/"+id, nil, &detail); st != 200 || len(detail.Memberships) != 1 || detail.Memberships[0].Role != "owner" || detail.Accounts[0].ProviderID != "credential" {
		t.Fatalf("detail: %d %+v", st, detail)
	}
	var orgs OrgList
	if st := call("GET", "/v1/projects/shop/auth/orgs", nil, &orgs); st != 200 || len(orgs.Items) != 1 || orgs.Items[0].Name != "Personal" {
		t.Fatalf("orgs: %d %+v", st, orgs)
	}
	var org OrgDetail
	if st := call("GET", "/v1/projects/shop/auth/orgs/"+orgs.Items[0].ID, nil, &org); st != 200 || len(org.Members) != 1 {
		t.Fatalf("org: %d %+v", st, org)
	}
	var ban BanResult
	if st := call("POST", "/v1/projects/shop/auth/users/"+id+"/ban", map[string]any{"reason": "testing"}, &ban); st != 200 || !ban.Banned {
		t.Fatalf("ban: %d %+v", st, ban)
	}
	if st := call("GET", "/v1/projects/shop/auth/users/nope", nil, &map[string]any{}); st != 404 {
		t.Fatalf("missing user: %d", st)
	}
	if st := call("GET", "/v1/projects/nope/auth", nil, &map[string]any{}); st != 404 {
		t.Fatalf("missing project: %d", st)
	}
	ev, _ := p.DB.AuditLog(ctx, 10)
	if len(ev) == 0 || ev[0].Action != "auth.user_ban" {
		t.Fatalf("audit: %+v", ev)
	}

	// Removing auth drops the schema and takes the project off the engine.
	apply(t, p, strings.Replace(shop, `,"auth":{"methods":["email","google","otp"]}`, "", 1))
	if err := m.Reconcile(ctx, p, "shop", "service/auth", nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := defaultEngine.do(ctx, "GET", "/projects/shop/stats", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "isn't set up") {
		t.Fatalf("after delete: %v", err)
	}
}
