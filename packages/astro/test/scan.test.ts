import { afterAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { scanProject } from "../src/scan";

const dir = mkdtempSync(join(tmpdir(), "tiffin-astro-scan-"));
afterAll(() => rmSync(dir, { recursive: true, force: true }));
function files(map: Record<string, string>): string {
  const src = mkdtempSync(join(dir, "src-"));
  for (const [p, text] of Object.entries(map)) {
    mkdirSync(join(src, p, ".."), { recursive: true });
    writeFileSync(join(src, p), text);
  }
  return src;
}

describe("scanProject", () => {
  test("finds on-demand pages, islands, actions", () => {
    const s = scanProject(
      files({
        "pages/index.astro": "---\n---\n<h1/>",
        "pages/api/x.ts": "export const prerender = false;\nexport const GET = () => new Response()",
        "pages/b.astro": "---\nexport const prerender: boolean = false\n---",
        "components/C.astro": "<Avatar server:defer />",
        "lib/notpage.ts": "export const prerender = false;",
        "actions/index.ts": "export const server = {}",
      }),
    );
    expect(s.onDemandPages.map((p) => p.replace(/^.*\/src-[^/]+\//, "")).sort()).toEqual(["pages/api/x.ts", "pages/b.astro"]);
    expect(s.serverIslands).toHaveLength(1);
    expect(s.actions).toBe(true);
  });
  test("route cache calls, not any cache.set", () => {
    const yes = ["Astro.cache.set({ maxAge: 60 })", "context.cache.set({ tags: ['a'] })", "cache.set(false)", "await cache.invalidate({ tags: 'x' })", "Astro.cache.invalidate(entry)"];
    const no = ["cache.set(src, fn)", "const cache = new Map(); cache.set('a', 1)", "lru.cache.invalidate(key)"];
    for (const t of yes) expect(scanProject(files({ "pages/a.ts": t })).cacheCalls).toHaveLength(1);
    for (const t of no) expect(scanProject(files({ "pages/a.ts": t })).cacheCalls).toHaveLength(0);
  });
});
