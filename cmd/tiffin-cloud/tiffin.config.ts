import { defineConfig } from "@shiptiffin/sdk";

// ShipTiffin's control plane worker (cmd/tiffin-cloud, Go), in a project of
// its own so that its secrets never reach the website (project "website",
// site/tiffin.config.ts), its builds or any preview. It creates managed boxes
// in customers' own Hetzner projects and keeps their <name>.shiptiffin.app
// records. Built from its Dockerfile with the repository's top as context;
// no previews, so a pull request never runs it.
//
// Secrets (this project only): CONTROL_DATABASE_URL (the website project's
// DATABASE_URL: the job table lives there), CLOUD_SEAL_KEY, CLOUD_LICENCE_KEY,
// CLOUDFLARE_API_TOKEN (shiptiffin.app zone only). Optional: CLOUD_SSH_FROM,
// CLOUD_RELEASE_SOURCE, CLOUD_CONTROL_URL, CLOUD_ZONE. `tiffin-cloud keygen`
// makes the key pairs. Apply with: tiffin apply (from cmd/tiffin-cloud).
export default defineConfig({
  project: "cloud",
  apps: {
    worker: {
      role: "worker",
      builder: "dockerfile",
      dockerfile: "cmd/tiffin-cloud/Dockerfile",
      git: { repo: "shiptiffin/tiffin", branch: "main", previews: "off" },
      watch: ["cmd/tiffin-cloud/**", "internal/**", "go.mod", "go.sum", "!**/*_test.go", "!**/*.md", "!**/tiffin.config.ts"],
    },
  },
});
