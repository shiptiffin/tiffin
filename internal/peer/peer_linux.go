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
	diagReqLen   = 56 // struct inet_diag_req_v2
	diagMsgLen   = 72 // struct inet_diag_msg
	attrV6Only   = 11 // INET_DIAG_SKV6ONLY, u8
	attrCgroup   = 21 // INET_DIAG_CGROUP_ID (Linux 5.9+), u64
	stateSynRecv = 3  // TCP_SYN_RECV: a request socket, not accepted yet
	stateListen  = 10 // TCP_LISTEN
)

var (
	errNotYet    = errors.New("peer: the far end has not finished the handshake")
	errNoCgroup  = errors.New("peer: the kernel does not report socket cgroups (INET_DIAG_CGROUP_ID needs Linux 5.9 or later)")
	errNoListen  = errors.New("peer: nothing listens on the port")
	errDeferring = errors.New("peer: the far end defers accepting")
)

// check names the far end of c by the cgroup of the socket that accepted
// it. A server that defers accepting until data arrives (TCP_DEFER_ACCEPT:
// Bun does) has no accepted socket yet, only a request queued on its
// listener; then every socket listening on the port must be in dir. A
// listener another process takes over between connect and this check
// would have to vanish and be replaced by dir's within microseconds.
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
	var ids []uint64
	var err error
	// On loopback the far end usually completes the handshake before
	// connect returns, but the last ACK may still be in the backlog queue.
	for try := 0; try < 20; try++ {
		var id uint64
		if id, err = acceptor(local, remote); err == nil {
			ids = []uint64{id}
			break
		}
		if errors.Is(err, errDeferring) && try >= 2 {
			ids, err = listeners(remote)
			break
		}
		if !errors.Is(err, errNotYet) && !errors.Is(err, errDeferring) {
			break
		}
		time.Sleep(time.Duration(try+1) * 20 * time.Microsecond)
	}
	if errors.Is(err, errNotYet) {
		ids, err = listeners(remote)
	}
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !within(dir, id) {
			return &ForeignError{Addr: remote.String(), Want: dir}
		}
	}
	return nil
}

// acceptor returns the cgroup ID of the socket at the far end of the
// connection local → remote: the socket the server accepted (it inherits
// the listening socket's cgroup), found by its exact four-tuple.
func acceptor(local, remote *net.TCPAddr) (uint64, error) {
	family, ip := addrFamily(remote.IP)
	req := diagRequest(family, false, 0xffffffff)
	sid := req[unix.NLMSG_HDRLEN+8:] // struct inet_diag_sockid, from the server's side
	binary.BigEndian.PutUint16(sid[0:], uint16(remote.Port))
	binary.BigEndian.PutUint16(sid[2:], uint16(local.Port))
	copy(sid[4:20], ip(remote.IP))
	copy(sid[20:36], ip(local.IP))
	var id uint64
	err := errNotYet // no answer: not in the established table yet
	if derr := diag(req, func(d []byte) {
		switch {
		case d[1] == stateSynRecv:
			err = errDeferring
		default:
			if cg, ok := attrU64(d, attrCgroup); ok {
				id, err = cg, nil
			} else {
				err = errNoCgroup
			}
		}
	}); derr != nil {
		return 0, derr
	}
	return id, err
}

// listeners returns the cgroup IDs of every socket listening on remote's
// port that a connection to remote could reach.
func listeners(remote *net.TCPAddr) ([]uint64, error) {
	var ids []uint64
	v4 := remote.IP.To4() != nil
	var ferr error
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		if family == unix.AF_INET && !v4 {
			continue
		}
		if err := diag(diagRequest(family, true, 1<<stateListen), func(d []byte) {
			if int(binary.BigEndian.Uint16(d[4:])) != remote.Port {
				return
			}
			var addr net.IP
			if d[0] == unix.AF_INET {
				addr = net.IP(d[8:12])
			} else {
				addr = net.IP(d[8:24])
				if only, ok := attrU8(d, attrV6Only); v4 && ok && only != 0 {
					return // takes no IPv4 connections
				}
			}
			if !addr.IsUnspecified() && !addr.Equal(remote.IP) {
				return
			}
			cg, ok := attrU64(d, attrCgroup)
			if !ok {
				ferr = errNoCgroup
				return
			}
			ids = append(ids, cg)
		}); err != nil {
			return nil, err
		}
	}
	if ferr != nil {
		return nil, ferr
	}
	if len(ids) == 0 {
		return nil, errNoListen
	}
	return ids, nil
}

