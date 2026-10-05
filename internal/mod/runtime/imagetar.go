package runtime

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

// An image tarball (deploy --prebuilt, project import) names its images:
// RepoTags in a docker archive's manifest.json, name annotations in an OCI
// layout's index.json. Loaded as it is, those names would replace whatever
// images the box keeps under them: another project's release, a base image.
// So every name in the tarball is rewritten to the release's own (ref) on
// the way in: the image arrives named ref, and none of the tarball's own
// names reaches the store.

// Annotations containerd's import names an image by (index.json manifests).
const (
	annImageName = "io.containerd.image.name"
	annRefName   = "org.opencontainers.image.ref.name"
)

const maxImageMeta = 16 << 20 // manifest.json and index.json are small

// loadImage loads the image in a tarball as ref, its own names replaced by
// ref on the way in.
func loadImage(ctx context.Context, eng Engine, file, ref string, log io.Writer) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	named := make(chan error, 1)
	go func() {
		err := nameImage(f, pw, ref)
		_ = pw.CloseWithError(err)
		named <- err
	}()
	err = eng.LoadImage(ctx, pr, ref, log)
	_ = pr.Close() // the engine may stop reading early: let the copy end
	if nerr := <-named; nerr != nil && !errors.Is(nerr, io.ErrClosedPipe) {
		return nerr
	}
	return err
}

// nameImage copies a docker or OCI image tarball (plain or gzip) to w as a
// plain tar whose every image is named ref. Containerd reads names only
// from manifest.json and index.json at the top (path-cleaned, as it
// compares them) and follows a symlinked index.json, so those must be
// regular files.
func nameImage(r io.Reader, w io.Writer, ref string) error {
	br := bufio.NewReader(r)
	r = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("not an image tarball: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	tr, tw := tar.NewReader(r), tar.NewWriter(w)
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("not an image tarball: %w", err)
		}
		entries++
		var rename func([]byte, string) ([]byte, error)
		switch path.Clean(hdr.Name) {
		case "manifest.json":
			rename = nameRepoTags
		case "index.json":
			rename = nameIndex
		}
		if rename == nil {
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("not an image tarball: %s is not a regular file", hdr.Name)
		}
		if hdr.Size > maxImageMeta {
			return fmt.Errorf("not an image tarball: %s is %d bytes", hdr.Name, hdr.Size)
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		out, err := rename(raw, ref)
		if err != nil {
			return fmt.Errorf("not an image tarball: %s: %w", hdr.Name, err)
		}
		hdr.Size = int64(len(out))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(out); err != nil {
			return err
		}
	}
	if entries == 0 {
		return errors.New("not an image tarball: it is empty")
	}
	return tw.Close()
}

// Containerd decodes these files with encoding/json, which matches keys
// regardless of case and merges what keys of the same name (in any case)
// decode into the same map: every spelling of a key is replaced by one.

// nameRepoTags makes ref the only RepoTag of each image of a docker
// manifest.json.
func nameRepoTags(raw []byte, ref string) ([]byte, error) {
	var ms []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ms); err != nil {
		return nil, err
	}
	tags, _ := json.Marshal([]string{ref})
	for _, m := range ms {
		if m == nil {
			return nil, errors.New("an image is null")
		}
		dropKeys(m, "RepoTags")
		m["RepoTags"] = tags
	}
	return json.Marshal(ms)
}

