package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/peer/peertest"
)

func TestMain(m *testing.M) {
	peertest.Main()
	os.Exit(m.Run())
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// The far end of a connection is named by the cgroup of the process that
// listens: one in another cgroup (another project's app holding the port)
// is refused; one in a cgroup below the owner (a container's scope in its
// project's slice) is the owner's.
func TestCheckNamesTheListenersCgroup(t *testing.T) {
	project, other := peertest.Cgroup(t), peertest.Cgroup(t)
	scope := filepath.Join(project, "app.scope")
	if err := os.Mkdir(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scope) })
	port := freePort(t)
	peertest.ServeIn(t, scope, port, "hello")
	target := fmt.Sprintf("127.0.0.1:%d", port)
	c, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := Check(c, project); err != nil {
		t.Fatalf("its own project's slice: %v", err)
	}
	var fe *ForeignError
	if err := Check(c, other); !errors.As(err, &fe) {
		t.Fatalf("another project's slice: %v, want a ForeignError", err)
	}
	d := &Dialer{Owner: func(int) (string, bool) { return other, true }}
	if _, err := d.DialContext(context.Background(), "tcp", target); !errors.As(err, &fe) {
		t.Fatalf("dial for another project: %v, want a ForeignError", err)
	}
	c2, err := d.DialContext(WithOwner(context.Background(), project), "tcp", target)
	if err != nil {
		t.Fatalf("dial for its project: %v", err)
	}
	c2.Close()
}

func BenchmarkCheck(b *testing.B) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	dir := peertest.Self(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Check(c, dir); err != nil {
			b.Fatal(err)
		}
	}
}

// A server that defers accepting until data arrives (Bun does) has no
// accepted socket when the check runs: its listeners are checked instead.
func TestCheckADeferringServer(t *testing.T) {
	project, other := peertest.Cgroup(t), peertest.Cgroup(t)
	port := freePort(t)
	peertest.ServeDeferredIn(t, project, port, "hello")
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := Check(c, project); err != nil {
		t.Fatalf("its own project's slice: %v", err)
	}
	var fe *ForeignError
	if err := Check(c, other); !errors.As(err, &fe) {
		t.Fatalf("another project's slice: %v, want a ForeignError", err)
	}
}
