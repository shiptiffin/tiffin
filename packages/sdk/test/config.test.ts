import { describe, expect, test } from "bun:test";
import { defineConfig, type TiffinConfig } from "../src";
import { defineConfig as defineFromConfig } from "../src/config";

// A sample that must type-check (the test file is part of `tsc -p packages/sdk`).
const sample = {
  project: "shop",
  env: { LOG_LEVEL: "info" },
  apps: {
    web: { framework: "next", routes: ["shop"], instances: 2, memoryMB: 1024 },
    api: { framework: "hono", healthcheck: "/healthz" },
    jobs: { role: "worker", memoryMB: 256, command: "bun run worker.ts" },
  },
  services: {
    postgres: { extensions: ["vector"] },
    valkey: {},
    storage: { buckets: { uploads: { public: true } } },
    auth: { methods: ["email", "google"], organizations: true },
    email: { from: "hello@example.com" },
    analytics: { retentionDays: 90 },
  },
  crons: {
    nightly: { schedule: "0 3 * * *", app: "jobs", path: "/jobs/nightly" },
    tick: { schedule: "@hourly", app: "jobs" },
    morning: { schedule: "0 9 * * mon-fri", app: "jobs", timezone: "America/New_York", overlap: true },
  },
  queues: {
    emails: { app: "jobs" },
    resize: { app: "jobs", path: "/jobs/resize", concurrency: 4, keyConcurrency: 1, rateLimit: 30, ratePeriodSeconds: 60, maxAttempts: 3, leaseSeconds: 600 },
  },
  topics: {
    "order.created": { subscribers: ["emails", "resize"] },
    "user.deleted": {},
  },
} satisfies TiffinConfig;

describe("@shiptiffin/sdk", () => {
  test("defineConfig is the identity function", () => {
    expect(defineConfig(sample)).toBe(sample);
    expect(defineFromConfig(sample)).toBe(sample);
  });

  test("only `project` is required", () => {
    const minimal: TiffinConfig = { project: "hello" };
    expect(defineConfig(minimal)).toEqual({ project: "hello" });
  });

  test("auth, email, analytics and crons are optional with all-optional fields", () => {
    const cfg: TiffinConfig = {
      project: "x",
      services: { auth: {}, email: {}, analytics: {} },
      crons: { tick: { schedule: "@daily", app: "web" } },
    };
    expect(defineConfig(cfg)).toBe(cfg);
  });

  test("queues and topics are optional; a queue needs an app", () => {
    const cfg: TiffinConfig = {
      project: "x",
      apps: { w: { role: "worker" } },
      queues: { work: { app: "w" } },
      topics: { "a.b": { subscribers: ["work"] }, c: {} },
    };
    expect(defineConfig(cfg)).toBe(cfg);
    expect(sample.queues.resize.maxAttempts).toBe(3);
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
    // @ts-expect-error unknown auth method
    defineConfig({ project: "x", services: { auth: { methods: ["sms"] } } });
    // @ts-expect-error retentionDays is a number
    defineConfig({ project: "x", services: { analytics: { retentionDays: "90" } } });
    // @ts-expect-error a cron needs a schedule and an app
    defineConfig({ project: "x", crons: { tick: {} } });
    // @ts-expect-error unknown cron field
    defineConfig({ project: "x", crons: { tick: { schedule: "@daily", app: "web", tz: "UTC" } } });
    // @ts-expect-error overlap is a boolean
    defineConfig({ project: "x", crons: { tick: { schedule: "@daily", app: "web", overlap: "yes" } } });
    // @ts-expect-error command is a string
    defineConfig({ project: "x", apps: { w: { command: ["bun", "worker.ts"] } } });
    // @ts-expect-error a queue needs an app
    defineConfig({ project: "x", queues: { work: {} } });
    // @ts-expect-error unknown queue field
    defineConfig({ project: "x", queues: { work: { app: "w", paused: true } } });
    // @ts-expect-error limits are numbers
    defineConfig({ project: "x", queues: { work: { app: "w", concurrency: "4" } } });
    // @ts-expect-error subscribers are queue names
    defineConfig({ project: "x", topics: { "a.b": { subscribers: "work" } } });
    // @ts-expect-error unknown topic field
    defineConfig({ project: "x", topics: { "a.b": { filter: "x" } } });
  });
});
