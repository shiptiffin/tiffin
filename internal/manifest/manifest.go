// Package manifest defines the project manifest: the single file
// (tiffin.config.ts, evaluated to canonical JSON) that describes a project.
//
// The Go types in this file are the contract. The JSON Schema, the
// TypeScript types in @shiptiffin/sdk and the change engine all derive from them.
package manifest

import (
	"bytes"
	"encoding/json"
)

// Version is the manifest format version this build understands.
const Version = 1

// Manifest is the canonical, fully-defaulted form of tiffin.config.ts.
type Manifest struct {
	// Version of the manifest format. Always 1 for now.
	Version int `json:"version"`
	// Project slug: lowercase letters, digits and dashes, 1-40 chars.
	Project string `json:"project"`
	// Resources caps how much of the box the project's apps may use. Absent
	// means automatic: the project grows into whatever the box has free,
	// shares the CPU fairly with other projects under contention, and can
	// never take the memory the platform or the other projects' running apps
	// need (a box-wide default share, if the owner set one, still applies).
	Resources *Resources `json:"resources,omitempty"`
	// SleepAfter lets the project's production apps sleep when nobody uses
	// them: after this long with no requests and no job, cron or workflow
	// deliveries, their containers stop (freeing memory and CPU) and the
	// next request or delivery starts them again. Hours or days, 1h to 30d:
	// "24h", "7d", "14d". Absent: they never sleep.
	SleepAfter string `json:"sleepAfter,omitempty"`
	// Apps keyed by name (same slug rules as Project).
	Apps map[string]App `json:"apps,omitempty"`
	// Services the project uses. Absent means "not provisioned".
	Services Services `json:"services,omitzero"`
	// Crons are scheduled HTTP calls into apps, keyed by name (same slug
	// rules as Project). Each one pushes a request to its app on a schedule.
	Crons map[string]Cron `json:"crons,omitempty"`
	// Queues are named job queues that push their jobs to an app, keyed by
	// name (same slug rules as Project). Declaring a queue sets its target
	// app, path, limits and retry policy; a queue you do not declare still
	// works with defaults once an app sends to it.
	Queues map[string]Queue `json:"queues,omitempty"`
	// Topics fan messages out to subscribed queues, keyed by name: 1-64
	// lowercase letters, digits, dots or dashes, starting with a letter
	// (e.g. "order.created"). A topic name must not also be a queue name.
	Topics map[string]Topic `json:"topics,omitempty"`
	// Domains holds options for the project's own domain names (not under
	// the box domain), keyed by host name ("example.com"). Which app serves
	// a name is set by that app's Routes ("example.com", "example.com/api");
	// a domain needs an entry here only for its options. The box checks each
	// such name's DNS and gets its certificate once it points at the box.
	Domains map[string]Domain `json:"domains,omitempty"`
	// Env holds plain, non-secret environment variables shared by all apps.
	// Secrets never live in the manifest.
	Env map[string]string `json:"env,omitempty"`

	// appOrder is the apps in the order the config declares them, when
	// Parse saw it (canonical JSON sorts them, so it is not stored).
	appOrder []string
}

// Framework is how an app is built and run. Bun is the only runtime.
type Framework string

const (
	FrameworkNext   Framework = "next"
	FrameworkHono   Framework = "hono"
	FrameworkBun    Framework = "bun"    // any Bun server listening on $PORT
	FrameworkStatic Framework = "static" // served straight by Caddy
)

// Role is what an app instance does.
type Role string

const (
	RoleWeb    Role = "web"    // serves HTTP on routes
	RoleWorker Role = "worker" // receives queue and workflow pushes only
)

