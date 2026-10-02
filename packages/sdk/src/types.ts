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
  /** Memory cap per instance in MiB, 64-8192. Default 512. */
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
 * Services are the box-provided backends. Leave a service out and it is not
 * provisioned.
 */
export interface ServicesConfig {
  postgres?: PostgresConfig;
  valkey?: ValkeyConfig;
  storage?: StorageConfig;
}

/** The shape of the default export of tiffin.config.ts. */
export interface TiffinConfig {
  /** Version of the manifest format. Always 1 for now (the default). */
  version?: 1;
  /** Project slug: lowercase letters, digits and dashes, 1-40 chars. */
  project: Slug;
  /** Apps keyed by name (same slug rules as `project`). */
  apps?: Record<Slug, AppConfig>;
  /** Services the project uses. Absent means "not provisioned". */
  services?: ServicesConfig;
  /**
   * Plain, non-secret environment variables shared by all apps.
   * Secrets never live in the manifest.
   */
  env?: Record<string, string>;
}
