import { defineConfig } from "@shiptiffin/sdk";

// A web app: TanStack Start on Bun (Nitro's Node server), reading and writing Postgres.
export default defineConfig({
  project: "start-notes",
  apps: {
    web: { framework: "bun", healthcheck: "/healthz" },
  },
  services: {
    postgres: {},
  },
});
