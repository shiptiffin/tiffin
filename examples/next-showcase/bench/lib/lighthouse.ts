// Lighthouse (performance only), mobile and desktop, N runs per page; the
// median of each metric. Chrome is Playwright's Chromium.
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright";
import type { Target } from "./net.ts";

export type LhRow = { page: string; formFactor: string; runs: number; score: number; fcp: number; lcp: number; tbt: number; cls: number; si: number; ttfb: number; bytes: number };

const median = (xs: number[]) => {
  const s = [...xs].sort((a, b) => a - b);
  return s.length % 2 ? s[(s.length - 1) / 2]! : (s[s.length / 2 - 1]! + s[s.length / 2]!) / 2;
};

export function lighthouse(t: Target, pages: string[], runs: number): LhRow[] {
  const dir = mkdtempSync(join(tmpdir(), "showcase-lh-"));
  const headers = join(dir, "headers.json");
  writeFileSync(headers, JSON.stringify({ Cookie: t.cookie }));
  const bin = join(import.meta.dirname, "..", "node_modules", ".bin", "lighthouse");
  const rows: LhRow[] = [];
  for (const page of pages) {
    for (const ff of ["mobile", "desktop"]) {
      const samples: Omit<LhRow, "page" | "formFactor" | "runs">[] = [];
      for (let i = 0; i < runs; i++) {
        const out = join(dir, `run.json`);
        const args = [t.base + page, "--quiet", "--output=json", `--output-path=${out}`, "--only-categories=performance",
          `--chrome-path=${chromium.executablePath()}`, "--chrome-flags=--headless=new --ignore-certificate-errors --no-first-run",
          `--extra-headers=${headers}`, "--max-wait-for-load=45000"];
        if (ff === "desktop") args.push("--preset=desktop");
        try {
          execFileSync(bin, args, { stdio: ["ignore", "ignore", "pipe"], timeout: 180_000 });
        } catch (e) {
          console.log(`  lighthouse ${page} ${ff} run ${i + 1} failed: ${(e as Error).message.slice(0, 200)}`);
          continue;
        }
        const r = JSON.parse(readFileSync(out, "utf8"));
        const a = (k: string) => Number(r.audits[k]?.numericValue ?? NaN);
        samples.push({
          score: Math.round((r.categories.performance.score ?? 0) * 100),
          fcp: a("first-contentful-paint"),
          lcp: a("largest-contentful-paint"),
          tbt: a("total-blocking-time"),
          cls: a("cumulative-layout-shift"),
          si: a("speed-index"),
          ttfb: a("server-response-time"),
          bytes: a("total-byte-weight"),
        });
      }
      if (!samples.length) continue;
      const m = (k: keyof (typeof samples)[number]) => median(samples.map((s) => s[k]));
      const row: LhRow = { page, formFactor: ff, runs: samples.length, score: m("score"), fcp: m("fcp"), lcp: m("lcp"), tbt: m("tbt"), cls: m("cls"), si: m("si"), ttfb: m("ttfb"), bytes: m("bytes") };
      console.log(`  ${page} ${ff}: score ${row.score}, LCP ${row.lcp.toFixed(0)} ms, TTFB ${row.ttfb.toFixed(0)} ms (${samples.length} runs)`);
      rows.push(row);
    }
  }
  return rows;
}
