import { expect, test } from "bun:test";
import { join } from "node:path";

// templates/queues-worker ships the SDK bundled into tiffin-sdk.gen.js. A
// stale copy kept the template's workflows from running for weeks: the box
// had started requiring each turn's jobId and attemptId on step and wait
// writes, and the old bundle didn't send them.
test("templates/queues-worker/tiffin-sdk.gen.js matches the SDK source", async () => {
  const dir = join(import.meta.dir, "..", "..", "..", "templates", "queues-worker");
  const built = await Bun.build({ entrypoints: [join(dir, "sdk-entry.ts")], target: "bun", format: "esm" });
  expect(built.success).toBe(true);
  const code = (s: string) =>
    s
      .split("\n")
      .filter((l) => l.trim() !== "" && !l.startsWith("// "))
      .join("\n");
  const fresh = code(await built.outputs[0].text());
  const committed = code(await Bun.file(join(dir, "tiffin-sdk.gen.js")).text());
  if (fresh !== committed) {
    throw new Error("templates/queues-worker/tiffin-sdk.gen.js is stale: run `bun run sync-sdk` in templates/queues-worker and commit it");
  }
});
