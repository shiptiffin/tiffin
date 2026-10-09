package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/spf13/cobra"
)

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// doctorCmd checks the box without depending on any app service, so it
// works when everything else is broken.
func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the box is healthy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			var checks []check
			add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }

			name, bx := a.currentBox()
			if bx != nil && a.url == "" && !a.homeExplicit {
				where := "local box"
				if bx.Provider != "" && bx.Provider != "local" {
					where = bx.Provider + " box"
				}
				add("box", true, fmt.Sprintf("%s %q at %s", where, name, bx.URL))
			} else if a.url == "" {
				fi, err := os.Stat(a.home)
				switch {
				case os.IsNotExist(err):
					add("home", true, a.home+" will be created on first use")
				case err != nil:
					add("home", false, err.Error())
				case fi.Mode().Perm()&0o077 != 0:
					add("home", false, fmt.Sprintf("%s is readable by others (mode %o); run chmod 700", a.home, fi.Mode().Perm()))
				default:
					add("home", true, a.home)
				}
				if fi, err := os.Stat(filepath.Join(a.home, ownerTokenFile)); err == nil && fi.Mode().Perm()&0o077 != 0 {
					add("owner token file", false, "owner-token is readable by others; run chmod 600")
				}
			}
			c, err := a.client(ctx)
			if err != nil {
				add("state", false, err.Error())
			} else {
				defer c.close()
				add("state", true, "platform state opens")
				status, raw, err := c.do(ctx, http.MethodGet, "/v1/health", nil, nil)
				var h struct{ Version, Build string }
				_ = json.Unmarshal(raw, &h)
				add("api", err == nil && status == 200, explain(status, err, answering(h.Version, h.Build), raw))
				status, raw, err = c.do(ctx, http.MethodGet, "/v1/whoami", nil, nil)
				var who struct {
					Name   string   `json:"name"`
					Kind   string   `json:"kind"`
					Scopes []string `json:"scopes"`
				}
				_ = json.Unmarshal(raw, &who)
				add("token", err == nil && status == 200, explain(status, err, fmt.Sprintf("%s (%s; scopes %s)", who.Name, who.Kind, strings.Join(who.Scopes, ", ")), raw))
				// The box's own checks: failed resources, orphaned containers,
				// stuck deploys, disk, services.
				status, raw, err = c.do(ctx, http.MethodGet, "/v1/status", nil, nil)
				var st struct{ Checks []check }
				_ = json.Unmarshal(raw, &st)
				failing := 0
				for _, ch := range st.Checks {
					if !ch.OK {
						failing++
						add(ch.Name, false, ch.Detail)
					}
				}
				if failing == 0 {
					add("box checks", err == nil && status == 200, explain(status, err, fmt.Sprintf("%d ok (tiffin status lists them)", len(st.Checks)), raw))
				}
			}
			add("platform", true, runtime.GOOS+"/"+runtime.GOARCH)

			ok := true
			for _, ch := range checks {
				ok = ok && ch.OK
			}
			if a.tty() {
				for _, ch := range checks {
					mark := a.paint("✓", green)
					if !ch.OK {
						mark = a.paint("✗", red)
					}
					fmt.Fprintf(a.io.Out, "%s %-16s %s\n", mark, ch.Name, a.paint(ch.Detail, dim))
				}
			} else {
				writeJSON(a.io.Out, map[string]any{"ok": ok, "checks": checks})
			}
			if !ok {
				a.code = ExitError
			}
			return nil
		},
	}
}

// answering names the tiffin that answers: its version when it is a
// release, else its build (a source build's version is "dev").
func answering(version, build string) string {
	if version != "" && version != "dev" {
		return "tiffin " + version + " answering"
	}
	if build != "" {
		return "tiffin answering (build " + install.Short(build) + ")"
	}
	return "tiffin answering (a source build)"
}

func explain(status int, err error, ok string, raw []byte) string {
	if err != nil {
		return err.Error()
	}
	if status == 200 {
		return ok
	}
	return fmt.Sprintf("HTTP %d: %s", status, trim(raw))
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
