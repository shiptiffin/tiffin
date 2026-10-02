// Package mcp exposes the Tiffin API as MCP tools. Tools are generated from
// the API's OpenAPI document, so every API operation is a tool with the same
// inputs, the same errors and annotations derived from its risk class.
// Calls run in-process against the API handler, so auth, validation and
// policy are exactly the HTTP API's.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/danielgtaylor/huma/v2"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Instructions tell a connecting agent how Tiffin works.
const Instructions = `Tiffin runs a whole app stack on one Linux box. You operate it through these tools.

Every change follows plan → review → apply:
1. Call "plan" with a manifest to see what would change. Each op has a risk tier (reversible, outbound, irreversible) and a plain-words reason.
2. Call "apply" with the same manifest and confirm=<plan hash>. Without confirm, or if the plan changed, nothing is applied and you get the plan back to review.
3. Every applied change can be reviewed with "changes_list" and reverted with "change_undo" (also plan-then-confirm).

Your token's scopes decide which risk tiers you may apply. If an apply is denied, show the plan to a human and ask them to apply it. Never guess a confirm hash; always use the one from the plan you reviewed. Tell the human what you changed and why (pass "intent").`

// TokenFunc returns the bearer token for a tool call. For the HTTP
// transport it reads the request's Authorization header; for stdio it
// returns the configured token.
type TokenFunc func(ctx context.Context, req *sdk.CallToolRequest) string

// FromHeader reads the Authorization header of the MCP HTTP request.
func FromHeader(_ context.Context, req *sdk.CallToolRequest) string {
	if req.Extra != nil && req.Extra.Header != nil {
		return req.Extra.Header.Get("Authorization")
	}
	return ""
}

// Static always uses token.
func Static(token string) TokenFunc {
	return func(context.Context, *sdk.CallToolRequest) string {
		return "Bearer " + strings.TrimPrefix(token, "Bearer ")
	}
}

// Tool is a generated tool plus the routing needed to call the API.
type Tool struct {
	Tool   *sdk.Tool
	op     *huma.Operation
	params map[string]string // arg name → "path" | "query"
	body   map[string]bool   // arg names that go in the JSON body
}

// NewServer builds an MCP server exposing every API operation as a tool.
// Calls go to h: the API's own handler in-process, or a proxy to a remote box.
func NewServer(a *api.API, h http.Handler, version string, token TokenFunc) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "tiffin", Title: "Tiffin", Version: version},
		&sdk.ServerOptions{Instructions: Instructions})
	for _, t := range Tools(a) {
		t := t
		s.AddTool(t.Tool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return t.call(ctx, h, token(ctx, req), req)
		})
	}
	return s
}

// ToolName maps an operation ID to a tool name ("projects-list" → "projects_list").
func ToolName(opID string) string { return strings.ReplaceAll(opID, "-", "_") }

