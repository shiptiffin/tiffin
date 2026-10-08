/**
 * Types for tiffin.config.ts. They mirror the Go manifest contract in
 * internal/manifest/manifest.go: every field the platform defaults is optional
 * here, and the evaluated, defaulted form lives in the Go `Manifest` type.
 * The JSON Schema (internal/manifest/schema.json) is the machine-readable twin.
 */

/** Slug rule shared by project, app and bucket names: `^[a-z][a-z0-9-]{0,39}$`. */
export type Slug = string;

/**
 * How an app is built and run (on Bun unless the app sets runtime: "node").
 *
 * - `"next"`: Next.js
 * - `"hono"`: Hono on Bun
 * - `"bun"`: any Bun server listening on `$PORT`
 * - `"static"`: served straight by Caddy
 * - `"fastapi"`: FastAPI (Python), one Uvicorn process on `$PORT`
 * - `"python"`: any Python server listening on `$PORT` (Flask, Django...)
 */
export type Framework = "next" | "hono" | "bun" | "static" | "fastapi" | "python";

/**
 * What an app instance does.
 *
 * - `"web"`: serves HTTP on routes
 * - `"worker"`: receives queue and workflow pushes only
 */
export type Role = "web" | "worker";

/** A disk folder's size, e.g. "500MB", "5GB" or "1TB" (1GB = 1024MB). */
export type DiskSize = `${number}MB` | `${number}GB` | `${number}TB`;

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
   * Default: the project name for its main app ("shop", served at shop.<box domain>)
   * and "<project>-<app>" for the others ("shop-docs"). The main app is the only
   * web app, else the one named like the project, else "web", else the first one
   * declared. Workers have no routes.
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
  /**
   * Builds and runs the app: "bun" (the default) or "node", for an app that
   * needs Node.js (a native module built for it, a library that leans on Node
   * internals). Applies from the next deploy. Not for static or Python apps
   * (their Python version comes from .python-version).
   */
  runtime?: "bun" | "node";
  /**
   * Starts the app instead of the start command the build detects
   * (package.json "start"), e.g. "bun run worker.ts", so one source folder can
   * run a web app and a worker. For a Dockerfile or prebuilt image it replaces
   * the image's own command (run with /bin/sh -c). Applies from the next
   * deploy. Not for static apps.
   */
  command?: string;
  /**
   * How the app's image is made: "auto" (the default: Railpack, or a
   * Dockerfile at the app's folder when the folder has no package.json or
   * Python project), "dockerfile" (BuildKit builds the app's Dockerfile; the
   * app listens on $PORT), "static" (the same as framework "static") or
   * "prebuilt" (only `tiffin deploy --prebuilt image.tar`). Applies from the
   * next deploy.
   */
  builder?: "auto" | "dockerfile" | "static" | "prebuilt";
  /** The Dockerfile's path, relative to the app's folder. Default "Dockerfile". Builder "dockerfile" only. */
  dockerfile?: string;
  /** The Dockerfile stage to build (docker build --target). Default: the last stage. Builder "dockerfile" only. */
  target?: string;
  /**
   * Replaces the detected install command, run at the top of the app's
   * workspace, e.g. "pnpm install --frozen-lockfile". Wins over vercel.json.
   * Not for builder "dockerfile" or "prebuilt".
   */
  install?: string;
  /**
   * Replaces the detected build command (package.json "build"), run in the
   * app's folder, e.g. "bun run build:web". Wins over vercel.json. Not for
   * builder "dockerfile" or "prebuilt".
   */
  build?: string;
  /**
   * The folder, relative to the app, that a static site (or a Next.js static
   * export) serves, e.g. "dist". Default: the first of dist, build, out and
   * public with an index.html. Static sites and Next.js apps only.
   */
  output?: string;
  /**
   * Only GitHub pushes and pull requests that change a file matching one of
   * these patterns deploy the app (monorepos), e.g. ["apps/web/**",
   * "packages/ui/**", "!**\/*.md"]. Relative to the top of the repository;
   * a leading ! excludes and the last match decides. Redeploys always build.
   */
  watch?: string[];
  /**
   * Runs once per deploy, after the build and before the new version takes
   * traffic, in a one-off container of the new image with the app's env, e.g.
   * "bunx drizzle-kit migrate". A failure stops the deploy and the running
   * version keeps serving. Rollbacks do not run it, so migrations must work
   * with the previous version too. Previews run it only against their own
   * database branch. Not for static apps.
   */
  release?: string;
  /**
   * Debian (apt) packages installed in the app's image, e.g. ["ffmpeg"] or
   * ["chromium"], for apps that run programs beside their own code. Applies
   * from the next deploy. Not for static apps.
   */
  packages?: string[];
  /**
   * Folders, relative to the app's working directory (e.g. "data"), that
   * persist across deploys and restarts, each with a size: `["data"]` makes
   * each 1GB, `{ data: "5GB", renders: "20GB" }` sets them (1GB = 1024MB).
   * Writes past a folder's size fail with "disk full"; growing it applies at
   * once, without a restart. Every production instance shares them; each
   * preview gets its own of the same size. A folder starts with what the
   * image has at that path. Their sizes together must fit in the project's
   * storage limit. Not for static apps.
   */
  disk?: string[] | Record<string, DiskSize>;
  /**
   * The most one request to the app may take, in seconds, 1-86400 (24
   * hours). Default 900 (15 minutes). Past it the box answers 504, or cuts a
   * response it is streaming. Not for static apps.
   */
  timeoutSeconds?: number;
  /** App-specific plain environment variables (merged over the top-level `env`). */
  env?: Record<string, string>;
  /**
   * Connects the app to a GitHub repository through the box's GitHub App
   * (Settings › Git): every push to `branch` deploys to production, and pull
   * requests get preview deploys.
   */
  git?: GitConfig;
  /**
   * The build's client-asset directory, for a framework the box does not
   * recognize (it finds Next.js, Nuxt, TanStack Start, SolidStart, React Router,
   * Remix, SvelteKit and Astro builds itself). The box serves its files and keeps
   * the previous release's for a day, so pages loaded before a deploy keep working.
   */
  assets?: AssetsConfig;
}

