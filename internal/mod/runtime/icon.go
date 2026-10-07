package runtime

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/projicon"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// Project icons. After each production deploy of the app that stands for
// its project, the box asks that app for its icon (projicon.Infer) and
// keeps it when it changed. People can upload one instead, or choose the
// letters. The dashboard reads the icon through the API; email reads a
// PNG from a public URL that names no project.

// IconImage is one of a project's icons.
type IconImage struct {
	URL       string    `json:"url" doc:"Where the dashboard loads it (same session); the v parameter changes with the image, so it can be cached for good"`
	MIME      string    `json:"mime" enum:"image/png,image/svg+xml"`
	Hash      string    `json:"hash" doc:"Identifies the image: it changes only when the image does"`
	Width     int       `json:"width" doc:"The source's width in pixels (an SVG's viewBox width)"`
	Height    int       `json:"height"`
	Tone      string    `json:"tone,omitempty" enum:"dark,light," doc:"dark: a dark mark on a see-through ground (needs a light plate on a dark page); light: the other way round"`
	Source    string    `json:"source" doc:"Where it came from: upload, or the path on the app it was found at (/favicon.ico, /icon.svg; inline for a data: URL)"`
	App       string    `json:"app,omitempty" doc:"The app it was found on"`
	UpdatedAt time.Time `json:"updatedAt" doc:"When this image was first seen or uploaded"`
}

// IconInfo is how a project's icon is chosen and what it shows.
type IconInfo struct {
	Project string `json:"project"`
	Mode    string `json:"mode" enum:"auto,upload,letter" doc:"auto: the app's own icon when the box found one, else the letters; upload: the uploaded icon; letter: always the letters"`
	Showing string `json:"showing" enum:"upload,inferred,letter" doc:"What the project shows now"`
	Letters string `json:"letters" doc:"The project's initials, shown on its colour when it has no image"`
	Enamel  string `json:"enamel" doc:"The project's colour (see appearance)"`
	// Image is what shows, nil for the letters.
	Image *IconImage `json:"image,omitempty" doc:"The image the project shows; absent when it shows its letters"`
	Found *IconImage `json:"found,omitempty" doc:"The icon the box found on the app, even when another is showing"`
	// LastCheck is the box's last look for the app's icon.
	LastCheck *projicon.IconCheck `json:"lastCheck,omitempty" doc:"When the box last looked for the app's icon (after each production deploy), and why it found none"`
	PublicURL string              `json:"publicUrl" doc:"A PNG of the icon (192 px, or the letters on the project's colour) that anyone can open, for email. It names no project and stays the same when the icon changes."`
}

type iconPath struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

const iconBody = 2 << 20 // two images of MaxBytes, base64 in JSON

