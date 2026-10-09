import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { PAGES, buildAgentFiles, setupPrompt } from "./llms";

const root = join(import.meta.dirname, "..");
const files = buildAgentFiles(join(root, "..", "docs", "guide"));

describe("agent files", () => {
  test("public/ matches docs/guide (after editing a guide, run make site-llms)", () => {
    for (const [path, body] of Object.entries(files)) {
      expect(readFileSync(join(root, "public", path), "utf8"), `public/${path}`).toBe(body);
    }
  });

  test("llms.txt follows llmstxt.org: an H1, a summary quote, then H2 sections of links", () => {
    const lines = files["llms.txt"].split("\n");
    expect(lines[0]).toBe("# ShipTiffin");
    expect(lines[2].startsWith("> ")).toBe(true);
    expect(lines.filter((l) => l.startsWith("# "))).toHaveLength(1);
    for (const l of lines.filter((x) => x.startsWith("- "))) expect(l).toMatch(/^- \[[^\]]+\]\(https:\/\/[^)]+\)(: .+)?$/);
    expect(lines).toContain("## Optional");
  });

  test("published guides link to each other on the site, never by a relative path", () => {
    for (const p of PAGES) expect(files[`docs/${p.name}.md`]).not.toMatch(/\]\([a-z0-9-]+\.md/);
    expect(files["docs/managed.md"]).not.toContain("Running the control plane");
  });

  test("every link to a guide stays on the site (the repository is private)", () => {
    for (const [path, body] of Object.entries(files)) expect(body, path).not.toContain("github.com/shiptiffin/tiffin");
    for (const name of ["limits", "security", "protection", "moving", "always-on-agents"]) expect(files[`docs/${name}.md`]).toBeDefined();
  });

  test("the setup prompt marks the person's steps and says to stop there", () => {
    const prompt = files["agent-setup.md"];
    expect(prompt.startsWith("Set up ShipTiffin for me")).toBe(true);
    expect(prompt).toContain("[Me] Pay with Stripe.");
    expect(prompt).toContain("stop and wait");
    expect(() => setupPrompt("no markers")).toThrow();
  });
});
