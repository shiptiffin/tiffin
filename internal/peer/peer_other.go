//go:build !linux

package peer

import "net"

func check(net.Conn, string) error { return nil }
