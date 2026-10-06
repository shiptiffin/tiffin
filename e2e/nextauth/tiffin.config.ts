import { defineConfig } from "tiffin-sdk";

// TestNextAuth: tiffin-sdk/next/auth in a Next.js 16 app, on two hosts.
export default defineConfig({
  project: "nextauth",
  apps: {
    web: { framework: "next", routes: ["nextauth", "nextauth-two"], memoryMB: 512, healthcheck: "/api/health" },
  },
  services: {
    postgres: {},
    email: {},
    auth: { methods: ["email", "passkey"] },
  },
});
