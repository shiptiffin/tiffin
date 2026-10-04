/**
 * Declare a Tiffin project. Returns its argument unchanged; it exists so
 * editors and agents get types and completion:
 *
 * ```ts
 * import { defineConfig } from "tiffin-sdk";
 * export default defineConfig({ project: "hello", apps: { web: { framework: "next" } } });
 * ```
 *
 * `tiffin` evaluates this file in a sandbox and checks the result against the
 * manifest JSON Schema, so keep it plain data: no network, no file access.
 * `process.env.X` is available for non-secret values.
 */
export function defineConfig(config) {
    return config;
}
