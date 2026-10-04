// Package sdkpkg carries tiffin-sdk and @tiffin/react inside the binary and
// vendors them into a project, because neither is on npm: `tiffin sdk add`
// (and `tiffin init`) writes vendor/<name>-<version>.tgz and points
// package.json at it ("tiffin-sdk": "file:./vendor/tiffin-sdk-0.1.0.tgz"), so
// `bun install` and `npm install` work without the registry for them, on a
// laptop and in the box's builds alike.
//
// files/ is built from packages/sdk and packages/react by scripts/sdk-pack.ts
// (make sdk) and committed, like the dashboard build.
package sdkpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed all:files
var files embed.FS

// Package is one vendorable package.
type Package struct {
	Name    string // the npm name: "tiffin-sdk", "@tiffin/react"
	Version string
	dir     string // under files/
}

// File is the tarball's name in vendor/: "tiffin-sdk-0.1.0.tgz".
func (p Package) File() string { return p.base() + "-" + p.Version + ".tgz" }

// base is the name without a scope: "tiffin-react" for "@tiffin/react".
func (p Package) base() string { return p.dir }

// Spec is the package.json dependency value: "file:./vendor/<file>".
func (p Package) Spec() string { return "file:./vendor/" + p.File() }

// The packages: the SDK, and the React components (tiffin sdk add --react).
var (
	SDK   = load("tiffin-sdk")
	React = load("tiffin-react")
)

func load(dir string) Package {
	raw, err := files.ReadFile("files/" + dir + "/package.json")
	if err != nil {
		panic("sdkpkg: " + dir + " is not built (make sdk): " + err.Error())
	}
	var m struct{ Name, Version string }
	if err := json.Unmarshal(raw, &m); err != nil || m.Name == "" || m.Version == "" {
		panic("sdkpkg: bad package.json for " + dir)
	}
	return Package{Name: m.Name, Version: m.Version, dir: dir}
}

// npm's fixed tarball mtime: the same files always make the same bytes, so a
// lockfile's integrity hash stays valid when the tarball is written again.
var epoch = time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC)

// Tarball packs the package the way `npm pack` lays it out (package/...),
// byte for byte reproducible.
func (p Package) Tarball() ([]byte, error) {
	root := "files/" + p.dir
	var names []string
	err := fs.WalkDir(files, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		b, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{Name: path.Join("package", strings.TrimPrefix(name, root+"/")), Mode: 0o644, Size: int64(len(b)),
			ModTime: epoch, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrNoPackageJSON means the directory has no package.json to add the SDK to.
var ErrNoPackageJSON = errors.New("no package.json")

// Added is what Add wrote.
type Added struct {
	Files   []string `json:"files" doc:"Tarballs written under vendor/"`
	Removed []string `json:"removed,omitempty" doc:"Older vendored versions deleted"`
	Deps    []string `json:"dependencies" doc:"package.json dependencies set, as name@spec"`
}

// Add vendors pkgs into the project at dir: writes each tarball to
// vendor/, removes other versions of it there, and sets the dependency in
// package.json (keeping its key order and two-space indentation).
func Add(dir string, pkgs ...Package) (*Added, error) {
	pj := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(pj)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoPackageJSON
	} else if err != nil {
		return nil, err
	}
	out := &Added{}
	vendor := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		tgz, err := p.Tarball()
		if err != nil {
			return nil, err
		}
		old, _ := filepath.Glob(filepath.Join(vendor, p.base()+"-*.tgz"))
		for _, o := range old {
			if filepath.Base(o) != p.File() && isVersionOf(filepath.Base(o), p.base()) {
				if err := os.Remove(o); err == nil {
					out.Removed = append(out.Removed, "vendor/"+filepath.Base(o))
				}
			}
		}
		if err := os.WriteFile(filepath.Join(vendor, p.File()), tgz, 0o644); err != nil {
			return nil, err
		}
		out.Files = append(out.Files, "vendor/"+p.File())
		raw, err = setDependency(raw, p.Name, p.Spec())
		if err != nil {
			return nil, fmt.Errorf("package.json: %w", err)
		}
		out.Deps = append(out.Deps, p.Name+"@"+p.Spec())
	}
	if err := os.WriteFile(pj, raw, 0o644); err != nil {
		return nil, err
	}
	return out, nil
}

// isVersionOf reports whether file is "<base>-<version>.tgz" (so
// tiffin-sdk-0.1.0.tgz, but not tiffin-sdk-extra-1.0.0.tgz).
func isVersionOf(file, base string) bool {
	v := strings.TrimSuffix(strings.TrimPrefix(file, base+"-"), ".tgz")
	return v != "" && v[0] >= '0' && v[0] <= '9'
}

// setDependency sets dependencies[name] = spec in a package.json, keeping
// the other keys in their order.
func setDependency(raw []byte, name, spec string) ([]byte, error) {
	top, err := decodeOrdered(raw)
	if err != nil {
		return nil, err
	}
	deps := &ordered{}
	if v, ok := top.get("dependencies"); ok {
		if deps, err = decodeOrdered(v); err != nil {
			return nil, fmt.Errorf("dependencies: %w", err)
		}
	}
	s, _ := json.Marshal(spec)
	deps.set(name, s)
	db, err := deps.marshal()
	if err != nil {
		return nil, err
	}
	top.set("dependencies", db)
	b, err := top.marshal()
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

// ordered is a JSON object that keeps its key order.
type ordered struct {
	keys []string
	vals map[string]json.RawMessage
}

func decodeOrdered(raw []byte) (*ordered, error) {
	o := &ordered{vals: map[string]json.RawMessage{}}
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.set(k, v)
	}
	return o, nil
}

func (o *ordered) get(k string) (json.RawMessage, bool) { v, ok := o.vals[k]; return v, ok }

func (o *ordered) set(k string, v json.RawMessage) {
	if o.vals == nil {
		o.vals = map[string]json.RawMessage{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *ordered) marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
