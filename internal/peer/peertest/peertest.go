// Package peertest runs HTTP servers in cgroups of their own, for tests of
// peer checks: a server in another cgroup is another project's app. They
// need Linux with cgroup v2, as root (the box; run them in a VM), and skip
// elsewhere.
package peertest

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

const envServe = "PEERTEST_SERVE" // "<addr> <body> <defer: 0 or 1>"

// Main serves, and never returns, in a process ServeIn started. Call it
// first in the package's TestMain.
func Main() {
	v := os.Getenv(envServe)
	if v == "" {
		return
	}
	var addr, body string
	var deferAccept int
	if _, err := fmt.Sscanf(v, "%s %s %d", &addr, &body, &deferAccept); err != nil {
		fmt.Fprintln(os.Stderr, "peertest:", err)
		os.Exit(2)
	}
	// The port may still be held a moment by sockets of a server that just
	// stopped.
	var ln net.Listener
	var err error
	for i := 0; i < 100; i++ {
		if ln, err = listen(addr, deferAccept == 1); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err == nil {
		err = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		}))
	}
	fmt.Fprintln(os.Stderr, "peertest:", err)
	os.Exit(1)
}
