package cli

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// upOptions are `tiffin up`'s flags.
type upOptions struct {
	provider, binary, name          string
	location, serverType, tokenFile string
	volumeGB                        int
	dryRun                          bool
	host, identity, sshKey, image   string
	dataDisk, dataDir, publicIP     string
	sshFrom                         []string
	rebootWindow                    string
}

func (a *app) upCmd() *cobra.Command {
	var o upOptions
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create (or update) your box",
		Long: "Creates the box if it does not exist, installs or updates Tiffin on it, and checks it answers over HTTPS. " +
			"Safe to run again: it converges. An unhealthy update rolls back automatically.\n\n" +
			"Providers:\n" +
			"  local    a Lima VM on this computer (the default)\n" +
			"  hetzner  a Hetzner Cloud server, a data volume and a firewall (token in HCLOUD_TOKEN);\n" +
			"           --dry-run prints what it would create and the monthly price\n" +
			"  ssh      any Ubuntu 24.04 or 26.04 server you can SSH into with sudo (--host user@ip)\n\n" +
			"Update a server box later with: tiffin up --name <box>",
		Example: "  tiffin up\n" +
			"  tiffin up --provider hetzner --dry-run\n" +
			"  tiffin up --provider hetzner --name shop --location nbg1\n" +
			"  tiffin up --provider ssh --host root@203.0.113.5 --data-disk /dev/sdb",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prov := o.provider
			if prov == "" {
				prov = "local"
				if o.name != "" && o.name != "local" {
					f, err := a.loadBoxes()
					if err != nil {
						return err
					}
					if bx := f.Boxes[o.name]; bx != nil {
						prov = bx.Provider
					} else {
						return &exitError{ExitInvalid, "there is no box named " + o.name + " yet: pass --provider hetzner or --provider ssh to create it"}
					}
				}
			}
			switch prov {
			case "local":
				if o.name != "" && o.name != "local" {
					return &exitError{ExitInvalid, "the local box is always named local"}
				}
				if o.dryRun {
					return &exitError{ExitInvalid, "--dry-run is for --provider hetzner and ssh"}
				}
				return a.upLocal(cmd.Context(), o.binary)
			case "hetzner", "ssh":
				return a.upServer(cmd, prov, o)
			}
			return &exitError{ExitInvalid, "--provider must be local, hetzner or ssh"}
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.provider, "provider", "", "where the box runs: local (default), hetzner or ssh")
	fl.StringVar(&o.binary, "binary", "", "linux tiffin binary to install (default: next to this one, or built from source)")
	fl.StringVar(&o.name, "name", "", "the box's name (server boxes; default tiffin). Hetzner resources are named after it")
	fl.BoolVar(&o.dryRun, "dry-run", false, "print what would be created and what it costs per month; change nothing")
	fl.StringVar(&o.location, "location", "", "hetzner: location (default $HCLOUD_LOCATION, else fsn1): fsn1, nbg1, hel1, ash, hil, sin")
	fl.StringVar(&o.serverType, "type", "", "hetzner: server type (default cax11, ARM 2 vCPU 4 GB; cx/cpx/ccx types are x86)")
	fl.IntVar(&o.volumeGB, "volume-size", 0, "hetzner: data volume size in GB (default 40)")
	fl.StringVar(&o.tokenFile, "token-file", "", "hetzner: file holding the API token (default: $HCLOUD_TOKEN)")
	fl.StringSliceVar(&o.sshFrom, "ssh-from", nil, "hetzner: addresses allowed to SSH in (default: this computer's public IP)")
	fl.StringVar(&o.sshKey, "ssh-key", "", "your own SSH private key to use (default $HCLOUD_SSH_KEY; else hetzner makes one for the box). Only its .pub is uploaded; an identical key already in the Hetzner project is reused")
	fl.StringVar(&o.image, "image", "", "hetzner: ubuntu-24.04 (default) or ubuntu-26.04")
	fl.StringVar(&o.host, "host", "", "ssh: the server, as user@host[:port] (the user needs sudo)")
	fl.StringVar(&o.identity, "identity", "", "ssh: private key file (default: your ssh agent and ~/.ssh)")
	fl.StringVar(&o.dataDisk, "data-disk", "", "ssh: a block device for /var/lib/tiffin (formatted XFS only if blank)")
	fl.StringVar(&o.dataDir, "data-dir", "", "ssh: a directory for /var/lib/tiffin when there is no separate disk")
	fl.StringVar(&o.publicIP, "public-ip", "", "ssh: the server's public IP, when --host is a name or a private address")
	fl.StringVar(&o.rebootWindow, "reboot-window", "", "server boxes: time of day (HH:MM, server time) when security updates may reboot; \"off\" (default) never reboots")
	return cmd
}

