package edge

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/edge/switchboard"
)

// TestEdgeRestartHandsOverSockets: with its ports held outside the edge (as
// systemd's socket unit holds them), an edge restart under load loses no
// request: connections that arrive meanwhile wait for the next edge.
func TestEdgeRestartHandsOverSockets(t *testing.T) {
	dir := shortDir(t)
	_, port := appServer(t, "steady", nil)
	fds := map[string]int{}
	for _, p := range []int{18445, 18082} {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Fatal(err)
		}
		f, err := ln.(*net.TCPListener).File() // the socket stays open while f is
		ln.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		fds["tcp/"+strconv.Itoa(p)] = int(f.Fd())
	}
	start := func(sb string) (*Server, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		s := &Server{Socket: filepath.Join(dir, EdgeSocket), Control: filepath.Join(dir, ControlSocket), State: filepath.Join(dir, SnapshotFile), Switchboard: sb, fds: fds}
		if err := s.Start(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		stop := func() { cancel(); s.Wait() }
		t.Cleanup(stop)
		return s, stop
	}
	s1, stop1 := start("127.0.0.1:0")
	table := switchboard.Table{Hosts: map[string][]switchboard.Route{"shop.box.test": {{Env: "shop/web"}}},
		Envs: map[string]*switchboard.Env{"shop/web": {Project: "shop", App: "web", Live: "d1", Instances: []switchboard.Instance{{Name: "web-1", Port: port}}}}}
	cfg := Config{Domain: "box.test", Upstream: "127.0.0.1:9", DataDir: filepath.Join(dir, "caddy"), Internal: true, HTTPPort: 18082, HTTPSPort: 18445}
	cl := NewClient(ClientOptions{Socket: s1.Socket, Base: cfg, Local: true})
	cl.TableSource(func() switchboard.Table { return table })
	if err := cl.SetRoutes([]Route{{Host: "shop.box.test", Upstream: s1.SwitchboardAddr()}}); err != nil {
		t.Fatal(err)
	}
	ca, err := RootCAPEM(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		ok, fail int
		errs     []string
	)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		c := client(t, ca, cfg.HTTPSPort)
		c.Timeout = 20 * time.Second
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := c.Get("https://shop.box.test:18445/")
				good := err == nil && res.StatusCode == 200
				if err == nil {
					b, _ := io.ReadAll(res.Body)
					res.Body.Close()
					if !good {
						err = fmt.Errorf("%s %q", res.Status, b)
					}
				}
				mu.Lock()
				if good {
					ok++
				} else {
					fail++
					errs = append(errs, fmt.Sprint(err))
				}
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	stop1()
	start(s1.SwitchboardAddr())
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	if fail > 0 || ok < 50 {
		t.Fatalf("across the edge restart: %d ok, %d failed: %v", ok, fail, errs[:min(len(errs), 5)])
	}
	t.Logf("%d requests across the restart, none failed", ok)
}
