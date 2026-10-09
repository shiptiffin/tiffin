package runtime

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/mod/runtime/ghapp"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

type gitInspectBody struct {
	URL string `json:"url" minLength:"1" maxLength:"2048" example:"https://gitlab.com/owner/repo" doc:"https URL of a public git repository, as for a git deploy"`
	Ref string `json:"ref,omitempty" maxLength:"200" doc:"Branch, tag or full commit SHA. Default: the repository's default branch."`
}

// GitInspect is what the box sees in a public repository before deploying it.
type GitInspect struct {
	Roots []RepoRoot `json:"roots" doc:"Folders that look like apps, most likely first, each with the framework the box would use (as for a GitHub repository)"`
}

func (m *Module) registerGitInspect(a huma.API) {
	op := api.Outbound(api.Op("inspect-git", http.MethodPost, "/v1/git/inspect", "git inspect", api.RiskRead, "Look inside a public git repository",
		"What the box sees in a public repository before deploying it from its URL: the folders that look like apps and the "+
			"framework the box would use for each (next, hono, bun or static), or the framework it can't run yet. It fetches "+
			"the commit the same way a git deploy does (https only, a public host, depth 1, size-capped) and keeps nothing.", "apps"))
	op.Errors = append(op.Errors, 422)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *struct{ Body gitInspectBody }) (*struct{ Body GitInspect }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		gs, err := checkGitSource(ctx, in.Body.URL, in.Body.Ref, "", r.resolve)
		var ue *gitURLError
		if errors.As(err, &ue) {
			return nil, problem(422, "validation", ue.msg, ue.hint)
		}
		if err != nil {
			return nil, err
		}
		roots, err := inspectGit(ctx, gs)
		var be *BuildError
		if errors.As(err, &be) {
			return nil, problem(422, "validation", be.Msg, be.Hint)
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body GitInspect }{GitInspect{Roots: roots}}, nil
	}))
}

// inspectGit fetches a repository the way a git deploy does and runs the
// GitHub picker's detection over its files, so a pasted URL gets the same
// guess. Only plain files count: links are never followed.
func inspectGit(ctx context.Context, gs *gitSource) ([]RepoRoot, error) {
	dir, err := os.MkdirTemp("", "tiffin-inspect-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := fetchGit(ctx, gs, dir, io.Discard); err != nil {
		return nil, err
	}
	var tree []ghapp.TreeEntry
	err = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if e.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			tree = append(tree, ghapp.TreeEntry{Path: filepath.ToSlash(rel), Type: "blob"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	read := func(rel string) ([]byte, error) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
			return nil, errors.New("not a plain file under 1 MiB")
		}
		return os.ReadFile(p)
	}
	return detectRoots(tree, read), nil
}
