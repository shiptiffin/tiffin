package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/dashboard"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/edge/switchboard"
	"github.com/btahir/tiffin/internal/manifest"
	tmcp "github.com/btahir/tiffin/internal/mcp"
	authmod "github.com/btahir/tiffin/internal/mod/auth"
	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
	"github.com/btahir/tiffin/internal/passkeys"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/sdkpkg"
	"github.com/btahir/tiffin/internal/version"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tiffin version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.tty() {
				fmt.Fprintln(a.io.Out, version.String())
				return nil
			}
			writeJSON(a.io.Out, map[string]string{"name": "tiffin", "version": version.Version, "commit": version.Commit, "date": version.Date})
			return nil
		},
	}
}

// loadManifest evaluates the config at target (a file, a directory, or "" for
// the current directory) and returns the raw manifest JSON for the API.
func (a *app) loadManifest(target string) (json.RawMessage, string, error) {
	if target == "" {
		target = "."
	}
	path := target
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		p, err := manifest.FindConfig(target)
		if err != nil {
			return nil, "", &exitError{ExitInvalid, err.Error()}
		}
		path = p
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		// Tiffin's own settings (TIFFIN_TOKEN above all) never reach the config.
		if k, v, ok := strings.Cut(kv, "="); ok && !strings.HasPrefix(k, "TIFFIN_") {
			env[k] = v
		}
	}
	raw, err := manifest.EvaluateJSON(path, env)
	if err != nil {
		return nil, path, &exitError{ExitInvalid, err.Error()}
	}
	return raw, path, nil
}

func (a *app) planCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan [dir|file]",
		Short: "Show what applying tiffin.config.ts would change",
		Long: "Evaluates tiffin.config.ts and shows the plan: every create, update and delete, its risk tier and why, " +
			"and the plan hash to confirm. Never changes anything.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, path, err := a.loadManifest(first(args))
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/plan", nil, map[string]any{"manifest": raw}); err != nil {
				return err
			}
			a.vercelNotes(raw, filepath.Dir(path))
			return nil
		},
	}
}

