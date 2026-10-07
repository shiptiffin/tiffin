import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website (home, privacy, terms). A Next.js static
// export, so the box's edge serves it as files. The box deploys it from GitHub:
// a push to main that touches site/ goes live, a pull request gets a preview.
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
  services: {
    analytics: {},
  },
});
