import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website (home, invite requests, privacy, terms). A
// Next.js server build: the pages are prerendered, and the request-an-invite form
// keeps requests in Postgres (table invite_requests, made on first use) and sends
// its confirmation email through the box (every project has Postgres, email
// and analytics, so there is no services block). The box deploys it from GitHub: a
// push to main that touches site/ goes live, a pull request gets a preview.
//
// Secrets (optional): EARLY_ACCESS_NOTIFY, the address told about each
// confirmed invite request. SITE_URL overrides https://shiptiffin.com in email links.
export default defineConfig({
  project: "website",
  apps: {
    web: {
      framework: "next",
      routes: ["website", "shiptiffin.com"],
      git: { repo: "shiptiffin/tiffin", branch: "main", path: "site" },
      watch: ["site/**"],
    },
  },
  domains: { "shiptiffin.com": { www: "redirect" } },
});
