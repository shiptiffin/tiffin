package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/approvals"
	"github.com/btahir/tiffin/internal/dashboard"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	tmcp "github.com/btahir/tiffin/internal/mcp"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/version"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"log/slog"
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
			raw, _, err := a.loadManifest(first(args))
			if err != nil {
				return err
			}
			return a.call(cmd.Context(), http.MethodPost, "/v1/plan", nil, map[string]any{"manifest": raw})
		},
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

const configTemplate = `import { defineConfig } from "tiffin-sdk";

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
			if a.tty() {
				fmt.Fprintf(a.io.Out, "Wrote %s, AGENTS.md and the Tiffin agent skill for project %q. Next: tiffin plan\n", path, project)
			} else {
				writeJSON(a.io.Out, map[string]string{"path": path, "project": project})
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
	var addr, domain, publicURL string
	var withEdge, onBox bool
	var httpsPort, httpPort int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the box API and MCP endpoint",
		Long: "Serves the Tiffin API at /v1, MCP (streamable HTTP, stateless) at /mcp and the dashboard at /. " +
			"With --edge it also runs the embedded HTTPS edge for dashboard.<domain>. " +
			"On first start it creates the owner token and prints it once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			started := time.Now()
			var ed *edge.Edge
			var plat *platform.Platform
			var openErr error
			b, fresh, err := openBox(ctx, a.home, func(d *api.Deps) {
				d.PublicURL = publicURL
				d.Checks = func(ctx context.Context) []api.Check { return boxChecks(a.home, ed, started) }
				if onBox {
					sec, err := platform.OpenSecrets(d.DB, a.home)
					if err != nil {
						openErr = err
						return
					}
					plat = &platform.Platform{DB: d.DB, Engine: d.Engine, Tokens: d.Tokens, Secrets: sec, Home: a.home,
						DataRoot: filepath.Dir(a.home), Domain: domain, PublicURL: publicURL, Version: version.Version,
						Log: slog.New(slog.NewJSONHandler(a.io.Err, nil))}
					d.Platform = plat
					if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
						am, err := approvals.New(d.DB, u.Hostname(), strings.TrimRight(publicURL, "/"))
						if err != nil {
							openErr = err
							return
						}
						d.Approvals = am
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
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: serveMux(b), ReadHeaderTimeout: 10 * time.Second}
			errc := make(chan error, 1)
			go func() { errc <- hs.Serve(ln) }()
			base := "http://" + ln.Addr().String()
			if withEdge {
				ed, err = edge.Start(ctx, edge.Config{Domain: domain, Upstream: ln.Addr().String(), DataDir: filepath.Join(a.home, "edge"),
					HTTPPort: httpPort, HTTPSPort: httpsPort, Internal: true, AccessLog: accessLog(onBox)})
				if err != nil {
					return fmt.Errorf("start the HTTPS edge: %w", err)
				}
				defer ed.Stop()
				pem, err := ed.RootCAPEM()
				if err != nil {
					return err
				}
				// Public: clients fetch it to trust the box's HTTPS.
				if err := os.WriteFile(filepath.Join(a.home, "ca.crt"), pem, 0o644); err != nil {
					return err
				}
				if plat != nil {
					plat.Edge = &edgeControl{ed: ed, base: edge.Config{Domain: domain, Upstream: ln.Addr().String(), DataDir: filepath.Join(a.home, "edge"),
						HTTPPort: httpPort, HTTPSPort: httpsPort, Internal: true, AccessLog: accessLog(onBox)}}
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
			}
			fmt.Fprintf(a.io.Err, "tiffin %s serving %s (API %s/v1, MCP %s/mcp, data %s)\n", version.Version, base, base, base, a.home)
			if fresh != "" {
				fmt.Fprintf(a.io.Err, "\nOwner token (shown once, also saved to %s):\n  %s\n\n"+
					"Give agents their own token, never this one. It can plan and make reversible changes only:\n"+
					"  TIFFIN_TOKEN=<owner token> tiffin --url %s tokens create --name claude-code\n"+
					"  claude mcp add --transport http tiffin %s/mcp --header \"Authorization: Bearer <agent token>\"\n\n",
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
	cmd.Flags().BoolVar(&onBox, "box", false, "run as a box: start platform modules (services, apps, reconcilers)")
	cmd.Flags().StringVar(&domain, "domain", "tiffin.localhost", "edge domain; the dashboard is dashboard.<domain>")
	cmd.Flags().IntVar(&httpsPort, "https-port", 443, "edge HTTPS port")
	cmd.Flags().IntVar(&httpPort, "http-port", 80, "edge HTTP port (redirects to HTTPS)")
	cmd.Flags().StringVar(&publicURL, "public-url", "", "the dashboard URL people use, for login links")
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

// edgeControl lets modules replace the edge's routes.
type edgeControl struct {
	ed   *edge.Edge
	base edge.Config
}

func (e *edgeControl) SetRoutes(routes []edge.Route) error {
	cfg := e.base
	cfg.Routes = routes
	return e.ed.Reload(cfg)
}

func (a *app) provisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "provision",
		Short:  "Install and update the box's system services (root, idempotent)",
		Long:   "Runs every module's provisioner: system packages, pinned downloads and systemd units. `tiffin up` runs it for you.",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return &exitError{ExitAuth, "provision must run as root on the box"}
			}
			sys := platform.NewSystem(func(s string) { fmt.Fprintln(a.io.Err, s) })
			for _, m := range platform.Modules() {
				pv, ok := m.(platform.Provisioner)
				if !ok {
					continue
				}
				start := time.Now()
				if err := pv.Provision(cmd.Context(), sys); err != nil {
					return &exitError{ExitError, m.Name() + ": " + err.Error()}
				}
				fmt.Fprintf(a.io.Err, "%s ready (%s)\n", m.Name(), time.Since(start).Round(time.Millisecond))
			}
			return nil
		},
	}
}

// serveMux routes the box's HTTP surface: the API under /v1 and MCP at /mcp.
func serveMux(b *box) http.Handler {
	srv := tmcp.NewServer(b.api, b.api.Handler(), version.Version, tmcp.FromHeader)
	mux := http.NewServeMux()
	mux.Handle("/v1/", b.api.Handler())
	mux.Handle("/", dashboard.Handler())
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	return mux
}

func (a *app) mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdio",
		Long: "Speaks MCP over stdin/stdout, for agents that launch tools as subprocesses:\n" +
			"  claude mcp add tiffin -- tiffin mcp\n" +
			"With TIFFIN_URL set it proxies to that box; otherwise it serves the local box in --home.\n" +
			"Without TIFFIN_TOKEN it uses a local agent token (read, plan, apply:reversible), never the owner token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
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
					// local agent token that cannot apply irreversible plans.
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
			srv := tmcp.NewServer(spec, h, version.Version, tmcp.Static(token))
			return srv.Run(ctx, &sdk.StdioTransport{})
		},
	}
}

// proxyTo forwards requests to u, with u's Host header (the edge routes by
// host, so the incoming request's Host must not leak through).
func proxyTo(u *url.URL, tr http.RoundTripper) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
		},
		Transport: tr,
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

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}