// App is one deployable unit.
type App struct {
	// Path to the app's source, relative to the manifest. Default ".".
	Path string `json:"path"`
	// Framework. Default "bun".
	Framework Framework `json:"framework"`
	// Role. Default "web".
	Role Role `json:"role"`
	// Routes are hostnames (optionally with a path prefix) this app serves,
	// e.g. "shop" (expands to shop.<box domain>), "example.com", "example.com/api".
	// Default: the project name for its main app ("shop") and
	// "<project>-<app>" for the others ("shop-docs"); see MainApp. Workers
	// have no routes.
	Routes []string `json:"routes,omitempty"`
	// Instances to run. Default 1.
	Instances int `json:"instances"`
	// MemoryMB optionally caps each instance (copy) in MiB, 64-8192.
	// Default 0: no per-copy cap; the app's copies share their project's
	// memory (see Resources).
	MemoryMB int `json:"memoryMB,omitempty"`
	// Healthcheck path. Default "/", which passes on any status below 500;
	// a path set here must answer 2xx or 3xx. Ignored for workers and static apps.
	Healthcheck string `json:"healthcheck,omitempty"`
	// Command starts the app instead of the start command the build
	// detects (package.json "start"), e.g. "bun run worker.ts": one source
	// folder can run a web app and a worker. Applies from the next deploy.
	// Not for static apps.
	Command string `json:"command,omitempty"`
	// Release runs once per deploy, after the build and before the new
	// version takes traffic, in a one-off container of the new image with
	// the app's env, e.g. "bunx drizzle-kit migrate". A failure stops the
	// deploy and the running version keeps serving. Rollbacks do not run it.
	// Previews run it only against their own database branch. Not for
	// static apps.
	Release string `json:"release,omitempty"`
	// Packages are Debian (apt) packages installed in the app's image, e.g.
	// "ffmpeg" or "chromium", for apps that run programs beside their own
	// code. Applies from the next deploy. Not for static apps.
	Packages []string `json:"packages,omitempty"`
	// Disk lists folders, relative to the app's working directory (e.g.
	// "data", "uploads/tmp"), that persist across deploys and restarts,
	// each with a size: ["data"] (1GB each) or {"data": "5GB"}. Writes past
	// a folder's size fail with "disk full"; growing it applies at once.
	// Every production instance of the app shares them; each preview gets
	// its own of the same size, started from what the preview's image has
	// there. A folder starts with what the image has at that path. Their
	// sizes count toward the project's storage limit. Not for static apps.
	Disk Disk `json:"disk,omitempty"`
	// TimeoutSeconds is the most one request to the app may take, in
	// seconds, 1-86400 (24 hours). Default 0: 900 (15 minutes). Past it the
	// box answers 504, or cuts a response it is streaming.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Env holds app-specific plain environment variables (merged over Manifest.Env).
	Env map[string]string `json:"env,omitempty"`
	// Git connects the app to a GitHub repository (through the box's GitHub
	// App): pushes to Branch deploy to production, pull requests get
	// previews. Absent: deploys only happen when asked.
	Git *Git `json:"git,omitempty"`
	// Assets names the build's client-asset directory, which the box then
	// serves itself (for a framework it does not recognize). Absent: the
	// box looks for a known framework's.
	Assets *Assets `json:"assets,omitempty"`
}

// Assets is a server app's client-asset directory.
type Assets struct {
	// Dir is the directory in the build, relative to the app, e.g. "dist/client".
	Dir string `json:"dir"`
	// Path is the URL path its files are served at. Default "/".
	Path string `json:"path,omitempty"`
}

// Git is where an app's code lives on GitHub.
type Git struct {
	// Repo is the repository, "owner/name".
	Repo string `json:"repo"`
	// Branch is the production branch: every push to it deploys. Default "main".
	Branch string `json:"branch"`
	// Path is the app's directory inside the repository (monorepos), e.g.
	// "apps/web". Default "" (the top).
	Path string `json:"path,omitempty"`
	// Previews says which pull requests get a preview. Default "same-repo".
	Previews GitPreviews `json:"previews"`
}

// GitPreviews says which pull requests get a preview deploy.
type GitPreviews string

const (
	// PreviewsSameRepo builds pull requests opened from a branch of the
	// repository itself.
	PreviewsSameRepo GitPreviews = "same-repo"
	// PreviewsForks also builds pull requests from forks: their code runs
	// on the box with the project's env and secrets.
	PreviewsForks GitPreviews = "forks"
	// PreviewsOff builds no previews.
	PreviewsOff GitPreviews = "off"
)

// Resources is a project's share of the box: one lever per project. Every
// field is optional; set any of them to give the project a fixed budget.
// When both memoryMB and maxSharePercent are set, the lower limit wins.
type Resources struct {
	// MemoryMB is the most memory all of the project's app copies
	// (production and previews) may use together, in MiB, at least 128. It
	// is a hard cap and also a guarantee: the memoryMB budgets of all
	// projects together must fit in the memory the box keeps for apps.
	MemoryMB int `json:"memoryMB,omitempty"`
	// CPUs is the most CPU time the project's apps may use together, in
	// cores, in steps of 0.25 (1.5 = one and a half cores). At most the
	// box's CPU count.
	CPUs float64 `json:"cpus,omitempty"`
	// MaxSharePercent caps the project at this share of the box, 5-100: of
	// the memory the box keeps for apps and of its CPUs. It is a ceiling,
	// not a reservation, and follows the box when it is resized.
	MaxSharePercent int `json:"maxSharePercent,omitempty"`
}

// Services are the box-provided backends. A nil pointer means "off".
type Services struct {
	Postgres  *Postgres  `json:"postgres,omitempty"`
	Valkey    *Valkey    `json:"valkey,omitempty"`
	Storage   *Storage   `json:"storage,omitempty"`
	Auth      *Auth      `json:"auth,omitempty"`
	Email     *Email     `json:"email,omitempty"`
	Analytics *Analytics `json:"analytics,omitempty"`
}

