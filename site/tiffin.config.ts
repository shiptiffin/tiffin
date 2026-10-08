import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website (home, early access, privacy, terms). A
// Next.js server build: the pages are prerendered, and the early-access list
// keeps sign-ups in Postgres (table early_access, made on first use) and sends
// its confirmation email through the box (every project has Postgres, email
// and analytics, so there is no services block). The box deploys it from GitHub: a
// push to main that touches site/ goes live, a pull request gets a preview.
//
// Secrets (optional): EARLY_ACCESS_NOTIFY, the address told about each
// confirmed sign-up. SITE_URL overrides https://shiptiffin.com in email links.
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
