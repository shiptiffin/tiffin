import { defineConfig } from "tiffin-sdk/config";

export default defineConfig({
  project: "shop",
  apps: {
    web: { framework: "next", path: "apps/web" },
    jobs: { framework: "bun", path: "apps/jobs", role: "worker" },
  },
  queues: {
    // Everything defaulted: path /queues/emails, 10 attempts, 60 s lease.
    emails: { app: "jobs" },
    // Explicit limits; the rate window defaults to 60 s.
    "image-resize": { app: "jobs", path: "/jobs/resize", concurrency: 4, keyConcurrency: 1, rateLimit: 30, maxAttempts: 3, leaseSeconds: 600 },
    audit: { app: "web", rateLimit: 5, ratePeriodSeconds: 10 },
  },
  topics: {
    "order.created": { subscribers: ["emails", "audit", "emails"] },
    "order.shipped": { subscribers: ["emails"] },
    // A topic with no subscribers yet.
    "user.deleted": {},
  },
});
