package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// containerdLoad is what nerdctl load (containerd's import through the
// transfer service) stores from a tarball: the index it reads by digest
// ("import@sha256:..."), then each image the index lists, by its name
// annotations (a tag-only name under nerdctl's "import" prefixes) or, with
// none, by digest. An OCI layout is read from index.json, a docker archive
// from manifest.json's RepoTags (normalized). It decodes the way containerd
// does (encoding/json into structs, path-cleaned entry names) and follows a
// symlinked index.json, as containerd once did. content stands for what the
// names point at.
func containerdLoad(r io.Reader) (names []string, content string, err error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	content = "loaded:" + hex.EncodeToString(sum[:6])
	digestName := func(b []byte) string {
		s := sha256.Sum256(b)
		return loadDigestPrefix + hex.EncodeToString(s[:])
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	blobs, links := map[string][]byte{}, map[string]string{}
	var layout bool
	var mfRaw []byte
	var manifests []struct{ RepoTags []string }
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		name := path.Clean(hdr.Name)
		if hdr.Typeflag == tar.TypeSymlink {
			links[name] = path.Join(path.Dir(name), hdr.Linkname)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, "", err
		}
		switch {
		case name == "oci-layout":
			layout = true
		case name == "manifest.json":
			if err := json.Unmarshal(b, &manifests); err != nil {
				return nil, "", err
			}
			mfRaw = b
		case hdr.Typeflag == tar.TypeReg:
			blobs[name] = b
		}
	}
	for name, target := range links {
		blobs[name] = blobs[target]
	}
	if layout {
		idxRaw, ok := blobs["index.json"]
		if !ok {
			return nil, "", errors.New("missing index.json in OCI layout")
		}
		var idx struct {
			Manifests []struct {
				Digest      string
				Annotations map[string]string
			}
		}
		if err := json.Unmarshal(idxRaw, &idx); err != nil {
			return nil, "", err
		}
		names = append(names, digestName(idxRaw))
		for _, m := range idx.Manifests {
			for _, prefix := range []string{"import", "import-today"} {
				switch n := importName(m.Annotations, prefix); {
				case n != "":
					names = append(names, n)
				case prefix == "import":
					names = append(names, "import@"+m.Digest)
				}
			}
		}
		return names, content, nil
	}
	if manifests == nil {
		return nil, "", errors.New("unrecognized image format")
	}
	names = append(names, digestName(mfRaw)) // the index containerd makes of it
	for i, m := range manifests {
		if len(m.RepoTags) == 0 {
			names = append(names, digestName(fmt.Appendf(nil, "%d %s", i, mfRaw)))
		}
		for _, t := range m.RepoTags {
			names = append(names, dockerName(t))
		}
	}
	return names, content, nil
}

// importName is the name containerd's import store gives an index entry
// under one of nerdctl's prefixes: a full io.containerd.image.name as it
// is (an incomplete one is no name), else a ref.name, a tag-only one under
// the prefix.
func importName(ann map[string]string, prefix string) string {
	full := func(s string) bool { return strings.ContainsAny(s, "/:@") }
	if n := ann[annImageName]; n != "" {
		if full(n) {
			return n
		}
		return ""
	}
	if n := ann[annRefName]; n != "" {
		if full(n) {
			return n
		}
		return prefix + ":" + n
	}
	return ""
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
	layer   = "LAYER BYTES"
	victim  = "docker.io/tiffin/other-web:dep_1" // another project's release
	testRef = "docker.io/tiffin/shop-api:dep_test"
)

var (
	dockerManifest = `[{"Config":"blobs/sha256/cfg","RepoTags":["` + victim + `"],"Layers":["blobs/sha256/aaa"]},` +
		`{"Config":"blobs/sha256/cfg","repotags":["postgres:18"],"Layers":[]}]`
	ociIndex = `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaa","size":1,` +
		`"annotations":{"io.containerd.image.name":"` + victim + `","org.opencontainers.image.ref.name":"dep_1","org.opencontainers.image.created":"2026-10-01"}},` +
		`{"digest":"sha256:bbb","Annotations":{"org.opencontainers.image.ref.name":"latest"}},` +
		// Two spellings: containerd merges both into one map.
		`{"digest":"sha256:ccc","annotations":{"io.containerd.image.name":"docker.io/library/postgres:18"},"ANNOTATIONS":{"keep":"me"}}]}`
	dockerArchive = []tarEntry{
		{name: "blobs/sha256/aaa", body: layer},
		{name: "manifest.json", body: dockerManifest},
	}
	ociLayout = []tarEntry{
		{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`},
		{name: "blobs/sha256/aaa", body: layer},
		{name: "./index.json", body: ociIndex},
	}
	// nerdctl save writes both: an OCI layout with a docker manifest.json.
	bothFiles = []tarEntry{
		{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`},
		{name: "blobs/sha256/aaa", body: layer},
		{name: "index.json", body: ociIndex},
		{name: "manifest.json", body: dockerManifest},
	}
)

