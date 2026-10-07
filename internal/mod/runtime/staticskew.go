package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/edge"
)

// A page of a static site loaded before a deploy asks for the hashed files
// of its own release afterwards (a lazy chunk of a Vite SPA, say), which
// the new release no longer has. So a new release of a static site takes
// the previous release's hashed files it lacks along, as links, for
// assetsKept after that release stopped being live: open tabs keep working,
// as with server apps' client assets (see ServeAsset).

// carriedFile lists, in a deploy's work folder, the hashed files its
// static release took from earlier releases, and when each one's own
// release stopped being live.
const carriedFile = "carried.json"

// precompressed are the extensions of the compressed copies next to a
// file (finishStatic): they travel with it.
var precompressed = []string{".zst", ".gz", ".br"}

// keepHashed links the hashed files of the environment's live static
// release that d's files lack into d's, and records them.
func (r *rt) keepHashed(d *Deploy, log io.Writer) {
	prevRoot, err := filepath.EvalSymlinks(r.staticLink(d.Project, d.App, d.Preview))
	if err != nil || prevRoot == d.StaticRoot {
		return
	}
	prev := filepath.Base(prevRoot)
	now := time.Now().UTC()
	carried := map[string]time.Time{}
	if raw, err := os.ReadFile(filepath.Join(r.workDir(&Deploy{Project: d.Project, App: d.App, ID: prev}), carriedFile)); err == nil {
		_ = json.Unmarshal(raw, &carried)
	}
	kept := map[string]time.Time{}
	n := keepHashedFiles(prevRoot, d.StaticRoot, carried, now, kept)
	if len(kept) == 0 {
		return
	}
	raw, _ := json.Marshal(kept)
	if err := os.WriteFile(filepath.Join(r.workDir(d), carriedFile), raw, 0o644); err != nil {
		r.p.Log.Warn("runtime: record kept files", "deploy", d.ID, "err", err)
	}
	fmt.Fprintf(log, "==> pages of earlier releases keep their hashed files: %d kept for %s after their release\n", n, assetsKept)
}

// keepHashedFiles links into root the hashed files of prevRoot that root
// lacks: prevRoot's own (their release ends now) and those it carried
// itself for less than assetsKept. kept gets each one's end time; it
// returns how many it linked.
func keepHashedFiles(prevRoot, root string, carried map[string]time.Time, now time.Time, kept map[string]time.Time) int {
	n := 0
	_ = filepath.WalkDir(prevRoot, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(prevRoot, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, ext := range precompressed {
			if strings.HasSuffix(rel, ext) {
				return nil // goes with its file
			}
		}
		if !edge.HashedAsset("/"+rel) || exists(filepath.Join(root, filepath.FromSlash(rel))) {
			return nil
		}
		ended := now
		if at, ok := carried[rel]; ok {
			if now.Sub(at) >= assetsKept {
				return nil
			}
			ended = at
		}
		dest := filepath.Join(root, filepath.FromSlash(rel))
		if os.MkdirAll(filepath.Dir(dest), 0o755) != nil || linkOrCopy(p, dest) != nil {
			return nil
		}
		for _, ext := range precompressed {
			if exists(p + ext) {
				_ = linkOrCopy(p+ext, dest+ext)
			}
		}
		kept[rel] = ended
		n++
		return nil
	})
	return n
}
