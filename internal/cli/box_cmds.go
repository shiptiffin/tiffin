package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/lima"
	"github.com/spf13/cobra"
)

const localDomain = "tiffin.localhost"

func (a *app) progress(msg string) {
	if a.tty() {
		fmt.Fprintf(a.io.Err, "%s %s\n", a.paint("·", dim), msg)
	}
}

func (a *app) upCmd() *cobra.Command {
	var prov, binary string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create (or update) your box",
		Long: "Creates the box if it does not exist, installs or updates Tiffin on it, and checks it answers over HTTPS. " +
			"Safe to run again: it converges. An unhealthy update rolls back automatically.\n\n" +
			"Providers: local (a Lima VM on this computer). Hetzner comes later.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if prov != "local" {
				return &exitError{ExitInvalid, "only --provider local is available so far"}
			}
			start := time.Now()
			p := lima.New()
			if err := lima.Available(); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			bin, err := a.linuxBinary(ctx, binary)
			if err != nil {
				return err
			}
			m, err := p.Up(ctx, a.progress)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			opts := install.Options{Domain: localDomain, HTTPSPort: lima.HTTPSPort, HTTPPort: 8080, PublicPort: p.HostPort()}
			res, err := install.Install(ctx, m, bin, opts, a.progress)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}

			// Remember the box, then talk to it the way every client will: over HTTPS.
			dir := filepath.Join(a.configDir(), "boxes", "local")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			caFile := filepath.Join(dir, "ca.crt")
			if err := os.WriteFile(caFile, res.CAPEM, 0o644); err != nil {
				return err
			}
			f, err := a.loadBoxes()
			if err != nil {
				return err
			}
			bx := f.Boxes["local"]
			if bx == nil {
				bx = &boxConfig{Provider: "local", CreatedAt: time.Now().UTC()}
				f.Boxes["local"] = bx
			}
			bx.URL, bx.Token, bx.CAFile, bx.Build = opts.PublicURL(), res.OwnerToken, caFile, res.Build
			f.Current = "local"
			if err := a.saveBoxes(f); err != nil {
				return err
			}

			a.progress("checking " + bx.URL + " over HTTPS")
			c, err := a.boxClient(bx, bx.Token)
			if err != nil {
				return err
			}
			if err := waitHTTPS(ctx, c, res.Build, 30*time.Second); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if bx.AgentToken == "" || !tokenWorks(ctx, a, bx, bx.AgentToken) {
				status, raw, err := c.do(ctx, http.MethodPost, "/v1/tokens", nil, map[string]any{"name": "claude-code"})
				if err != nil || status != 200 {
					return &exitError{ExitError, fmt.Sprintf("create the agent token: %v %s", err, raw)}
				}
				var ct struct {
					Secret string `json:"secret"`
				}
				_ = json.Unmarshal(raw, &ct)
				bx.AgentToken = ct.Secret
				if err := a.saveBoxes(f); err != nil {
					return err
				}
			}
			login, _ := a.loginLink(ctx, c)

			out := map[string]any{
				"box": "local", "url": bx.URL, "build": res.Build[:12], "login": login, "ca": caFile,
				"mcp": "claude mcp add tiffin -- tiffin mcp", "seconds": int(time.Since(start).Seconds()),
			}
			if len(res.Warnings) > 0 {
				out["warnings"] = res.Warnings
			}
			if !a.tty() {
				writeJSON(a.io.Out, out)
				return nil
			}
			w := a.io.Out
			fmt.Fprintf(w, "\n%s Your box is up %s\n\n", a.paint("✓", green), a.paint(fmt.Sprintf("(local · build %s · %s)", res.Build[:12], time.Since(start).Round(time.Second)), dim))
			fmt.Fprintf(w, "  %-10s %s\n", "Dashboard", a.paint(bx.URL, bold))
			if login != "" {
				fmt.Fprintf(w, "  %-10s %s %s\n", "Sign in", login, a.paint("(one-time, 10 min)", dim))
			}
			fmt.Fprintf(w, "  %-10s %s\n", "Agents", "claude mcp add tiffin -- tiffin mcp")
			for _, wn := range res.Warnings {
				fmt.Fprintf(w, "  %s %s\n", a.paint("!", amber), wn)
			}
			fmt.Fprintf(w, "\n%s Browsers will warn about the certificate until you trust the box once: %s\n", a.paint("→", amber), a.paint("tiffin trust", bold))
			return nil
		},
	}
	cmd.Flags().StringVar(&prov, "provider", "local", "where the box runs: local")
	cmd.Flags().StringVar(&binary, "binary", "", "linux tiffin binary to install (default: next to this one, or built from source)")
	return cmd
}

