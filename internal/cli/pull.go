package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/starters"
	"github.com/spf13/cobra"
)

// pullCmd writes a project's current manifest (whatever made it: apply, the
// dashboard, an agent) to tiffin.config.ts, and the source of any app that
// runs a starter, so a project started in the dashboard can be edited here.
func (a *app) pullCmd() *cobra.Command {
	var project string
	var force bool
	cmd := &cobra.Command{
		Use:   "pull [dir]",
		Short: "Write the project's current manifest to tiffin.config.ts (and a starter's source)",
		Long: "Fetches the project's current manifest from the box (made by tiffin apply, the dashboard or an agent) and writes it " +
			"as a readable tiffin.config.ts in dir (default: the current directory), so your files match the box again.\n\n" +
			"An app whose live version is a starter (the dashboard's New project) also gets that starter's source in its folder, " +
			"so you can change it and tiffin deploy. Files already there are kept.\n\n" +
			"An existing config that differs is never overwritten without --force; the differences are printed either way. " +
			"The project comes from --project, else from the existing config, else the box's only project.",
		Example: "  tiffin pull                 # into ./tiffin.config.ts\n  tiffin pull shop --project shop && cd shop   # then edit and tiffin deploy\n  tiffin pull --force         # overwrite after reviewing the diff",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir := orDefault(first(args), ".")
			existing, _ := manifest.FindConfig(dir)
			if project == "" && existing != "" {
				raw, _, err := a.loadManifest(existing)
				if err != nil {
					return &exitError{ExitInvalid, fmt.Sprintf("%s does not evaluate (%v); name the project with --project", existing, err)}
				}
				var head struct {
					Project string `json:"project"`
				}
				_ = json.Unmarshal(raw, &head)
				project = head.Project
			}
			c, err := a.client(ctx)
			if err != nil {
				return err
			}
			defer c.close()
			if project == "" {
				status, raw, err := c.do(ctx, http.MethodGet, "/v1/projects", nil, nil)
				if err != nil {
					return &exitError{ExitError, err.Error()}
				}
				if status != http.StatusOK {
					a.emit(status, raw)
					a.code = exitFor(status, raw)
					return nil
				}
				var ps []struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(raw, &ps)
				switch len(ps) {
				case 0:
					return &exitError{ExitInvalid, "the box has no projects yet: create one with tiffin apply (or the dashboard) first"}
				case 1:
					project = ps[0].Name
				default:
					names := make([]string, len(ps))
					for i, p := range ps {
						names[i] = p.Name
					}
					return &exitError{ExitInvalid, "the box has several projects (" + strings.Join(names, ", ") + "); pick one with --project"}
				}
			}
			status, raw, err := c.do(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(project)+"/manifest", nil, nil)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if status != http.StatusOK {
				a.emit(status, raw)
				a.code = exitFor(status, raw)
				return nil
			}
			var pm struct {
				Project  string             `json:"project"`
				Version  int64              `json:"version"`
				Manifest *manifest.Manifest `json:"manifest"`
				Config   string             `json:"config"`
			}
			if err := json.Unmarshal(raw, &pm); err != nil || pm.Manifest == nil {
				return &exitError{ExitError, "unexpected manifest response from the box"}
			}
			target := existing
			if target == "" {
				target = filepath.Join(dir, "tiffin.config.ts")
			}
			content := []byte(pm.Config)
			if strings.HasSuffix(target, ".json") {
				if content, err = manifest.Canonical(pm.Manifest); err != nil {
					return err
				}
			}
			old, readErr := os.ReadFile(target)
			had := readErr == nil
			res := pullResult{Path: target, Project: pm.Project, Version: pm.Version, Summary: summarize(pm.Manifest)}
			stamp := false // only the version in the header differs
			if had {
				res.Diff, res.Added, res.Removed = lineDiff(string(old), string(content))
				res.Changed = res.Added+res.Removed > 0
				if res.Changed && unversioned(string(old)) == unversioned(string(content)) {
					stamp, res.Changed, res.Diff, res.Added, res.Removed = true, false, "", 0, 0
				}
			} else {
				res.Changed = true
			}
			switch {
			case stamp:
				if err := os.WriteFile(target, content, 0o644); err != nil {
					return err
				}
				res.Written = true
				res.Note = fmt.Sprintf("already matches the box; its header now says version %d", pm.Version)
			case had && !res.Changed:
				res.Note = "already matches the box"
			case had && !force:
				res.Note = "differs from the box; not overwritten (run again with --force to replace it)"
				a.code = ExitInvalid
			default:
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(target, content, 0o644); err != nil {
					return err
				}
				res.Written = true
			}
			res.Sources = pullSources(ctx, c, pm.Project, pm.Manifest, filepath.Dir(target))
			a.reportPull(res)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project to pull (default: the existing config's project, or the box's only project)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config that differs from the box")
	return cmd
}

// versionStamp is the part of a pulled config's header that names the
// box's version ("Project shop at version 12, pulled from the box."): it
// moves with every change, a secret's included, while the config does not.
var versionStamp = regexp.MustCompile(`at version \d+, pulled from the box`)

func unversioned(config string) string {
	return versionStamp.ReplaceAllString(config, "at version N, pulled from the box")
}

type pullResult struct {
	Path    string `json:"path"`
	Project string `json:"project"`
	Version int64  `json:"version"`
	Written bool   `json:"written"`
	Changed bool   `json:"changed"`
	Added   int    `json:"linesAdded"`
	Removed int    `json:"linesRemoved"`
	Summary string `json:"summary"`
	Diff    string `json:"diff,omitempty"`
	Note    string `json:"note,omitempty"`
	// Sources are the starter apps whose source was written next to the config.
	Sources []pulledSource `json:"sources,omitempty"`
}

type pulledSource struct {
	App     string   `json:"app"`
	Starter string   `json:"starter"`
	Dir     string   `json:"dir"`
	Written []string `json:"written"`
	Kept    []string `json:"kept,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// pullSources writes the source of each app whose live production version
// came from a starter (the dashboard's New project), into the app's path next
// to the config. Apps deployed from your files, git or GitHub already have
// their source with you, and nothing that exists is overwritten.
func pullSources(ctx context.Context, c *client, project string, m *manifest.Manifest, base string) []pulledSource {
	names := make([]string, 0, len(m.Apps))
	for n := range m.Apps {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []pulledSource
	for _, name := range names {
		status, raw, err := c.do(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(project)+"/apps/"+url.PathEscape(name)+"/deploys", url.Values{"limit": {"20"}}, nil)
		if err != nil || status != http.StatusOK {
			continue // no runtime here, or no deploys: only the config
		}
		var list struct {
			Deploys []struct {
				Status   string `json:"status"`
				Source   string `json:"source"`
				Template string `json:"template"`
			} `json:"deploys"`
		}
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		starter := ""
		for _, d := range list.Deploys { // newest first: the live one is what runs
			if d.Status == "live" {
				if d.Source == "template" {
					starter = d.Template
				}
				break
			}
		}
		rel := orDefault(m.Apps[name].Path, ".")
		if starter == "" || !filepath.IsLocal(rel) {
			continue
		}
		ps := pulledSource{App: name, Starter: starter, Dir: filepath.Join(base, rel)}
		ps.Written, ps.Kept, err = starters.WriteSource(starter, ps.Dir)
		if err != nil {
			ps.Error = err.Error()
		}
		out = append(out, ps)
	}
	return out
}

func (a *app) reportPull(r pullResult) {
	if !a.tty() {
		writeJSON(a.io.Out, r)
		return
	}
	out := a.io.Out
	if r.Diff != "" {
		for _, line := range strings.Split(strings.TrimSuffix(r.Diff, "\n"), "\n") {
			code := dim
			switch {
			case strings.HasPrefix(line, "+"):
				code = green
			case strings.HasPrefix(line, "-"):
				code = red
			}
			fmt.Fprintln(out, a.paint(line, code))
		}
	}
	verb := "Wrote"
	switch {
	case !r.Written && r.Changed:
		verb = "Not overwritten:"
	case !r.Written:
		verb = "Up to date:"
	}
	fmt.Fprintf(out, "%s %s (project %s, version %d)", verb, r.Path, r.Project, r.Version)
	if r.Added+r.Removed > 0 {
		fmt.Fprintf(out, ", %d lines added, %d removed", r.Added, r.Removed)
	}
	fmt.Fprintf(out, "\n  %s\n", r.Summary)
	for _, s := range r.Sources {
		if s.Error != "" {
			fmt.Fprintf(out, "Couldn't write %s's source: %s\n", s.App, s.Error)
			continue
		}
		fmt.Fprintf(out, "Wrote %s's source (the %s starter) to %s: %s", s.App, s.Starter, s.Dir, plural(len(s.Written), "file"))
		if len(s.Kept) > 0 {
			fmt.Fprintf(out, "; kept %s already there", plural(len(s.Kept), "file"))
		}
		fmt.Fprintln(out)
	}
	switch {
	case !r.Written && r.Changed:
		fmt.Fprintf(out, "%s %s\n", a.paint("next:", dim), "review the diff, then tiffin pull --force")
	case len(r.Sources) > 0:
		fmt.Fprintf(out, "%s %s\n", a.paint("next:", dim), "change a file, then tiffin deploy (same address, zero downtime)")
	}
}

// summarize describes a manifest in one line: apps, services, env.
func summarize(m *manifest.Manifest) string {
	var parts []string
	if len(m.Apps) > 0 {
		names := make([]string, 0, len(m.Apps))
		for n, app := range m.Apps {
			names = append(names, n+" ("+string(app.Framework)+")")
		}
		sort.Strings(names)
		parts = append(parts, plural(len(names), "app")+": "+strings.Join(names, ", "))
	} else {
		parts = append(parts, "no apps")
	}
	var svc []string
	s := m.Services
	for _, x := range []struct {
		on   bool
		name string
	}{{s.Postgres != nil, "postgres"}, {s.Valkey != nil, "valkey"}, {s.Storage != nil, "storage"},
		{s.Auth != nil, "auth"}, {s.Email != nil, "email"}, {s.Analytics != nil, "analytics"}} {
		if x.on {
			svc = append(svc, x.name)
		}
	}
	if len(svc) > 0 {
		parts = append(parts, "services: "+strings.Join(svc, ", "))
	} else {
		parts = append(parts, "no services")
	}
	if n := len(m.Env); n > 0 {
		parts = append(parts, plural(n, "env var"))
	}
	for _, x := range []struct {
		n    int
		name string
	}{{len(m.Queues), "queue"}, {len(m.Topics), "topic"}, {len(m.Crons), "cron"}} {
		if x.n > 0 {
			parts = append(parts, plural(x.n, x.name))
		}
	}
	return strings.Join(parts, "; ")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// lineDiff is a small line diff (longest common subsequence): changed lines
// prefixed "-"/"+", one line of context around each change, "@@" between
// hunks. Configs are short, so O(n·m) is fine.
func lineDiff(a, b string) (string, int, int) {
	x := strings.Split(strings.TrimSuffix(a, "\n"), "\n")
	y := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	n, m := len(x), len(y)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte // ' ', '-', '+'
		text string
	}
	var lines []line
	added, removed := 0, 0
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			lines = append(lines, line{' ', x[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, line{'-', x[i]})
			removed, i = removed+1, i+1
		default:
			lines = append(lines, line{'+', y[j]})
			added, j = added+1, j+1
		}
	}
	if added+removed == 0 {
		return "", 0, 0
	}
	keep := make([]bool, len(lines))
	for k, l := range lines {
		if l.op != ' ' {
			for c := max(0, k-1); c <= min(len(lines)-1, k+1); c++ {
				keep[c] = true
			}
		}
	}
	var buf bytes.Buffer
	gap := false
	for k, l := range lines {
		if !keep[k] {
			gap = true
			continue
		}
		if gap && buf.Len() > 0 {
			buf.WriteString("@@\n")
		}
		gap = false
		buf.WriteByte(l.op)
		buf.WriteString(" " + l.text + "\n")
	}
	return buf.String(), added, removed
}
