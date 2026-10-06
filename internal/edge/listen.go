package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"syscall"
)

// Socket activation: on the box, systemd holds the edge's ports
// (tiffin-edge.socket) and passes them to the edge process. When the edge
// restarts, the listening sockets stay open, so connections that arrive
// meanwhile wait in the kernel's queue for the next edge to accept them
// instead of being refused; requests under way finish in the old one.
// SO_REUSEPORT would let two edges overlap, but a closing listener drops
// the connections queued on it; passing sockets between processes needs a
// supervisor that knows the hand-off. systemd already is one.

// activationFDs maps the sockets systemd passed (LISTEN_FDS, from fd 3) to
// "tcp/<port>" or "udp/<port>". None: the edge listens itself.
func activationFDs() (map[string]int, error) {
	n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if n == 0 || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_PID")
	os.Unsetenv("LISTEN_FDNAMES")
	out := map[string]int{}
	for fd := 3; fd < 3+n; fd++ {
		syscall.CloseOnExec(fd)
		sa, err := syscall.Getsockname(fd)
		if err != nil {
			return nil, fmt.Errorf("edge: socket %d from systemd: %w", fd, err)
		}
		typ, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
		if err != nil {
			return nil, fmt.Errorf("edge: socket %d from systemd: %w", fd, err)
		}
		var port int
		switch a := sa.(type) {
		case *syscall.SockaddrInet4:
			port = a.Port
		case *syscall.SockaddrInet6:
			port = a.Port
		default:
			continue
		}
		proto := "tcp"
		if typ == syscall.SOCK_DGRAM {
			proto = "udp"
		}
		out[proto+"/"+strconv.Itoa(port)] = fd
	}
	return out, nil
}

// relisten rewrites the listen addresses of every server in a Caddy config:
// to maps one address (":443") and the server's protocols to its new
// addresses, each with its protocols (listen_protocols).
func relisten(raw json.RawMessage, to func(addr string, protos []any) (addrs []string, perAddr [][]any)) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	apps, _ := cfg["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	for _, v := range servers {
		srv, _ := v.(map[string]any)
		listen, _ := srv["listen"].([]any)
		protos := []any{"h1", "h2", "h3"} // Caddy's default
		if p, ok := srv["protocols"].([]any); ok {
			protos = p
		}
		var addrs, perAddr []any
		for _, l := range listen {
			addr, _ := l.(string)
			as, ps := to(addr, protos)
			for i := range as {
				addrs, perAddr = append(addrs, as[i]), append(perAddr, ps[i])
			}
		}
		srv["listen"], srv["listen_protocols"] = addrs, perAddr
	}
	return json.Marshal(cfg)
}

// streamProtos are protos without HTTP/3 (which needs UDP).
func streamProtos(protos []any) []any {
	var out []any
	for _, p := range protos {
		if p != "h3" {
			out = append(out, p)
		}
	}
	return out
}

// listeners points a Caddy config's servers at the sockets systemd passed:
// ":443" becomes fd/N, plus fdgram/M for HTTP/3 when a UDP socket on that
// port was passed (HTTP/3 is left out otherwise). Ports without a passed
// socket are listened on as before.
func (s *Server) listeners(raw json.RawMessage) (json.RawMessage, error) {
	if len(s.fds) == 0 || raw == nil {
		return raw, nil
	}
	return relisten(raw, func(addr string, protos []any) ([]string, [][]any) {
		_, port, err := net.SplitHostPort(addr)
		fd, ok := s.fds["tcp/"+port]
		if err != nil || !ok {
			return []string{addr}, [][]any{protos}
		}
		addrs, per := []string{"fd/" + strconv.Itoa(fd)}, [][]any{streamProtos(protos)}
		if ufd, ok := s.fds["udp/"+port]; ok && slices.Contains(protos, any("h3")) {
			addrs, per = append(addrs, "fdgram/"+strconv.Itoa(ufd)), append(per, []any{"h3"})
		}
		return addrs, per
	})
}

// warm loads r on loopback ports of its own before the edge's first config
// goes onto the sockets systemd passed. Caddy serves as soon as it loads a
// config but loads certificates from storage only after, and connections
// already waiting on those sockets (an edge restart) would fail their TLS
// handshake in between. Certificates stay loaded across the next load.
func (s *Server) warm(ctx context.Context, r Rendered) error {
	ports := map[string]string{}
	var err error
	move := func(addr string, protos []any) ([]string, [][]any) {
		_, port, perr := net.SplitHostPort(addr)
		if perr != nil {
			return []string{addr}, [][]any{protos}
		}
		if ports[port] == "" {
			ln, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				err = lerr
				return []string{addr}, [][]any{protos}
			}
			ports[port] = ln.Addr().String()
			ln.Close()
		}
		return []string{ports[port]}, [][]any{streamProtos(protos)}
	}
	if r.Config, err = relisten(r.Config, move); err != nil {
		return err
	}
	if r.Fallback != nil {
		if r.Fallback, err = relisten(r.Fallback, move); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(r.Probe.Addr)
	r.Probe.Addr = ports[port]
	_, err = r.load(ctx)
	return err
}
