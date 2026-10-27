package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/linkpreview"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Link previews. After a deploy of a web app goes live (production or a
// preview), the box reads its home page as link unfurlers do and keeps
// what a shared link to it shows, what's missing, and what changed since
// the version before (linkpreview.Run). The report and a small copy of the
// share image live with the deploy and go when it does.

func nsLinkPreview(project, app string) string { return "runtime/linkpreview/" + project + "/" + app }
func nsLinkThumb(project, app string) string {
	return "runtime/linkpreview-image/" + project + "/" + app
}

func (s store) getLinkPreview(ctx context.Context, project, app, id string) (*linkpreview.LinkReport, error) {
	b, ok, err := s.db.KVGet(ctx, nsLinkPreview(project, app), id)
	if err != nil || !ok {
		return nil, err
	}
	var rep linkpreview.LinkReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, nil // written by another version: checked again when asked
	}
	return &rep, nil
}

func (s store) putLinkPreview(ctx context.Context, project, app string, rep *linkpreview.LinkReport, th *linkpreview.Thumb) error {
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	if th != nil {
		tb, _ := json.Marshal(th)
		if err := s.db.KVPut(ctx, nsLinkThumb(project, app), rep.Deploy, tb); err != nil {
			return err
		}
	} else {
		_ = s.db.KVDelete(ctx, nsLinkThumb(project, app), rep.Deploy)
	}
	return s.db.KVPut(ctx, nsLinkPreview(project, app), rep.Deploy, b)
}

func (s store) getLinkThumb(ctx context.Context, project, app, id string) (*linkpreview.Thumb, error) {
	b, ok, err := s.db.KVGet(ctx, nsLinkThumb(project, app), id)
	if err != nil || !ok {
		return nil, err
	}
	var th linkpreview.Thumb
	return &th, json.Unmarshal(b, &th)
}

func (s store) deleteLinkPreview(ctx context.Context, project, app, id string) {
	_ = s.db.KVDelete(ctx, nsLinkPreview(project, app), id)
	_ = s.db.KVDelete(ctx, nsLinkThumb(project, app), id)
}

// attachLinkPreviews adds each deploy's link preview in brief.
func (r *rt) attachLinkPreviews(ctx context.Context, ds []*Deploy) {
	for _, d := range ds {
		if d.URL == "" && d.AppURL == "" {
			continue // a worker
		}
		if rep, err := r.st.getLinkPreview(ctx, d.Project, d.App, d.ID); err == nil && rep != nil {
			d.LinkPreview = rep.Summary()
		}
	}
}

// linkInflight keeps one check per deploy at a time; a waiter can wait on it.
var linkInflight sync.Map // deploy ID → chan struct{} (closed when done)

// checkLinkPreviewLater checks a deploy that just went live in the
// background; the deploy doesn't wait for it. With missing set, it only
// checks a deploy that has no report yet (a rollback to an older one).
func (r *rt) checkLinkPreviewLater(d *Deploy, missing bool) {
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, 45*time.Second)
		defer cancel()
		if missing {
			if rep, _ := r.st.getLinkPreview(ctx, d.Project, d.App, d.ID); rep != nil {
				return
			}
		}
		if _, err := r.checkLinkPreview(ctx, d); err != nil && err != errNotServing {
			r.p.Log.Warn("runtime: link preview", "project", d.Project, "app", d.App, "deploy", d.ID, "err", err)
		}
	}()
}

// errNotServing: the deploy isn't what serves its environment now, or it
// is asleep, so it can't be read.
var errNotServing = problem(409, "conflict", "this deploy isn't running now, so the box can't read its page",
	"The box checks a deploy when it goes live. Open the site to wake it, then check again.")

