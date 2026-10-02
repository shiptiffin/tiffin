package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// evalTimeout bounds how long a config may run. Bundling is not counted.
var evalTimeout = 2 * time.Second

// ConfigNames are the file names FindConfig looks for, in priority order.
var ConfigNames = []string{"tiffin.config.ts", "tiffin.config.js", "tiffin.config.mjs", "tiffin.config.json"}

// EvalError is a failure to turn a config file into JSON: a syntax or import
// error, a thrown exception, a timeout, or a missing/invalid default export.
type EvalError struct {
	Message string
}

func (e *EvalError) Error() string { return e.Message }

// sdkShim replaces `import ... from "tiffin-sdk"` so configs evaluate with no
// node_modules present.
const sdkShim = `export const defineConfig = (c) => c;
export default defineConfig;
`

// FindConfig returns the config file in dir, trying ConfigNames in order.
func FindConfig(dir string) (string, error) {
	for _, n := range ConfigNames {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", &EvalError{Message: fmt.Sprintf("no config found in %s: create tiffin.config.ts (or .js, .mjs, .json)", dir)}
}

// Option customizes Evaluate.
type Option func(*evalOptions)

type evalOptions struct{ env map[string]string }

// WithEnv sets what process.env exposes to the config. The default is empty,
// which keeps configs deterministic.
func WithEnv(env map[string]string) Option { return func(o *evalOptions) { o.env = env } }

// Evaluate runs the config file at path and returns the validated, normalized
// manifest. See EvaluateJSON for how the config is run.
func Evaluate(path string, opts ...Option) (*Manifest, error) {
	var o evalOptions
	for _, opt := range opts {
		opt(&o)
	}
	raw, err := EvaluateJSON(path, o.env)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// EvaluateJSON runs the config file at path and returns its default export as
// JSON, with no validation or defaults applied.
//
// .ts/.js/.mjs files are bundled with esbuild (relative imports are followed;
// `tiffin-sdk` and `tiffin-sdk/config` are provided by a built-in shim) and run
// in a goja interpreter with a 2 second limit and no filesystem, network or
// module access. The only globals beyond the language are `process.env`
// (populated from env) and a silent `console`. .json files are read as-is.
func EvaluateJSON(path string, env map[string]string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(abs)
	if _, err := os.Stat(abs); err != nil {
		return nil, &EvalError{Message: fmt.Sprintf("%s: cannot read config: %v", base, errors.Unwrap(err))}
	}
	if strings.EqualFold(filepath.Ext(abs), ".json") {
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, &EvalError{Message: fmt.Sprintf("%s: cannot read config: %v", base, err)}
		}
		if !json.Valid(raw) {
			return nil, &EvalError{Message: base + ": not valid JSON"}
		}
		return raw, nil
	}
	code, err := bundle(abs)
	if err != nil {
		return nil, err
	}
	return run(base, code, env)
}

func bundle(abs string) (string, error) {
	dir, base := filepath.Dir(abs), filepath.Base(abs)
	res := api.Build(api.BuildOptions{
		EntryPoints:   []string{base},
		AbsWorkingDir: dir,
		Bundle:        true,
		Write:         false,
		Format:        api.FormatIIFE,
		GlobalName:    "__tiffin",
		Platform:      api.PlatformNeutral,
		MainFields:    []string{"module", "main"},
		Target:        api.ES2017,
		LogLevel:      api.LogLevelSilent,
		// Ignore any tsconfig.json so results do not depend on the host project.
		TsconfigRaw: "{}",
		Plugins:     []api.Plugin{sdkPlugin()},
	})
	if len(res.Errors) > 0 {
		lines := make([]string, 0, len(res.Errors))
		for _, m := range res.Errors {
			if m.Location != nil {
				lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", m.Location.File, m.Location.Line, m.Location.Column+1, m.Text))
			} else {
				lines = append(lines, base+": "+m.Text)
			}
		}
		return "", &EvalError{Message: strings.Join(lines, "\n")}
	}
	if len(res.OutputFiles) == 0 {
		return "", &EvalError{Message: base + ": bundler produced no output"}
	}
	return string(res.OutputFiles[0].Contents), nil
}

func sdkPlugin() api.Plugin {
	return api.Plugin{
		Name: "tiffin-sdk-shim",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: `^tiffin-sdk(/config)?$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: a.Path, Namespace: "tiffin-sdk"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "tiffin-sdk"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
				s := sdkShim
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
			})
		},
	}
}

func run(base, code string, env map[string]string) ([]byte, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(1000)

	envObj := vm.NewObject()
	for k, v := range env {
		_ = envObj.Set(k, v)
	}
	process := vm.NewObject()
	_ = process.Set("env", envObj)
	_ = vm.Set("process", process)
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	console := vm.NewObject()
	for _, n := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(n, noop)
	}
	_ = vm.Set("console", console)

	timer := time.AfterFunc(evalTimeout, func() { vm.Interrupt("timeout") })
	defer timer.Stop()

	if _, err := vm.RunString(code); err != nil {
		return nil, wrapRunError(base, err)
	}
	v, err := vm.RunString(`typeof __tiffin === "object" && __tiffin !== null && __tiffin.default !== undefined ? JSON.stringify(__tiffin.default) : undefined`)
	if err != nil {
		return nil, wrapRunError(base, err)
	}
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, &EvalError{Message: base + ": no usable default export; use `export default defineConfig({ ... })` with a JSON-serializable object"}
	}
	raw := []byte(v.String())
	if len(raw) == 0 || raw[0] != '{' {
		return nil, &EvalError{Message: base + ": default export must be an object, got " + string(raw)}
	}
	return raw, nil
}

func wrapRunError(base string, err error) error {
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		return &EvalError{Message: fmt.Sprintf("%s: evaluation timed out after %s (infinite loop or heavy computation in config?)", base, evalTimeout)}
	}
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return &EvalError{Message: fmt.Sprintf("%s: config threw: %s", base, exc.Value().String())}
	}
	return &EvalError{Message: fmt.Sprintf("%s: %v", base, err)}
}