/** A server app's client-asset directory. */
export interface AssetsConfig {
  /** The directory in the build, relative to the app, e.g. "dist/client". */
  dir: string;
  /** The URL path its files are served at. Default "/". */
  path?: string;
}

/** Where an app's code lives on GitHub. */
export interface GitConfig {
  /** The repository, "owner/name", e.g. "acme/shop". */
  repo: string;
  /** The production branch: every push to it deploys. Default "main". */
  branch?: string;
  /** The app's folder inside the repository (monorepos), e.g. "apps/web". Default: the top. */
  path?: string;
  /**
   * Which pull requests get a preview. "same-repo" (default): branches of this
   * repository. "forks": also forks, whose code then runs with the project's
   * env and secrets. "off": none.
   */
  previews?: "same-repo" | "forks" | "off";
}

/** Postgres gives the project its own database. */
export interface PostgresConfig {
  /** Extensions to enable, e.g. "vector", "pg_cron". Sorted and de-duplicated. */
  extensions?: string[];
  /**
   * Stops any one query after this many seconds, 1-3600. Default 30. A query
   * can raise it for itself with SET LOCAL statement_timeout.
   */
  statementTimeoutSeconds?: number;
  /**
   * Which database app previews use. "branch" (default): each preview gets its
   * own copy-on-write copy of the database, made on its first deploy and
   * deleted with the preview. "shared": previews use the production database
   * and skip apps' release commands.
   */
  previews?: "branch" | "shared";
}

/** Valkey gives the project a KV/cache namespace. */
export interface ValkeyConfig {
  /** This project's cache limit, in MiB. Default 64. Enforced while the project has a limit (resources). */
  maxMemoryMB?: number;
}

