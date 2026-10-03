import { defineConfig } from "tiffin-sdk/config";

export default defineConfig({
  project: "platform",
  apps: {
    web: { framework: "next", path: "apps/web" },
    jobs: { framework: "bun", path: "apps/jobs", role: "worker" },
  },
  services: {
    auth: { methods: ["passkey", "google", "email", "github", "otp", "magic-link", "email"], organizations: false },
    email: { from: "hello@example.com" },
    analytics: { retentionDays: 90 },
  },
  crons: {
    "nightly-report": { schedule: "0 3 * * *", app: "jobs", path: "/jobs/report" },
    "sweep": { schedule: "*/15 * * * mon-fri", app: "jobs" },
    "digest": { schedule: "@weekly", app: "web" },
  },
});
