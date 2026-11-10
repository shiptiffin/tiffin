/**
 * What the project's source uses, read before Astro builds its route list.
 *
 * The adapter has to be chosen in `astro:config:setup`, before Astro knows
 * which routes render on demand, so this reads the files as text. It is a
 * heuristic: a `prerender` value computed at build time is missed, and Astro
 * then stops the build with its own "no adapter installed" error, which
 * adding `adapter: node({ mode: "standalone" })` by hand fixes.
 */
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

export interface ProjectScan {
  /** A page or endpoint says `export const prerender = false`. */
  onDemandPages: string[];
  /** `server:defer` appears in a component. */
  serverIslands: string[];
  /** src/actions exists (Astro Actions render on demand). */
  actions: boolean;
  /** `Astro.cache` / `context.cache` is set or invalidated somewhere. */
  cacheCalls: string[];
}

const SOURCE = /\.(astro|md|mdx|markdoc|[cm]?[jt]sx?)$/;
const PRERENDER_FALSE = /export\s+const\s+prerender\s*(?::\s*boolean\s*)?=\s*false\b/;
const SERVER_DEFER = /\bserver:defer\b/;
const CACHE_CALL = /\bcache\s*\.\s*(set|invalidate)\s*\(/;

function walk(dir: string, out: string[], limit = 20_000): void {
  let names: string[];
  try {
    names = readdirSync(dir);
  } catch {
    return;
  }
  for (const n of names) {
    if (out.length >= limit) return;
    if (n === "node_modules" || n.startsWith(".")) continue;
    const p = join(dir, n);
    let s;
    try {
      s = statSync(p);
    } catch {
      continue;
    }
    if (s.isDirectory()) walk(p, out, limit);
    else if (SOURCE.test(n) && s.size < 2_000_000) out.push(p);
  }
}

/** Reads every source file under srcDir once. */
export function scanProject(srcDir: string): ProjectScan {
  const files: string[] = [];
  walk(srcDir, files);
  const pages = join(srcDir, "pages");
  const scan: ProjectScan = { onDemandPages: [], serverIslands: [], actions: false, cacheCalls: [] };
  for (const f of files) {
    let text: string;
    try {
      text = readFileSync(f, "utf8");
    } catch {
      continue;
    }
    if (f.startsWith(pages) && PRERENDER_FALSE.test(text)) scan.onDemandPages.push(f);
    if (SERVER_DEFER.test(text)) scan.serverIslands.push(f);
    if (CACHE_CALL.test(text)) scan.cacheCalls.push(f);
  }
  scan.actions = existsSync(join(srcDir, "actions"));
  return scan;
}
