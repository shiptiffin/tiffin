package runtime

import (
	"context"
	"strings"
	"testing"
)

// App containers and release tasks share the host network: they run
// without raw sockets, low ports or a way to gain privileges.
func TestHostNetworkContainersGiveUpCapabilities(t *testing.T) {
	n := &nerdctl{bin: "/bin/true"}
	for _, args := range [][]string{
		{"run", "--detach", "--network", "host"},
		{"run", "--rm", "--network", "host"},
	} {
		c := n.withSpec(context.Background(), args, RunSpec{Name: "tf.shop.web.prod.1", MemoryMB: 128})
		got := strings.Join(c.Args, " ")
		for _, want := range []string{"--cap-drop NET_RAW", "--cap-drop NET_BIND_SERVICE", "--security-opt no-new-privileges"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q lacks %q", got, want)
			}
		}
	}
}
