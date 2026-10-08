import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website and the customer side of ShipTiffin's
// control plane (project "website").
//
// - web: the Next.js site (home, sign-up list, privacy, terms) and the customer
//   pages (/start, /account, /sign-in, /managed, /abuse, /admin) with their API:
//   Stripe Checkout and webhooks, the Hetzner key check, managed boxes'
//   check-ins, the monitor cron.
//
// The worker that touches infrastructure is a separate project, "provisioner"
// (cmd/tiffin-provisioner/tiffin.config.ts), so the Cloudflare token, the licence
// signing key and the key that opens customers' Hetzner tokens never reach
// this app. The website's Postgres holds the cloud_* tables; the worker
// reaches it with this project's DATABASE_URL as its CONTROL_DATABASE_URL.
// Mail goes through the box; accounts use the box's sign-in (services.auth).
// Previews are off: a pull request's code would get this project's secrets.
//
// Secrets: STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, STRIPE_COUPON_FOUNDING,
// STRIPE_PRICE_MONTHLY (optional; else the price with lookup key box_monthly_v1),
// STRIPE_CHECKOUT_LINK (optional, "1": offer Link in Checkout besides cards),
// CLOUD_SEAL_PUBLIC and CLOUD_LICENCE_PUBLIC (public keys only),
// CLOUD_ADMIN_USER_IDS (account ids allowed into /admin), CLOUD_ABUSE_NOTIFY
// (optional), EARLY_ACCESS_NOTIFY (optional). Until the Stripe and CLOUD_ ones
// are set, /start shows the sign-up list. SITE_URL overrides
// https://shiptiffin.com in email links.
export default defineConfig({
  project: "website",
  apps: {
    web: {
      framework: "next",
      routes: ["website", "shiptiffin.com"],
      git: { repo: "shiptiffin/tiffin", branch: "main", path: "site", previews: "off" },
      watch: ["site/**"],
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