func (m *Module) registerIcon(a huma.API, p *platform.Platform) {
	base := "/v1/projects/{project}/icon"
	need := func(ctx context.Context, project string, scope tokens.Scope) error {
		if p == nil {
			return unavailable(errNotReady)
		}
		if err := api.PrincipalFrom(ctx).Require(scope, project); err != nil {
			return err
		}
		v, _, err := p.DB.Load(ctx, project)
		if err != nil {
			return err
		}
		if v == 0 {
			return api.NewProblem(404, "not_found", "project "+project+" does not exist")
		}
		return nil
	}

	get := api.Op("icon-get", http.MethodGet, base, "projects icon get", api.RiskRead, "Get a project's icon",
		"How the project's icon is chosen and what it shows: the app's own icon (the box looks for it after each production deploy "+
			"of the app at the project's main address: <link rel=icon>, apple-touch-icon, the web manifest, then /favicon.ico), an uploaded one, "+
			"or the project's initials on its colour. Also gives a public PNG URL for email.", "projects")
	get.Errors = append(get.Errors, 404)
	huma.Register(a, get, api.Wrap(func(ctx context.Context, in *iconPath) (*struct{ Body IconInfo }, error) {
		if err := need(ctx, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		rec, err := projicon.Get(ctx, p.DB, in.Project)
		if err != nil {
			return nil, err
		}
		return &struct{ Body IconInfo }{iconInfo(ctx, p, in.Project, rec)}, nil
	}))

	img := api.Op("icon-image", http.MethodGet, base+"/image", "-", api.RiskRead, "Read a project's icon image",
		"The image bytes, for the dashboard. 404 when the project shows its letters.", "projects")
	img.Errors = append(img.Errors, 404)
	img.Responses = map[string]*huma.Response{"200": {Description: "The icon", Content: map[string]*huma.MediaType{
		"image/png": {Schema: &huma.Schema{Type: "string", Format: "binary"}}, "image/svg+xml": {Schema: &huma.Schema{Type: "string"}}}}}
	huma.Register(a, img, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Which   string `query:"which" enum:"showing,found," doc:"showing (the default) or found: the app's own icon"`
		V       string `query:"v" doc:"The image's hash, to cache it for good"`
	}) (*huma.StreamResponse, error) {
		if err := need(ctx, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		rec, err := projicon.Get(ctx, p.DB, in.Project)
		if err != nil {
			return nil, err
		}
		s, _ := rec.Showing()
		if in.Which == "found" {
			s = rec.Inferred
		}
		if s == nil {
			return nil, api.NewProblem(404, "not_found", "project "+in.Project+" shows its letters; it has no icon image")
		}
		cache := "private, no-cache"
		if in.V == s.Hash {
			cache = "private, max-age=31536000, immutable"
		}
		return serveIcon(s.MIME, s.Data, s.Hash, cache), nil
	}))

	up := api.Op("icon-upload", http.MethodPut, base, "projects icon upload", api.RiskWrite, "Upload a project's icon",
		"Sets the project's icon to an image you send: PNG, JPEG, GIF, WebP, ICO or SVG, at most 512 KB, ideally square and at least 64 px. "+
			"Rasters are re-encoded as PNG (at most 256 px); SVG goes through a safety pass (no scripts, outside links or embedded content). "+
			"With an SVG, send a PNG of it as png too, for email (which can't show SVG). Cosmetic: not a Change. "+
			"From the CLI, --image @logo.png reads a file.", "projects")
	up.Errors = append(up.Errors, 404, 413)
	up.MaxBodyBytes = iconBody
	huma.Register(a, up, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Image []byte `json:"image" required:"true" doc:"The image file, base64"`
			PNG   []byte `json:"png,omitempty" doc:"With an SVG image: the same icon as a raster (PNG, at least 96 px), base64, used for email"`
		}
	}) (*struct{ Body IconInfo }, error) {
		if err := need(ctx, in.Project, tokens.ScopeApplyReversible); err != nil {
			return nil, err
		}
		im, err := projicon.Normalize(in.Body.Image)
		if err != nil {
			return nil, iconProblem(err)
		}
		var raster *projicon.Image
		if im.SVG() && len(in.Body.PNG) > 0 {
			if raster, err = projicon.NormalizeRaster(in.Body.PNG); err != nil {
				return nil, iconProblem(errors.New("png: " + err.Error()))
			}
		}
		rec, err := projicon.Update(ctx, p.DB, in.Project, func(r *projicon.Record) error {
			r.Mode, r.Upload = projicon.ModeUpload, projicon.Stamp(im, raster, "upload", "", time.Now())
			return nil
		})
		if err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "project.icon", in.Project, map[string]any{"use": "upload", "hash": im.Hash(), "mime": im.MIME, "session": pr.Session})
		return &struct{ Body IconInfo }{iconInfo(ctx, p, in.Project, rec)}, nil
	}))

	reset := api.Op("icon-reset", http.MethodPost, base+"/reset", "projects icon reset", api.RiskWrite, "Use the app's icon or the letters",
		"use=app: show the icon the project's app serves (the box looks again now; until it finds one the project shows its letters). "+
			"use=letter: always show the project's initials on its colour. Either way an uploaded icon is removed. Cosmetic: not a Change.", "projects")
	reset.Errors = append(reset.Errors, 404)
	huma.Register(a, reset, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Use string `json:"use" enum:"app,letter" required:"true" doc:"app: the app's own icon (its favicon); letter: the project's initials"`
		}
	}) (*struct{ Body IconInfo }, error) {
		if err := need(ctx, in.Project, tokens.ScopeApplyReversible); err != nil {
			return nil, err
		}
		mode := projicon.ModeLetter
		if in.Body.Use == "app" {
			mode = projicon.ModeAuto
		}
		if _, err := projicon.Update(ctx, p.DB, in.Project, func(r *projicon.Record) error {
			r.Mode, r.Upload = mode, nil
			return nil
		}); err != nil {
			return nil, err
		}
		if mode == projicon.ModeAuto {
			if r, err := m.rt(); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				r.refreshIcon(cctx, in.Project, "", "")
				cancel()
			}
		}
		rec, err := projicon.Get(ctx, p.DB, in.Project)
		if err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "project.icon", in.Project, map[string]any{"use": in.Body.Use, "session": pr.Session})
		return &struct{ Body IconInfo }{iconInfo(ctx, p, in.Project, rec)}, nil
	}))

	pub := api.Op("icon-public", http.MethodGet, "/v1/icons/{file}", "-", api.RiskRead, "A project's icon for email",
		"A 192 px PNG of a project's icon, or its letters on its colour, for email. Anyone can open it: the address names no project "+
			"and can't be guessed from one. It stays the same when the icon changes.", "projects")
	pub.Security = nil
	pub.Errors = []int{404}
	pub.Responses = map[string]*huma.Response{"200": {Description: "The icon", Content: map[string]*huma.MediaType{
		"image/png": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}}
	huma.Register(a, pub, func(ctx context.Context, in *struct {
		File string `path:"file" pattern:"^[a-z2-7]{24}\\.png$" doc:"<id>.png"`
	}) (*huma.StreamResponse, error) {
		notFound := api.NewProblem(404, "not_found", "no such icon")
		if p == nil {
			return nil, notFound
		}
		id := strings.TrimSuffix(in.File, ".png")
		project, err := projicon.ByPublicID(ctx, p.DB, id)
		if err != nil || project == "" {
			return nil, notFound
		}
		rec, err := projicon.Get(ctx, p.DB, project)
		if err != nil || rec.PublicID != id {
			return nil, notFound
		}
		b, hash, err := emailIcon(ctx, p, project, rec)
		if err != nil {
			return nil, err
		}
		return serveIcon("image/png", b, hash, "public, max-age=3600"), nil
	})
}