// Postgres gives the project its own database.
type Postgres struct {
	// Extensions to enable, e.g. "vector", "pg_cron". Sorted, unique.
	Extensions []string `json:"extensions,omitempty"`
	// StatementTimeoutSeconds stops any one query of the project's after
	// this many seconds, 1-3600. Default 0: 300 seconds, or 30 for a project
	// with a limit. A query can raise it
	// for itself (SET LOCAL statement_timeout).
	StatementTimeoutSeconds int `json:"statementTimeoutSeconds,omitempty"`
	// Previews says which database app previews use: "branch" (the
	// default, also when empty) gives each preview its own copy-on-write
	// copy of the database, made on its first deploy and deleted with it;
	// "shared" lets previews use the production database.
	Previews PreviewDatabase `json:"previews,omitempty"`
}

// PreviewDatabase is the database app previews use.
type PreviewDatabase string

const (
	PreviewDBBranch PreviewDatabase = "branch"
	PreviewDBShared PreviewDatabase = "shared"
)

// Valkey gives the project a KV/cache namespace.
type Valkey struct {
	// MaxMemoryMB caps this project's share. Default 64. Enforced while the
	// project has a limit (see Resources); otherwise only reported.
	MaxMemoryMB int `json:"maxMemoryMB"`
}

// Storage gives the project S3-compatible buckets.
type Storage struct {
	// Buckets keyed by name.
	Buckets map[string]Bucket `json:"buckets,omitempty"`
}

// Bucket is one S3 bucket.
type Bucket struct {
	// Public buckets are readable without a signature.
	Public bool `json:"public"`
	// CORS lists the browser origins that may call the bucket's S3 API
	// (presigned uploads and downloads): "https://example.com", a wildcard
	// such as "https://*.example.com", or "*". Absent: the project's own app
	// hosts (previews and custom domains included) and http://localhost.
	CORS []string `json:"cors,omitempty"`
	// MaxFileSize is the largest object an upload may create, in bytes.
	// 0: no limit beyond the project's storage limit.
	MaxFileSize int64 `json:"maxFileSize,omitempty"`
	// AllowedTypes limits uploads to these MIME types; "image/*" matches a
	// whole family. Absent: any type.
	AllowedTypes []string `json:"allowedTypes,omitempty"`
}

// Auth Method values.
const (
	AuthEmail     = "email"      // email + password
	AuthMagicLink = "magic-link" // one-time sign-in link sent by email
	AuthOTP       = "otp"        // one-time code sent by email
	AuthPasskey   = "passkey"    // WebAuthn passkeys
	AuthGoogle    = "google"     // Sign in with Google
	AuthGitHub    = "github"     // Sign in with GitHub
)

// AuthMethods lists every valid Auth.Methods value, in sorted order.
var AuthMethods = []string{AuthEmail, AuthGitHub, AuthGoogle, AuthMagicLink, AuthOTP, AuthPasskey}

// Auth gives the project user accounts and sessions. The box serves the auth
// endpoint at "/api/auth" on each app's own routes and exposes its base URL to
// every app as TIFFIN_AUTH_URL.
type Auth struct {
	// Methods users can sign in with: "email" (email + password),
	// "magic-link", "otp" (one-time code), "passkey", "google" or "github".
	// Default ["email", "magic-link"]. Sorted and de-duplicated.
	Methods []string `json:"methods"`
	// Organizations enables teams (organizations) with the roles owner, admin,
	// member and viewer. Default true.
	Organizations bool `json:"organizations"`
	// EmailVerification: whether new users must confirm their email address
	// before they can sign in. Unset means automatic: required when the box
	// sends real mail (an SMTP relay is set up), not required while mail
	// only reaches the dev inbox, so test sign-ups work at once.
	EmailVerification *bool `json:"emailVerification,omitempty"`
}

// UnmarshalJSON decodes an Auth, defaulting Organizations to true when the
// field is absent (a plain bool cannot tell "absent" from "false").
func (a *Auth) UnmarshalJSON(b []byte) error {
	type plain Auth
	p := plain{Organizations: true}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*a = Auth(p)
	return nil
}

// Email lets the project send transactional email. Until an SMTP relay is
// configured on the box, mail goes to the box's dev inbox instead of the
// recipient.
type Email struct {
	// From is the sender address, e.g. "hello@example.com". Default
	// "<project>@<box domain>", resolved by the box: leave it unset to take
	// the default.
	From string `json:"from,omitempty"`
}

