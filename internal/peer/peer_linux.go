package peer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Kernel ABI (linux/inet_diag.h, linux/sock_diag.h).
const (
	diagReqLen  = 56 // struct inet_diag_req_v2
	diagMsgLen  = 72 // struct inet_diag_msg
	attrCgroup  = 21 // INET_DIAG_CGROUP_ID (Linux 5.9+), u64
	stateSynRcv = 3  // TCP_SYN_RECV: a request socket, not accepted yet
)

var (
	errNotYet   = errors.New("peer: the far end has not finished the handshake")
	errNoCgroup = errors.New("peer: the kernel does not report socket cgroups (INET_DIAG_CGROUP_ID needs Linux 5.9 or later)")
)

func check(c net.Conn, dir string) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("peer: %T is not a TCP connection", c)
	}
	local, _ := tc.LocalAddr().(*net.TCPAddr)
	remote, _ := tc.RemoteAddr().(*net.TCPAddr)
	if local == nil || remote == nil {
		return errors.New("peer: connection without addresses")
	}
	var id uint64
	var err error
	// On loopback the far end usually completes the handshake before
	// connect returns, but the last ACK may still be in the backlog queue.
	for try := 0; try < 100; try++ {
		if id, err = acceptor(local, remote); !errors.Is(err, errNotYet) {
			break
		}
		time.Sleep(time.Duration(try+1) * 20 * time.Microsecond)
	}
	if err != nil {
		return err
	}
	if !within(dir, id) {
		return &ForeignError{Addr: remote.String(), Want: dir}
	}
	return nil
}

// acceptor returns the cgroup ID of the socket at the far end of the
// connection local → remote: the socket the server accepted (it inherits
// the listening socket's cgroup), found by its exact four-tuple.
func acceptor(local, remote *net.TCPAddr) (uint64, error) {
	family, size := unix.AF_INET, 4
	if remote.IP.To4() == nil {
		family, size = unix.AF_INET6, 16
	}
	ip := func(a net.IP) []byte {
		if size == 4 {
			return a.To4()
		}
		return a.To16()
	}
	req := make([]byte, unix.NLMSG_HDRLEN+diagReqLen)
	binary.NativeEndian.PutUint32(req[0:], uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], unix.SOCK_DIAG_BY_FAMILY)
	binary.NativeEndian.PutUint16(req[6:], unix.NLM_F_REQUEST)
	b := req[unix.NLMSG_HDRLEN:]
	b[0], b[1] = byte(family), unix.IPPROTO_TCP
	binary.NativeEndian.PutUint32(b[4:], 0xffffffff) // every state
	sid := b[8:]                                     // struct inet_diag_sockid, from the server's side
	binary.BigEndian.PutUint16(sid[0:], uint16(remote.Port))
	binary.BigEndian.PutUint16(sid[2:], uint16(local.Port))
	copy(sid[4:20], ip(remote.IP))
	copy(sid[20:36], ip(local.IP))
	binary.NativeEndian.PutUint32(sid[40:], 0xffffffff) // INET_DIAG_NOCOOKIE
	binary.NativeEndian.PutUint32(sid[44:], 0xffffffff)

	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_INET_DIAG)
	if err != nil {
		return 0, fmt.Errorf("peer: sock_diag: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("peer: sock_diag: %w", err)
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return 0, fmt.Errorf("peer: sock_diag: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return 0, fmt.Errorf("peer: sock_diag: %w", err)
	}
	for _, m := range msgs {
		switch m.Header.Type {
		case unix.NLMSG_ERROR:
			if len(m.Data) >= 4 {
				if errno := -int32(binary.NativeEndian.Uint32(m.Data)); errno == int32(unix.ENOENT) {
					return 0, errNotYet // not in the established table yet
				} else if errno != 0 {
					return 0, fmt.Errorf("peer: sock_diag: %w", syscall.Errno(errno))
				}
			}
			continue
		case unix.SOCK_DIAG_BY_FAMILY:
		default:
			continue
		}
		d := m.Data
		if len(d) < diagMsgLen {
			return 0, errors.New("peer: sock_diag: short answer")
		}
		if d[1] == stateSynRcv {
			return 0, errNotYet
		}
		for at := diagMsgLen; at+4 <= len(d); {
			l := int(binary.NativeEndian.Uint16(d[at:]))
			if l < 4 || at+l > len(d) {
				break
			}
			if binary.NativeEndian.Uint16(d[at+2:]) == attrCgroup && l >= 12 {
				return binary.NativeEndian.Uint64(d[at+4:]), nil
			}
			at += (l + 3) &^ 3
		}
		return 0, errNoCgroup
	}
	return 0, errNotYet
}
