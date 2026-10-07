import { defineConfig } from "@shiptiffin/sdk";

// A web app: SvelteKit 3 on Bun (adapter-bun), reading and writing Postgres.
export default defineConfig({
  project: "kit-notes",
  apps: {
    web: { framework: "bun", healthcheck: "/healthz" },
  },
  services: {
    postgres: {},
  },
});
