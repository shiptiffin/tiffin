import { defineConfig } from "@shiptiffin/sdk";

// A web app: React Router 8 (framework mode) on Bun, reading and writing Postgres.
export default defineConfig({
  project: "rr-notes",
  apps: {
    web: { framework: "bun", healthcheck: "/healthz" },
  },
});
