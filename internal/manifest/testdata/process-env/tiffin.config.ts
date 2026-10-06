import { defineConfig } from "@shiptiffin/sdk";

const stage = process.env.STAGE ?? "dev";

export default defineConfig({
  project: "envy-" + stage,
  env: {
    STAGE: stage,
    // Not set in the provided env: undefined values are dropped.
    MISSING: process.env.NOT_SET as string,
    GREETING: process.env.GREETING || "hello",
  },
  apps: { web: {} },
});
