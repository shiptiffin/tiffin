/**
 * Types for tiffin.config.ts. They mirror the Go manifest contract in
 * internal/manifest/manifest.go: every field the platform defaults is optional
 * here, and the evaluated, defaulted form lives in the Go `Manifest` type.
 * The JSON Schema (internal/manifest/schema.json) is the machine-readable twin.
 */

/** Slug rule shared by project, app and bucket names: `^[a-z][a-z0-9-]{0,39}$`. */
export type Slug = string;

/**
 * How an app is built and run. Bun is the only runtime.
 *
 * - `"next"`: Next.js
 * - `"hono"`: Hono on Bun
 * - `"bun"`: any Bun server listening on `$PORT`
 * - `"static"`: served straight by Caddy
 */
export type Framework = "next" | "hono" | "bun" | "static";

/**
 * What an app instance does.
 *
 * - `"web"`: serves HTTP on routes
 * - `"worker"`: receives queue and workflow pushes only
 */
export type Role = "web" | "worker";

/** One deployable unit. */
export interface AppConfig {
  /** Path to the app's source, relative to the manifest. Default ".". */
  path?: string;
  /** Framework. Default "bun". */
  framework?: Framework;
  /** Role. Default "web". */
  role?: Role;
  /**
   * Routes are hostnames (optionally with a path prefix) this app serves,
   * e.g. "shop" (expands to shop.<box domain>), "example.com", "example.com/api".
   * Default: the app name. Workers have no routes.
   */
  routes?: string[];
  /** Instances to run, 1-16. Default 1. */
  instances?: number;
  /**
   * Optional memory cap per instance (copy) in MiB, 64-8192. Default: no
   * per-copy cap; the copies share their project's memory (see `resources`).
   */
  memoryMB?: number;
  /** Healthcheck path. Default "/". Ignored for workers and static apps. */
  healthcheck?: string;
  /** App-specific plain environment variables (merged over the top-level `env`). */
  env?: Record<string, string>;
}

/** Postgres gives the project its own database. */
export interface PostgresConfig {
  /** Extensions to enable, e.g. "vector", "pg_cron". Sorted and de-duplicated. */
  extensions?: string[];
}

/** Valkey gives the project a KV/cache namespace. */
export interface ValkeyConfig {
  /** Caps this project's share, in MiB. Default 64. */
  maxMemoryMB?: number;
}

/** One S3 bucket. */
export interface BucketConfig {
  /** Public buckets are readable without a signature. Default false. */
  public?: boolean;
}

/** Storage gives the project S3-compatible buckets. */
export interface StorageConfig {
  /** Buckets keyed by name (same slug rules as the project name). */
  buckets?: Record<Slug, BucketConfig>;
}

/**
 * Methods users can sign in with.
 *
 * - `"email"`: email + password
 * - `"magic-link"`: one-time sign-in link sent by email
 * - `"otp"`: one-time code sent by email
 * - `"passkey"`: WebAuthn passkeys
 * - `"google"`: Sign in with Google
 * - `"github"`: Sign in with GitHub
 */
export type AuthMethod = "email" | "magic-link" | "otp" | "passkey" | "google" | "github";

/**
 * Auth gives the project user accounts and sessions. The box serves the auth
 * endpoint at "/api/auth" on each app's own routes and exposes its base URL to
 * every app as `TIFFIN_AUTH_URL`.
 */
export interface AuthConfig {
  /**
   * Methods users can sign in with: "email" (email + password),
   * "magic-link", "otp" (one-time code), "passkey", "google" or "github".
   * Default ["email", "magic-link"]. Sorted and de-duplicated.
   */
  methods?: AuthMethod[];
  /**
   * Enables teams (organizations) with the roles owner, admin, member and
   * viewer. Default true.
   */
  organizations?: boolean;
}

/**
 * Email lets the project send transactional email. Until an SMTP relay is
 * configured on the box, mail goes to the box's dev inbox instead of the
 * recipient.
 */
export interface EmailConfig {
  /**
   * Sender address, e.g. "hello@example.com". Default
   * "<project>@<box domain>", resolved by the box: leave it out to take the
   * default.
   */
  from?: string;
}

/** Analytics gives the project cookieless, first-party web analytics. */
export interface AnalyticsConfig {
  /** How long raw events are kept, in days, 1-3650. Default 365. */
  retentionDays?: number;
}

/** One scheduled call into an app. */
export interface CronConfig {
  /**
   * A 5-field cron expression ("minute hour day-of-month month day-of-week",
   * fields separated by single spaces, e.g. "0 3 * * *") or one of
   * "@hourly", "@daily", "@weekly", "@monthly".
   */
  schedule: string;
  /** Name of the app to call. Must be an app defined in `apps`. */
  app: string;
  /** Request path on the app. Must start with "/". Default "/cron/<cron name>". */
  path?: string;
}

