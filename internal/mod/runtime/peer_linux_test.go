package runtime

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/peer/peertest"
)

func TestMain(m *testing.M) {
	peertest.Main()
	os.Exit(m.Run())
}

// Another project's app takes a sleeping app's port and answers on it. The
// wake must not take it for the app: its health check sees who answers,
// the kept containers are replaced by new ones on other ports, and no
// request (or job) of the app reaches the other process.
func TestWakeIgnoresAPortAnotherProjectTook(t *testing.T) {
	attacker := peertest.Cgroup(t)
	self := peertest.Self(t) // the fake engine's containers serve in this process
	h := newHarness(t)
	h.r.opt.PeerCgroup = func(project string) string {
		if project == "shop" {
			return self
		}
		return attacker
	}
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	st := h.asleep("api")
	taken := st.Parked[0].Port
	peertest.ServeIn(t, attacker, taken, "stolen")
	for i := 0; i < 6; i++ {
		code, body := h.get("shop.tiffin.localhost", "/api/x")
		if code != 200 || strings.Contains(body, "stolen") {
			t.Fatalf("request %d: %d %q", i, code, body)
		}
	}
	st = h.state("api", "")
	for _, in := range st.Instances {
		if in.Port == taken {
			t.Fatalf("instance %s still on the taken port %d", in.Name, taken)
		}
	}
	// Deliveries (the queue's dialer) are checked the same way.
	if _, err := h.m.DialApp(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(taken)); err == nil {
		t.Fatal("dialed a port no instance of the app holds")
	}
	c, err := h.m.DialApp(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(st.Instances[0].Port))
	if err != nil {
		t.Fatalf("dial the app: %v", err)
	}
	c.Close()
}
