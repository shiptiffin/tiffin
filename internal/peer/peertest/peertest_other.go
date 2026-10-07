//go:build !linux

package peertest

import (
	"net"
	"testing"
)

func listen(addr string, _ bool) (net.Listener, error) { return net.Listen("tcp", addr) }

// Cgroup skips the test: cgroups are Linux's.
func Cgroup(t testing.TB) string { t.Skip("cgroups need Linux"); return "" }

// Self skips the test: cgroups are Linux's.
func Self(t testing.TB) string { t.Skip("cgroups need Linux"); return "" }

// ServeIn skips the test: cgroups are Linux's.
func ServeIn(t testing.TB, _ string, _ int, _ string) { t.Skip("cgroups need Linux") }

// ServeDeferredIn skips the test: cgroups are Linux's.
func ServeDeferredIn(t testing.TB, _ string, _ int, _ string) { t.Skip("cgroups need Linux") }