/**
 * One named job queue. The box POSTs each job sent to the queue to `path` on
 * `app` as a signed HTTP request and retries failures with backoff. The call is
 * pushed internally, so a worker app (which has no routes) is a valid target.
 * Zero limits mean "no limit".
 */
export interface QueueConfig {
  /** Name of the app that receives the jobs. Must be an app defined in `apps`. */
  app: string;
  /** Request path jobs are POSTed to. Must start with "/". Default "/queues/<queue name>". */
  path?: string;
  /** Most jobs of this queue running at once, 0-1000. Default 0: no limit. */
  concurrency?: number;
  /**
   * Most jobs running at once for the same job key (the `key` option of a
   * send), 0-1000. Default 0: no limit.
   */
  keyConcurrency?: number;
  /**
   * Most jobs started per `ratePeriodSeconds` for the same job key, 0-10000.
   * Default 0: no limit.
   */
  rateLimit?: number;
  /**
   * Window `rateLimit` counts in, in seconds, 1-86400. Default 60 when
   * `rateLimit` is set.
   */
  ratePeriodSeconds?: number;
  /** How many times a job is tried before it goes to the dead-letter queue, 1-100. Default 8. */
  maxAttempts?: number;
  /**
   * How long one attempt may run without a response or heartbeat before it
   * counts as failed, 5-3600. Default 60. Long jobs extend their lease with
   * heartbeats.
   */
  leaseSeconds?: number;
}

/**
 * A fan-out name: every message sent to the topic becomes one job for each
 * subscriber queue, delivered and retried independently.
 */
export interface TopicConfig {
  /**
   * Names of queues defined in `queues`. Each message sent to the topic is
   * POSTed to every subscriber queue's app and path. Sorted and
   * de-duplicated. Default: none (messages sent to the topic are dropped).
   */
  subscribers?: string[];
}

/**
 * Services are the box-provided backends. Leave a service out and it is not
 * provisioned.
 */
export interface ServicesConfig {
  postgres?: PostgresConfig;
  valkey?: ValkeyConfig;
  storage?: StorageConfig;
  auth?: AuthConfig;
  email?: EmailConfig;
  analytics?: AnalyticsConfig;
}

/**
 * A project's share of the box: one lever per project. Every field is
 * optional; when `memoryMB` and `maxSharePercent` are both set, the lower
 * limit wins. Changes apply live, without restarting apps.
 */
export interface ResourcesConfig {
  /**
   * The most memory all of the project's app copies (production and
   * previews) may use together, in MiB, at least 128. A hard cap and a
   * guarantee: all projects' `memoryMB` budgets together must fit in the
   * memory the box keeps for apps (the plan checks).
   */
  memoryMB?: number;
  /** The most CPU the project's apps may use together, in cores, in steps of 0.25. At most the box's CPUs. */
  cpus?: number;
  /**
   * Caps the project at this percentage (5-100) of the box: of the memory it
   * keeps for apps and of its CPUs. A ceiling, not a reservation; it follows
   * the box when it is resized.
   */
  maxSharePercent?: number;
}

/** The shape of the default export of tiffin.config.ts. */
export interface TiffinConfig {
  /** Version of the manifest format. Always 1 for now (the default). */
  version?: 1;
  /** Project slug: lowercase letters, digits and dashes, 1-40 chars. */
  project: Slug;
  /**
   * How much of the box the project's apps may use. Leave it out for
   * automatic: the project grows into whatever the box has free, shares the
   * CPU fairly under contention and can never starve the platform or the
   * other projects' running apps.
   */
  resources?: ResourcesConfig;
  /** Apps keyed by name (same slug rules as `project`). */
  apps?: Record<Slug, AppConfig>;
  /** Services the project uses. Absent means "not provisioned". */
  services?: ServicesConfig;
  /**
   * Scheduled calls into apps, keyed by name (same slug rules as `project`).
   * Each cron sends an HTTP request to its app's path on the schedule. The
   * call is pushed to the app internally, so a worker app (which has no
   * routes) is a valid target.
   */
  crons?: Record<Slug, CronConfig>;
  /**
   * Named job queues, keyed by name (same slug rules as `project`). Declaring
   * a queue sets its target app and path, limits and retry policy; a queue
   * you do not declare still works with defaults once an app sends to it.
   * Settings declared here are re-applied on every `tiffin apply`.
   */
  queues?: Record<Slug, QueueConfig>;
  /**
   * Topics fan messages out to subscribed queues, keyed by name: a lowercase
   * letter followed by up to 63 lowercase letters, digits, dots or dashes,
   * e.g. "order.created". A topic name must not also be a queue name.
   */
  topics?: Record<string, TopicConfig>;
  /**
   * Plain, non-secret environment variables shared by all apps.
   * Secrets never live in the manifest.
   */
  env?: Record<string, string>;
}
