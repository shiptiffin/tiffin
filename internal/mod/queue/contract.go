package queue

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// This file is the queue module's contract with the modules it meets. The
// queue module never imports them: it finds them among platform.Modules() by
// these method sets, so either side can land first.
//
// # Runtime (internal/mod/runtime) implements AppUpstreams
//
// Jobs, cron ticks and workflow turns are pushed to an app over plain HTTP
// on the box (never through the public edge). The runtime tells the queue
// where an app's instances listen and which release is current:
//
//	AppEndpoint(ctx, p, project, app, release) → "http://127.0.0.1:PORT"
//	    release "" means the current release. Return ErrReleaseGone (match
//	    with errors.Is; any error whose text contains "release gone" also
//	    counts) when that release no longer runs, and the queue moves the
//	    work to the current release.
//	CurrentRelease(ctx, p, project, app) → release id ("" when not deployed)
//
// and connects the queue to those ports (AppDialer), checking that the app
// owning the port answers, not another app that took it while it was free:
//
//	DialApp(ctx, network, "127.0.0.1:PORT") → net.Conn
//
// # The queue module implements PinnedReleases for the runtime
//
// Workflow runs are pinned to the release that started them: every turn of
// a run is delivered to that release's instances until the run finishes.
// Before the runtime stops an old release's processes (after a deploy) it
// asks
//
//	queue.PinnedReleases(ctx, p, project, app) → []release
//
// and keeps any listed release running, without public traffic, until a
// later call no longer lists it ("old workers drain before stop"). Polling
// every 10-30 s is fine. Plain queue jobs are not pinned: they go to the
// current release.
//
// # The queue module implements Delivering for the runtime
//
// A project can let its production apps sleep when unused. Before the
// runtime puts an app to sleep it asks
//
//	queue.Delivering(ctx, p, project, app) → bool
//
// and keeps the app up while a delivery to it is under way, so a long job
// is not cut off. A delivery to a sleeping app wakes it first: AppEndpoint
// starts it before answering.
//
// # The queue module implements SetAppCrons for the runtime
//
// An app's own files can declare crons (vercel.json). When an app's
// production release changes (a deploy, a rollback, a converge) the runtime
// hands over that release's whole set:
//
//	queue.SetAppCrons(ctx, p, project, app, "vercel.json", crons)
//
// They replace the app's earlier set from that file, are called with GET as
// Vercel calls them, and a manifest cron with the same name (or app and
// path) wins. `queue crons list` shows each cron's origin.
//
// # The queue module implements ServeLive for the runtime
//
// Browsers watch a job or workflow run on the app's own host, at
// GET /_tiffin/runs/{id}/events (LivePath). The runtime's switchboard hands
// every request under LivePath to
//
//	queue.ServeLive(w, r)
//
// before it could reach the app, on every app host, previews included.
//
// # Postgres (internal/mod/postgres) implements SystemDatabase
//
//	SystemDatabase(ctx, p, "tiffin_queue") → DSN of a platform-owned
//	    database (created if missing) the queue may run DDL in.
//
// Until that lands (and in tests) TIFFIN_QUEUE_DATABASE_URL points the
// module at any Postgres 14+ database. sendTx (the transactional outbox)
// reads each project's DATABASE_URL from the postgres module's EnvProvider.

// ErrReleaseGone is what AppEndpoint returns for a release that has stopped.
var ErrReleaseGone = errors.New("release gone")

// AppUpstreams is implemented by the runtime module.
type AppUpstreams interface {
	AppEndpoint(ctx context.Context, p *platform.Platform, project, app, release string) (string, error)
	CurrentRelease(ctx context.Context, p *platform.Platform, project, app string) (string, error)
}

// AppDialer is implemented by the runtime module.
type AppDialer interface {
	DialApp(ctx context.Context, network, addr string) (net.Conn, error)
}

// SystemDatabases is implemented by the postgres module.
type SystemDatabases interface {
	SystemDatabase(ctx context.Context, p *platform.Platform, name string) (string, error)
}

// PinnedReleaser is what this module offers the runtime (see above).
type PinnedReleaser interface {
	PinnedReleases(ctx context.Context, p *platform.Platform, project, app string) ([]string, error)
}

var (
	_ PinnedReleaser       = (*Module)(nil)
	_ platform.PlanChecker = (*Module)(nil)
)

// DeliveryReporter is what this module offers the runtime (see above).
type DeliveryReporter interface {
	Delivering(ctx context.Context, p *platform.Platform, project, app string) (bool, error)
}

var _ DeliveryReporter = (*Module)(nil)

// LiveServer is what this module offers the runtime (see above).
type LiveServer interface {
	ServeLive(w http.ResponseWriter, r *http.Request)
}

var _ LiveServer = (*Module)(nil)

func findModule[T any]() (T, bool) {
	for _, m := range platform.Modules() {
		if t, ok := m.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}
