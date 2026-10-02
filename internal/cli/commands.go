package cli

import (
	"context"
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
	"github.com/btahir/tiffin/internal/manifest"
	tmcp "github.com/btahir/tiffin/internal/mcp"
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
		if k, v, ok := strings.Cut(kv, "="); ok {
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
			if a.tty() {
				fmt.Fprintf(a.io.Out, "Wrote %s for project %q. Next: tiffin plan\n", path, project)
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
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the box API and MCP endpoint",
		Long: "Serves the Tiffin API at /v1 and MCP (streamable HTTP, stateless) at /mcp. " +
			"On first start it creates the owner token and prints it once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			b, fresh, err := openBox(ctx, a.home)
			if err != nil {
				return err
			}
			defer b.Close()
			mux := serveMux(b)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			base := "http://" + ln.Addr().String()
			fmt.Fprintf(a.io.Err, "tiffin %s serving %s (API %s/v1, MCP %s/mcp, data %s)\n", version.Version, base, base, base, a.home)
			if fresh != "" {
				fmt.Fprintf(a.io.Err, "\nOwner token (shown once, also saved to %s):\n  %s\n\nConnect Claude Code:\n  claude mcp add --transport http tiffin %s/mcp --header \"Authorization: Bearer <token>\"\n\n",
					filepath.Join(a.home, ownerTokenFile), fresh, base)
			}
			errc := make(chan error, 1)
			go func() { errc <- hs.Serve(ln) }()
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
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7070", "listen address")
	return cmd
}

// serveMux routes the box's HTTP surface: the API under /v1 and MCP at /mcp.
func serveMux(b *box) http.Handler {
	srv := tmcp.NewServer(b.api, b.api.Handler(), version.Version, tmcp.FromHeader)
	mux := http.NewServeMux()
	mux.Handle("/v1/", b.api.Handler())
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
			"With TIFFIN_URL set it proxies to that box; otherwise it serves the local box in --home.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			spec := api.New(api.Deps{Version: version.Version})
			var h http.Handler
			token := a.token
			if a.url != "" {
				u, err := url.Parse(a.url)
				if err != nil {
					return &exitError{ExitInvalid, "--url: " + err.Error()}
				}
				h = httputil.NewSingleHostReverseProxy(u)
			} else {
				b, _, err := openBox(ctx, a.home)
				if err != nil {
					return err
				}
				defer b.Close()
				h = b.api.Handler()
				if token == "" {
					token = readOwnerToken(a.home)
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
