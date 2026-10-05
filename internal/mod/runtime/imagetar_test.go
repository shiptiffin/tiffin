package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// containerdNames is what containerd's import (as nerdctl load drives it)
// would name the images of a tarball: an OCI layout's index.json
// annotations, else a docker manifest.json's RepoTags. It decodes the way
// containerd does (encoding/json into structs, path-cleaned entry names,
// symlinks followed).
func containerdNames(r io.Reader) ([]string, error) {
	tr := tar.NewReader(r)
	blobs, links := map[string][]byte{}, map[string]string{}
	var layout bool
	var manifests []struct{ RepoTags []string }
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean(hdr.Name)
		if hdr.Typeflag == tar.TypeSymlink {
			links[name] = path.Join(path.Dir(name), hdr.Linkname)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		switch {
		case name == "oci-layout":
			layout = true
		case name == "manifest.json":
			if err := json.Unmarshal(b, &manifests); err != nil {
				return nil, err
			}
		case hdr.Typeflag == tar.TypeReg:
			blobs[name] = b
		}
	}
	for name, target := range links {
		blobs[name] = blobs[target]
	}
	var names []string
	if layout {
		var idx struct {
			Manifests []struct{ Annotations map[string]string }
		}
		if err := json.Unmarshal(blobs["index.json"], &idx); err != nil {
			return nil, err
		}
		for _, m := range idx.Manifests {
			if n := m.Annotations["io.containerd.image.name"]; n != "" {
				names = append(names, n)
			} else if n := m.Annotations["org.opencontainers.image.ref.name"]; n != "" {
				names = append(names, "import-today:"+n)
			}
		}
		return names, nil
	}
	for _, m := range manifests {
		names = append(names, m.RepoTags...)
	}
	return names, nil
}

type tarEntry struct{ name, body, link string }

func imageTar(t *testing.T, gz bool, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, e.link, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		_ = zw.Close()
	}
	return buf.Bytes()
}

// tarFiles reads a plain tar into name → body.
func tarFiles(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = string(body)
	}
}

const (
	layer  = "LAYER BYTES"
	victim = "docker.io/tiffin/other-web:dep_1" // another project's release
)

var (
	dockerArchive = []tarEntry{
		{name: "blobs/sha256/aaa", body: layer},
		{name: "manifest.json", body: `[{"Config":"blobs/sha256/cfg","RepoTags":["` + victim + `"],"Layers":["blobs/sha256/aaa"]},` +
			`{"Config":"blobs/sha256/cfg","repotags":["docker.io/library/postgres:18"],"Layers":[]}]`},
	}
	ociLayout = []tarEntry{
		{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`},
		{name: "blobs/sha256/aaa", body: layer},
		{name: "./index.json", body: `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaa","size":1,` +
			`"annotations":{"io.containerd.image.name":"` + victim + `","org.opencontainers.image.ref.name":"dep_1","org.opencontainers.image.created":"2026-10-01"}},` +
			`{"digest":"sha256:bbb","Annotations":{"org.opencontainers.image.ref.name":"latest"}}]}`},
	}
)

// Every name a tarball carries is stripped; everything else passes as it is.
func TestStripImageNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		gz      bool
	}{
		{"docker archive", dockerArchive, false},
		{"OCI layout", ociLayout, false},
		{"gzip", ociLayout, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if names, err := containerdNames(bytes.NewReader(imageTar(t, false, tc.entries...))); err != nil || len(names) != 2 {
				t.Fatalf("the tarball should name 2 images: %v %v", names, err)
			}
			var out bytes.Buffer
			if err := stripImageNames(bytes.NewReader(imageTar(t, tc.gz, tc.entries...)), &out); err != nil {
				t.Fatal(err)
			}
			if names, err := containerdNames(bytes.NewReader(out.Bytes())); err != nil || len(names) != 0 {
				t.Fatalf("names left: %v %v", names, err)
			}
			files := tarFiles(t, out.Bytes())
			if files["blobs/sha256/aaa"] != layer || len(files) != len(tc.entries) {
				t.Fatalf("contents changed: %v", slices.Sorted(maps.Keys(files)))
			}
			if idx := files["./index.json"]; idx != "" && (!strings.Contains(idx, "org.opencontainers.image.created") || !strings.Contains(idx, "sha256:bbb")) {
				t.Errorf("index.json lost more than names: %s", idx)
			}
		})
	}
	for name, entries := range map[string][]tarEntry{
		"a symlinked index.json":          {{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`}, {name: "evil", body: `{}`}, {name: "index.json", link: "evil"}},
		"a manifest.json that is not one": {{name: "manifest.json", body: `{"RepoTags":"x"}`}},
		"an empty tarball":                {},
	} {
		if err := stripImageNames(bytes.NewReader(imageTar(t, false, entries...)), io.Discard); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := stripImageNames(strings.NewReader("not a tarball at all, just text"), io.Discard); err == nil {
		t.Error("text was accepted")
	}
}

// A prebuilt image named like another project's release (or a box image)
// is loaded under the release's own name only: no other name appears or
// is replaced.
func TestReleaseImageTarKeepsOnlyItsName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	images := func() []string {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return slices.Sorted(maps.Keys(h.eng.images))
	}
	for _, entries := range [][]tarEntry{dockerArchive, ociLayout} {
		file := filepath.Join(t.TempDir(), "image.tar")
		if err := os.WriteFile(file, imageTar(t, false, entries...), 0o644); err != nil {
			t.Fatal(err)
		}
		before := images()
		d, err := h.m.Release(ctx, "shop", "api", ReleaseSource{ImageTar: file}, "tok_test")
		if err != nil || d.Status != StatusLive {
			t.Fatalf("release: %v %+v", err, d)
		}
		if added := slices.DeleteFunc(images(), func(n string) bool { return slices.Contains(before, n) }); !slices.Equal(added, []string{d.Image}) {
			t.Fatalf("names added by the load: %v (want only %s)", added, d.Image)
		}
	}
	// The fake does take names from an unstripped tarball.
	if err := h.eng.LoadImage(ctx, bytes.NewReader(imageTar(t, false, dockerArchive...)), "x", io.Discard); err != nil || !slices.Contains(images(), victim) {
		t.Fatalf("the fake engine ignores tarball names: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(bad, []byte("not a tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.m.Release(ctx, "shop", "api", ReleaseSource{ImageTar: bad}, "tok_test"); d == nil || d.Status != StatusFailed || !strings.Contains(d.Error, "not an image tarball") {
		t.Fatalf("release of a bad tarball: %+v", d)
	}
}