// nameIndex names each manifest of an OCI index.json ref: its name
// annotations are set to ref (and ref's tag), its other annotations kept.
func nameIndex(raw []byte, ref string) ([]byte, error) {
	var idx map[string]json.RawMessage
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, err
	}
	for k, v := range idx {
		if !strings.EqualFold(k, "manifests") {
			continue
		}
		var ms []map[string]json.RawMessage
		if err := json.Unmarshal(v, &ms); err != nil {
			return nil, err
		}
		for _, m := range ms {
			if m == nil {
				return nil, errors.New("a manifest is null")
			}
			var ann map[string]string
			for ak, av := range m {
				if strings.EqualFold(ak, "annotations") {
					if err := json.Unmarshal(av, &ann); err != nil {
						return nil, err
					}
				}
			}
			if ann == nil {
				ann = map[string]string{}
			}
			ann[annImageName], ann[annRefName] = ref, refTag(ref)
			b, err := json.Marshal(ann)
			if err != nil {
				return nil, err
			}
			dropKeys(m, "annotations")
			m["annotations"] = b
		}
		var err error
		if idx[k], err = json.Marshal(ms); err != nil {
			return nil, err
		}
	}
	return json.Marshal(idx)
}

// dropKeys deletes every spelling of key from m.
func dropKeys(m map[string]json.RawMessage, key string) {
	for k := range m {
		if strings.EqualFold(k, key) {
			delete(m, k)
		}
	}
}

// refTag is the tag of an image reference ("dep_01..." of
// "docker.io/tiffin/shop-web:dep_01...").
func refTag(ref string) string {
	if _, tag, ok := strings.Cut(ref[strings.LastIndex(ref, "/")+1:], ":"); ok {
		return tag
	}
	return "latest"
}

// dockerName is an image name the way nerdctl's commands read one:
// docker.io/library/ before a one-part name, docker.io/ before a name
// without a registry, :latest after one without a tag or digest. The names
// nerdctl load stores are kept as they are ("import@sha256:..."), which is
// why such a name cannot be tagged.
func dockerName(name string) string {
	domain, rest, ok := strings.Cut(name, "/")
	if !ok || !(strings.ContainsAny(domain, ".:") || domain == "localhost") {
		domain, rest = "docker.io", name
	}
	if domain == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	if !strings.ContainsAny(rest[strings.LastIndex(rest, "/")+1:], ":@") {
		rest += ":latest"
	}
	return domain + "/" + rest
}

// loadDigestPrefix is how nerdctl load names the index it reads a tarball
// by (index.json itself, or the one containerd makes of a manifest.json):
// by its digest, beside the names inside.
const loadDigestPrefix = "import@sha256:"

func isLoadDigestName(name string) bool {
	dg, ok := strings.CutPrefix(name, loadDigestPrefix)
	return ok && len(dg) == 64 && strings.Trim(dg, "0123456789abcdef") == ""
}

// loadedNames lists the names nerdctl load says it stored ("Loaded image:"
// lines), each once.
func loadedNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		_, name, ok := strings.Cut(line, "Loaded image: ")
		if name = strings.TrimSpace(name); ok && name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// settleLoad checks the names a load of a renamed tarball stored: ref, and
// the digest name of the tarball's index, which is dropped. Any other name
// means the tarball still named an image: that name now points at the
// tarball's image, so it is dropped too (better gone than starting another
// project's app from this image), with ref, and the load is refused. has
// and remove look up and delete an image by name.
func settleLoad(names []string, ref string, has func(string) bool, remove func(string) error, log io.Writer) error {
	var digests, others []string
	for _, name := range names {
		switch {
		case dockerName(name) == ref:
		case isLoadDigestName(name):
			digests = append(digests, name)
		default:
			others = append(others, name)
		}
	}
	drop := func(names ...string) {
		for _, name := range names {
			if err := remove(name); err != nil {
				fmt.Fprintf(log, "could not drop the loaded name %s: %v\n", name, err)
			}
		}
	}
	drop(digests...)
	if len(others) > 0 {
		drop(others...)
		if has(ref) {
			drop(ref)
		}
		return fmt.Errorf("the tarball named other images (%s); an image is loaded under its release's name only", strings.Join(others, ", "))
	}
	if !has(ref) {
		return errors.New("the tarball contained no image")
	}
	fmt.Fprintf(log, "loaded the image as %s\n", ref)
	return nil
}