// vercelNotes says, on a terminal, what deploys take from each app's
// vercel.json, so nothing it changes comes as a surprise.
func (a *app) vercelNotes(raw []byte, dir string) {
	var m struct {
		Apps map[string]struct {
			Path string `json:"path"`
		} `json:"apps"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	names := make([]string, 0, len(m.Apps))
	for n := range m.Apps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v, err := vercelcfg.Read(filepath.Join(dir, filepath.FromSlash(orDefault(m.Apps[n].Path, "."))))
		switch {
		case err != nil:
			a.say("apps.%s: %v (its deploys fail until that is fixed)", n, err)
		case v != nil:
			a.say("apps.%s: vercel.json, read at each deploy: %s", n, orDefault(v.Summary(), "nothing the box uses"))
			if len(v.Ignored) > 0 {
				a.say("  not used by the box: %s", strings.Join(v.Ignored, ", "))
			}
		}
	}
}

func (a *app) applyCmd() *cobra.Command {
	var confirm, intent string
	cmd := &cobra.Command{
		Use:   "apply [dir|file]",
		Short: "Apply tiffin.config.ts (needs --confirm <plan hash>)",
		Long: "Plans tiffin.config.ts and applies it if --confirm matches the plan hash. Without --confirm, or if the plan " +
			"changed since you looked, nothing is applied: the plan is printed and the exit code is 4.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, _, err := a.loadManifest(first(args))
			if err != nil {
				return err
			}
			body := map[string]any{"manifest": raw}
			if confirm != "" {
				body["confirm"] = confirm
			}
			if intent != "" {
				body["intent"] = intent
			}
			return a.call(cmd.Context(), http.MethodPost, "/v1/apply", nil, body)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the plan hash (or first 8+ characters) you reviewed")
	cmd.Flags().StringVarP(&intent, "intent", "m", "", "why you are making this change, in one sentence")
	return cmd
}

func (a *app) undoCmd() *cobra.Command {
	var confirm, intent string
	cmd := &cobra.Command{
		Use:   "undo <change-id>",
		Short: "Undo a change (needs --confirm <undo plan hash>)",
		Long:  "Same as `tiffin changes undo`. Without --confirm the undo plan is printed and the exit code is 4.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if confirm != "" {
				body["confirm"] = confirm
			}
			if intent != "" {
				body["intent"] = intent
			}
			return a.call(cmd.Context(), http.MethodPost, "/v1/changes/"+url.PathEscape(args[0])+"/undo", nil, body)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the undo plan's hash (or first 8+ characters)")
	cmd.Flags().StringVarP(&intent, "intent", "m", "", "why you are undoing, in one sentence")
	return cmd
}

//go:embed scaffold/AGENTS.md
var scaffoldAgents []byte

//go:embed scaffold/SKILL.md
var scaffoldSkill []byte

const configTemplate = `import { defineConfig } from "@shiptiffin/sdk";

// Your app in a box. Run "tiffin plan" to see what this would set up.
export default defineConfig({
  project: %q,
  apps: {
    web: { framework: "next" },
  },
  services: {
    postgres: {},
  },
});
`

func (a *app) initCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write a starter tiffin.config.ts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := orDefault(first(args), ".")
			if p, err := manifest.FindConfig(dir); err == nil {
				return &exitError{ExitInvalid, "a config already exists: " + p}
			}
			if project == "" {
				abs, _ := filepath.Abs(dir)
				project = slugify(filepath.Base(abs))
			}
			path := filepath.Join(dir, "tiffin.config.ts")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, fmt.Appendf(nil, configTemplate, project), 0o644); err != nil {
				return err
			}
			// Teach the agents that will work here how to work here.
			extra := map[string][]byte{
				filepath.Join(dir, "AGENTS.md"):                               scaffoldAgents,
				filepath.Join(dir, ".claude", "skills", "tiffin", "SKILL.md"): scaffoldSkill,
			}
			for p, b := range extra {
				if _, err := os.Stat(p); err == nil {
					continue // never overwrite the project's own files
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, b, 0o644); err != nil {
					return err
				}
			}
			// Vendor the SDK into an app that has a package.json (unless it installs it from npm).
			sdk, err := sdkpkg.Add(dir)
			switch {
			case errors.Is(err, sdkpkg.ErrNoPackageJSON):
				sdk = nil
			case err != nil:
				return err
			}
			if a.tty() {
				fmt.Fprintf(a.io.Out, "Wrote %s, AGENTS.md and the Tiffin agent skill for project %q.\n", path, project)
				switch {
				case sdk != nil && sdk.FromNPM != "":
					fmt.Fprintf(a.io.Out, "@shiptiffin/sdk is installed from npm (%s).\n", sdk.FromNPM)
				case sdk != nil:
					fmt.Fprintf(a.io.Out, "Added @shiptiffin/sdk (%s, vendored: commit it). Run bun install.\n", strings.Join(sdk.Files, ", "))
				default:
					fmt.Fprintln(a.io.Out, "No package.json yet: once the app has one, `tiffin sdk add` vendors @shiptiffin/sdk into it.")
				}
				fmt.Fprintln(a.io.Out, "Next: tiffin plan")
			} else {
				out := map[string]any{"path": path, "project": project}
				if sdk != nil {
					out["sdk"] = sdk
				}
				writeJSON(a.io.Out, out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project slug (default: the directory name)")
	return cmd
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "app-" + out
	}
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	return out
}

func (a *app) serveCmd() *cobra.Command {
	var addr, domain, publicURL, publicIPv6 string
	var withEdge, edgeExternal, onBox bool
	var httpsPort, httpPort int
	var tlsMode, acmeCA, acmeRoots, acmeEmail string
	var publicIPs, resolvers []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the box API and MCP endpoint",
		Long: "Serves the Tiffin API at /v1, MCP (streamable HTTP, stateless, API key required) at /mcp and the dashboard at /. " +
			"With --edge it also runs the embedded HTTPS edge for dashboard.<domain>; with --edge-external it drives the one `tiffin edge` runs. " +
			"On first start it creates the owner token and prints it once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			started := time.Now()
			envDefault(cmd, a.io.Env, map[string]string{"tls": "TIFFIN_TLS", "public-ip": "TIFFIN_PUBLIC_IP",
				"acme-ca": "TIFFIN_ACME_CA", "acme-ca-roots": "TIFFIN_ACME_CA_ROOTS", "acme-email": "TIFFIN_ACME_EMAIL", "dns-resolver": "TIFFIN_DNS_RESOLVER"})
			var reach platform.Reach
			var ecl *edge.Client
			var plat *platform.Platform
			var openErr error
			if onBox {
				// A box import swaps in its state while nothing holds it open.
				if _, err := platform.ApplyPendingImport(filepath.Dir(a.home), func(s string) { fmt.Fprintln(a.io.Err, s) }); err != nil {
					fmt.Fprintln(a.io.Err, "box import:", err)
				}
			}
			b, fresh, err := openBox(ctx, a.home, func(d *api.Deps) {
				d.Checks = func(ctx context.Context) []api.Check { return boxChecks(a.home, ecl, started) }
				if onBox {
					var err error
					reach, domain, publicURL, err = boxReach(ctx, d.DB, reachFlags{domain: domain, publicURL: publicURL, httpsPort: httpsPort,
						tls: tlsMode, ips: append(publicIPs, nonEmpty(publicIPv6)...), acmeCA: acmeCA, acmeRoots: acmeRoots, acmeEmail: acmeEmail, resolvers: resolvers})
					if err != nil {
						openErr = err
						return
					}
				}
				d.PublicURL = publicURL
				if onBox {
					sec, err := platform.OpenSecrets(d.DB, a.home)
					if err != nil {
						openErr = err
						return
					}
					plat = &platform.Platform{DB: d.DB, Engine: d.Engine, Tokens: d.Tokens, Secrets: sec, Home: a.home,
						DataRoot: filepath.Dir(a.home), Domain: domain, PublicURL: publicURL, Version: version.Version,
						Log: slog.New(slog.NewJSONHandler(a.io.Err, nil)), Reach: reach}
					plat.Restart = func(reason string) {
						plat.Log.Info("restarting the service", "reason", reason)
						// After the answer that asked for it is sent; systemd
						// (Restart=always) starts the service again.
						time.AfterFunc(time.Second, stop)
					}
					d.Platform = plat
					d.Engine.Estimate = plat.EstimateLoss
					if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
						am, err := passkeys.New(d.DB, u.Hostname(), strings.TrimRight(publicURL, "/"))
						if err != nil {
							openErr = err
							return
						}
						d.Passkeys = am
					}
				}
			})
			if err != nil {
				return err
			}
			defer b.Close()
			if openErr != nil {
				return openErr
			}
			if plat != nil {
				plat.BoxChecks = func(ctx context.Context) []platform.Check { return b.api.Status(ctx, started).Checks }
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: serveMux(b), ReadHeaderTimeout: 10 * time.Second}
			errc := make(chan error, 1)
			go func() { errc <- hs.Serve(ln) }()
			base := "http://" + ln.Addr().String()
			withEdge = withEdge || edgeExternal
			if withEdge {
				ecfg := edge.Config{Domain: domain, Apps: reach.AppsDomain, Dashboard: reach.Dashboard, DashboardURL: publicURL, Upstream: ln.Addr().String(), DataDir: filepath.Join(a.home, "edge"),
					HTTPPort: httpPort, HTTPSPort: httpsPort, Internal: !reach.ACME, AccessLog: accessLog(onBox)}
				if reach.ACME {
					ecfg.ACME = &edge.ACME{CA: reach.ACMEDirectory, Email: reach.ACMEEmail, TrustedRoots: reach.ACMERoots}
				}
				ecl, err = a.startEdge(ctx, ecfg, edgeExternal)
				if err != nil {
					return fmt.Errorf("start the HTTPS edge: %w", err)
				}
				if plat != nil {
					plat.Edge = ecl
				}
				if publicURL != "" {
					base = publicURL
				}
			}
			if plat != nil {
				if err := plat.Start(ctx); err != nil {
					return fmt.Errorf("start platform: %w", err)
				}
				if err := plat.RefreshRoutes(ctx); err != nil {
					plat.Log.Error("routes", "err", err)
				}
			} else if ecl != nil {
				if err := ecl.SetRoutes(nil); err != nil {
					fmt.Fprintln(a.io.Err, "edge:", err)
				}
			}
			if withEdge && !reach.ACME {
				// Public: clients fetch it to trust the box's HTTPS (a local
				// box; with public certificates nobody needs it). The edge
				// makes it when it loads its first config.
				go writeCA(ctx, filepath.Join(a.home, "edge"), filepath.Join(a.home, "ca.crt"))
			}
			fmt.Fprintf(a.io.Err, "tiffin %s serving %s (API %s/v1, MCP %s/mcp, data %s)\n", version.Version, base, base, base, a.home)
			if fresh != "" && !isTerminal(a.io.Err) {
				// Never write a secret into a service log (journald keeps it).
				fmt.Fprintf(a.io.Err, "owner token created and saved to %s\n", filepath.Join(a.home, ownerTokenFile))
			} else if fresh != "" {
				fmt.Fprintf(a.io.Err, "\nOwner token (shown once, also saved to %s):\n  %s\n\n"+
					"Give agents their own API key, never this token, so History shows who did what:\n"+
					"  TIFFIN_TOKEN=<owner token> tiffin --url %s tokens create --name claude-code --projects all --access full\n"+
					"  claude mcp add --transport http tiffin %s/mcp --header \"Authorization: Bearer <key>\"\n\n",
					filepath.Join(a.home, ownerTokenFile), fresh, base, base)
			}
			select {
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return hs.Shutdown(sctx)
			case err := <-errc:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7070", "API listen address (the edge proxies to it)")
	cmd.Flags().BoolVar(&withEdge, "edge", false, "also run the HTTPS edge (embedded Caddy, internal CA)")
	cmd.Flags().BoolVar(&edgeExternal, "edge-external", false, "drive the HTTPS edge `tiffin edge` runs (its own process, sockets in --home) instead")
	cmd.Flags().BoolVar(&onBox, "box", false, "run as a box: start platform modules (services, apps, reconcilers)")
	cmd.Flags().StringVar(&domain, "domain", "tiffin.localhost", "edge domain; the dashboard is dashboard.<domain>")
	cmd.Flags().IntVar(&httpsPort, "https-port", 443, "edge HTTPS port")
	cmd.Flags().IntVar(&httpPort, "http-port", 80, "edge HTTP port (redirects to HTTPS)")
	cmd.Flags().StringVar(&publicURL, "public-url", "", "the dashboard URL people use, for login links")
	cmd.Flags().StringVar(&tlsMode, "tls", "auto", "certificates: auto (public ACME on a server with a public IP, the internal CA otherwise), acme or internal [TIFFIN_TLS]")
	cmd.Flags().StringVar(&publicIPv6, "public-ipv6", "", "the server's public IPv6 address (same as adding it to --public-ip)")
	cmd.Flags().StringSliceVar(&publicIPs, "public-ip", nil, "the server's public address(es); default: the global addresses on its interfaces [TIFFIN_PUBLIC_IP]")
	cmd.Flags().StringVar(&acmeCA, "acme-ca", "", "ACME directory URL; default Let's Encrypt (tests: Pebble, or "+edge.LetsEncryptStaging+") [TIFFIN_ACME_CA]")
	cmd.Flags().StringVar(&acmeRoots, "acme-ca-roots", "", "PEM file of roots to trust for the ACME directory itself (a test CA) [TIFFIN_ACME_CA_ROOTS]")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "ACME account contact; also enables the ZeroSSL fallback [TIFFIN_ACME_EMAIL]")
	cmd.Flags().StringSliceVar(&resolvers, "dns-resolver", nil, "DNS servers (host:port) for the box's DNS checks; default public resolvers [TIFFIN_DNS_RESOLVER]")
	return cmd
}

// AccessLogPath is where the box's edge writes JSON access logs (analytics reads them).
const AccessLogPath = "/var/lib/tiffin/logs/access.log"

func accessLog(onBox bool) string {
	if onBox {
		return AccessLogPath
	}
	return ""
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// startEdge connects to the box's edge: the one `tiffin edge` runs
// (external), or one started in this process. Either way the control plane
// drives it over its socket, the same way.
func (a *app) startEdge(ctx context.Context, cfg edge.Config, external bool) (*edge.Client, error) {
	log := slog.New(slog.NewJSONHandler(a.io.Err, nil))
	sock, sb := filepath.Join(a.home, edge.EdgeSocket), edge.SwitchboardAddr
	if !external {
		srv := &edge.Server{Socket: sock, Control: filepath.Join(a.home, edge.ControlSocket), State: filepath.Join(a.home, edge.SnapshotFile),
			Switchboard: "127.0.0.1:0", Build: version.Version, Log: log}
		if err := srv.Start(ctx); err != nil {
			return nil, err
		}
		sb = srv.SwitchboardAddr()
	}
	var ctl switchboard.Control
	for _, m := range platform.Modules() {
		if c, ok := m.(switchboard.Control); ok {
			ctl = c
		}
	}
	if err := edge.ServeControl(ctx, filepath.Join(a.home, edge.ControlSocket), ctl); err != nil {
		return nil, err
	}
	o := edge.ClientOptions{Socket: sock, Base: cfg, Switchboard: sb, Local: !external, Log: log}
	if external {
		o.RestartEdge = func() { _ = exec.Command("systemctl", "restart", "tiffin-edge").Run() }
	}
	cl := edge.NewClient(o)
	cl.Start(ctx, 30*time.Second)
	return cl, nil
}

// writeCA copies the edge's internal CA root to dst once the edge made it.
func writeCA(ctx context.Context, dataDir, dst string) {
	for ctx.Err() == nil {
		if pem, err := edge.RootCAPEM(dataDir); err == nil {
			if old, err := os.ReadFile(dst); err != nil || string(old) != string(pem) {
				_ = os.WriteFile(dst, pem, 0o644)
			}
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// edgeCmd runs the box's edge process (unit tiffin-edge): Caddy and the
// switchboard, driven by `tiffin serve --edge-external`.
func (a *app) edgeCmd() *cobra.Command {
	var sb string
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Run the box's HTTPS edge (Caddy and the switchboard) for `tiffin serve`",
		Long: "Serves every site on the box from the last configuration `tiffin serve --edge-external` sent (kept in --home), " +
			"so restarting or updating the rest of Tiffin interrupts no app. Ports come from systemd (tiffin-edge.socket) when it passes them.",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			srv := &edge.Server{Socket: filepath.Join(a.home, edge.EdgeSocket), Control: filepath.Join(a.home, edge.ControlSocket),
				State: filepath.Join(a.home, edge.SnapshotFile), Switchboard: sb, Build: version.Version, Log: slog.New(slog.NewJSONHandler(a.io.Err, nil))}
			if err := srv.Start(ctx); err != nil {
				return err
			}
			fmt.Fprintf(a.io.Err, "tiffin %s edge: switchboard %s, socket %s\n", version.Version, srv.SwitchboardAddr(), srv.Socket)
			srv.Wait()
			return nil
		},
	}
	cmd.Flags().StringVar(&sb, "switchboard", edge.SwitchboardAddr, "where the switchboard listens (loopback)")
	return cmd
}

func (a *app) provisionCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:    "provision",
		Short:  "Install and update the box's system services (root, idempotent)",
		Long:   "Runs every module's provisioner: system packages, pinned downloads and systemd units. `tiffin up` runs it for you.",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return &exitError{ExitAuth, "provision must run as root on the box"}
			}
			// One run at a time: the box runs it again after a failure while
			// `tiffin up` may start one.
			if lock, err := os.OpenFile(platform.ProvisionReportPath+".lock", os.O_CREATE|os.O_RDWR, 0o600); err == nil {
				defer lock.Close()
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
			}
			sys := platform.NewSystem(func(s string) { fmt.Fprintln(a.io.Err, s) })
			// One broken service must not block updating Tiffin itself (the
			// update may be the fix): provision every module, record failures
			// for /v1/status, and fail only with --strict.
			started := time.Now()
			report := platform.ProvisionAll(cmd.Context(), sys, func(line string) { fmt.Fprintln(a.io.Err, line) })
			fmt.Fprintf(a.io.Err, "provisioned in %s\n", time.Since(started).Round(time.Second))
			if err := platform.SaveProvisionReport(report); err != nil {
				fmt.Fprintln(a.io.Err, "could not save the provision report:", err)
			}
			if failed := report.Failed(); len(failed) > 0 && strict {
				return &exitError{ExitError, "provisioning failed for: " + strings.Join(failed, ", ")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "fail if any module fails to provision")
	return cmd
}

// serveMux routes the box's HTTP surface: the API under /v1 and MCP at /mcp.
func serveMux(b *box) http.Handler {
	// /mcp lists the core tools; /mcp?tools=all lists every one.
	core := tmcp.NewServer(b.api, b.api.Handler(), version.Version, tmcp.FromHeader, tmcp.GroupCore)
	all := tmcp.NewServer(b.api, b.api.Handler(), version.Version, tmcp.FromHeader, tmcp.GroupAll)
	mux := http.NewServeMux()
	mux.Handle("/v1/", b.api.Handler())
	mux.Handle("/api/auth/", authmod.DashboardHandler()) // sign-in callbacks for the box's keys
	mux.Handle("/", dashboard.Handler())
	mux.Handle("/mcp", requireKey(b, sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		if r.URL.Query().Get("tools") == tmcp.GroupAll {
			return all
		}
		return core
	},
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true,
			// The box serves /mcp behind its edge under its public name, and every
			// request needs an API key (requireKey), so the SDK's loopback Host check
			// only refuses legitimate callers.
			DisableLocalhostProtection: true})))
	return mux
}

// requireKey refuses MCP requests without a valid key before any tool is
// listed or called: "run" executes its code before the API checks anything.
// Each tool call is still authorized by the API as that key.
func requireKey(b *box, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := b.tokens.Authenticate(r.Context(), r.Header.Get("Authorization")); err != nil {
			msg := "MCP needs an API key in the Authorization header."
			if r.Header.Get("Authorization") != "" {
				msg = "This API key is not valid: it was revoked, has expired, or is mistyped."
			}
			p := api.NewProblem(http.StatusUnauthorized, "unauthenticated", msg)
			p.Hint = `Create a key (tiffin tokens create) and connect with --header "Authorization: Bearer <key>".`
			w.Header().Set("WWW-Authenticate", `Bearer realm="tiffin"`)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (a *app) mcpCmd() *cobra.Command {
	var tools string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdio",
		Long: "Speaks MCP over stdin/stdout, for agents that launch tools as subprocesses:\n" +
			"  claude mcp add tiffin -- tiffin mcp\n" +
			"It talks to the box from `tiffin up` as that box's agent API key: full access to every project,\n" +
			"recorded in History as an agent, never the owner. TIFFIN_URL and TIFFIN_TOKEN point it at another\n" +
			"box or key (a box in --home gets its own local agent key, also with full access to every project).\n" +
			"History labels each change with TIFFIN_SESSION if set (else mcp:stdio-<random>) and TIFFIN_MODEL.\n" +
			"--tools core (the default) lists the ~40 most-used tools plus run, which calls any other operation by name;\n" +
			"--tools all lists every operation as its own tool.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			group, err := tmcp.ParseGroup(tools)
			if err != nil {
				return &exitError{ExitInvalid, "--tools: " + err.Error()}
			}
			spec := api.New(api.Deps{Version: version.Version})
			var h http.Handler
			token := a.token
			_, bx := a.currentBox()
			switch {
			case a.url != "":
				u, err := url.Parse(a.url)
				if err != nil {
					return &exitError{ExitInvalid, "--url: " + err.Error()}
				}
				h = proxyTo(u, nil)
			case bx != nil && !a.homeExplicit:
				// The box from `tiffin up`: proxy over HTTPS, as its agent token.
				u, err := url.Parse(bx.URL)
				if err != nil {
					return &exitError{ExitInvalid, "box url: " + err.Error()}
				}
				tr, err := boxTransport(bx.CAFile)
				if err != nil {
					return err
				}
				h = proxyTo(u, tr)
				if token == "" {
					token = bx.AgentToken
				}
			default:
				b, _, err := openBox(ctx, a.home)
				if err != nil {
					return err
				}
				defer b.Close()
				h = b.api.Handler()
				if token == "" {
					// Agents never get the owner token implicitly: they get a
					// local agent key (full access to every project, recorded as an agent).
					t, err := b.agentToken(ctx)
					if err != nil {
						return err
					}
					token = t
				}
			}
			if token == "" {
				return &exitError{ExitAuth, "no token: set TIFFIN_TOKEN"}
			}
			srv := tmcp.NewServer(spec, h, version.Version, tmcp.Static(token), group)
			if a.url == "" && bx != nil && !a.homeExplicit && a.token == "" {
				name, _ := a.currentBox()
				a.addMoveTool(srv, name, bx) // a move spans two of the CLI's boxes
			}
			return srv.Run(ctx, &sdk.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&tools, "tools", tmcp.GroupCore, "which tools to list: core (most-used, plus run for the rest) or all")
	return cmd
}

// proxyTo forwards requests to u, with u's Host header (the edge routes by
// host, so the incoming request's Host must not leak through). A request cut
// off by the box reloading its edge is sent again when that is safe.
func proxyTo(u *url.URL, tr http.RoundTripper) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
		},
		Transport: newRetryTransport(tr, nil),
	}
}

func (a *app) ownerCmd() *cobra.Command {
	owner := &cobra.Command{Use: "owner", Short: "Manage the box owner token (local access only)"}
	owner.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Revoke the owner token and issue a new one",
		Long:  "Needs local access to the box's data directory. Every token minted by the old owner keeps working until revoked.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.url != "" {
				return &exitError{ExitInvalid, "owner rotate runs on the box itself, not over TIFFIN_URL"}
			}
			b, _, err := openBox(cmd.Context(), a.home)
			if err != nil {
				return err
			}
			defer b.Close()
			secret, err := b.tokens.RotateOwner(cmd.Context())
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(a.home, ownerTokenFile), []byte(secret+"\n"), 0o600); err != nil {
				return err
			}
			if a.tty() {
				fmt.Fprintf(a.io.Out, "New owner token (also saved to %s):\n  %s\n", filepath.Join(a.home, ownerTokenFile), secret)
			} else {
				writeJSON(a.io.Out, map[string]string{"secret": secret})
			}
			return nil
		},
	})
	return owner
}

// quotaGetCmd shows a project's storage limit and what counts toward it:
// the storage part of the project's usage.
func (a *app) quotaGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <project>",
		Short: "Show a project's storage limit and what counts toward it",
		Long: "The project's storage limit (limitBytes: its own, or the box default; 0 means none), what it uses (usedBytes: its databases " +
			"and files together, and each part) and why it is read-only, if it is. The storage part of tiffin projects usage.",
		Example: "  tiffin storage quota get shop",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client(ctx)
			if err != nil {
				return err
			}
			defer c.close()
			status, raw, err := c.do(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(args[0])+"/usage", nil, nil)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status == http.StatusOK {
				var u struct {
					Storage map[string]any `json:"storage"`
				}
				if json.Unmarshal(raw, &u) != nil || u.Storage == nil {
					return &exitError{ExitError, "the box has not measured " + args[0] + "'s storage yet (it does every 30 seconds): try again shortly"}
				}
				u.Storage["project"] = args[0]
				raw, _ = json.Marshal(u.Storage)
			}
			a.emit(status, raw)
			a.code = exitFor(status, raw)
			return nil
		},
	}
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// nonEmpty returns s as a one-element list, or nothing when it is empty.
func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
