package switchboard

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/peer/peertest"
)

func TestMain(m *testing.M) {
	peertest.Main()
	os.Exit(m.Run())
}

// An app's port held by a process of another project (it took the port
// while the app slept) gets no request: the switchboard answers 502
// instead of handing it the visitor's request, cookies and all.
func TestRequestsNeverReachAnotherProjectsProcess(t *testing.T) {
	victim, attacker := peertest.Cgroup(t), peertest.Cgroup(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	peertest.ServeIn(t, attacker, port, "stolen")
	get := func(owner string) (int, string) {
		b := New(nil, nil)
		b.Set(Table{
			Hosts: map[string][]Route{"app.test": {{Env: "p/a"}}},
			Envs:  map[string]*Env{"p/a": {Project: "p", App: "a", Live: "d1", Instances: []Instance{{Name: "p-a-1", Port: port}}, Cgroup: owner}},
		})
		srv := httptest.NewServer(b)
		defer srv.Close()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Host = "app.test"
		req.Header.Set("Cookie", "session=secret")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	if code, body := get(victim); code != http.StatusBadGateway || strings.Contains(body, "stolen") {
		t.Fatalf("the victim's request went to the other project's process: %d %q", code, body)
	}
	if code, body := get(attacker); code != http.StatusOK || body != "stolen" {
		t.Fatalf("its own project's process: %d %q", code, body)
	}
}