func (a *app) boxClient(bx *boxConfig, token string) (*client, error) {
	tr, err := boxTransport(bx.CAFile)
	if err != nil {
		return nil, err
	}
	return &client{base: strings.TrimRight(bx.URL, "/"), token: token, session: a.session, transport: tr, close: func() error { return nil }}, nil
}

func tokenWorks(ctx context.Context, a *app, bx *boxConfig, tok string) bool {
	c, err := a.boxClient(bx, tok)
	if err != nil {
		return false
	}
	status, _, err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, nil)
	return err == nil && status == 200
}

func waitHTTPS(ctx context.Context, c *client, build string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last string
	for time.Now().Before(deadline) {
		status, raw, err := c.do(ctx, http.MethodGet, "/v1/health", nil, nil)
		if err == nil && status == 200 {
			var h struct {
				Build string `json:"build"`
			}
			_ = json.Unmarshal(raw, &h)
			if h.Build == build {
				return nil
			}
			last = "a different build is answering (" + install.Short(h.Build) + ")"
		} else if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("HTTP %d", status)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the box did not answer over HTTPS at %s: %s", c.base, last)
}

func (a *app) loginLink(ctx context.Context, c *client) (string, error) {
	status, raw, err := c.do(ctx, http.MethodPost, "/v1/login-links", nil, nil)
	if err != nil || status != 200 {
		return "", fmt.Errorf("login link: %v %s", err, raw)
	}
	var l struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &l)
	return l.URL, nil
}

// linuxBinary finds the tiffin build to install on the box: the --binary
// flag, a tiffin-linux-<arch> next to this executable, or a fresh build when
// running from the source tree.
func (a *app) linuxBinary(ctx context.Context, flag string) (string, error) {
	if flag != "" {
		if _, err := os.Stat(flag); err != nil {
			return "", &exitError{ExitInvalid, "--binary: " + err.Error()}
		}
		return flag, nil
	}
	arch := runtime.GOARCH
	if runtime.GOOS == "linux" {
		if exe, err := os.Executable(); err == nil {
			return exe, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		sib := filepath.Join(filepath.Dir(exe), "tiffin-linux-"+arch)
		if _, err := os.Stat(sib); err == nil {
			return sib, nil
		}
	}
	if root := repoRoot(); root != "" {
		a.progress("building tiffin for linux/" + arch + " from " + root)
		out := filepath.Join(os.TempDir(), "tiffin-linux-"+arch)
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", out, "./cmd/tiffin")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		if o, err := cmd.CombinedOutput(); err != nil {
			return "", &exitError{ExitError, fmt.Sprintf("build linux binary: %v\n%s", err, o)}
		}
		return out, nil
	}
	return "", &exitError{ExitInvalid, "no linux build of tiffin found: pass --binary, or put tiffin-linux-" + arch + " next to this binary"}
}

