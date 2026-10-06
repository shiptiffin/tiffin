import { defineConfig } from "tiffin-sdk";

// A small store, blog and dashboard that uses most of what Next.js 16 offers:
// static pages, ISR, Cache Components with a dynamic hole, streaming,
// Server Actions, route handlers, SSE, next/image and next/font. It is also
// the app bench/ measures (see bench/README.md).
export default defineConfig({
  project: "next-showcase",
  apps: {
    web: { framework: "next", instances: 2, memoryMB: 512, healthcheck: "/api/health", release: "bun lib/seed-run.ts" },
  },
  services: {
    postgres: {},
    valkey: {},
    storage: { buckets: { media: { public: true } } },
  },
});