// checkLinkPreview reads the deploy's home page now and keeps the report.
func (r *rt) checkLinkPreview(ctx context.Context, d *Deploy) (*linkpreview.LinkReport, error) {
	done := make(chan struct{})
	if prev, busy := linkInflight.LoadOrStore(d.ID, done); busy {
		select {
		case <-prev.(chan struct{}):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return r.st.getLinkPreview(ctx, d.Project, d.App, d.ID)
	}
	defer func() { linkInflight.Delete(d.ID); close(done) }()

	t, err := r.linkTarget(ctx, d)
	if err != nil {
		return nil, err
	}
	rep, th := linkpreview.Run(ctx, t, d.ID)
	if old, c := r.linkBaseline(ctx, d); old != nil {
		rep.Compare(old, c)
	}
	if err := r.st.putLinkPreview(ctx, d.Project, d.App, rep, th); err != nil {
		return nil, err
	}
	return rep, nil
}

// linkTarget reaches a deploy that serves its environment.
func (r *rt) linkTarget(ctx context.Context, d *Deploy) (linkpreview.Target, error) {
	spec, err := r.appSpec(ctx, d.Project, d.App)
	if err != nil || spec == nil || spec.Role == manifest.RoleWorker {
		return linkpreview.Target{}, problem(404, "not_found", d.App+" is a worker: it has no pages to share", "")
	}
	st, err := r.st.getState(ctx, d.Project, d.App, d.Preview)
	if err != nil {
		return linkpreview.Target{}, err
	}
	if st.Live != d.ID || st.Stopped {
		return linkpreview.Target{}, errNotServing
	}
	var t linkpreview.Target
	if d.Preview != "" {
		u, _ := url.Parse(r.deployURL(d, spec))
		t.Page, t.Preview = u, true
		t.Hosts = []string{u.Hostname()}
	} else {
		main, others := r.siteURLs(d.Project, d.App, spec)
		u, _ := url.Parse(main)
		t.Page = u
		for _, rt := range appRoutes(d.Project, d.App, spec) {
			if h, prefix := r.splitRoute(rt); prefix == "" && !slices.Contains(t.Hosts, h) {
				t.Hosts = append(t.Hosts, h)
			}
		}
		for _, o := range others {
			if ou, err := url.Parse(o); err == nil && !slices.Contains(t.Hosts, ou.Hostname()) {
				t.Hosts = append(t.Hosts, ou.Hostname())
			}
		}
	}
	if t.Page == nil || t.Page.Host == "" {
		return linkpreview.Target{}, problem(404, "not_found", d.App+" has no address yet", "")
	}
	if !strings.HasSuffix(t.Page.Path, "/") {
		// The home page; for an app under a path (example.com/docs), there.
		t.Page = t.Page.JoinPath("/")
	}
	t.BoxHost = func(h string) bool { return r.p.IsBoxHost(h) && !slices.Contains(t.Hosts, strings.ToLower(h)) }
	t.Outside = outsideClient()

	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if d.StaticRoot != "" {
		if _, err := os.Stat(d.StaticRoot); err != nil {
			return linkpreview.Target{}, errNotServing
		}
		t.Base, _ = url.Parse("http://static.invalid")
		t.Client = &http.Client{Transport: handlerTransport{http.FileServer(http.Dir(d.StaticRoot))}, CheckRedirect: noFollow}
		return t, nil
	}
	if len(st.Instances) == 0 || st.Sleeping {
		return linkpreview.Target{}, errNotServing
	}
	t.Base, _ = url.Parse("http://127.0.0.1:" + strconv.Itoa(st.Instances[0].Port))
	var files http.Handler
	if www := filepath.Join(r.assetsDir(d.Project, d.App, d.Preview), d.ID, "www"); exists(www) {
		files = http.FileServer(http.Dir(www))
	}
	t.Client = &http.Client{Transport: fallbackTransport{base: r.appClient(0).Transport, files: files}, CheckRedirect: noFollow}
	return t, nil
}

var outsideOnce = sync.OnceValue(func() *http.Client { return linkpreview.OutsideClient(dnskit.LocalPublicIPs) })

func outsideClient() *http.Client { return outsideOnce() }

// linkBaseline is the report a deploy's is compared with: the production
// version before it, or production's live version for a preview.
func (r *rt) linkBaseline(ctx context.Context, d *Deploy) (*linkpreview.LinkReport, *linkpreview.LinkBaseline) {
	if d.Preview != "" {
		st, err := r.st.getState(ctx, d.Project, d.App, "")
		if err != nil || st.Live == "" {
			return nil, nil
		}
		old, _ := r.st.getLinkPreview(ctx, d.Project, d.App, st.Live)
		if old == nil {
			return nil, nil
		}
		c := &linkpreview.LinkBaseline{Deploy: st.Live, Production: true}
		if pd, err := r.st.getDeploy(ctx, d.Project, d.App, st.Live); err == nil {
			c.Version = pd.Version
		}
		return old, c
	}
	ds, err := r.st.listDeploys(ctx, d.Project, d.App, "")
	if err != nil {
		return nil, nil
	}
	for _, o := range ds { // newest first
		if o.ID >= d.ID || !addressed(o) {
			continue
		}
		if old, _ := r.st.getLinkPreview(ctx, d.Project, d.App, o.ID); old != nil {
			return old, &linkpreview.LinkBaseline{Deploy: o.ID, Version: o.Version}
		}
	}
	return nil, nil
}

// ---- API ----

// LinkPreview is a deploy's link preview, for the dashboard and agents.
type LinkPreview struct {
	linkpreview.LinkReport
	ThumbURL string `json:"thumbUrl,omitempty" doc:"A small copy of the share image the box kept (same session); the v parameter changes with the image"`
	CanCheck bool   `json:"canCheck" doc:"Whether the deploy serves its environment and is awake, so it can be checked again now"`
}

func (r *rt) linkPreviewBody(ctx context.Context, d *Deploy, rep *linkpreview.LinkReport) *LinkPreview {
	out := &LinkPreview{LinkReport: *rep}
	if rep.Image != nil && rep.Image.Thumb {
		out.ThumbURL = "/v1/projects/" + d.Project + "/apps/" + d.App + "/deploys/" + d.ID + "/link-preview/image?v=" + rep.Image.Hash
	}
	_, err := r.linkTarget(ctx, d)
	out.CanCheck = err == nil
	return out
}

func (m *Module) registerLinkPreview(a huma.API, appPath string) {
	type DeployRef struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		ID      string `path:"id" pattern:"^dep_[0-9A-Z]{26}$" doc:"Deploy ID"`
	}
	load := func(ctx context.Context, p DeployRef, scope tokens.Scope) (*rt, *Deploy, error) {
		r, err := m.rt()
		if err != nil {
			return nil, nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(scope, p.Project); err != nil {
			return nil, nil, err
		}
		d, err := r.st.getDeploy(ctx, p.Project, p.App, p.ID)
		if err != nil {
			return nil, nil, notFound(err, "deploy "+p.ID)
		}
		return r, d, nil
	}
	path := appPath + "/deploys/{id}/link-preview"

	get := api.Op("deploy-link-preview", http.MethodGet, path, "deploys link-preview", api.RiskRead, "See how a deploy's link shares",
		"What a link to the deploy's home page shows on X, Facebook, LinkedIn, Slack and in search results, read the way their crawlers do: "+
			"title, description, share image (fetched and measured), twitter:card, canonical, robots and icon, each checked (ok, warn or bad, "+
			"with what to fix), the tags as written, and what changed since the version before (a preview: against production). "+
			"The box checks every web deploy when it goes live; deploys get gives the same in brief (linkPreview). "+
			"Pass wait (seconds) right after a deploy to get the report once its check is done.", "apps")
	get.Errors = append(get.Errors, 404)
	huma.Register(a, api.Untrusted(get), api.Wrap(func(ctx context.Context, p *struct {
		DeployRef
		Wait int `query:"wait" minimum:"0" maximum:"30" doc:"Seconds to wait for a check that hasn't finished (a deploy that just went live)"`
	}) (*struct{ Body *LinkPreview }, error) {
		r, d, err := load(ctx, p.DeployRef, tokens.ScopeRead)
		if err != nil {
			return nil, err
		}
		for deadline := time.Now().Add(time.Duration(p.Wait) * time.Second); ; {
			rep, err := r.st.getLinkPreview(ctx, d.Project, d.App, d.ID)
			if err != nil {
				return nil, err
			}
			if rep != nil {
				return &struct{ Body *LinkPreview }{r.linkPreviewBody(ctx, d, rep)}, nil
			}
			if !d.Terminal() || time.Now().After(deadline) {
				break
			}
			if _, busy := linkInflight.Load(d.ID); !busy && d.Status == StatusLive {
				break // nothing running: check now, below
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
			if d, err = r.st.getDeploy(ctx, d.Project, d.App, d.ID); err != nil {
				return nil, err
			}
		}
		if d.Status != StatusLive {
			why := "it isn't live yet: the box checks it when it goes live"
			if d.Terminal() {
				why = "it never went live, or was replaced before it was checked"
			}
			return nil, problem(404, "not_found", "deploy "+d.ID+" has no link preview: "+why, "")
		}
		// Live but never checked (deployed before the box checked deploys,
		// or the check was cut short): check it now.
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rep, err := r.checkLinkPreview(cctx, d)
		if err == errNotServing {
			return nil, problem(404, "not_found", "deploy "+d.ID+" hasn't been checked, and it is asleep now", "Open the site to wake it, then look again.")
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body *LinkPreview }{r.linkPreviewBody(ctx, d, rep)}, nil
	}))

	post := api.Op("deploy-link-preview-check", http.MethodPost, path, "deploys link-preview check", api.RiskRead, "Check a deploy's link preview again",
		"Reads the deploy's home page again now and keeps the new report (after you changed a share image the app serves from storage, say). "+
			"Only the deploy that serves its environment, while it's awake.", "apps")
	post.Errors = append(post.Errors, 404, 409)
	huma.Register(a, api.Untrusted(post), api.Wrap(func(ctx context.Context, p *DeployRef) (*struct{ Body *LinkPreview }, error) {
		r, d, err := load(ctx, *p, tokens.ScopeApplyReversible)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rep, err := r.checkLinkPreview(cctx, d)
		if err != nil {
			return nil, err
		}
		return &struct{ Body *LinkPreview }{r.linkPreviewBody(ctx, d, rep)}, nil
	}))

	img := api.Op("deploy-link-preview-image", http.MethodGet, path+"/image", "-", api.RiskRead, "Read a deploy's share image",
		"The box's small copy of the deploy's share image (800 px wide), for the dashboard. 404 when it kept none.", "apps")
	img.Errors = append(img.Errors, 404)
	img.Responses = thumbResponses
	huma.Register(a, img, api.Wrap(func(ctx context.Context, p *struct {
		DeployRef
		V string `query:"v" doc:"The image's hash, to cache it for good"`
	}) (*huma.StreamResponse, error) {
		r, d, err := load(ctx, p.DeployRef, tokens.ScopeRead)
		if err != nil {
			return nil, err
		}
		return r.serveThumb(ctx, d.Project, d.App, d.ID, p.V)
	}))

	card := api.Op("project-preview-image", http.MethodGet, "/v1/projects/{project}/preview", "-", api.RiskRead, "Read a project's share image",
		"The share image of the live version of the app at the project's main address (the box's small copy), for the dashboard's project cards. "+
			"404 when it has none.", "projects")
	card.Errors = append(card.Errors, 404)
	card.Responses = thumbResponses
	huma.Register(a, card, api.Wrap(func(ctx context.Context, p *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*huma.StreamResponse, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, p.Project); err != nil {
			return nil, err
		}
		app := iconApp(p.Project, r.appSpecs(ctx, p.Project), r.splitRoute)
		if app == "" {
			return nil, problem(404, "not_found", "project "+p.Project+" has no web app", "")
		}
		st, err := r.st.getState(ctx, p.Project, app, "")
		if err != nil || st.Live == "" {
			return nil, problem(404, "not_found", "project "+p.Project+" has nothing live", "")
		}
		return r.serveThumb(ctx, p.Project, app, st.Live, "")
	}))
}

var thumbResponses = map[string]*huma.Response{"200": {Description: "The image", Content: map[string]*huma.MediaType{
	"image/jpeg": {Schema: &huma.Schema{Type: "string", Format: "binary"}}, "image/png": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}}

func (r *rt) serveThumb(ctx context.Context, project, app, id, v string) (*huma.StreamResponse, error) {
	th, err := r.st.getLinkThumb(ctx, project, app, id)
	if err != nil {
		return nil, err
	}
	if th == nil {
		return nil, problem(404, "not_found", "no share image for deploy "+id, "")
	}
	cache := "private, no-cache"
	if v != "" && v == th.Hash {
		cache = "private, max-age=31536000, immutable"
	}
	return serveIcon(th.MIME, th.Data, th.Hash, cache), nil
}
