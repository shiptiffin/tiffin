import { defineConfig } from "@shiptiffin/sdk";

// shiptiffin.com: the public website (home, privacy, terms). A Next.js static
// export, so the box's edge serves it as files.
export default defineConfig({
  project: "website",
  apps: {
    web: { framework: "next", routes: ["website", "shiptiffin.com"] },
  },
  domains: { "shiptiffin.com": { www: "redirect" } },
  services: {
    analytics: {},
  },
});
