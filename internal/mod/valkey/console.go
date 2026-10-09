package valkey

// The console: commands typed as in valkey-cli, run as the project's own
// user with the REST endpoint's transparent prefixing (keys found from the
// server's key specs), so "GET user:1" reads p_shop:user:1 and replies name
// keys without the prefix. Commands that change data run only with write
// set, and each can be undone like any other write. SCAN, KEYS and DBSIZE,
// which a project user may not run, answer from the admin connection for the
// project's own keys. Nothing administrative or dangerous runs at all.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// KVCommandResult is one command's outcome.
type KVCommandResult struct {
	Command []string `json:"command" doc:"The command's words, as parsed"`
	Reply   any      `json:"reply" doc:"Valkey's reply: text, a number, a list or null"`
	Error   string   `json:"error,omitempty"`
	Write   bool     `json:"write" doc:"It changes data"`
	Undo    string   `json:"undo,omitempty" doc:"For a write: pass to kv-undo to put its keys back"`
	Ms      float64  `json:"ms"`
}

// KVConsole is what a batch of commands did, one result per command.
type KVConsole struct {
	Results []KVCommandResult `json:"results"`
}

const consoleMax = 100

type commandBody struct {
	Commands string `json:"commands" minLength:"1" maxLength:"8388608" doc:"One command per line, e.g. HGETALL session:u_2041"`
	Write    bool   `json:"write,omitempty" doc:"Allow commands that change data"`
}

func registerConsole(a huma.API, p *platform.Platform, tag string) {
	o := api.Op("kv-command", http.MethodPost, "/v1/projects/{project}/kv/command", "kv run", api.RiskWrite,
		"Run KV commands",
		"Runs commands written as in valkey-cli (one per line, \"quotes\" for spaces), as the project's own user: keys are given and shown without "+
			"the project prefix. Reading needs read access; commands that change data run only with write=true, need write access, and each "+
			"reply carries an undo id. Administrative and dangerous commands are refused.", tag)
	o.Errors = append(o.Errors, 409)
	o.MaxBodyBytes = 8 << 20
	huma.Register(a, api.Untrusted(o), api.Wrap(func(ctx context.Context, in *writeIn[commandBody]) (*struct{ Body *KVConsole }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		cmds, err := splitCommands(in.Body.Commands)
		if err != nil {
			return nil, err
		}
		h := conns(p)
		out := &KVConsole{Results: []KVCommandResult{}}
		for _, args := range cmds {
			r := h.command(ctx, in.Project, args, in.Body.Write, pr)
			if r.Undo != "" {
				auditWrite(ctx, p, pr, in.Project, "command", &KVWriteResult{Keys: []string{strings.Join(args, " ")}, Undo: r.Undo})
			}
			out.Results = append(out.Results, r)
		}
		return &struct{ Body *KVConsole }{out}, nil
	}))
}

// command runs one console command. pr may be nil (tests): everything allowed.
func (h *rest) command(ctx context.Context, project string, args []string, write bool, pr *tokens.Principal) (r KVCommandResult) {
	start := time.Now()
	r.Command = args
	defer func() { r.Ms = float64(time.Since(start).Microseconds()) / 1000 }()
	fail := func(err error) KVCommandResult {
		var re RedisError
		var pb *api.Problem
		if errors.As(err, &re) && strings.HasPrefix(string(re), "NOPERM") && limits.held(project) {
			err = kvErr(project, err)
		}
		switch {
		case errors.As(err, &re):
			r.Error = string(re)
		case errors.As(err, &pb):
			r.Error = pb.Detail
			if pb.Hint != "" {
				r.Error += " (" + pb.Hint + ")"
			}
		default:
			r.Error = err.Error()
		}
		return r
	}
	name := strings.ToLower(args[0])
	switch name {
	case "scan", "keys", "dbsize":
		v, err := h.listKeys(ctx, project, args)
		if err != nil {
			return fail(err)
		}
		r.Reply = v
		return r
	}
	if restDenied[name] || name == "randomkey" {
		return fail(badRequest("%s isn't available in the console", strings.ToUpper(name)))
	}
	var prep prepared
	var keys []string
	err := h.with(ctx, project, func(c *Client) error {
		cmds, err := h.commands(ctx, c)
		if err != nil {
			return err
		}
		info := cmds.lookup(args)
		if info != nil && info.admin {
			return badRequest("%s is for the box's admin only, not a project", strings.ToUpper(name))
		}
		r.Write = info == nil || !info.readonly
		if name == "ping" || name == "echo" {
			r.Write = false
		}
		if prep, err = prepare(cmds, project, false, args); err != nil {
			return err
		}
		if r.Write && !write {
			return badRequest("%s changes data: turn on Allow changes to run it", strings.ToUpper(name))
		}
		if r.Write && pr != nil {
			if err := pr.Require(tokens.ScopeApplyReversible, project); err != nil {
				return err
			}
		}
		if info != nil {
			for _, i := range info.keyPositions(args) {
				keys = append(keys, prep.args[i])
			}
		}
		if r.Write && len(keys) > 0 {
			return errWrite // a write to keys: below, with a copy for Undo
		}
		v, err := send(ctx, c, prep)
		if err != nil {
			return err
		}
		if re, ok := v.(RedisError); ok {
			return re
		}
		r.Reply = encodeReply(unprefixReply(args, project, v), false, true)
		return nil
	})
	if errors.Is(err, errWrite) {
		w := kvWrite{op: "command", keys: keys, cmds: [][]string{prep.args}, confirmed: true, raw: true}
		allow := func(bool) error { return nil }
		if pr != nil {
			allow = allowFor(pr, project)
		}
		res, err := h.write(ctx, project, w, allow)
		if err != nil {
			return fail(err)
		}
		r.Reply, r.Undo = encodeReply(unprefixReply(args, project, fromScript(prep, res.Replies[0])), false, true), res.Undo
		return r
	}
	if err != nil {
		return fail(err)
	}
	return r
}

