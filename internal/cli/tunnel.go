package cli

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/btahir/tiffin/internal/provider/lima"
	"github.com/btahir/tiffin/internal/provider/remote"
	"github.com/spf13/cobra"
)

// A tunnel's two kinds: the project's database and its KV store. Both
// listen on the box's 127.0.0.1 only; the tunnel forwards a local port to
// them over the SSH access `tiffin up` already set up.
type tunnelKind struct {
	what, op, field, flagNote string
	port                      int
}

var tunnelKinds = map[string]tunnelKind{
	"db": {what: "database", op: "postgres/connection", field: "databaseUrl", port: 15432,
		flagNote: "psql, TablePlus or a local app"},
	"kv": {what: "KV store", op: "kv/connection?reveal=true", field: "redisUrl", port: 16379,
		flagNote: "redis-cli, a GUI or a local app"},
}

func (a *app) tunnelCmd(kind string) *cobra.Command {
	k := tunnelKinds[kind]
	var port int
	var branch string
	cmd := &cobra.Command{
		Use:   "tunnel <project>",
		Short: "Reach a project's " + k.what + " from this computer over SSH",
		Long: "Forwards localhost:" + strconv.Itoa(k.port) + " to the project's " + k.what + " on the current box, over the box's SSH access " +
			"(a server box's key, or the Lima VM's for a local box), and prints the URL to use with " + k.flagNote + ". " +
			"The URL carries the project's password, so it needs a key with full access to the project; every reveal is recorded. " +
			"Runs until Ctrl-C.",
		Example: "  tiffin " + kind + " tunnel shop\n  tiffin " + kind + " tunnel shop --port " + strconv.Itoa(k.port+1),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			project := args[0]
			if port < 1 || port > 65535 {
				return &exitError{ExitInvalid, "--port must be 1-65535"}
			}
			name, bx := a.currentBox()
			if a.url != "" || a.homeExplicit || bx == nil {
				return &exitError{ExitInvalid, "a tunnel reuses the SSH access of a box made with `tiffin up` on this computer; there is none selected here"}
			}
			c, err := a.client(ctx)
			if err != nil {
				return err
			}
			defer c.close()
			path := "/v1/projects/" + url.PathEscape(project) + "/" + k.op
			if branch != "" {
				path += "?branch=" + url.QueryEscape(branch)
			}
			var conn map[string]any
			if err := a.getJSON(ctx, c, path, &conn); err != nil {
				return err
			}
			boxURL, _ := conn[k.field].(string)
			local, remoteAddr, err := tunnelURL(boxURL, port)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			sshArgs, err := tunnelSSH(bx, port, remoteAddr, limaHome(a.io.Env), lima.New().Instance)
			if err != nil {
				return &exitError{ExitInvalid, err.Error()}
			}
			// Say so plainly when the port is taken, before ssh says it less plainly.
			l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return &exitError{ExitError, fmt.Sprintf("port %d is in use on this computer; pick another with --port", port)}
			}
			l.Close()

			if a.tty() {
				fmt.Fprintf(a.io.Out, "Forwarding localhost:%d to %s's %s on %s. Ctrl-C closes it.\n\n  %s\n\n", port, project, k.what, name, local)
				fmt.Fprintln(a.io.Out, a.paint("The URL carries the password: keep it out of shared screens and files.", dim))
			} else {
				writeJSON(a.io.Out, map[string]any{"project": project, "box": name, "url": local, "localPort": port, "remote": remoteAddr, "branch": branch})
			}
			// Ctrl-C reaches ssh too (same process group); either way it ends here.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			ssh := exec.CommandContext(ctx, "ssh", sshArgs...)
			ssh.Stdout, ssh.Stderr = a.io.Err, a.io.Err
			if err := ssh.Run(); err != nil && ctx.Err() == nil {
				return &exitError{ExitError, "the tunnel closed: " + err.Error()}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", k.port, "local port to listen on (127.0.0.1)")
	if kind == "db" {
		cmd.Flags().StringVar(&branch, "branch", "", "a database branch (e.g. pr-12) instead of the main database")
	}
	return cmd
}

// tunnelURL turns the box's connection URL into the one to use on this
// computer (the same credentials, at 127.0.0.1:port) and returns the box-side
// address to forward to.
func tunnelURL(boxURL string, port int) (local, remoteAddr string, err error) {
	u, err := url.Parse(boxURL)
	if err != nil || u.Host == "" || u.User == nil {
		return "", "", fmt.Errorf("the box sent no usable connection URL")
	}
	host, rport, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", "", fmt.Errorf("the box's connection URL has no port: %s", u.Redacted())
	}
	u.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	return u.String(), net.JoinHostPort(host, rport), nil
}

// tunnelSSH is the ssh command line that forwards 127.0.0.1:localPort here
// to remoteAddr on the box: with the server's key and pinned host key, or
// through the Lima VM's own ssh config for a local box.
func tunnelSSH(bx *boxConfig, localPort int, remoteAddr, limaHome, instance string) ([]string, error) {
	fwd := []string{
		"-N", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", "-o", "LogLevel=ERROR",
		"-L", "127.0.0.1:" + strconv.Itoa(localPort) + ":" + remoteAddr,
	}
	if bx.Server != nil {
		t, err := remote.ParseTarget(bx.Server.SSH)
		if err != nil {
			return nil, err
		}
		a := append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-p", strconv.Itoa(t.Port)}, fwd...)
		if bx.Server.KnownHosts != "" {
			a = append(a, "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile="+bx.Server.KnownHosts, "-o", "GlobalKnownHostsFile=/dev/null")
		}
		if bx.Server.Identity != "" {
			a = append(a, "-i", bx.Server.Identity, "-o", "IdentitiesOnly=yes")
		}
		return append(a, "--", t.User+"@"+t.Host), nil
	}
	if bx.Provider != "local" {
		return nil, fmt.Errorf("this box (%s) has no SSH access Tiffin knows of", orDefault(bx.Provider, "unknown provider"))
	}
	cfg := filepath.Join(limaHome, instance, "ssh.config")
	return append(append([]string{"-F", cfg}, fwd...), "--", "lima-"+instance), nil
}

// limaHome is where Lima keeps its VMs ($LIMA_HOME, else ~/.lima).
func limaHome(env func(string) string) string {
	if h := env("LIMA_HOME"); h != "" {
		return h
	}
	home := env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".lima")
}