func repoRoot() string {
	dir, _ := os.Getwd()
	for dir != "" && dir != "/" {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(raw), "module github.com/btahir/tiffin\n") {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func (a *app) downCmd() *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Destroy your box and everything on it",
		Long:  "Deletes the VM and its data disk: every project, database and file on the box. Irreversible. Needs --confirm local.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			p := lima.New()
			st, err := p.State(ctx)
			if err != nil && !errors.Is(err, provider.ErrUnavailable) {
				return &exitError{ExitError, err.Error()}
			}
			if confirm != "local" {
				detail := "nothing to destroy: there is no local box"
				if st != provider.StateAbsent {
					detail = "this deletes the local box (VM \"tiffin\" and its data disk) and everything on it; it cannot be undone"
				}
				if a.tty() {
					fmt.Fprintf(a.io.Out, "%s %s\n%s re-run with %s\n", a.paint("irreversible:", red), detail, a.paint("→", amber), a.paint("tiffin down --confirm local", bold))
				} else {
					writeJSON(a.io.Out, map[string]any{"code": "confirm_required", "status": 428, "detail": detail, "hint": "re-run with --confirm local"})
				}
				a.code = ExitConfirm
				return nil
			}
			a.progress("deleting the VM and its data disk")
			if err := p.Destroy(ctx); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if f, err := a.loadBoxes(); err == nil {
				delete(f.Boxes, "local")
				if f.Current == "local" {
					f.Current = ""
				}
				_ = a.saveBoxes(f)
			}
			_ = os.RemoveAll(filepath.Join(a.configDir(), "boxes", "local"))
			if a.tty() {
				fmt.Fprintf(a.io.Out, "%s The local box is gone.\n", a.paint("✓", green))
			} else {
				writeJSON(a.io.Out, map[string]any{"destroyed": "local"})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the box name, to confirm (local)")
	return cmd
}

func (a *app) loginCmd() *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Get a one-time dashboard sign-in link",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client(cmd.Context())
			if err != nil {
				return err
			}
			defer c.close()
			link, err := a.loginLink(cmd.Context(), c)
			if err != nil {
				return &exitError{ExitAuth, err.Error()}
			}
			if open && runtime.GOOS == "darwin" {
				_ = exec.Command("open", link).Start()
			}
			if a.tty() {
				fmt.Fprintf(a.io.Out, "%s %s\n", link, a.paint("(one-time, 10 min)", dim))
			} else {
				writeJSON(a.io.Out, map[string]string{"url": link})
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the link in your browser")
	return cmd
}

func (a *app) trustCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Trust your box's HTTPS certificate in this computer's browsers",
		Long: "Adds the box's own certificate authority (made on the box, never shared) to your login keychain, so browsers " +
			"accept https://dashboard.tiffin.localhost. macOS asks for your password. Firefox keeps its own list; import the file there by hand.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, bx := a.currentBox()
			if bx == nil || bx.CAFile == "" {
				return &exitError{ExitInvalid, "no box yet: run tiffin up first"}
			}
			args := []string{"add-trusted-cert", "-r", "trustRoot", "-k", filepath.Join(a.io.Env("HOME"), "Library", "Keychains", "login.keychain-db"), bx.CAFile}
			if printOnly || runtime.GOOS != "darwin" {
				fmt.Fprintf(a.io.Out, "security %s\n", strings.Join(args, " "))
				return nil
			}
			c := exec.CommandContext(cmd.Context(), "security", args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, a.io.Out, a.io.Err
			if err := c.Run(); err != nil {
				return &exitError{ExitError, "security add-trusted-cert: " + err.Error()}
			}
			fmt.Fprintf(a.io.Out, "%s Trusted. Open %s\n", a.paint("✓", green), bx.URL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "only print the command")
	return cmd
}

func (a *app) selfUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "self-update <binary>",
		Short:  "Switch this box to another tiffin build (rolls back if unhealthy)",
		Long:   "Runs on the box as root. `tiffin up` calls it for you.",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return &exitError{ExitAuth, "self-update must run as root on the box"}
			}
			u := install.NewUpdater()
			u.Progress = func(s string) { fmt.Fprintln(a.io.Err, s) }
			if err := u.Update(cmd.Context(), args[0]); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			fmt.Fprintln(a.io.Err, "updated")
			return nil
		},
	}
}
