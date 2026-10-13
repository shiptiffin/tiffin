package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Railpack hands a Node app's install step the whole source when any
// package.json has a file: dependency or an install script (preinstall,
// postinstall, prepare): the script may read anything. Then every commit,
// however small, reinstalls node_modules and every deploy's image carries a
// new copy of it. A file: dependency alone needs only the package it names,
// so narrowInstall gives the step what Railpack gives any other app (the
// package files and lockfiles, copied one by one) plus those packages: the
// install stays cached until one of them changes.

// installFiles are the names Railpack copies into an install step it does
// not give the whole source (SupportingInstallFiles in its node provider).
var installFiles = map[string]bool{
	"package.json": true, "package.json5": true, "package-lock.json": true, "pnpm-workspace.yaml": true,
	"yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true, "bun.lock": true, "bunfig.toml": true,
	".yarn": true, ".yarnrc.yml": true, ".npmrc": true, ".node-version": true, ".nvmrc": true,
	"patches": true, ".pnpm-patches": true, "prisma": true,
}

// installScripts are the scripts that keep the whole source in the step.
var installScripts = []string{"preinstall", "install", "postinstall", "prepare"}

// narrowInstall rewrites the plan's install step as above, when a file:
// dependency is the only reason it has the whole source. It leaves the plan
// alone in every other case, and reports whether it changed it.
func narrowInstall(planPath, srcDir string, env map[string]string, log io.Writer) (bool, error) {
	if env["NODE_INSTALL_PATTERNS"] != "" {
		return false, nil // the app adds files of its own to the install
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return false, err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return false, err
	}
	var install map[string]any
	steps, _ := plan["steps"].([]any)
	for _, s := range steps {
		if step, _ := s.(map[string]any); step != nil && step["name"] == "install" {
			install = step
		}
	}
	if install == nil {
		return false, nil
	}
	inputs, _ := install["inputs"].([]any)
	kept := make([]any, 0, len(inputs))
	whole := false
	for _, in := range inputs {
		if m, _ := in.(map[string]any); m != nil && m["local"] == true {
			whole = true
			continue
		}
		kept = append(kept, in)
	}
	if !whole {
		return false, nil
	}
	files, locals, ok := installInputs(srcDir)
	if !ok || len(locals) == 0 {
		return false, nil
	}
	copies := make([]any, 0, len(files)+len(locals))
	seen := map[string]bool{}
	for _, f := range append(files, locals...) {
		if !seen[f] {
			seen[f] = true
			copies = append(copies, map[string]any{"src": f, "dest": f})
		}
	}
	// The copies go before the first command that runs something other than
	// mkdir, where Railpack puts them: the install itself.
	cmds, _ := install["commands"].([]any)
	at := len(cmds)
	for i, c := range cmds {
		m, _ := c.(map[string]any)
		if cmd, _ := m["cmd"].(string); cmd != "" && !strings.HasPrefix(cmd, "mkdir ") {
			at = i
			break
		}
	}
	install["commands"] = append(append(append([]any{}, cmds[:at]...), copies...), cmds[at:]...)
	install["inputs"] = kept
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(planPath, out, 0o644); err != nil {
		return false, err
	}
	fmt.Fprintf(log, "==> install: the package files and the local packages it uses (%s), not the whole source, so unchanged dependencies stay cached\n", strings.Join(locals, ", "))
	return true, nil
}

// installInputs lists, relative to srcDir, the package files Railpack would
// copy and the targets of file: dependencies. ok is false when the install
// needs the whole source after all: an install script, a target outside
// srcDir or missing, or a package.json it cannot read.
func installInputs(srcDir string) (files, locals []string, ok bool) {
	root, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return nil, nil, false
	}
	bad := false
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && (name == "node_modules" || name == ".git") {
			return filepath.SkipDir
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if installFiles[name] || strings.HasPrefix(name, ".pnp.") {
			files = append(files, filepath.ToSlash(rel))
		}
		if d.IsDir() || (name != "package.json" && name != "package.json5") {
			if d.IsDir() && installFiles[name] {
				return filepath.SkipDir // copied whole
			}
			return nil
		}
		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		raw, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(raw, &pkg) != nil {
			if name == "package.json" {
				bad = true
			}
			return nil
		}
		for _, s := range installScripts {
			if pkg.Scripts[s] != "" {
				bad = true
			}
		}
		for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
			for _, v := range deps {
				target, isFile := strings.CutPrefix(v, "file:")
				if !isFile {
					continue
				}
				t := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
				if !exists(t) || !inside(root, t) {
					bad = true
					continue
				}
				r, _ := filepath.Rel(root, t)
				locals = append(locals, filepath.ToSlash(r))
			}
		}
		return nil
	})
	if err != nil || bad {
		return nil, nil, false
	}
	sort.Strings(files)
	sort.Strings(locals)
	return files, locals, true
}
