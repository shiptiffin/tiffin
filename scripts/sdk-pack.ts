// Builds tiffin-sdk (packages/sdk) and @tiffin/react (packages/react) into
// plain ESM + .d.ts with a publishable package.json, into
// internal/sdkpkg/files/<name>/. The Go binary embeds them and `tiffin sdk add`
// (and `tiffin init`) writes them into a project as vendor/<name>-<version>.tgz,
// so apps install the SDK with no npm registry. The output is committed, like
// the dashboard build, so plain `go build` works without Bun.
//
//   bun scripts/sdk-pack.ts        (make sdk; make build and make release run it)
import { $ } from "bun";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";

const root = join(import.meta.dir, "..");
const outRoot = join(root, "internal", "sdkpkg", "files");

type Pkg = {
  name: string;
  version: string;
  description?: string;
  license?: string;
  exports: Record<string, string | { types: string; default: string }>;
  dependencies?: Record<string, string>;
  peerDependencies?: Record<string, string>;
  peerDependenciesMeta?: Record<string, { optional?: boolean }>;
};

async function pack(dir: string, outName: string) {
  const src = join(root, dir);
  const pkg = JSON.parse(readFileSync(join(src, "package.json"), "utf8")) as Pkg;
  const out = join(outRoot, outName);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(join(out, "lib"), { recursive: true });

  // tsc emits JS and declarations file by file, so the package keeps its
  // module structure (shared state such as tiffin-sdk/next's configure() is
  // one module, not a copy per entry) and "use client" directives.
  const tsconfig = join(src, "tsconfig.pack.json");
  writeFileSync(
    tsconfig,
    JSON.stringify({
      extends: "./tsconfig.json",
      compilerOptions: { noEmit: false, declaration: true, outDir: relative(src, join(out, "lib")), rootDir: "src", sourceMap: false },
      include: ["src"],
    }),
  );
  try {
    await $`bunx tsc -p ${tsconfig}`.cwd(root);
  } finally {
    rmSync(tsconfig);
  }
  // The sources import "./store"; Node's ESM loader wants "./store.js".
  const glob = new Bun.Glob("**/*.{js,d.ts}");
  for (const rel of glob.scanSync(join(out, "lib"))) {
    const file = join(out, "lib", rel);
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

  const exportsOut: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(pkg.exports)) {
    if (typeof v === "string") {
      exportsOut[k] = v;
      continue;
    }
    const rel = v.default.replace(/^\.\/src\//, "./lib/").replace(/\.tsx?$/, "");
    exportsOut[k] = { types: rel + ".d.ts", default: rel + ".js" };
    if (!existsSync(join(out, rel + ".js")) || !existsSync(join(out, rel + ".d.ts"))) throw new Error(`${pkg.name}: ${k} was not built (${rel})`);
  }
  const clean = (deps?: Record<string, string>) =>
    deps && Object.keys(deps).length ? Object.fromEntries(Object.entries(deps).filter(([, v]) => !v.startsWith("workspace:"))) : undefined;
  const manifest = {
    name: pkg.name,
    version: pkg.version,
    description: pkg.description,
    license: pkg.license,
    type: "module",
    sideEffects: false,
    exports: exportsOut,
    dependencies: clean(pkg.dependencies),
    peerDependencies: pkg.peerDependencies,
    peerDependenciesMeta: pkg.peerDependenciesMeta,
  };
  writeFileSync(join(out, "package.json"), JSON.stringify(manifest, null, 2) + "\n");
  for (const f of ["LICENSE"]) {
    const p = join(root, f);
    if (existsSync(p)) writeFileSync(join(out, f), readFileSync(p));
  }
  console.log(`${pkg.name}@${pkg.version} → ${relative(root, out)}`);
}

mkdirSync(dirname(outRoot), { recursive: true });
await pack("packages/sdk", "tiffin-sdk");
await pack("packages/react", "tiffin-react");
