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
	code    int // exit code chosen by the last command
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
	err := root.ExecuteContext(ctx)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				a.fail(ee.msg)
			}
			return ee.code
		}
		// cobra usage errors: unknown command, bad flags, wrong arg count.
		a.fail(err.Error())
		return ExitInvalid
	}
	return a.code
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
	}
	env := a.io.Env
	pf := root.PersistentFlags()
	pf.BoolVar(&a.jsonOut, "json", false, "print JSON (the default when stdout is not a terminal)")
	pf.StringVar(&a.url, "url", env("TIFFIN_URL"), "box API URL (env TIFFIN_URL); empty means the local box in --home")
	pf.StringVar(&a.home, "home", orDefault(env("TIFFIN_HOME"), defaultHome(env)), "local box data directory (env TIFFIN_HOME)")
	pf.StringVar(&a.session, "session", env("TIFFIN_SESSION"), "agent session label recorded on changes (env TIFFIN_SESSION)")
	a.token = env("TIFFIN_TOKEN")
	root.CompletionOptions.HiddenDefaultCmd = true

	root.AddCommand(a.versionCmd(), a.planCmd(), a.applyCmd(), a.undoCmd(), a.initCmd(),
		a.serveCmd(), a.mcpCmd(), a.doctorCmd(), a.ownerCmd())
	a.generate(root, api.New(api.Deps{}))
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

func (a *app) fail(msg string) {
	if a.tty() {
		fmt.Fprintln(a.io.Err, "tiffin: "+msg)
		return
	}
	writeJSON(a.io.Out, map[string]any{"title": "error", "status": 0, "code": "cli", "detail": msg})
}
