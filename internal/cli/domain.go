package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// domainStatus is the part of GET /v1/domain the CLI acts on.
type domainStatus struct {
	Domain       string `json:"domain"`
	AppsDomain   string `json:"appsDomain"`
	Dashboard    string `json:"dashboard"`
	DashboardURL string `json:"dashboardUrl"`
	State        string `json:"state"`
	Summary      string `json:"summary"`
}

// domainCmd is `tiffin domain` (status) and `tiffin domain set <domain>`;
// the generated check and unset commands join it.
func (a *app) domainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "The box's domain: status, set, check, unset",
		Long: "Shows the box's domain, its dashboard address, where apps live and whether its certificates are live.\n\n" +
			"A server without a domain uses <its-ipv4-with-dashes>.sslip.io: real names, real certificates, no setup.\n" +
			"Your own domain takes two DNS records (tiffin domain check --domain example.com lists them):\n" +
			"  tiffin domain set example.com\n" +
			"Apps can live on a domain of their own, apart from the dashboard (like vercel.com and vercel.app):\n" +
			"  tiffin domain set example.com --apps-domain example.app",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client(cmd.Context())
			if err != nil {
				return err
			}
			defer c.close()
			status, raw, err := c.do(cmd.Context(), http.MethodGet, "/v1/domain", nil, nil)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			a.emit(status, raw)
			a.code = exitFor(status, raw)
			if status == http.StatusOK {
				var st domainStatus
				if json.Unmarshal(raw, &st) == nil {
					a.followDashboard(cmd.Context(), st)
				}
			}
			return nil
		},
	}
	var dashboard, appsDomain, email string
	var createRecords, force, noWait bool
	set := &cobra.Command{
		Use:   "set <domain>",
		Short: "Use your own domain for the box",
		Long: "Switches the box to <domain>: the dashboard moves to dashboard.<domain> and apps to <app>.<domain>.\n\n" +
			"With --apps-domain, apps and previews go to <app>.<apps-domain> instead, a registrable domain of their own\n" +
			"(example.app beside example.com), so app code cannot set cookies on the dashboard's domain. The dashboard,\n" +
			"API and webhooks stay on <domain>.\n\n" +
			"<domain> and *.<domain> must point at the box first (with --apps-domain: dashboard.<domain> and\n" +
			"*.<apps-domain>; A/AAAA records); if they don't, nothing changes and you get the exact records to add.\n" +
			"With --create-records and a connected DNS provider (tiffin dns connect), the box adds the ones in zones the\n" +
			"provider holds. The service restarts (apps keep running), certificates are obtained, and the old names keep\n" +
			"working until the new ones have certificates. This computer switches to the new address once it answers.",
		Example: "  tiffin domain set example.com\n  tiffin domain set apps.example.com --dashboard admin\n  tiffin domain set example.com --create-records\n" +
			"  tiffin domain set example.com --apps-domain example.app --create-records",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client(ctx)
			if err != nil {
				return err
			}
			defer c.close()
			body := map[string]any{"domain": args[0]}
			if dashboard != "" {
				body["dashboard"] = dashboard
			}
			if appsDomain != "" {
				body["appsDomain"] = appsDomain
			}
			if email != "" {
				body["email"] = email
			}
			if createRecords {
				body["createRecords"] = true
			}
			if force {
				body["force"] = true
			}
			status, raw, err := c.do(ctx, http.MethodPost, "/v1/domain", nil, body)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusOK || noWait {
				a.emit(status, raw)
				a.code = exitFor(status, raw)
				return nil
			}
			var want domainStatus
			_ = json.Unmarshal(raw, &want)
			if a.tty() {
				fmt.Fprintln(a.io.Err, want.Summary)
			}
			// The service restarts, then gets certificates: wait (bounded)
			// on the old address, which keeps working meanwhile.
			st, ok := a.waitDomain(ctx, c, want, 4*time.Minute)
			if !ok {
				a.emit(status, raw)
				fmt.Fprintf(a.io.Err, "The box is switching to %s but its dashboard certificate is not live yet. Check with: tiffin domain\n", want.Domain)
				return nil
			}
			_, raw2, _ := c.do(ctx, http.MethodGet, "/v1/domain", nil, nil)
			a.emit(http.StatusOK, raw2)
			a.followDashboard(ctx, st)
			return nil
		},
	}
	set.Flags().StringVar(&dashboard, "dashboard", "", "the dashboard's first-level name (default dashboard)")
	set.Flags().StringVar(&appsDomain, "apps-domain", "", "a separate domain for apps and previews, e.g. example.app (default: <domain>)")
	set.Flags().StringVar(&email, "email", "", "a contact for the certificate authority (optional; enables the ZeroSSL fallback)")
	set.Flags().BoolVar(&createRecords, "create-records", false, "create the DNS records through the connected DNS provider")
	set.Flags().BoolVar(&force, "force", false, "switch even if DNS does not point here yet")
	set.Flags().BoolVar(&noWait, "no-wait", false, "return at once instead of waiting for the new certificates")
	cmd.AddCommand(set)
	return cmd
}

// waitDomain polls the box until it serves want's domain, dashboard and apps
// domain with a live dashboard certificate (or a local box's internal one).
func (a *app) waitDomain(ctx context.Context, c *client, want domainStatus, d time.Duration) (domainStatus, bool) {
	deadline := time.Now().Add(d)
	last := ""
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return domainStatus{}, false
		case <-time.After(2 * time.Second):
		}
		status, raw, err := c.do(ctx, http.MethodGet, "/v1/domain", nil, nil)
		if err != nil || status != http.StatusOK {
			continue // restarting
		}
		var st domainStatus
		if json.Unmarshal(raw, &st) != nil || st.Domain != want.Domain || st.Dashboard != want.Dashboard ||
			(want.AppsDomain != "" && st.AppsDomain != want.AppsDomain) {
			continue // not restarted yet
		}
		if st.State == "live" || st.State == "internal" {
			return st, true
		}
		if a.tty() && st.State != last {
			fmt.Fprintf(a.io.Err, "  %s: %s\n", st.Dashboard, st.State)
			last = st.State
		}
	}
	return domainStatus{}, false
}

// followDashboard points this computer's box entry at the new dashboard
// address once it answers from here (DNS can lag behind on a laptop).
func (a *app) followDashboard(ctx context.Context, st domainStatus) {
	if a.url != "" || a.homeExplicit || st.DashboardURL == "" || (st.State != "live" && st.State != "internal") {
		return
	}
	f, err := a.loadBoxes()
	if err != nil || f.Current == "" || f.Boxes[f.Current] == nil {
		return
	}
	bx := f.Boxes[f.Current]
	old, err1 := url.Parse(bx.URL)
	nu, err2 := url.Parse(st.DashboardURL)
	if err1 != nil || err2 != nil || strings.EqualFold(old.Hostname(), nu.Hostname()) {
		return
	}
	// Keep the port this computer reaches the box on (a forwarded VM).
	next := "https://" + nu.Hostname()
	if p := old.Port(); p != "" && p != "443" {
		next += ":" + p
	}
	tr, err := boxTransport(bx.CAFile)
	if err != nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(pctx, http.MethodGet, next+"/v1/health", nil)
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		fmt.Fprintf(a.io.Err, "This computer cannot reach %s yet (%v); it keeps using %s while that works.\n", next, shortErr(err), bx.URL)
		return
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return
	}
	bx.URL = next
	if err := a.saveBoxes(f); err == nil {
		fmt.Fprintf(a.io.Err, "This computer now reaches the box at %s.\n", next)
	}
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s)-i < 120 {
		return s[i+2:]
	}
	return s
}
