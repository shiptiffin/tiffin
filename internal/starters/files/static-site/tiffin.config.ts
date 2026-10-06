import { defineConfig } from "@shiptiffin/sdk";

// A static site: no container runs. The box's edge serves public/ over HTTPS.
export default defineConfig({
  project: "my-site",
  apps: {
    site: { framework: "static" },
  },
});