// Tools generates one tool per API operation, sorted by name.
func Tools(a *api.API) []*Tool {
	oapi := a.OpenAPI()
	var out []*Tool
	for _, o := range a.Operations() {
		t := &Tool{op: o, params: map[string]string{}, body: map[string]bool{}}
		props := map[string]any{}
		var required []string
		defs := map[string]any{}
		for _, p := range o.Parameters {
			if p.In != "path" && p.In != "query" {
				continue
			}
			props[p.Name] = withDescription(inline(oapi, p.Schema, defs), p.Description)
			t.params[p.Name] = p.In
			if p.Required {
				required = append(required, p.Name)
			}
		}
		if o.RequestBody != nil {
			if mt := o.RequestBody.Content["application/json"]; mt != nil && mt.Schema != nil {
				bs, _ := inline(oapi, mt.Schema, defs).(map[string]any)
				bp, _ := bs["properties"].(map[string]any)
				for name, ps := range bp {
					props[name] = ps
					t.body[name] = true
				}
				if req, ok := bs["required"].([]any); ok {
					for _, r := range req {
						required = append(required, r.(string))
					}
				}
			}
		}
		sort.Strings(required)
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		if len(defs) > 0 {
			schema["$defs"] = defs
		}
		raw, _ := json.Marshal(schema)
		t.Tool = &sdk.Tool{
			Name:        ToolName(o.OperationID),
			Title:       o.Summary,
			Description: description(o),
			InputSchema: json.RawMessage(raw),
			Annotations: annotations(o),
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out
}

func description(o *huma.Operation) string {
	d := o.Summary + ". " + o.Description
	if api.RiskOf(o) == api.RiskDestructive {
		d += " Calling without a confirm hash is always safe: it only returns the plan."
	}
	return d
}

func annotations(o *huma.Operation) *sdk.ToolAnnotations {
	f, t := false, true
	a := &sdk.ToolAnnotations{Title: o.Summary, OpenWorldHint: &f}
	switch api.RiskOf(o) {
	case api.RiskRead:
		a.ReadOnlyHint, a.IdempotentHint = true, true
	case api.RiskWrite:
		a.DestructiveHint = &f
	default:
		a.DestructiveHint = &t
	}
	return a
}

func withDescription(s any, desc string) any {
	m, ok := s.(map[string]any)
	if ok && desc != "" {
		if _, has := m["description"]; !has {
			m["description"] = desc
		}
	}
	return s
}

// inline turns a huma schema into plain JSON Schema with every component
// $ref inlined, and swaps the permissive manifest placeholder for the real
// manifest schema (whose $defs are hoisted into defs).
func inline(oapi *huma.OpenAPI, s *huma.Schema, defs map[string]any) any {
	if s == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(s)
	var v any
	_ = json.Unmarshal(b, &v)
	return resolve(oapi, v, defs, 0)
}

func resolve(oapi *huma.OpenAPI, v any, defs map[string]any, depth int) any {
	if depth > 32 {
		return map[string]any{}
	}
	switch x := v.(type) {
	case map[string]any:
		if x["x-tiffin-manifest"] == true {
			return manifestSchema(defs)
		}
		if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
			target := oapi.Components.Schemas.SchemaFromRef(ref)
			return resolve(oapi, inline(oapi, target, defs), defs, depth+1)
		}
		out := map[string]any{}
		for k, val := range x {
			if k == "$schema" || strings.HasPrefix(k, "x-") {
				continue
			}
			out[k] = resolve(oapi, val, defs, depth+1)
		}
		return out
	case []any:
		for i := range x {
			x[i] = resolve(oapi, x[i], defs, depth+1)
		}
		return x
	}
	return v
}

func manifestSchema(defs map[string]any) any {
	var m map[string]any
	_ = json.Unmarshal(manifest.Schema(), &m)
	if d, ok := m["$defs"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
	}
	delete(m, "$defs")
	delete(m, "$schema")
	delete(m, "$id")
	return m
}

func (t *Tool) call(ctx context.Context, h http.Handler, token string, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args map[string]any
	if raw := req.Params.Arguments; len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return errorResult(fmt.Sprintf("arguments must be a JSON object: %v", err)), nil
		}
	}
	path := t.op.Path
	q := url.Values{}
	body := map[string]any{}
	for k, v := range args {
		switch {
		case t.params[k] == "path":
			path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(fmt.Sprint(v)))
		case t.params[k] == "query":
			q.Set(k, fmt.Sprint(v))
		case t.body[k]:
			body[k] = v
		default:
			return errorResult(fmt.Sprintf("unknown argument %q", k)), nil
		}
	}
	if strings.Contains(path, "{") {
		return errorResult("missing a required path argument for " + t.op.Path), nil
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rd *bytes.Reader
	if t.op.RequestBody != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	hr := httptest.NewRequestWithContext(ctx, t.op.Method, path, rd)
	hr.Header.Set("Content-Type", "application/json")
	if token != "" {
		hr.Header.Set("Authorization", token)
	}
	if req.Session != nil {
		hr.Header.Set(api.SessionHeader, "mcp:"+req.Session.ID())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	return toResult(rec.Code, rec.Body.Bytes()), nil
}

func toResult(status int, raw []byte) *sdk.CallToolResult {
	var v any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &v)
	} else {
		v = map[string]any{"ok": true}
		raw = []byte(`{"ok":true}`)
	}
	res := &sdk.CallToolResult{StructuredContent: v}
	text := string(raw)
	switch {
	case status < 300:
	case status == http.StatusPreconditionRequired:
		// Not a failure: this is the plan the agent must review.
		text = "Nothing was applied yet. Review this plan, then call again with confirm set to its hash.\n" + text
	default:
		res.IsError = true
	}
	res.Content = []sdk.Content{&sdk.TextContent{Text: text}}
	return res
}

func errorResult(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}