// emails caches rendered email PNGs by what they are drawn from.
var emails sync.Map // key → []byte

func emailIcon(ctx context.Context, p *platform.Platform, project string, rec *projicon.Record) ([]byte, string, error) {
	s, _ := rec.Showing()
	src := []byte(nil)
	if s != nil {
		src = s.Data
		if s.MIME != "image/png" {
			src = s.Raster // an SVG's raster twin; none: the letters
		}
	}
	var key string
	if src != nil {
		key = "img:" + s.Hash + ":" + strconv.Itoa(len(src))
	} else {
		key = "letters:" + projicon.Letters(project) + ":" + api.EnamelOf(ctx, p.DB, project)
	}
	if v, ok := emails.Load(key); ok {
		return v.([]byte), shortHash(key), nil
	}
	var b []byte
	var err error
	if src != nil {
		b, err = projicon.EmailPNG(src)
	} else {
		b, err = projicon.MonogramPNG(project, api.EnamelOf(ctx, p.DB, project), projicon.EmailSide)
	}
	if err != nil {
		return nil, "", err
	}
	emails.Store(key, b)
	return b, shortHash(key), nil
}

func shortHash(s string) string {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h, 36)
}

// serveIcon writes an image with its ETag (If-None-Match gets a 304).
// An SVG opened on its own runs nothing and loads nothing.
func serveIcon(mime string, b []byte, etag, cache string) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humago.Unwrap(hctx)
		h := w.Header()
		h.Set("Content-Type", mime)
		h.Set("ETag", `"`+etag+`"`)
		h.Set("Cache-Control", cache)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}}
}

func iconProblem(err error) error {
	if errors.Is(err, projicon.ErrTooLarge) {
		return api.NewProblem(413, "too_large", err.Error()+". Export a smaller one (256 px is plenty).")
	}
	return api.NewProblem(422, "validation", err.Error()+".")
}

func iconInfo(ctx context.Context, p *platform.Platform, project string, rec *projicon.Record) IconInfo {
	out := IconInfo{Project: project, Mode: rec.Mode, Letters: projicon.Letters(project), Enamel: api.EnamelOf(ctx, p.DB, project), LastCheck: rec.Checked}
	path := "/v1/projects/" + project + "/icon/image"
	s, showing := rec.Showing()
	out.Showing = showing
	if s != nil {
		out.Image = iconImage(s, path)
	}
	if rec.Inferred != nil {
		out.Found = iconImage(rec.Inferred, path)
		out.Found.URL = path + "?which=found&v=" + rec.Inferred.Hash
	}
	if id, err := projicon.PublicID(ctx, p.DB, project); err == nil {
		out.PublicURL = strings.TrimRight(p.PublicURL, "/") + "/v1/icons/" + id + ".png"
	}
	return out
}

func iconImage(s *projicon.Stored, path string) *IconImage {
	return &IconImage{URL: path + "?v=" + s.Hash, MIME: s.MIME, Hash: s.Hash, Width: s.Width, Height: s.Height, Tone: s.Tone,
		Source: s.Source, App: s.App, UpdatedAt: s.UpdatedAt}
}

// ---- inference after a deploy ----

// refreshIconLater looks for the project's icon in the background after a
// production deploy (or rollback) of app; the deploy doesn't wait for it.
func (r *rt) refreshIconLater(project, app, deploy string) {
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
		defer cancel()
		r.refreshIcon(ctx, project, app, deploy)
	}()
}

// iconInflight keeps one look per project at a time.
var iconInflight sync.Map

