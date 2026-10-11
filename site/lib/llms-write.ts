// Writes the agent files (see lib/llms.ts) into public/. Run in site/: bun lib/llms-write.ts
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { buildAgentFiles, docDates, type DocDates } from "./llms";

const root = join(import.meta.dirname, "..");
for (const [path, body] of Object.entries(buildAgentFiles(join(root, "..", "docs", "guide")))) {
  const out = join(root, "public", path);
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, body);
  console.log(`public/${path}`);
}

// The day each guide last changed, for the docs pages and the sitemap.
const datesFile = join(root, "lib", "docs-dates.json");
const previous: DocDates = existsSync(datesFile) ? JSON.parse(readFileSync(datesFile, "utf8")) : {};
const today = new Date().toISOString().slice(0, 10);
writeFileSync(datesFile, JSON.stringify(docDates(join(root, "..", "docs", "guide"), previous, today), null, 2) + "\n");
console.log("lib/docs-dates.json");