/** One S3 bucket. */
export interface BucketConfig {
  /** Public buckets are readable without a signature. Default false. */
  public?: boolean;
  /**
   * Browser origins allowed to call the bucket's S3 API (presigned uploads
   * and downloads): "https://example.com", "https://*.example.com" or "*".
   * Default: the project's own app hosts (previews and custom domains
   * included) and http://localhost.
   */
  cors?: string[];
  /** Largest object an upload may create, in bytes. Default: no limit beyond the project's storage limit. */
  maxFileSize?: number;
  /** MIME types uploads may have, e.g. ["image/*", "application/pdf"]. Default: any. */
  allowedTypes?: string[];
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
 * - `"google"`, `"github"`, `"apple"`, `"microsoft"`, `"discord"`,
 *   `"facebook"`, `"twitter"` (X), `"linkedin"`, `"gitlab"`, `"slack"`,
 *   `"twitch"`: Sign in with that service
 * - `"oidc"`: any OpenID Connect provider (Okta, Auth0, Keycloak, company SSO)
 *
 * A sign-in service uses the box-wide keys set in Box settings → Sign-in
 * providers, or the project's own `<PROVIDER>_CLIENT_ID` and
 * `<PROVIDER>_CLIENT_SECRET` secrets, which win.
 */
export type AuthMethod =
  | "email"
  | "magic-link"
  | "otp"
  | "passkey"
  | "google"
  | "github"
  | "apple"
  | "microsoft"
  | "discord"
  | "facebook"
  | "twitter"
  | "linkedin"
  | "gitlab"
  | "slack"
  | "twitch"
  | "oidc";

/**
 * Auth gives the project user accounts and sessions. The box serves the auth
 * endpoint at "/api/auth" on each app's own routes and exposes its base URL to
 * every app as `TIFFIN_AUTH_URL`.
 */
export interface AuthConfig {
  /**
   * Methods users can sign in with: "email" (email + password),
   * "magic-link", "otp" (one-time code), "passkey", or a sign-in service
   * (see AuthMethod). Default ["email", "magic-link"]. Sorted and
   * de-duplicated.
   */
  methods?: AuthMethod[];
  /**
   * Enables teams (organizations) with the roles owner, admin, member and
   * viewer. Default true.
   */
  organizations?: boolean;
  /**
   * Whether new users must confirm their email address before they can sign
   * in. Leave it out for automatic: required once the box has an SMTP relay
   * (real mail goes out), not required while mail only reaches the dev inbox,
   * so test sign-ups work at once. true or false forces it.
   */
  emailVerification?: boolean;
  /**
   * The button colour in the app's sign-in emails, as "#rrggbb", e.g.
   * "#2f6b4f". Leave it out for the default: the same brass button as the
   * dashboard. The text on the button is white or near-black, whichever
   * reads better.
   */
  emailAccent?: string;
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

/**
 * What a cron or queue calls: an app of the project (internally, so a worker
 * app with no routes is a valid target), or an http(s) address outside the
 * box. Calls to a URL are signed (Tiffin-Signature, HMAC-SHA256 with the
 * project's signing secret; check them with verifyRequest from
 * @shiptiffin/sdk/verify) and retried with backoff. Addresses of the box itself
 * and private, loopback or link-local ones are refused.
 */
export type JobTarget =
  | {
      /** Name of the app to call. Must be an app defined in `apps`. */
      app: string;
      /** Request path on the app. Must start with "/". Default "/cron/<name>" or "/queues/<name>". */
      path?: string;
      url?: never;
    }
  | {
      /** An http(s) address outside the box to POST to, e.g. "https://hooks.example.com/digest". */
      url: string;
      app?: never;
      path?: never;
    };

/** One scheduled call into an app, or to an address outside the box. */
export type CronConfig = JobTarget & {
  /**
   * A 5-field cron expression ("minute hour day-of-month month day-of-week",
   * fields separated by single spaces, e.g. "0 3 * * *") or one of
   * "@hourly", "@daily", "@weekly", "@monthly".
   */
  schedule: string;
  /**
   * IANA time zone the schedule is read in, e.g. "America/New_York". Default
   * UTC. When clocks change, a time that happens twice runs once and a time
   * that is skipped runs at the change.
   */
  timezone?: string;
  /**
   * Run a tick even while the previous one is still queued or running.
   * Default false: such a tick is skipped.
   */
  overlap?: boolean;
  /**
   * How long one call may take before it counts as failed and is retried,
   * 5-3600 seconds. Default 60 (an app may extend it with heartbeats).
   */
  timeoutSeconds?: number;
};

/**
 * One named job queue. The box POSTs each job sent to the queue to `path` on
 * `app` (or to `url`, outside the box) as a signed HTTP request and retries
 * failures with backoff. Zero limits mean "no limit".
 */
export type QueueConfig = JobTarget & {
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
  /** How many times a job is tried before it goes to the dead-letter queue, 1-100. Default 10. */
  maxAttempts?: number;
  /**
   * How long one attempt may run without a response or heartbeat before it
   * counts as failed, 5-3600. Default 60. Long jobs extend their lease with
   * heartbeats. For a `url` target it is each call's timeout.
   */
  leaseSeconds?: number;
};

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

/** Options for one of the project's own domains. */
export interface DomainConfig {
  /**
   * "redirect" also serves www.<domain> and sends its visitors to <domain>
   * (308, path and query kept); point www.<domain> at the box too.
   * Default "" (off; to serve www.<domain> itself, add it to an app's routes).
   */
  www?: "" | "redirect";
}

/**
 * Services are the box-provided backends. Leave a service out and it is not
 * provisioned.
 */
export interface ServicesConfig {
  /** Database in the dashboard. Always there; list it to set options. */
  postgres?: PostgresConfig;
  /** KV in the dashboard. Always there; list it to set options. */
  valkey?: ValkeyConfig;
  /** Files in the dashboard. Always there with a private bucket "files"; list it to add buckets. */
  storage?: StorageConfig;
  auth?: AuthConfig;
  email?: EmailConfig;
  analytics?: AnalyticsConfig;
  /** The dashboard's name for `postgres`; stored (and pulled) as `postgres`. */
  database?: PostgresConfig;
  /** The dashboard's name for `valkey`; stored (and pulled) as `valkey`. */
  cache?: ValkeyConfig;
  /** The dashboard's name for `storage`; stored (and pulled) as `storage`. */
  files?: StorageConfig;
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
  /**
   * Lets the project's production apps sleep when nobody uses them: after
   * this long with no requests and no job, cron or workflow deliveries,
   * their containers stop (freeing memory and CPU) and the next request or
   * delivery starts them again, in a few seconds. Hours or days, 1h to 30d:
   * "24h", "7d", "14d". Leave it out and they never sleep.
   */
  sleepAfter?: `${number}h` | `${number}d`;
  /**
   * Who may open the address every production deploy of a web app gets
   * (d-<id>--<app>.<apps domain>, which serves that version while the box
   * keeps it): "signed-in" (the default), people signed in to the box's
   * dashboard; "public", anyone with the address.
   */
  deployAddresses?: "signed-in" | "public";
  /** Apps keyed by name (same slug rules as `project`). */
  apps?: Record<Slug, AppConfig>;
  /**
   * Services' options. Every project always has postgres, valkey, storage
   * (with a private bucket "files"), email and analytics: list one only to
   * set its options; leaving it out never deletes it. Auth is the one you add.
   */
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
   * Options for the project's own domain names (not under the box domain),
   * keyed by host name, e.g. "example.com". Which app serves a name is set by
   * that app's `routes` ("example.com", "example.com/api"); a domain needs an
   * entry here only for its options. The box checks each name's DNS and gets
   * its certificate once it points at the box (`tiffin domains list`).
   */
  domains?: Record<string, DomainConfig>;
  /**
   * Plain, non-secret environment variables shared by all apps.
   * Secrets never live in the manifest.
   */
  env?: Record<string, string>;
}
