// Writes the agent files (see lib/llms.ts) into public/. Run in site/: bun lib/llms-write.ts
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { buildAgentFiles } from "./llms";

const root = join(import.meta.dirname, "..");
for (const [path, body] of Object.entries(buildAgentFiles(join(root, "..", "docs", "guide")))) {
  const out = join(root, "public", path);
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, body);
  console.log(`public/${path}`);
}
