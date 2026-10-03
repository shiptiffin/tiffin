// Compiles the engine into self-contained Linux binaries (no Bun needed on
// the box) and gzips them for the Go module to embed:
//   bun run scripts/build.ts [--out DIR] [--targets linux-arm64,linux-x64,darwin-arm64]
import { $ } from "bun";
import { gzipSync } from "node:zlib";
import { mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const args = process.argv.slice(2);
const opt = (n: string, d: string) => {
  const i = args.indexOf(n);
  return i >= 0 && args[i + 1] ? args[i + 1]! : d;
};
const out = resolve(opt("--out", join(import.meta.dir, "..", "dist")));
const targets = opt("--targets", "linux-arm64,linux-x64").split(",");
mkdirSync(out, { recursive: true });
const entry = join(import.meta.dir, "..", "src", "main.ts");
const mb = (n: number) => (n / 1024 / 1024).toFixed(1) + " MB";

for (const t of targets) {
  const bin = join(out, `tiffin-auth-${t}`);
  await $`bun build ${entry} --compile --minify --sourcemap=none --target=bun-${t} --outfile ${bin}`.quiet();
  const raw = readFileSync(bin);
  const gz = gzipSync(raw, { level: 9 });
  writeFileSync(bin + ".gz", gz);
  const sum = new Bun.CryptoHasher("sha256").update(raw).digest("hex");
  writeFileSync(bin + ".sha256", sum + "\n");
  console.log(`${t}: ${mb(statSync(bin).size)} (gzip ${mb(gz.length)}) sha256 ${sum.slice(0, 16)}…`);
}
