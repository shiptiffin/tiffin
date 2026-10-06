// Builds @shiptiffin/sdk (packages/sdk, `bun run build`) and copies the
// package as npm would publish it into internal/sdkpkg/files/shiptiffin-sdk/.
// The Go binary embeds it: `tiffin sdk add` (and `tiffin init`) writes it into
// a project as vendor/shiptiffin-sdk-<version>.tgz for apps that don't install
// from npm, and the box copies its Next.js cache handlers into Next.js
// builds. The output is committed, like the dashboard build, so plain
// `go build` works without Bun.
//
//   bun scripts/sdk-pack.ts        (make sdk; make build and make release run it)
import { $ } from "bun";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";

const root = join(import.meta.dir, "..");
const src = join(root, "packages", "sdk");
const out = join(root, "internal", "sdkpkg", "files", "shiptiffin-sdk");

await $`bun run build`.cwd(src).quiet();
const pkg = JSON.parse(readFileSync(join(src, "package.json"), "utf8")) as Record<string, unknown>;
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
cpSync(join(src, "dist"), join(out, "dist"), { recursive: true });
for (const f of ["README.md", "LICENSE"]) if (existsSync(join(src, f))) cpSync(join(src, f), join(out, f));
// What an installed copy needs: no scripts or dev dependencies.
const { scripts: _s, devDependencies: _d, publishConfig: _p, ...manifest } = pkg;
writeFileSync(join(out, "package.json"), JSON.stringify(manifest, null, 2) + "\n");
console.log(`${pkg.name}@${pkg.version} → ${relative(root, out)}`);
