import { defineConfig } from "tiffin-sdk/config";

export default defineConfig({
  project: "platform",
  apps: {
    web: { framework: "next", path: "apps/web" },
    // Same source as web, started with its own command.
    jobs: { framework: "bun", path: "apps/web", role: "worker", command: "bun run worker.ts" },
  },
  services: {
    auth: { methods: ["passkey", "google", "email", "github", "otp", "magic-link", "email"], organizations: false },
    email: { from: "hello@example.com" },
    analytics: { retentionDays: 90 },
  },
  crons: {
    "nightly-report": { schedule: "0 3 * * *", app: "jobs", path: "/jobs/report", timezone: "America/New_York" },
    "sweep": { schedule: "*/15 * * * mon-fri", app: "jobs", overlap: true },
    "digest": { schedule: "@weekly", app: "web" },
  },
});
