import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "oops",
  region: "eu",
  apps: { web: { framework: "next", replicas: 3 } },
} as any);
