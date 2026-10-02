// Package manifest defines the project manifest: the single file
// (tiffin.config.ts, evaluated to canonical JSON) that describes a project.
//
// The Go types in this file are the contract. The JSON Schema, the
// TypeScript types in tiffin-sdk and the change engine all derive from them.
package manifest

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
	Postgres *Postgres `json:"postgres,omitempty"`
	Valkey   *Valkey   `json:"valkey,omitempty"`
	Storage  *Storage  `json:"storage,omitempty"`
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
