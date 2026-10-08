import { defineConfig } from "@shiptiffin/sdk";

// A web app: Next.js (App Router) on Bun, reading and writing Postgres.
export default defineConfig({
  project: "next-notes",
  apps: {
    web: { framework: "next" },
  },
});
