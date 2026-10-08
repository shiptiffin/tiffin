import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website and ShipTiffin's control plane.
//
// - web: the Next.js site (home, sign-up list, privacy, terms) and the customer
//   pages (/start, /account, /sign-in, /managed, /abuse, /admin) with their API:
//   Stripe Checkout and webhooks, the Hetzner key check, managed boxes' daily
//   check-in, the monitor cron.
// - cloud: the control plane worker (cmd/tiffin-cloud, Go). It creates boxes in
//   customers' own Hetzner projects and keeps their <name>.shiptiffin.app
//   records. Built from its Dockerfile with the repository's top as context;
//   no previews, so a pull request never runs it.
//
// Both use the project's Postgres (the worker makes the cloud_* tables). Mail
// goes through the box; accounts use the box's sign-in (services.auth). The box
// deploys from GitHub: a push to main that touches an app's watch paths.
//
// Secrets: STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, STRIPE_COUPON_FOUNDING,
// STRIPE_PRICE_MONTHLY (optional; else the price with lookup key box_monthly_v1),
// CLOUD_KEK, CLOUD_LICENCE_KEY, CLOUDFLARE_API_TOKEN (shiptiffin.app zone only),
// CLOUD_ADMIN_EMAILS, CLOUD_ABUSE_NOTIFY (optional), EARLY_ACCESS_NOTIFY
// (optional). Until the Stripe and CLOUD_ ones are set, /start shows the
// sign-up list. SITE_URL overrides https://shiptiffin.com in email links.
export default defineConfig({
  project: "website",
  apps: {
    web: {
      framework: "next",
      routes: ["website", "shiptiffin.com"],
      git: { repo: "shiptiffin/tiffin", branch: "main", path: "site" },
      watch: ["site/**"],
    },
    cloud: {
      role: "worker",
      builder: "dockerfile",
      dockerfile: "cmd/tiffin-cloud/Dockerfile",
      git: { repo: "shiptiffin/tiffin", branch: "main", previews: "off" },
      watch: ["cmd/tiffin-cloud/**", "internal/**", "go.mod", "go.sum", "!**/*_test.go", "!**/*.md"],
    },
  },
  services: {
    auth: { methods: ["magic-link", "google", "github"], organizations: false },
  },
  crons: {
    monitor: { schedule: "*/5 * * * *", app: "web", path: "/api/cron/monitor", timeoutSeconds: 120 },
  },
  domains: { "shiptiffin.com": { www: "redirect" } },
});
