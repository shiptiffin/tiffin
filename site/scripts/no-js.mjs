// The site has no interactive parts: every page is complete HTML at build
// time. So after `next build` writes out/, drop the React runtime and the
// RSC payload from each page (about 180 KB of gzipped JS a visit). Links
// become plain page loads, and the CSS and fonts stay as they are.
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const out = new URL("../out/", import.meta.url).pathname;
let pages = 0;
let saved = 0;

function walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p);
    else if (e.name.endsWith(".html")) strip(p);
  }
}

function strip(file) {
  const before = readFileSync(file, "utf8");
  const after = before
    .replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, "")
    .replace(/<link\b[^>]*\bas="script"[^>]*\/?>/g, "")
    .replace(/<link\b[^>]*\brel="modulepreload"[^>]*\/?>/g, "");
  if (/<script\b/.test(after)) throw new Error(`${file}: a script is left`);
  writeFileSync(file, after);
  pages++;
  saved += before.length - after.length;
}

walk(out);
console.log(`no-js: ${pages} pages, ${Math.round(saved / 1024)} KB of scripts removed`);