// refreshIcon asks the project's icon app for its icon and keeps it when
// it changed. When app is set, it only looks if app is the icon app (a
// deploy of the project's docs doesn't change its icon).
func (r *rt) refreshIcon(ctx context.Context, project, app, deploy string) {
	if _, busy := iconInflight.LoadOrStore(project, true); busy {
		return
	}
	defer iconInflight.Delete(project)
	specs := r.appSpecs(ctx, project)
	main := iconApp(project, specs, r.splitRoute)
	if main == "" || (app != "" && app != main) {
		return
	}
	spec := specs[main]
	st, err := r.st.getState(ctx, project, main, "")
	if err != nil || st.Live == "" {
		return
	}
	d, err := r.st.getDeploy(ctx, project, main, st.Live)
	if err != nil {
		return
	}
	var res projicon.Result
	if api := apiFrameworks[spec.Framework]; api != "" {
		res = projicon.Result{Reason: "the app is an API (" + api + "), so the project keeps its letters", None: true}
	} else if f, ok := r.iconFetcher(project, main, &spec, st, d); ok {
		res, _ = projicon.Infer(ctx, f)
	} else {
		return // asleep or not running: look again on the next deploy
	}
	if deploy == "" {
		deploy = d.ID
	}
	if _, changed, err := projicon.SaveInferred(ctx, r.p.DB, project, main, deploy, res, time.Now()); err != nil {
		r.p.Log.Warn("runtime: save the project icon", "project", project, "err", err)
	} else if changed {
		r.p.Log.Info("project icon updated", "project", project, "app", main, "source", res.Source)
	}
}

// iconApp is the app that stands for its project: the one at the
// project's main address (a route with no path, its own name first), else
// the first web app by name.
func iconApp(project string, specs map[string]manifest.App, split func(string) (string, string)) string {
	var names []string
	for n, s := range specs {
		if s.Role != manifest.RoleWorker {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	pick := ""
	for _, n := range names {
		s := specs[n]
		for _, rt := range appRoutes(project, n, &s) {
			if _, prefix := split(rt); prefix != "" {
				continue
			}
			if rt == project {
				return n
			}
			if pick == "" {
				pick = n
			}
		}
	}
	if pick != "" {
		return pick
	}
	return names[0]
}

// iconFetcher reaches a running app: its first instance, with the files
// the box serves for it (Next.js's public/, a static site) behind that.
func (r *rt) iconFetcher(project, app string, spec *manifest.App, st *AppState, d *Deploy) (projicon.Fetcher, bool) {
	var hosts []string
	for _, rt := range appRoutes(project, app, spec) {
		if h, prefix := r.splitRoute(rt); prefix == "" {
			hosts = append(hosts, h)
		}
	}
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if d.StaticRoot != "" {
		root := d.StaticRoot
		if _, err := os.Stat(root); err != nil {
			return projicon.Fetcher{}, false
		}
		u, _ := url.Parse("http://static.invalid")
		return projicon.Fetcher{Client: &http.Client{Transport: handlerTransport{http.FileServer(http.Dir(root))}, CheckRedirect: noFollow},
			Base: u, Hosts: hosts}, true
	}
	if len(st.Instances) == 0 || st.Sleeping {
		return projicon.Fetcher{}, false
	}
	u, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(st.Instances[0].Port))
	var files http.Handler
	if www := filepath.Join(r.assetsDir(project, app, ""), d.ID, "www"); exists(www) {
		files = http.FileServer(http.Dir(www))
	}
	return projicon.Fetcher{Client: &http.Client{Transport: fallbackTransport{base: r.appClient(0).Transport, files: files}, CheckRedirect: noFollow},
		Base: u, Hosts: hosts}, true
}

// handlerTransport answers requests from a handler, in process.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := &memResponse{header: http.Header{}, code: http.StatusOK}
	t.h.ServeHTTP(rec, req)
	return &http.Response{StatusCode: rec.code, Header: rec.header, Body: readCloser{bytes.NewReader(rec.body.Bytes())},
		ContentLength: int64(rec.body.Len()), Request: req}, nil
}

// fallbackTransport asks the app, and for what it doesn't have (404),
// the files the box serves in front of it.
type fallbackTransport struct {
	base  http.RoundTripper
	files http.Handler
}

func (t fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || t.files == nil || res.StatusCode != http.StatusNotFound || req.URL.Path == "/" {
		return res, err
	}
	res.Body.Close()
	return handlerTransport{t.files}.RoundTrip(req)
}

type memResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
	wrote  bool
}

func (m *memResponse) Header() http.Header { return m.header }
func (m *memResponse) WriteHeader(c int) {
	if !m.wrote {
		m.code, m.wrote = c, true
	}
}
func (m *memResponse) Write(b []byte) (int, error) {
	m.wrote = true
	if m.body.Len()+len(b) > projicon.MaxBytes+1<<20 {
		return 0, errors.New("too large")
	}
	return m.body.Write(b)
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

// apiFrameworks serve JSON, not pages: their projects have no icon to find.
var apiFrameworks = map[manifest.Framework]string{manifest.FrameworkHono: "Hono", manifest.FrameworkFastAPI: "FastAPI"}
