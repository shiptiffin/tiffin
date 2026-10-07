import { spawnSync } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { port } from "./helpers";

// Global teardown runs before Playwright stops the web server, so the serve
// script's own cleanup would find its folder gone: stop the jobs box's
// Postgres (e2e/serve-jobs.sh) here, then remove the throwaway box.
export default function teardown() {
  if (process.env.E2E_BASE_URL) return;
  const dir = join(tmpdir(), `tiffin-dash-e2e-${port}`);
  const pgCtl = join(dir, "pg", "bin", "pg_ctl");
  if (existsSync(pgCtl)) spawnSync(pgCtl, ["-D", join(dir, "pg", "data"), "-m", "immediate", "stop"], { stdio: "ignore" });
  rmSync(dir, { recursive: true, force: true });
}
