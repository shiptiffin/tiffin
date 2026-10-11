import { describe, expect, test } from "bun:test";
import { loadDoc } from "./docs";
import { PAGES } from "./llms";

const docs = await Promise.all(PAGES.map((p) => loadDoc(p.name)));

describe("docs pages", () => {
  test("every guide renders, with a title and its date", () => {
    for (const d of docs) {
      expect(d?.title, d?.page.name).toBeTruthy();
      expect(d?.updated, d?.page.name).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    }
  });

  test("every link to a guide's heading lands on a heading that exists", () => {
    const ids = new Map(docs.map((d) => [d!.page.name, new Set([...d!.html.matchAll(/ id="([^"]+)"/g)].map((m) => m[1]))]));
    const broken: string[] = [];
    for (const d of docs) {
      for (const [, name, hash] of d!.html.matchAll(/href="\/docs\/([a-z0-9-]+)(?:#([^"]+))?"/g)) {
        if (!ids.has(name)) broken.push(`${d!.page.name} → ${name}`);
        else if (hash && !ids.get(name)!.has(hash)) broken.push(`${d!.page.name} → ${name}#${hash}`);
      }
      for (const [, hash] of d!.html.matchAll(/href="#([^"]+)"/g)) {
        if (!ids.get(d!.page.name)!.has(hash)) broken.push(`${d!.page.name} → #${hash}`);
      }
    }
    expect(broken).toEqual([]);
  });

  test("prose written as <word> shows as text, not as a tag", () => {
    for (const d of docs) expect(d!.html, d!.page.name).not.toMatch(/<(box|key|name|ip)[ >]/);
  });
});