// upLocal creates or updates the local Lima box.
func (a *app) upLocal(ctx context.Context, binary string) error {
	start := time.Now()
	p := lima.New()
	if err := lima.Available(); err != nil {
		return &exitError{ExitError, err.Error()}
	}
	bin, err := a.linuxBinary(ctx, binary, runtime.GOARCH)
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
	if changed, err := ensureAgentKey(ctx, a, c, bx); err != nil {
		return &exitError{ExitError, err.Error()}
	} else if changed {
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
}

func (a *app) boxClient(bx *boxConfig, token string) (*client, error) {
	tr, err := boxTransport(bx.CAFile)
	if err != nil {
		return nil, err
	}
	return &client{base: strings.TrimRight(bx.URL, "/"), token: token, session: a.session, model: a.model, transport: tr, close: func() error { return nil }}, nil
}

// agentKeyCurrent reports whether tok works and is what the agent key
// should be: full access to all projects. It also returns tok's ID, so an
// older, narrower key can be revoked once it is replaced.
func agentKeyCurrent(ctx context.Context, a *app, bx *boxConfig, tok string) (bool, string) {
	c, err := a.boxClient(bx, tok)
	if err != nil {
		return false, ""
	}
	status, raw, err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, nil)
	if err != nil || status != 200 {
		return false, ""
	}
	var who struct {
		TokenID  string   `json:"tokenId"`
		Scopes   []string `json:"scopes"`
		Projects []string `json:"projects"`
	}
	_ = json.Unmarshal(raw, &who)
	return slices.Contains(who.Scopes, "*") && slices.Contains(who.Projects, "*"), who.TokenID
}

// ensureAgentKey makes sure bx has the API key `tiffin mcp` gives agents:
// full access to all projects (Claude Code asks the person before
// destructive tools; Tiffin records every change and can undo it). Older
// boxes minted a narrower token: it is replaced and revoked. owner is a
// client with the owner token. It reports whether bx changed.
func ensureAgentKey(ctx context.Context, a *app, owner *client, bx *boxConfig) (bool, error) {
	var oldID string
	if bx.AgentToken != "" {
		ok, id := agentKeyCurrent(ctx, a, bx, bx.AgentToken)
		if ok {
			return false, nil
		}
		oldID = id
	}
	status, raw, err := owner.do(ctx, http.MethodPost, "/v1/tokens", nil, map[string]any{"name": "claude-code", "projects": "all", "access": "full"})
	if err != nil || status != http.StatusOK {
		return false, fmt.Errorf("create the agent key: %v %s", err, raw)
	}
	var ck struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(raw, &ck)
	bx.AgentToken = ck.Secret
	if oldID != "" {
		_, _, _ = owner.do(ctx, http.MethodDelete, "/v1/tokens/"+oldID, nil, nil)
	}
	return true, nil
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
func (a *app) linuxBinary(ctx context.Context, flag, arch string) (string, error) {
	if flag != "" {
		if _, err := os.Stat(flag); err != nil {
			return "", &exitError{ExitInvalid, "--binary: " + err.Error()}
		}
		if got := elfArch(flag); got != "" && got != arch {
			return "", &exitError{ExitInvalid, fmt.Sprintf("--binary %s is built for linux/%s, but the box is %s: build it with GOOS=linux GOARCH=%s", flag, got, arch, arch)}
		}
		return flag, nil
	}
	if runtime.GOOS == "linux" && arch == runtime.GOARCH {
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

// elfArch is the Go architecture of a Linux executable ("" when the file
// is not an ELF binary, e.g. a script).
func elfArch(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	switch f.Machine {
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_X86_64:
		return "amd64"
	}
	return f.Machine.String()
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
	var confirm, prov, tokenFile string
	var deleteData bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Destroy your box and everything on it",
		Long: "Without --confirm, shows what would be deleted (and what keeps costing money). Irreversible.\n\n" +
			"  local    deletes the VM and its data disk: every project, database and file. --confirm local\n" +
			"  hetzner  deletes the server, its firewall and its SSH key. The data volume is detached and kept\n" +
			"           (it costs money until deleted) unless you add --delete-data. --confirm <name>\n" +
			"  ssh      stops Tiffin on the server and forgets the box; the server and its data stay. --confirm <name>",
		Example: "  tiffin down --confirm local\n  tiffin down --confirm shop --delete-data",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := confirm
			if name == "" {
				name, _ = a.currentBox()
			}
			f, err := a.loadBoxes()
			if err != nil {
				return err
			}
			bx := f.Boxes[name]
			switch {
			case bx != nil && bx.Provider != "local":
				return a.downServer(cmd.Context(), name, bx, confirm == name, deleteData, tokenFile)
			case bx == nil && prov == "hetzner" && name != "" && name != "local":
				// A box this computer forgot (e.g. only its kept volume is left).
				return a.downServer(cmd.Context(), name, &boxConfig{Provider: "hetzner", Server: &serverBox{}}, confirm == name, deleteData, tokenFile)
			case name != "" && name != "local" && confirm != "":
				return &exitError{ExitInvalid, "there is no box named " + name + " on this computer (for a Hetzner box it forgot, add --provider hetzner)"}
			}
			if deleteData {
				return &exitError{ExitInvalid, "--delete-data is for Hetzner boxes; down always deletes a local box's data disk"}
			}
			return a.downLocal(cmd.Context(), confirm)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the box's name, to confirm (local for the local box)")
	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "hetzner: also delete the data volume (every project, database and file)")
	cmd.Flags().StringVar(&prov, "provider", "", "hetzner: delete a box this computer no longer knows, by --confirm <name>")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "hetzner: file holding the API token (default: $HCLOUD_TOKEN)")
	return cmd
}

// downLocal deletes the local Lima box.
func (a *app) downLocal(ctx context.Context, confirm string) error {
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