var errWrite = errors.New("write")

// listKeys answers SCAN, KEYS and DBSIZE for the project's own keys.
func (h *rest) listKeys(ctx context.Context, project string, args []string) (any, error) {
	c, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pre := Prefix(project)
	name := strings.ToLower(args[0])
	strip := func(keys []string) []any {
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = strings.TrimPrefix(k, pre)
		}
		return out
	}
	switch name {
	case "dbsize":
		var n int64
		err := scanAll(ctx, c, pre+"*", "", 10_000_000, func(ks []string) error { n += int64(len(ks)); return nil }, nil)
		return n, err
	case "keys":
		if len(args) != 2 {
			return nil, RedisError("ERR wrong number of arguments for 'keys' command")
		}
		var all []string
		err := scanAll(ctx, c, pre+args[1], "", 10_000, func(ks []string) error { all = append(all, ks...); return nil }, nil)
		slices.Sort(all)
		return strip(slices.Compact(all)), err
	}
	if len(args) < 2 {
		return nil, RedisError("ERR wrong number of arguments for 'scan' command")
	}
	scan := []string{"SCAN", args[1], "MATCH", pre + "*"}
	for i := 2; i+1 < len(args); i += 2 {
		switch strings.ToLower(args[i]) {
		case "match":
			scan[3] = pre + args[i+1]
		case "count", "type":
			scan = append(scan, args[i], args[i+1])
		default:
			return nil, RedisError("ERR syntax error")
		}
	}
	v, err := c.Do(ctx, scan...)
	if err != nil {
		return nil, err
	}
	next, keys := scanReply(v)
	return []any{next, strip(keys)}, nil
}

// splitCommands reads commands as valkey-cli does: words split by spaces,
// "double quotes" with \n \t \r \" \\ and \xHH escapes, 'single quotes' (only
// \' escapes). A newline outside quotes ends a command; a line starting
// with # is a comment.
func splitCommands(text string) ([][]string, error) {
	var out [][]string
	var cmd []string
	var word strings.Builder
	inWord := false
	end := func() {
		if inWord {
			cmd = append(cmd, word.String())
			word.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '#' && !inWord && len(cmd) == 0:
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case ch == '\n':
			end()
			if len(cmd) > 0 {
				out = append(out, cmd)
				cmd = nil
			}
		case ch == ' ' || ch == '\t' || ch == '\r':
			end()
		case (ch == '"' || ch == '\'') && !inWord:
			inWord = true
			q := ch
			closed := false
			for i++; i < len(text); i++ {
				c := text[i]
				if c == q {
					closed = true
					break
				}
				if c == '\\' && i+1 < len(text) {
					n := text[i+1]
					if q == '\'' {
						if n == '\'' {
							word.WriteByte('\'')
							i++
							continue
						}
						word.WriteByte(c)
						continue
					}
					i++
					switch n {
					case 'n':
						word.WriteByte('\n')
					case 't':
						word.WriteByte('\t')
					case 'r':
						word.WriteByte('\r')
					case 'x':
						if i+2 < len(text) {
							if b, err := strconv.ParseUint(text[i+1:i+3], 16, 8); err == nil {
								word.WriteByte(byte(b))
								i += 2
								continue
							}
						}
						word.WriteByte('x')
					default:
						word.WriteByte(n)
					}
					continue
				}
				word.WriteByte(c)
			}
			if !closed {
				return nil, api.NewProblem(400, "bad_request", "a quote is not closed")
			}
			if i+1 < len(text) && !strings.ContainsRune(" \t\r\n", rune(text[i+1])) {
				return nil, api.NewProblem(400, "bad_request", "a closing quote must be followed by a space")
			}
			end()
		default:
			inWord = true
			word.WriteByte(ch)
		}
	}
	end()
	if len(cmd) > 0 {
		out = append(out, cmd)
	}
	if len(out) == 0 {
		return nil, api.NewProblem(400, "bad_request", "no command to run")
	}
	if len(out) > consoleMax {
		return nil, api.NewProblem(400, "bad_request", fmt.Sprintf("at most %d commands at once", consoleMax))
	}
	return out, nil
}
