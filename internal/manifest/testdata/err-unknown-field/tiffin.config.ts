import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "oops",
  region: "eu",
  apps: { web: { framework: "next", replicas: 3 } },
} as any);
