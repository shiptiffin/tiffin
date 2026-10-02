import { describe, expect, test } from "bun:test";
import { defineConfig, type TiffinConfig } from "tiffin-sdk";
import { defineConfig as defineFromConfig } from "tiffin-sdk/config";

// A sample that must type-check (the test file is part of `tsc -p packages/sdk`).
const sample = {
  project: "shop",
  env: { LOG_LEVEL: "info" },
  apps: {
    web: { framework: "next", routes: ["shop"], instances: 2, memoryMB: 1024 },
    api: { framework: "hono", healthcheck: "/healthz" },
    jobs: { role: "worker", memoryMB: 256 },
  },
  services: {
    postgres: { extensions: ["vector"] },
    valkey: {},
    storage: { buckets: { uploads: { public: true } } },
  },
} satisfies TiffinConfig;

describe("tiffin-sdk", () => {
  test("defineConfig is the identity function", () => {
    expect(defineConfig(sample)).toBe(sample);
    expect(defineFromConfig(sample)).toBe(sample);
  });

  test("only `project` is required", () => {
    const minimal: TiffinConfig = { project: "hello" };
    expect(defineConfig(minimal)).toEqual({ project: "hello" });
  });

  test("bad configs are type errors", () => {
    // These are checked by tsc; at runtime defineConfig does not validate.
    // @ts-expect-error project is required
    defineConfig({});
    // @ts-expect-error unknown framework
    defineConfig({ project: "x", apps: { a: { framework: "rails" } } });
    // @ts-expect-error unknown role
    defineConfig({ project: "x", apps: { a: { role: "daemon" } } });
    // @ts-expect-error unknown field
    defineConfig({ project: "x", replicas: 3 });
    // @ts-expect-error env values are strings
    defineConfig({ project: "x", env: { PORT: 3000 } });
  });
});
