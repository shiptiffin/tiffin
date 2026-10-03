import { defineConfig } from "tiffin-sdk";

// A Next.js app (App Router) on Bun, reading and writing Postgres.
export default defineConfig({
  project: "next-notes",
  apps: {
    web: { framework: "next" },
  },
  services: {
    postgres: {},
  },
});
