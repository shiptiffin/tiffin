import type { TiffinConfig } from "./types";

export type {
  AppConfig,
  AssetsConfig,
  BucketConfig,
  Framework,
  GitConfig,
  PostgresConfig,
  QueueConfig,
  ResourcesConfig,
  Role,
  ServicesConfig,
  Slug,
  StorageConfig,
  TiffinConfig,
  TopicConfig,
  ValkeyConfig,
} from "./types";

/**
 * Declare a Tiffin project. Returns its argument unchanged; it exists so
 * editors and agents get types and completion:
 *
 * ```ts
 * import { defineConfig } from "@shiptiffin/sdk";
 * export default defineConfig({ project: "hello", apps: { web: { framework: "next" } } });
 * ```
 *
 * `tiffin` evaluates this file in a sandbox and checks the result against the
 * manifest JSON Schema, so keep it plain data: no network, no file access.
 * It sees no environment (`process.env` is empty, as when a push deploys it)
 * and imports only files of its repository.
 */
export function defineConfig(config: TiffinConfig): TiffinConfig {
  return config;
}