// Analytics gives the project cookieless, first-party web analytics.
type Analytics struct {
	// RetentionDays is how long raw events are kept, 1-3650. Default 365.
	RetentionDays int `json:"retentionDays"`
}

// Cron is one scheduled call into an app, or to an address outside the box.
// The box sends an HTTP request to the app's Path (or to URL) on every tick.
// An app is called internally, so a worker app (which has no routes) is a
// valid target. Exactly one of App and URL is set.
type Cron struct {
	// Schedule is a 5-field cron expression ("minute hour day-of-month month
	// day-of-week", fields separated by single spaces, e.g. "0 3 * * *") or
	// one of @hourly, @daily, @weekly, @monthly.
	Schedule string `json:"schedule"`
	// App is the name of the app to call. It must be an app in this manifest.
	App string `json:"app,omitempty"`
	// URL is an http(s) address outside the box to POST to instead of an
	// app, e.g. "https://hooks.example.com/digest". Calls are signed
	// (Tiffin-Signature) with the project's signing secret. Addresses of the
	// box itself and private, loopback or link-local ones are refused.
	URL string `json:"url,omitempty"`
	// Path is the request path on the app. Must start with "/".
	// Default "/cron/<cron name>". App targets only.
	Path string `json:"path,omitempty"`
	// Timezone is the IANA time zone the schedule is read in, e.g.
	// "America/New_York". Default "" (UTC). When clocks change, a time that
	// happens twice runs once and a time that is skipped runs at the change.
	Timezone string `json:"timezone,omitempty"`
	// Overlap lets a tick run while the previous one is still queued or
	// running. Default false: such a tick is skipped (cron list shows when).
	Overlap bool `json:"overlap,omitempty"`
	// TimeoutSeconds is how long one call may take before it counts as
	// failed and is retried, 5-3600. Default 0: 60 seconds (an app may
	// extend it with heartbeats).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// Queue is a named job queue. The box pushes each job sent to the queue to
// Path on App (or to URL, outside the box) as a signed HTTP request,
// retrying failures with backoff. An app is called internally, so a worker
// app (which has no routes) is a valid target. Exactly one of App and URL
// is set. Zero limits mean "no limit".
type Queue struct {
	// App is the name of the app that receives the jobs. It must be an app in
	// this manifest.
	App string `json:"app,omitempty"`
	// URL is an http(s) address outside the box that jobs are POSTed to
	// instead of an app, signed (Tiffin-Signature) with the project's
	// signing secret. Addresses of the box itself and private, loopback or
	// link-local ones are refused.
	URL string `json:"url,omitempty"`
	// Path is the request path jobs are POSTed to. Must start with "/".
	// Default "/queues/<queue name>". App targets only.
	Path string `json:"path,omitempty"`
	// Concurrency is the most jobs of this queue running at once, 0-1000.
	// Default 0: no limit.
	Concurrency int `json:"concurrency"`
	// KeyConcurrency is the most jobs running at once for the same job key (the
	// "key" option of a send), 0-1000. Default 0: no limit.
	KeyConcurrency int `json:"keyConcurrency"`
	// RateLimit is the most jobs started per RatePeriodSeconds for the same
	// job key, 0-10000. Default 0: no limit.
	RateLimit int `json:"rateLimit"`
	// RatePeriodSeconds is the window RateLimit counts in, 1-86400. Default 60
	// when RateLimit is set; 0 when it is not.
	RatePeriodSeconds int `json:"ratePeriodSeconds"`
	// MaxAttempts is how many times a job is tried before it goes to the
	// dead-letter queue, 1-100. Default 10.
	MaxAttempts int `json:"maxAttempts"`
	// LeaseSeconds is how long one attempt may run without a response or
	// heartbeat before it counts as failed, 5-3600. Default 60. For a URL
	// target it is each call's timeout.
	LeaseSeconds int `json:"leaseSeconds"`
}

// WWWRedirect is the Domain.WWW value that redirects www.<domain> to <domain>.
const WWWRedirect = "redirect"

// Domain holds options for one of the project's own domains.
type Domain struct {
	// WWW "redirect" also serves www.<domain> and sends its visitors to
	// <domain> (308, path and query kept); point www.<domain> at the box
	// too. Default "" (off; to serve www.<domain> itself, add it to an
	// app's routes).
	WWW string `json:"www,omitempty"`
}

// Topic is a fan-out name: every message sent to the topic becomes one job
// for each subscriber, delivered and retried independently.
type Topic struct {
	// Subscribers are the names of queues in this manifest. Each message sent
	// to the topic is POSTed to every subscriber queue's app and path. Sorted
	// and de-duplicated. Default: none (messages sent to the topic are dropped).
	Subscribers []string `json:"subscribers,omitempty"`
}
