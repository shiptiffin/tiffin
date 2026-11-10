// Builds the package into dist/: plain ESM and .d.ts, file by file, so it
// keeps its module structure (shared state such as next's configure() is one
// module, not a copy per entry) and "use client" directives.
//
//   bun run build     (scripts/sdk-pack.ts runs it too)
import { $ } from "bun";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = join(import.meta.dir, "..");
const dist = join(root, "dist");
rmSync(dist, { recursive: true, force: true });

const tsconfig = join(root, "tsconfig.build.json");
writeFileSync(
  tsconfig,
  JSON.stringify({
    extends: "./tsconfig.json",
    compilerOptions: { noEmit: false, declaration: true, outDir: "dist", rootDir: "src", sourceMap: false },
    include: ["src"],
  }),
);
try {
  await $`bunx tsc -p ${tsconfig}`.cwd(root);
} finally {
  rmSync(tsconfig);
}

// The sources import "./store"; Node's ESM loader wants "./store.js".
for (const rel of new Bun.Glob("**/*.{js,d.ts}").scanSync(dist)) {
  const file = join(dist, rel);
  const text = readFileSync(file, "utf8");
  const fixed = text.replace(/(\bfrom\s*|\bimport\s*\(\s*|\bimport\s+)(["'])(\.{1,2}\/[^"']+)\2/g, (m, pre, q, spec) => {
    if (/\.(m?js|json)$/.test(spec)) return m;
    const base = join(dirname(file), spec);
    if (existsSync(base + ".js")) return `${pre}${q}${spec}.js${q}`;
    if (existsSync(join(base, "index.js"))) return `${pre}${q}${spec}/index.js${q}`;
    return m; // not a file of the package (an example in a comment)
  });
  if (fixed !== text) writeFileSync(file, fixed);
}

// Every export must exist.
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as { name: string; version: string; exports: Record<string, string | Record<string, string>> };
for (const [k, v] of Object.entries(pkg.exports)) {
  for (const f of typeof v === "string" ? [v] : Object.values(v)) {
    if (!existsSync(join(root, f))) throw new Error(`${pkg.name}: export ${k} points at ${f}, which was not built`);
  }
}
console.log(`${pkg.name}@${pkg.version} → dist/`);
