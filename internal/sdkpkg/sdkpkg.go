// Package sdkpkg carries @shiptiffin/sdk inside the binary, the same files
// npm has, so an app can get the SDK without the registry: `tiffin sdk add`
// (and `tiffin init`) writes vendor/shiptiffin-sdk-<version>.tgz and points
// package.json at it ("@shiptiffin/sdk": "file:./vendor/shiptiffin-sdk-0.2.1.tgz"),
// unless the app already installs it from npm, which wins. The box also
// copies the SDK's Next.js cache handlers into Next.js builds from here, so
// those never depend on what the app installed.
//
// files/ is built from packages/sdk by scripts/sdk-pack.ts (make sdk) and
// committed, like the dashboard build.
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
	Name    string // the npm name: "@shiptiffin/sdk"
	Version string
	dir     string // under files/; also the tarball's base name, as npm pack names it
}

// File is the tarball's name in vendor/: "shiptiffin-sdk-0.2.1.tgz".
func (p Package) File() string { return p.dir + "-" + p.Version + ".tgz" }

// Spec is the package.json dependency value: "file:./vendor/<file>".
func (p Package) Spec() string { return "file:./vendor/" + p.File() }

// SDK is @shiptiffin/sdk.
var SDK = load("shiptiffin-sdk")

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

// Added is what Add did.
type Added struct {
	Files   []string `json:"files,omitempty" doc:"Tarballs written under vendor/"`
	Removed []string `json:"removed,omitempty" doc:"Older vendored versions deleted"`
	Deps    []string `json:"dependencies,omitempty" doc:"package.json dependencies set, as name@spec"`
	FromNPM string   `json:"fromNpm,omitempty" doc:"The app already installs the SDK from npm (this version range), so nothing was vendored"`
}

// Add vendors the SDK into the project at dir: writes the tarball to
// vendor/, removes other versions of it there, and sets the dependency in
// package.json (keeping its key order and two-space indentation). An app
// that already depends on the SDK from npm (a version range, not file:) is
// left as it is.
func Add(dir string) (*Added, error) {
	p := SDK
	pj := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(pj)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoPackageJSON
	} else if err != nil {
		return nil, err
	}
	if spec := dependency(raw, p.Name); spec != "" && !strings.HasPrefix(spec, "file:") {
		return &Added{FromNPM: spec}, nil
	}
	tgz, err := p.Tarball()
	if err != nil {
		return nil, err
	}
	out := &Added{}
	vendor := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		return nil, err
	}
	old, _ := filepath.Glob(filepath.Join(vendor, p.dir+"-*.tgz"))
	for _, o := range old {
		if filepath.Base(o) != p.File() && isVersionOf(filepath.Base(o), p.dir) {
			if err := os.Remove(o); err == nil {
				out.Removed = append(out.Removed, "vendor/"+filepath.Base(o))
			}
		}
	}
	if err := os.WriteFile(filepath.Join(vendor, p.File()), tgz, 0o644); err != nil {
		return nil, err
	}
	out.Files = append(out.Files, "vendor/"+p.File())
	if raw, err = setDependency(raw, p.Name, p.Spec()); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	out.Deps = append(out.Deps, p.Name+"@"+p.Spec())
	if err := os.WriteFile(pj, raw, 0o644); err != nil {
		return nil, err
	}
	return out, nil
}

// dependency is the spec package.json gives name in dependencies or
// devDependencies, or "".
func dependency(raw []byte, name string) string {
	var m struct{ Dependencies, DevDependencies map[string]string }
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v := m.Dependencies[name]; v != "" {
		return v
	}
	return m.DevDependencies[name]
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

// NextCacheHandlers returns the SDK's Next.js cache handlers as plain ESM
// files in one directory (name → contents): cache-handler.js and
// use-cache.js, which import ./store.js, which imports ./resp.js. The box
// writes them into Next.js builds next to its adapter, so an app gets the
// shared cache without depending on the SDK.
func NextCacheHandlers() (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, from := range map[string]string{
		"resp.js":          "dist/resp.js",
		"store.js":         "dist/next/store.js",
		"cache-handler.js": "dist/next/cache-handler.js",
		"use-cache.js":     "dist/next/use-cache.js",
	} {
		b, err := files.ReadFile("files/" + SDK.dir + "/" + from)
		if err != nil {
			return nil, err
		}
		// One directory here: store.js's "../resp.js" is "./resp.js".
		out[name] = bytes.ReplaceAll(b, []byte(`from "../resp.js"`), []byte(`from "./resp.js"`))
	}
	return out, nil
}