// withoutDigestNames drops the digest names nerdctl gives a load's index.
func withoutDigestNames(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), isLoadDigestName)
}

// Every name a tarball carries becomes ref; everything else passes as it is.
func TestNameImage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		gz      bool
	}{
		{"docker archive", dockerArchive, false},
		{"OCI layout", ociLayout, false},
		{"OCI layout and manifest.json", bothFiles, false},
		{"gzip", bothFiles, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if names, _, err := containerdLoad(bytes.NewReader(imageTar(t, false, tc.entries...))); err != nil || !slices.Contains(names, victim) {
				t.Fatalf("the tarball should name %s: %v %v", victim, names, err)
			}
			var out bytes.Buffer
			if err := nameImage(bytes.NewReader(imageTar(t, tc.gz, tc.entries...)), &out, testRef); err != nil {
				t.Fatal(err)
			}
			names, _, err := containerdLoad(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			named := withoutDigestNames(names)
			if len(named) == 0 || slices.ContainsFunc(named, func(n string) bool { return dockerName(n) != testRef }) || len(names)-len(named) != 1 {
				t.Fatalf("loaded as %v, want %s (and the index's digest name)", names, testRef)
			}
			files := tarFiles(t, out.Bytes())
			if files["blobs/sha256/aaa"] != layer || len(files) != len(tc.entries) {
				t.Fatalf("contents changed: %v", slices.Sorted(maps.Keys(files)))
			}
			if mf, ok := files["manifest.json"]; ok {
				var ms []struct{ RepoTags []string }
				if err := json.Unmarshal([]byte(mf), &ms); err != nil || len(ms) != 2 {
					t.Fatalf("manifest.json: %s %v", mf, err)
				}
				for _, m := range ms {
					if !slices.Equal(m.RepoTags, []string{testRef}) {
						t.Errorf("RepoTags %v in %s", m.RepoTags, mf)
					}
				}
			}
			idx := files["./index.json"] + files["index.json"]
			if idx == "" {
				return
			}
			var x struct {
				Manifests []struct {
					Digest      string
					Annotations map[string]string
				}
			}
			if err := json.Unmarshal([]byte(idx), &x); err != nil || len(x.Manifests) != 3 {
				t.Fatalf("index.json: %s %v", idx, err)
			}
			for _, m := range x.Manifests {
				if m.Annotations[annImageName] != testRef || m.Annotations[annRefName] != "dep_test" {
					t.Errorf("%s is named %v", m.Digest, m.Annotations)
				}
			}
			if x.Manifests[0].Annotations["org.opencontainers.image.created"] != "2026-10-01" || x.Manifests[2].Annotations["keep"] != "me" {
				t.Errorf("index.json lost more than names: %s", idx)
			}
		})
	}
	for name, entries := range map[string][]tarEntry{
		"a symlinked index.json":          {{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`}, {name: "evil", body: `{}`}, {name: "index.json", link: "evil"}},
		"a symlinked manifest.json":       {{name: "evil", body: `[]`}, {name: "./manifest.json", link: "evil"}},
		"a manifest.json that is not one": {{name: "manifest.json", body: `{"RepoTags":"x"}`}},
		"a null image in manifest.json":   {{name: "manifest.json", body: `[null]`}},
		"an index.json that is not one":   {{name: "oci-layout", body: `{}`}, {name: "index.json", body: `{"manifests":{"a":1}}`}},
		"odd annotations":                 {{name: "oci-layout", body: `{}`}, {name: "index.json", body: `{"manifests":[{"annotations":["x"]}]}`}},
		"an empty tarball":                {},
	} {
		if err := nameImage(bytes.NewReader(imageTar(t, false, entries...)), io.Discard, testRef); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := nameImage(strings.NewReader("not a tarball at all, just text"), io.Discard, testRef); err == nil {
		t.Error("text was accepted")
	}
}

// A load keeps ref only: the index's digest name is dropped, and any other
// name refuses the load and is dropped with ref.
func TestSettleLoad(t *testing.T) {
	dg := loadDigestPrefix + strings.Repeat("ab", 32)
	store := map[string]bool{}
	has := func(n string) bool { return store[n] }
	remove := func(n string) error {
		if !store[n] {
			return errors.New("no such image")
		}
		delete(store, n)
		return nil
	}
	for _, tc := range []struct {
		name   string
		loaded []string
		err    string
	}{
		{"ref and the index", []string{dg, testRef}, ""},
		{"ref as nerdctl reads it", []string{"tiffin/shop-api:dep_test"}, ""},
		{"another name too", []string{dg, testRef, victim}, victim},
		{"a short digest name", []string{"import@sha256:abc", testRef}, "import@sha256:abc"},
		{"a tag-only name", []string{"import-2026-10-05:latest", testRef}, "import-2026-10-05:latest"},
		{"no image", []string{dg}, "contained no image"},
	} {
		clear(store)
		for _, n := range tc.loaded {
			store[n] = true
		}
		if tc.loaded[0] == "tiffin/shop-api:dep_test" {
			store = map[string]bool{testRef: true}
		}
		var log bytes.Buffer
		err := settleLoad(tc.loaded, testRef, has, remove, &log)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err == "" && !slices.Equal(slices.Collect(maps.Keys(store)), []string{testRef}):
			t.Errorf("%s: left %v", tc.name, store)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: %v, want an error about %s", tc.name, err, tc.err)
		case tc.err != "" && len(store) != 0:
			t.Errorf("%s: left %v", tc.name, store)
		}
	}
}

func TestImageNames(t *testing.T) {
	for in, want := range map[string]string{
		"import@sha256:fc97": "docker.io/library/import@sha256:fc97", // what nerdctl tag looked for
		"b1f99f52ca59":       "docker.io/library/b1f99f52ca59:latest",
		"postgres:18":        "docker.io/library/postgres:18",
		"me/app":             "docker.io/me/app:latest",
		"ghcr.io/me/app:v1":  "ghcr.io/me/app:v1",
		"localhost:5000/app": "localhost:5000/app:latest",
		testRef:              testRef,
	} {
		if got := dockerName(in); got != want {
			t.Errorf("dockerName(%q) = %q, want %q", in, got, want)
		}
	}
	if refTag(testRef) != "dep_test" || refTag("localhost:5000/app") != "latest" {
		t.Error("refTag")
	}
	got := loadedNames("unpacking...\nLoaded image: " + testRef + "\nLoaded image: import@sha256:ab\nLoaded image: " + testRef + "\n")
	if !slices.Equal(got, []string{testRef, "import@sha256:ab"}) {
		t.Errorf("loadedNames: %v", got)
	}
	if !isLoadDigestName(loadDigestPrefix+strings.Repeat("0f", 32)) || isLoadDigestName(loadDigestPrefix+"0f") || isLoadDigestName("x@sha256:"+strings.Repeat("0f", 32)) {
		t.Error("isLoadDigestName")
	}
}

// A prebuilt image named like another project's release (or a box image)
// is loaded under the release's own name only: no other name appears or
// is replaced.
func TestReleaseImageTarKeepsOnlyItsName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	images := func() map[string]string {
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		return maps.Clone(h.eng.images)
	}
	h.eng.mu.Lock()
	h.eng.images[victim] = "the other project's image"
	h.eng.mu.Unlock()
	for _, entries := range [][]tarEntry{dockerArchive, ociLayout, bothFiles} {
		file := filepath.Join(t.TempDir(), "image.tar")
		if err := os.WriteFile(file, imageTar(t, false, entries...), 0o644); err != nil {
			t.Fatal(err)
		}
		before := images()
		d, err := h.m.Release(ctx, "shop", "api", ReleaseSource{ImageTar: file}, "tok_test")
		if err != nil || d.Status != StatusLive {
			t.Fatalf("release: %v %+v", err, d)
		}
		after := images()
		var added []string
		for n, c := range after {
			if before[n] == "" {
				added = append(added, n)
			} else if before[n] != c {
				t.Errorf("the load replaced %s", n)
			}
		}
		if !slices.Equal(added, []string{d.Image}) {
			t.Fatalf("names added by the load: %v (want only %s)", added, d.Image)
		}
	}
	// The fake does take names from a tarball as it is: such a load is
	// refused, and the name it took is dropped rather than left pointing at
	// the tarball's image.
	var log bytes.Buffer
	err := h.eng.LoadImage(ctx, bytes.NewReader(imageTar(t, false, dockerArchive...)), testRef, &log)
	if err == nil || !strings.Contains(err.Error(), victim) {
		t.Fatalf("a load that named %s: %v", victim, err)
	}
	if left := images(); left[victim] != "" || left[testRef] != "" {
		t.Fatalf("a refused load left names behind: %v", left)
	}
	// nerdctl tag reads a name as nerdctl does: a load's digest name is not one.
	if err := h.eng.TagImage(ctx, loadDigestPrefix+strings.Repeat("ab", 32), "x"); err == nil {
		t.Fatal("the fake tagged a digest name")
	}
	bad := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(bad, []byte("not a tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.m.Release(ctx, "shop", "api", ReleaseSource{ImageTar: bad}, "tok_test"); d == nil || d.Status != StatusFailed || !strings.Contains(d.Error, "not an image tarball") {
		t.Fatalf("release of a bad tarball: %+v", d)
	}
}
