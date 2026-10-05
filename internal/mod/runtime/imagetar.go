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
	"strings"
)

// An image tarball (deploy --prebuilt, project import) names its images:
// RepoTags in a docker archive's manifest.json, name annotations in an OCI
// layout's index.json. Loaded as it is, those names would replace whatever
// images the box keeps under them: another project's release, a base image.
// So the tarball is loaded with every name stripped: containerd then knows
// the image only by its digest, and Tiffin names it for the release.

// Annotations containerd's import names an image by (index.json manifests).
var imageNameAnnotations = []string{"io.containerd.image.name", "org.opencontainers.image.ref.name"}

const maxImageMeta = 16 << 20 // manifest.json and index.json are small

// loadImage loads the image in a tarball as ref, its own names stripped on
// the way in.
func loadImage(ctx context.Context, eng Engine, file, ref string, log io.Writer) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	stripped := make(chan error, 1)
	go func() {
		err := stripImageNames(f, pw)
		_ = pw.CloseWithError(err)
		stripped <- err
	}()
	err = eng.LoadImage(ctx, pr, ref, log)
	_ = pr.Close() // the engine may stop reading early: let the copy end
	if serr := <-stripped; serr != nil && !errors.Is(serr, io.ErrClosedPipe) {
		return serr
	}
	return err
}

// stripImageNames copies a docker or OCI image tarball (plain or gzip) to w
// as a plain tar without image names. Containerd reads names only from
// manifest.json and index.json at the top (path-cleaned, as it compares
// them) and follows a symlinked index.json, so those must be regular files.
func stripImageNames(r io.Reader, w io.Writer) error {
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
		var strip func([]byte) ([]byte, error)
		switch path.Clean(hdr.Name) {
		case "manifest.json":
			strip = stripRepoTags
		case "index.json":
			strip = stripRefNames
		}
		if strip == nil {
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
		out, err := strip(raw)
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
// regardless of case; strings.EqualFold folds the same way.

// stripRepoTags drops RepoTags from each image of a docker manifest.json.
func stripRepoTags(raw []byte) ([]byte, error) {
	var ms []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ms); err != nil {
		return nil, err
	}
	for _, m := range ms {
		for k := range m {
			if strings.EqualFold(k, "RepoTags") {
				delete(m, k)
			}
		}
	}
	return json.Marshal(ms)
}

// stripRefNames drops the name annotations of each manifest in an OCI
// index.json.
func stripRefNames(raw []byte) ([]byte, error) {
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
			for ak, av := range m {
				if !strings.EqualFold(ak, "annotations") {
					continue
				}
				var ann map[string]string
				if err := json.Unmarshal(av, &ann); err != nil {
					return nil, err
				}
				for _, name := range imageNameAnnotations {
					delete(ann, name)
				}
				b, err := json.Marshal(ann)
				if err != nil {
					return nil, err
				}
				m[ak] = b
			}
		}
		var err error
		if idx[k], err = json.Marshal(ms); err != nil {
			return nil, err
		}
	}
	return json.Marshal(idx)
}
