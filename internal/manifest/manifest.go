// Package manifest defines the project manifest: the single file
// (tiffin.config.ts, evaluated to canonical JSON) that describes a project.
//
// The Go types in this file are the contract. The JSON Schema, the
// TypeScript types in tiffin-sdk and the change engine all derive from them.
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
	// Env holds plain, non-secret environment variables shared by all apps.
	// Secrets never live in the manifest.
	Env map[string]string `json:"env,omitempty"`
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
	// Default: the app name. Workers have no routes.
	Routes []string `json:"routes,omitempty"`
	// Instances to run. Default 1.
	Instances int `json:"instances"`
	// Memory cap per instance in MiB. Default 512.
	MemoryMB int `json:"memoryMB"`
	// Healthcheck path. Default "/". Ignored for workers and static apps.
	Healthcheck string `json:"healthcheck,omitempty"`
	// Env holds app-specific plain environment variables (merged over Manifest.Env).
	Env map[string]string `json:"env,omitempty"`
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
}

// Valkey gives the project a KV/cache namespace.
type Valkey struct {
	// MaxMemoryMB caps this project's share. Default 64.
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

// Cron is one scheduled call into an app. The box sends an HTTP request to the
// app's Path on every tick. The call is pushed to the app internally, so a
// worker app (which has no routes) is a valid target.
type Cron struct {
	// Schedule is a 5-field cron expression ("minute hour day-of-month month
	// day-of-week", fields separated by single spaces, e.g. "0 3 * * *") or
	// one of @hourly, @daily, @weekly, @monthly.
	Schedule string `json:"schedule"`
	// App is the name of the app to call. It must be an app in this manifest.
	App string `json:"app"`
	// Path is the request path on the app. Must start with "/".
	// Default "/cron/<cron name>".
	Path string `json:"path"`
}

// Queue is a named job queue. The box pushes each job sent to the queue to
// Path on App as a signed HTTP request, retrying failures with backoff. The
// call is pushed internally, so a worker app (which has no routes) is a valid
// target. Zero limits mean "no limit".
type Queue struct {
	// App is the name of the app that receives the jobs. It must be an app in
	// this manifest.
	App string `json:"app"`
	// Path is the request path jobs are POSTed to. Must start with "/".
	// Default "/queues/<queue name>".
	Path string `json:"path"`
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
	// dead-letter queue, 1-100. Default 8.
	MaxAttempts int `json:"maxAttempts"`
	// LeaseSeconds is how long one attempt may run without a response or
	// heartbeat before it counts as failed, 5-3600. Default 60.
	LeaseSeconds int `json:"leaseSeconds"`
}

// Topic is a fan-out name: every message sent to the topic becomes one job
// for each subscriber, delivered and retried independently.
type Topic struct {
	// Subscribers are the names of queues in this manifest. Each message sent
	// to the topic is POSTed to every subscriber queue's app and path. Sorted
	// and de-duplicated. Default: none (messages sent to the topic are dropped).
	Subscribers []string `json:"subscribers,omitempty"`
}
