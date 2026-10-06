package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/datakit"
)

// ReleaseSource is what Release puts live without a build: an image
// tarball, an image already in the box's store, or a static site's files.
type ReleaseSource struct {
	ImageTar  string // a docker/OCI image tarball to load
	Image     string // an image in the store, tagged anew for this deploy
	StaticDir string // a static site's files (moved into place)
	Framework string // the source deploy's framework ("static+spa" keeps SPA routing)
	Commit    string
	Repo      string
	Note      string // what it is, for the build log ("duplicated from shop/web dep_...")
}

// Release puts an app of project live from an exported or copied release
// (project import and duplicate): no build, the usual health checks and
// switch. It returns when the deploy is live or failed; a failed deploy is
// returned with its error, not as an error.
func (m *Module) Release(ctx context.Context, project, app string, src ReleaseSource, by string) (*Deploy, error) {
	r, err := m.rt()
	if err != nil {
		return nil, err
	}
	spec, perr := r.checkDeployable(ctx, project, app, "", false)
	if perr != nil {
		return nil, perr
	}
	d, err := r.newDeploy(ctx, project, app, "", SourcePrebuilt, by, spec)
	if err != nil {
		return nil, err
	}
	d.Commit, d.Repo = src.Commit, src.Repo
	log, err := os.OpenFile(r.buildLogPath(d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.fail(ctx, d, err, "", io.Discard)
		return d, nil
	}
	defer log.Close()
	fmt.Fprintf(log, "==> deploy %s of %s/%s: %s\n", d.ID, project, app, src.Note)
	if err := r.release(ctx, d, spec, src, log); err != nil {
		hint := ""
		var he *healthError
		if errors.As(err, &he) {
			hint = he.hint
		}
		r.fail(ctx, d, err, hint, log)
	}
	return d, nil
}

func (r *rt) release(ctx context.Context, d *Deploy, spec *manifest.App, src ReleaseSource, log io.Writer) error {
	began := time.Now()
	d.Status = StatusBuilding
	_ = r.st.putDeploy(ctx, d)
	switch {
	case src.StaticDir != "":
		dest := filepath.Join(r.opt.DataDir, "static", d.Project, d.App, d.ID)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.Rename(src.StaticDir, dest); err != nil {
			// Another disk: copy instead.
			if _, cerr := datakit.Run(ctx, "cp", "-a", "--reflink=auto", src.StaticDir, dest); cerr != nil {
				return fmt.Errorf("put the site's files in place: %w", cerr)
			}
		}
		n, size := countFiles(dest)
		fmt.Fprintf(log, "==> serving %d files (%s)\n", n, humanBytes(size))
		d.StaticRoot = dest
		if src.Framework != "" {
			d.Framework = src.Framework
		}
	case src.ImageTar != "":
		ref := imageRef(d.Project, d.App, d.ID)
		fmt.Fprintf(log, "==> loading the image (%s)\n", humanBytes(fileSize(src.ImageTar)))
		if err := loadImage(ctx, r.eng, src.ImageTar, ref, log); err != nil {
			return err
		}
		d.Image = ref
	case src.Image != "":
		ref := imageRef(d.Project, d.App, d.ID)
		if err := r.eng.TagImage(ctx, src.Image, ref); err != nil {
			return fmt.Errorf("tag %s: %w", src.Image, err)
		}
		fmt.Fprintf(log, "==> using image %s as %s\n", src.Image, ref)
		d.Image = ref
	default:
		return errors.New("nothing to release: no image and no site")
	}
	if d.Image != "" {
		d.Digest, _ = r.eng.ImageDigest(ctx, d.Image)
		d.Assets = clientAssets("", spec) // no source to look at: the app's setting or Next.js's
	}
	now := time.Now().UTC()
	d.BuiltAt = &now
	d.BuildSecs = round1(time.Since(began).Seconds())
	d.Status = StatusStarting
	_ = r.st.putDeploy(ctx, d)
	if err := r.promote(ctx, d, spec, modeDeploy, log); err != nil {
		return err
	}
	fmt.Fprintf(log, "==> live in %.1fs total%s\n", d.TotalSecs, urlNote(d.URL))
	return nil
}

// LiveRelease returns the deploy serving an app's production, or nil when
// it has none (never deployed, or stopped).
func (m *Module) LiveRelease(ctx context.Context, project, app string) (*Deploy, error) {
	r, err := m.rt()
	if err != nil {
		return nil, err
	}
	st, err := r.st.getState(ctx, project, app, "")
	if err != nil || st.Live == "" {
		return nil, err
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return d, err
}

// GitDir is where a project's push-to-deploy repository lives (it may not
// exist).
func (m *Module) GitDir(project string) (string, error) {
	r, err := m.rt()
	if err != nil {
		return "", err
	}
	return r.gitDir(project), nil
}

// stopped reports whether project has a stop (change.KindStopped).
func (r *rt) stopped(ctx context.Context, project string) bool {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return false
	}
	_, ok := res[change.KindStopped]
	return ok
}

// reconcileStop stops every app environment of a project (stop) or brings
// them back (the stop was removed).
func (r *rt) reconcileStop(ctx context.Context, project string, stop bool) error {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return err
	}
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range states {
		if s.Project != project {
			continue
		}
		if stop {
			errs = append(errs, r.stopEnv(ctx, s))
			continue
		}
		rs, ok := res[change.KindApp+"/"+s.App]
		if !ok {
			continue // the app is gone: it stays stopped
		}
		var a manifest.App
		if err := json.Unmarshal(rs.Spec, &a); err != nil {
			return err
		}
		errs = append(errs, r.converge(ctx, s.Project, s.App, s.Preview, &a))
	}
	return errors.Join(errs...)
}
