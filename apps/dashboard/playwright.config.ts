import { existsSync } from "node:fs";
import { defineConfig } from "@playwright/test";

// Runs against a seeded box. By default it starts one (e2e/serve.sh builds the
// dashboard into the binary, seeds a throwaway TIFFIN_HOME and serves it).
// Point E2E_BASE_URL + E2E_OWNER_TOKEN_FILE at a running box to reuse it.
const port = Number(process.env.E2E_PORT ?? 7392);
const external = !!process.env.E2E_BASE_URL;
const baseURL = process.env.E2E_BASE_URL ?? `http://localhost:${port}`;
const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

export default defineConfig({
  testDir: "e2e",
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  reporter: [["list"]],
  globalTeardown: "./e2e/teardown.ts",
  use: {
    baseURL,
    browserName: "chromium",
    launchOptions: { executablePath: process.env.CHROMIUM_PATH ?? (existsSync(chrome) ? chrome : undefined) },
    trace: "retain-on-failure",
    // A dev box serves the dashboard over HTTPS with its own CA.
    ignoreHTTPSErrors: baseURL.startsWith("https://"),
  },
  webServer: external
    ? undefined
    : {
        // E2E_SERVE=e2e/serve-jobs.sh starts a box whose Jobs module runs (jobs.spec.ts).
        command: `bash ${process.env.E2E_SERVE ?? "e2e/serve.sh"} ${port}`,
        url: `http://127.0.0.1:${port}/v1/health`, // signIn() also waits for seed-live.sh to finish
        timeout: 180_000,
        reuseExistingServer: false,
        stdout: "pipe",
      },
});
