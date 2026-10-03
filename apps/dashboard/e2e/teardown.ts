import { rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { port } from "./helpers";

// Playwright stops the web server with SIGKILL, so serve.sh can't clean up
// after itself: remove the throwaway box here.
export default function teardown() {
  if (!process.env.E2E_BASE_URL) rmSync(join(tmpdir(), `tiffin-dash-e2e-${port}`), { recursive: true, force: true });
}
