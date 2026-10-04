package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/danielgtaylor/huma/v2"
	"github.com/spf13/cobra"
)

// generate adds one command per API operation, at the operation's CLI path.
// Hand-written commands win: a path that already exists is skipped.
func (a *app) generate(root *cobra.Command, spec *api.API) {
	oapi := spec.OpenAPI()
	for _, o := range spec.Operations() {
		words := api.CLIPath(o)
		if len(words) == 1 && words[0] == "-" {
			continue // dashboard-only operation
		}
		parent := root
		for _, w := range words[:len(words)-1] {
			parent = subgroup(parent, w)
		}
		leaf := words[len(words)-1]
		if find(parent, leaf) != nil {
			continue
		}
		parent.AddCommand(a.opCommand(oapi, o, leaf, func(n string) bool { return root.PersistentFlags().Lookup(n) != nil }))
	}
}

func find(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func subgroup(parent *cobra.Command, name string) *cobra.Command {
	if c := find(parent, name); c != nil {
		return c
	}
	c := &cobra.Command{Use: name, Short: groupShort[name]}
	if c.Short == "" {
		c.Short = "Manage " + name
	}
	parent.AddCommand(c)
	return c
}

var groupShort = map[string]string{
	"projects":  "See projects and their current state",
	"changes":   "See, inspect and undo changes",
	"tokens":    "Create, list and revoke API keys",
	"audit":     "See security events",
	"schema":    "Print JSON Schemas",
	"secrets":   "Set and remove a project's secret env vars",
	"passkeys":  "List and remove the passkeys people sign in with",
	"people":    "Invite people and manage their roles",
	"session":   "Dashboard sessions",
	"db":        "Inspect a project's Postgres database",
	"kv":        "Inspect a project's Valkey keys",
	"branches":  "Clone and drop preview database branches",
	"snapshots": "List and restore database snapshots",
	"backups":   "List backups and set the backup schedule",
	"storage":   "Buckets, files, presigned links and quotas",
	"email":     "Send mail, read the dev inbox, set up a relay",
	"apps":      "Apps, deploys, rollbacks and logs",
	"deploys":   "List and inspect deploys",
	"queues":    "Jobs, queues, topics and dead letters",
	"queue":     "Send jobs and manage queues, topics, crons and dead letters",
	"dlq":       "Replay jobs from the dead-letter queue",
	"topics":    "Topics and their subscribers",
	"crons":     "Scheduled calls into your apps",
	"runs":      "Workflow runs",
	"events":    "Send events that workflows wait for",
	"jobs":      "Inspect, retry and cancel jobs",
	"workflows": "Durable workflow runs, events and approvals",
	"metrics":   "Box and app metrics",
	"logs":      "Search and tail logs",
	"errors":    "Errors reported by your apps",
	"alerts":    "Alert rules and history",
	"analytics": "Visitors, pageviews and events",
	"auth":      "Your apps' users, sessions and organizations",
	"protect":   "Rate limits, bans, the bot challenge and under-attack mode",
	"git":       "The box's git remote for push-to-deploy",
	"issues":    "Errors your apps reported, grouped into issues",
	"observe":   "Observability settings and overview",
	"previews":  "Preview deploys: list, sleep and delete",
	"templates": "Starter apps to create a project from",
	"box":       "This machine: CPU, memory, disks and what each service and app uses",
	"exports":   "List, inspect and delete box exports",
	"imports":   "List, inspect, apply and discard uploaded box exports",
}

type bodyFlag struct {
	name string
	kind string // string | integer | boolean | array
	flag string // the CLI flag (usually the kebab-case name)
	// words are the string values a "word or list" field takes as a bare
	// string (projects: "all" or a list): --projects all sends "all".
	words []string
}

// wordOrList returns the enum of a oneOf {string enum, array of strings}
// schema, such as an API key's projects ("all" or a list).
func wordOrList(s *huma.Schema) ([]string, bool) {
	if s == nil || len(s.OneOf) != 2 {
		return nil, false
	}
	var words []string
	list := false
	for _, o := range s.OneOf {
		switch {
		case o.Type == "string" && len(o.Enum) > 0:
			for _, e := range o.Enum {
				if w, ok := e.(string); ok {
					words = append(words, w)
				}
			}
		case o.Type == "array" && o.Items != nil && o.Items.Type == "string":
			list = true
		}
	}
	return words, list && len(words) > 0
}

func (a *app) opCommand(oapi *huma.OpenAPI, o *huma.Operation, leaf string, isGlobal func(flag string) bool) *cobra.Command {
	var pathParams []string
	var queryParams []*huma.Param
	for _, p := range o.Parameters {
		switch p.In {
		case "path":
			pathParams = append(pathParams, p.Name)
		case "query":
			queryParams = append(queryParams, p)
		}
	}
	use := leaf
	for _, p := range pathParams {
		use += " <" + p + ">"
	}
	long := o.Summary + ".\n\n" + o.Description
	if api.Confirmable(o) {
		long += "\n\nWithout --confirm nothing changes: the plan is printed and the exit code is 4."
	}
	cmd := &cobra.Command{
		Use:     use,
		Short:   o.Summary,
		Long:    long,
		Args:    cobra.ExactArgs(len(pathParams)),
		Example: "  tiffin " + strings.Join(api.CLIPath(o), " ") + strings.Repeat(" <id>", len(pathParams)),
	}
	query := map[string]*string{}
	for _, p := range queryParams {
		v := new(string)
		query[p.Name] = v
		def := ""
		if p.Schema != nil && p.Schema.Default != nil {
			def = fmt.Sprint(p.Schema.Default)
		}
		cmd.Flags().StringVar(v, flagName(p.Name), "", describe(p.Description, def))
		if p.Schema != nil && p.Schema.Type == "boolean" {
			cmd.Flags().Lookup(flagName(p.Name)).NoOptDefVal = "true" // --revoked means --revoked=true
		}
	}
	var flags []bodyFlag
	values := map[string]any{}
	var rawBody, bodyFile string
	if o.RequestBody != nil {
		if mt := o.RequestBody.Content["application/json"]; mt != nil && mt.Schema != nil {
			s := mt.Schema
			if s.Ref != "" {
				s = oapi.Components.Schemas.SchemaFromRef(s.Ref)
			}
			names := make([]string, 0, len(s.Properties))
			for n := range s.Properties {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				ps := s.Properties[n]
				if ps.Ref != "" {
					ps = oapi.Components.Schemas.SchemaFromRef(ps.Ref)
				}
				// A body field named like a global flag (--url, --home...)
				// gets the command's name in front (--git-url), so the global
				// flag keeps working.
				fn := flagName(n)
				if isGlobal(fn) {
					fn = leaf + "-" + fn
				}
				f := bodyFlag{name: n, kind: ps.Type, flag: fn}
				switch ps.Type {
				case "string":
					values[n] = cmd.Flags().String(fn, "", ps.Description)
				case "integer":
					values[n] = cmd.Flags().Int(fn, 0, ps.Description)
				case "boolean":
					values[n] = cmd.Flags().Bool(fn, false, ps.Description)
				case "array":
					values[n] = cmd.Flags().StringSlice(fn, nil, ps.Description+" (comma-separated)")
				default:
					words, ok := wordOrList(ps)
					if !ok {
						continue // objects come in through --body
					}
					f.kind, f.words = "array", words
					values[n] = cmd.Flags().StringSlice(fn, nil, ps.Description+" (comma-separated, or "+strings.Join(words, " or ")+")")
				}
				flags = append(flags, f)
			}
			cmd.Flags().StringVar(&rawBody, "body", "", "request body as JSON (merged under the flags)")
			cmd.Flags().StringVar(&bodyFile, "body-file", "", "read the request body from a JSON file (- for stdin)")
		}
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		path := o.Path
		for i, p := range pathParams {
			path = strings.ReplaceAll(path, "{"+p+"}", url.PathEscape(args[i]))
		}
		q := url.Values{}
		for name, v := range query {
			if cmd.Flags().Changed(flagName(name)) {
				q.Set(name, *v)
			}
		}
		var body any
		if o.RequestBody != nil {
			m := map[string]any{}
			if err := a.readBody(rawBody, bodyFile, m); err != nil {
				return err
			}
			for _, f := range flags {
				if !cmd.Flags().Changed(f.flag) {
					continue
				}
				switch v := values[f.name].(type) {
				case *string:
					m[f.name] = *v
				case *int:
					m[f.name] = *v
				case *bool:
					m[f.name] = *v
				case *[]string:
					if len(*v) == 1 && slices.Contains(f.words, (*v)[0]) {
						m[f.name] = (*v)[0]
					} else {
						m[f.name] = *v
					}
				}
			}
			body = m
		}
		return a.call(cmd.Context(), o.Method, path, q, body)
	}
	return cmd
}

func (a *app) readBody(raw, file string, into map[string]any) error {
	var data []byte
	switch {
	case file == "-":
		b, err := readAll(a.io.In)
		if err != nil {
			return err
		}
		data = b
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return &exitError{ExitInvalid, err.Error()}
		}
		data = b
	case raw != "":
		data = []byte(raw)
	default:
		return nil
	}
	if err := json.Unmarshal(data, &into); err != nil {
		return &exitError{ExitInvalid, "request body is not a JSON object: " + err.Error()}
	}
	return nil
}

// flagName turns camelCase API names into kebab-case flags (ttlHours → ttl-hours).
func flagName(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func describe(desc, def string) string {
	if def != "" {
		return desc + " (default " + def + ")"
	}
	return desc
}

// call performs an API call, prints the result and sets the exit code.
func (a *app) call(ctx context.Context, method, path string, q url.Values, body any) error {
	c, err := a.client(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	status, raw, err := c.do(ctx, method, path, q, body)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	a.emit(status, raw)
	a.code = exitFor(status, raw)
	return nil
}
