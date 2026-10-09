// Writes every sample email to a folder, to open in a browser (light and
// dark) or a mail app: <id>.html and <id>.txt (subject on top).
//   bun emails/write-samples.ts <dir>
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { samples } from "./samples";

const out = process.argv[2];
if (!out) throw new Error("usage: bun emails/write-samples.ts <dir>");
mkdirSync(out, { recursive: true });
for (const s of samples) {
  const m = await s.build();
  writeFileSync(join(out, `${s.id}.html`), m.html ?? "");
  writeFileSync(join(out, `${s.id}.txt`), `Subject: ${m.subject}\n\n${m.text}`);
}
console.log(`${samples.length} emails in ${out}`);
