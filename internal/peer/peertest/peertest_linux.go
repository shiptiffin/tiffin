package peertest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cgroup makes a new cgroup for the test, removed at its end. It skips the
// test where cgroups cannot be made.
func Cgroup(t testing.TB) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to make cgroups")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("needs cgroup v2")
	}
	dir, err := os.MkdirTemp("/sys/fs/cgroup", "peertest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 100 && os.Remove(dir) != nil; i++ {
			time.Sleep(20 * time.Millisecond) // its processes are still exiting
		}
	})
	return dir
}

// Self is the cgroup directory this process runs in.
func Self(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip(err)
	}
	return filepath.Join("/sys/fs/cgroup", strings.TrimSpace(strings.TrimPrefix(string(b), "0::")))
}

// ServeIn starts a process in cgroup dir that answers every HTTP request on
// 127.0.0.1:port with body (no spaces), until the test ends. It returns
// once the server answers.
func ServeIn(t testing.TB, dir string, port int, body string) {
	t.Helper()
	cg, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cg.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envServe+"="+addr+" "+body)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.Fd())}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 500; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peertest: nothing listens on %s", addr)
}