func addrFamily(a net.IP) (int, func(net.IP) []byte) {
	if a.To4() != nil {
		return unix.AF_INET, func(x net.IP) []byte { return x.To4() }
	}
	return unix.AF_INET6, func(x net.IP) []byte { return x.To16() }
}

// diagRequest is a SOCK_DIAG_BY_FAMILY request for TCP sockets of family
// in states (a bit per state), the whole table when dump.
func diagRequest(family int, dump bool, states uint32) []byte {
	req := make([]byte, unix.NLMSG_HDRLEN+diagReqLen)
	flags := uint16(unix.NLM_F_REQUEST)
	if dump {
		flags |= unix.NLM_F_DUMP
	}
	binary.NativeEndian.PutUint32(req[0:], uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], unix.SOCK_DIAG_BY_FAMILY)
	binary.NativeEndian.PutUint16(req[6:], flags)
	b := req[unix.NLMSG_HDRLEN:]
	b[0], b[1] = byte(family), unix.IPPROTO_TCP
	binary.NativeEndian.PutUint32(b[4:], states)
	binary.NativeEndian.PutUint32(b[8+40:], 0xffffffff) // INET_DIAG_NOCOOKIE
	binary.NativeEndian.PutUint32(b[8+44:], 0xffffffff)
	return req
}

// diag sends req and calls fn with every inet_diag_msg (and its
// attributes) the kernel answers. A socket it cannot find is no answer.
func diag(req []byte, fn func(msg []byte)) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_INET_DIAG)
	if err != nil {
		return fmt.Errorf("peer: sock_diag: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("peer: sock_diag: %w", err)
	}
	dump := binary.NativeEndian.Uint16(req[6:])&unix.NLM_F_DUMP != 0
	buf := make([]byte, 32<<10)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return fmt.Errorf("peer: sock_diag: %w", err)
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return fmt.Errorf("peer: sock_diag: %w", err)
		}
		for _, m := range msgs {
			switch m.Header.Type {
			case unix.NLMSG_DONE:
				return nil
			case unix.NLMSG_ERROR:
				if len(m.Data) >= 4 {
					if errno := -int32(binary.NativeEndian.Uint32(m.Data)); errno != 0 && errno != int32(unix.ENOENT) {
						return fmt.Errorf("peer: sock_diag: %w", syscall.Errno(errno))
					}
				}
				return nil
			case unix.SOCK_DIAG_BY_FAMILY:
				if len(m.Data) >= diagMsgLen {
					fn(m.Data)
				}
			}
		}
		if !dump {
			return nil
		}
	}
}

// attr finds attribute typ after an inet_diag_msg.
func attr(d []byte, typ uint16) ([]byte, bool) {
	for at := diagMsgLen; at+4 <= len(d); {
		l := int(binary.NativeEndian.Uint16(d[at:]))
		if l < 4 || at+l > len(d) {
			break
		}
		if binary.NativeEndian.Uint16(d[at+2:]) == typ {
			return d[at+4 : at+l], true
		}
		at += (l + 3) &^ 3
	}
	return nil, false
}

func attrU64(d []byte, typ uint16) (uint64, bool) {
	v, ok := attr(d, typ)
	if !ok || len(v) < 8 {
		return 0, false
	}
	return binary.NativeEndian.Uint64(v), true
}

func attrU8(d []byte, typ uint16) (uint8, bool) {
	v, ok := attr(d, typ)
	if !ok || len(v) < 1 {
		return 0, false
	}
	return v[0], true
}
