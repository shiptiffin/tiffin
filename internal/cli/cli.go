// Package cli is the tiffin command line. Most commands are generated from
// the API's OpenAPI document (one command per operation); a few are written
// by hand because they work with local files (plan, apply, init) or run
// servers (serve, mcp).
//
// Conventions, for humans and agents alike:
//   - Output is JSON whenever stdout is not a terminal, or with --json.
//   - Exit codes: 0 ok, 1 error, 2 auth, 3 validation, 4 confirmation required.
//   - Never prompts. Auth comes from TIFFIN_TOKEN (or the local owner token).
//   - Without TIFFIN_URL the CLI runs against a local box in TIFFIN_HOME.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/spf13/cobra"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitAuth    = 2
	ExitInvalid = 3
	ExitConfirm = 4
)

// IO is where the CLI reads and writes. Tests substitute buffers.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
	// TTY forces terminal (human) output when true; nil means detect.
	TTY *bool
	// Env looks up environment variables; nil means os.Getenv.
	Env func(string) string
}

type app struct {
	io      IO
	jsonOut bool
	url     string
	token   string
	home    string
	session string
	model   string
	code    int  // exit code chosen by the last command
	started bool // arguments and flags parsed; the command itself is running
	// homeExplicit is set when --home or TIFFIN_HOME picks a local box,
	// which then wins over a box created with `tiffin up`.
	homeExplicit bool
}

// exitError carries a specific exit code out of a command.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// Execute runs the CLI with args (without the program name) and returns the exit code.
func Execute(ctx context.Context, args []string, sio IO) int {
	if sio.Env == nil {
		sio.Env = os.Getenv
	}
	if sio.In == nil {
		sio.In = os.Stdin
	}
	a := &app{io: sio}
	root := a.root()
	root.SetArgs(args)
	root.SetOut(sio.Out)
	root.SetErr(sio.Err)
	cmd, err := root.ExecuteContextC(ctx)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				a.fail(ee.msg)
			}
			return ee.code
		}
		if a.started {
			// The command ran and failed (state db, filesystem, listen...).
			a.fail(err.Error())
			return ExitError
		}
		// cobra usage errors: unknown command, bad flags, wrong arg count.
		// Say how the command is used, so the next try gets it right.
		a.failWith(err.Error(), usageHint(cmd))
		return ExitInvalid
	}
	return a.code
}

// usageHint is a command's usage line and first example ("" for the root,
// whose error already suggests commands).
func usageHint(cmd *cobra.Command) string {
	if cmd == nil || !cmd.HasParent() {
		return ""
	}
	if cmd.HasAvailableSubCommands() {
		var names []string
		for _, c := range cmd.Commands() {
			if c.IsAvailableCommand() {
				names = append(names, c.Name())
			}
		}
		return "usage: " + cmd.CommandPath() + " <command>, one of: " + strings.Join(names, ", ")
	}
	hint := "usage: " + cmd.UseLine()
	if ex, _, _ := strings.Cut(strings.TrimSpace(cmd.Example), "\n"); ex != "" {
		hint += "; example: " + strings.TrimSpace(ex)
	}
	return hint
}

// groups makes every command that only groups others refuse a subcommand it
// does not have (cobra would print its help and exit 0) and print its help
// when called alone.
func groups(c *cobra.Command) {
	for _, s := range c.Commands() {
		if s.HasSubCommands() && !s.Runnable() {
			s.Args = func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					return nil
				}
				return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			s.Run = func(cmd *cobra.Command, _ []string) { _ = cmd.Help() }
		}
		groups(s)
	}
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:   "tiffin",
		Short: "Your app in a box",
		Long: "Tiffin runs your whole app stack on one Linux box, and agents can operate it safely.\n\n" +
			"Every change is planned first and applied only with the plan's hash:\n" +
			"  tiffin plan            # what would change, and how risky it is\n" +
			"  tiffin apply --confirm <hash>\n" +
			"  tiffin changes list    # what changed, who did it and why\n" +
			"  tiffin undo <change>   # put it back",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Runs after cobra has parsed flags and checked arguments.
		PersistentPreRun: func(*cobra.Command, []string) { a.started = true },
	}
	env := a.io.Env
	pf := root.PersistentFlags()
	pf.BoolVar(&a.jsonOut, "json", false, "print JSON (the default when stdout is not a terminal)")
	pf.StringVar(&a.url, "url", env("TIFFIN_URL"), "box API URL (env TIFFIN_URL); empty means the local box in --home")
	pf.StringVar(&a.home, "home", orDefault(env("TIFFIN_HOME"), defaultHome(env)), "local box data directory (env TIFFIN_HOME)")
	pf.StringVar(&a.session, "session", env("TIFFIN_SESSION"), "agent session label recorded on changes (env TIFFIN_SESSION)")
	pf.StringVar(&a.model, "model", env("TIFFIN_MODEL"), "the model an agent runs, shown beside its name in the change log (env TIFFIN_MODEL)")
	a.token = env("TIFFIN_TOKEN")
	if a.session == "" && env("CLAUDECODE") == "1" && len(env("CLAUDE_CODE_SESSION_ID")) >= 8 {
		a.session = "claude-code:" + env("CLAUDE_CODE_SESSION_ID")[:8] // tells one Claude Code session from another in History
	}
	root.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		a.started = true
		a.homeExplicit = env("TIFFIN_HOME") != "" || cmd.Flags().Changed("home")
	}
	root.CompletionOptions.HiddenDefaultCmd = true

	root.AddCommand(a.versionCmd(), a.planCmd(), a.applyCmd(), a.undoCmd(), a.initCmd(),
		a.serveCmd(), a.mcpCmd(), a.doctorCmd(), a.ownerCmd(), a.pullCmd(),
		a.upCmd(), a.downCmd(), a.loginCmd(), a.trustCmd(), a.selfUpdateCmd(), a.provisionCmd(), a.boxCmd(), a.domainCmd(), a.sdkCmd(), a.projectsCmd())
	root.AddCommand(a.runtimeCmds()...) // deploy, logs, rollback, git-remote (internal/cli/deploy.go)
	a.generate(root, api.New(api.Deps{}))
	if s := find(root, "storage"); s != nil {
		if q := find(s, "quota"); q != nil {
			q.AddCommand(a.quotaGetCmd())
		}
	}
	groups(root)
	return root
}

func defaultHome(env func(string) string) string {
	if h := env("HOME"); h != "" {
		return filepath.Join(h, ".tiffin")
	}
	return ".tiffin"
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// tty reports whether to render for humans.
func (a *app) tty() bool {
	if a.jsonOut {
		return false
	}
	if a.io.TTY != nil {
		return *a.io.TTY
	}
	f, ok := a.io.Out.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (a *app) fail(msg string) { a.failWith(msg, "") }

func (a *app) failWith(msg, hint string) {
	if a.tty() {
		fmt.Fprintln(a.io.Err, "tiffin: "+msg)
		if hint != "" {
			fmt.Fprintln(a.io.Err, "  "+strings.Replace(hint, "; example: ", "\n  example: ", 1))
		}
		return
	}
	out := map[string]any{"title": "error", "status": 0, "code": "cli", "detail": msg}
	if hint != "" {
		out["hint"] = hint
	}
	writeJSON(a.io.Out, out)
}
