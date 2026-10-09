// Writes THIRD_PARTY_NOTICES: the third-party software inside the tiffin
// binary, its licences, its NOTICE files and where the source of the MPL-2.0
// parts is. The binary embeds the file (tiffin licenses, the dashboard's
// /licenses) and make release copies it next to the builds.
//
//   bun scripts/notices.ts [os/arch ...]     (make notices; needs Go, Bun and bun install)
//
// What it reads:
//   - Go: go-licenses report on ./cmd/tiffin for every release target.
//   - The dashboard: every node_modules file in a Vite build of apps/dashboard
//     (modules and assets such as fonts), plus Tailwind, whose base styles are
//     in the CSS.
//   - The auth engine: every node_modules input of a Bun build of
//     packages/auth-engine (the bundle in internal/mod/auth/engine).
//   - The SDK: its dependencies, which a project installs with it (the SDK
//     bundles none).
// The output has no dates or paths, so the same tree gives the same file.
import { $ } from "bun";
import { existsSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, relative, sep } from "node:path";

const GO_LICENSES = "github.com/google/go-licenses/v2@v2.0.1";
const OWN_MODULE = "github.com/shiptiffin/tiffin";
const root = join(import.meta.dir, "..");
const targets = process.argv.slice(2).length ? process.argv.slice(2) : ["linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"];
const apache = join(root, "packages", "sdk", "LICENSE"); // the plain Apache-2.0 text

type Entry = {
  name: string;
  version: string;
  licence: string;
  texts: string[]; // licence files
  notices: string[]; // NOTICE files
  author?: string; // named when the licence text carries no copyright line
  note?: string;
  source?: string[]; // the module and where its source is (MPL-2.0)
};

const licenceFile = /^(licen[cs]e|copying)([-._].*)?$/i;
const noticeFile = /^notice(\.(txt|md))?$/i;
const filesIn = (dir: string, re: RegExp) =>
  existsSync(dir) ? readdirSync(dir).filter((f) => re.test(f)).sort().map((f) => join(dir, f)) : [];

// ---- Go ----------------------------------------------------------------------

async function goEntries(): Promise<Entry[]> {
  const tmp = mkdtempSync(join(tmpdir(), "tiffin-notices-"));
  try {
    await $`go install ${GO_LICENSES}`.env({ ...process.env, GOBIN: tmp, GOOS: "", GOARCH: "" }).quiet();
    const tpl = join(tmp, "report.tpl");
    writeFileSync(tpl, "{{range .}}{{.Name}}\t{{.Version}}\t{{.LicenseName}}\t{{.LicensePath}}\t{{.LicenseURL}}\n{{end}}");
    const libs = new Map<string, { version: string; licences: Set<string>; path: string; url: string }>();
    for (const t of targets) {
      const [os, arch] = t.split("/");
      const out = await $`${join(tmp, "go-licenses")} report --ignore ${OWN_MODULE} --template ${tpl} ./cmd/tiffin`
        .cwd(root)
        .env({ ...process.env, GOOS: os, GOARCH: arch, CGO_ENABLED: "0" })
        .quiet()
        .text();
      for (const line of out.split("\n").filter(Boolean)) {
        const [name, version, licence, path, url] = line.split("\t");
        const lib = libs.get(name) ?? { version, licences: new Set<string>(), path, url };
        lib.licences.add(licence);
        if (lib.url === "Unknown") lib.url = url;
        libs.set(name, lib);
      }
    }
    const modcache = (await $`go env GOMODCACHE`.quiet().text()).trim();
    const entries: Entry[] = [];
    for (const [name, lib] of libs) {
      const e: Entry = { name, version: lib.version, licence: [...lib.licences].sort().join(", "), texts: [], notices: [] };
      if (lib.path !== "Unknown") {
        e.texts.push(lib.path);
        e.notices = filesIn(dirname(lib.path), noticeFile);
      }
      const o = goOverrides[name];
      if (o) {
        e.licence = o.licence;
        e.note = o.note;
        e.texts = o.text === "apache" ? [apache] : [join(moduleDir(modcache, name, lib.version), o.text)];
      }
      if (e.licence === "Unknown") throw new Error(`go-licenses found no licence for ${name} ${lib.version}: add it to goOverrides`);
      if (lib.licences.has("MPL-2.0")) e.source = mplSource(modcache, lib.path, lib.url, lib.version);
      entries.push(e);
    }
    return entries;
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

// Modules go-licenses cannot classify (checked by hand in the 2026-10-08 audit).
const goOverrides: Record<string, { licence: string; text: string; note: string }> = {
  "github.com/hslatman/ipstore": {
    licence: "Apache-2.0",
    text: "apache",
    note: "Apache-2.0 headers in each source file; the module has no LICENSE file",
  },
  "github.com/ncruces/go-sqlite3-wasm/v6": {
    licence: "MIT-0 AND blessing",
    text: "LICENSE",
    note: "SQLite itself is in the public domain",
  },
};

// The module cache folder of a package (module paths escape capitals as !x).
function moduleDir(modcache: string, pkg: string, version: string) {
  const esc = (s: string) => s.replace(/[A-Z]/g, (c) => "!" + c.toLowerCase());
  for (let p = pkg; p.includes("/"); p = p.slice(0, p.lastIndexOf("/"))) {
    const d = join(modcache, `${esc(p)}@${version}`);
    if (existsSync(d)) return d;
  }
  throw new Error(`no module folder for ${pkg}@${version} in ${modcache}`);
}

function mplSource(modcache: string, licencePath: string, url: string, version: string) {
  const rel = relative(modcache, licencePath).split(sep).join("/");
  const at = rel.indexOf("@");
  const mod = rel.slice(0, at).replace(/!([a-z])/g, (_, c: string) => c.toUpperCase());
  const parts = [`https://proxy.golang.org/${rel.slice(0, at)}/@v/${version}.zip`];
  if (url.startsWith("https://github.com/")) parts.unshift(url.replace("/blob/", "/tree/").replace(/\/[^/]+$/, ""));
  return [`${mod} ${version}`, ...parts];
}

// ---- JavaScript --------------------------------------------------------------

// The package folder a file in node_modules belongs to.
function packageDir(file: string): string | undefined {
  const i = file.lastIndexOf("/node_modules/");
  if (i < 0) return;
  const rest = file.slice(i + 14).split("/");
  const n = rest[0].startsWith("@") ? 2 : 1;
  return file.slice(0, i + 14) + rest.slice(0, n).join("/");
}

// Finds a dependency the way Node does: node_modules folders from here up.
function resolveDir(from: string, name: string): string {
  for (let d = from; ; d = dirname(d)) {
    const p = join(d, "node_modules", name);
    if (existsSync(join(p, "package.json"))) return realpathSync(p);
    if (dirname(d) === d) throw new Error(`cannot find ${name} from ${from}: run bun install`);
  }
}

function jsEntry(dir: string, note?: string): Entry {
  const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  const lic = pkg.license ?? pkg.licenses;
  const licence = typeof lic === "string" ? lic : Array.isArray(lic) ? lic.map((l) => l.type ?? l).join(" OR ") : (lic?.type ?? "see the package");
  const texts = filesIn(dir, licenceFile);
  const e: Entry = { name: pkg.name, version: pkg.version, licence, texts, notices: filesIn(dir, noticeFile), note };
  const copyright = texts.some((t) => /copyright\s+(\(c\)|©|\d{4})/i.test(readFileSync(t, "utf8")));
  if (!copyright) {
    const meta = existsSync(join(dir, "metadata.json")) ? JSON.parse(readFileSync(join(dir, "metadata.json"), "utf8")) : {};
    const author = typeof pkg.author === "string" ? pkg.author : (pkg.author?.name ?? meta.license?.attribution);
    if (author) e.author = author;
  }
  if (!texts.length) e.note = [note, "the package has no licence file; its package.json says " + licence].filter(Boolean).join("; ");
  return e;
}

async function dashboardDirs(): Promise<string[]> {
  const dash = join(root, "apps", "dashboard");
  const req = createRequire(join(dash, "package.json"));
  const vite = await import(req.resolve("vite"));
  const files = new Set<string>();
  await vite.build({
    root: dash,
    configFile: join(dash, "vite.config.ts"),
    logLevel: "warn",
    // Nothing is written: the plugin only reads the module graph.
    build: { write: false, outDir: join(tmpdir(), "tiffin-notices-dashboard"), emptyOutDir: false },
    plugins: [
      {
        name: "tiffin-notices",
        generateBundle(this: { getModuleIds(): Iterable<string> }, _: unknown, bundle: Record<string, { type: string; originalFileNames?: string[] }>) {
          for (const id of this.getModuleIds()) files.add(id);
          for (const f of Object.values(bundle)) for (const n of f.originalFileNames ?? []) files.add(join(dash, n));
        },
      },
    ],
  });
  const dirs = new Set<string>();
  for (const f of files) {
    const d = packageDir(f.replace(/\?.*$/, ""));
    if (d) dirs.add(realpathSync(d));
  }
  dirs.add(resolveDir(dash, "tailwindcss"));
  return [...dirs];
}

async function authEngineDirs(): Promise<string[]> {
  const r = await Bun.build({ entrypoints: [join(root, "packages", "auth-engine", "src", "main.ts")], target: "bun", minify: true, metafile: true });
  if (!r.success || !r.metafile) throw new Error("bundling the auth engine failed: " + r.logs.join("\n"));
  const dirs = new Set<string>();
  for (const f of Object.keys(r.metafile.inputs)) {
    const d = packageDir(join(root, f));
    if (d) dirs.add(realpathSync(d));
  }
  return [...dirs];
}

function sdkDirs(): string[] {
  const dirs = new Set<string>();
  const walk = (dir: string) => {
    const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
    for (const name of Object.keys(pkg.dependencies ?? {})) {
      const d = resolveDir(dir, name);
      if (!dirs.has(d)) {
        dirs.add(d);
        walk(d);
      }
    }
  };
  walk(join(root, "packages", "sdk"));
  return [...dirs];
}

const jsEntries = (dirs: string[], skip = /^@(shiptiffin|tiffin)\//, note = (_: string) => undefined as string | undefined) =>
  dirs.map((d) => jsEntry(d, note(d))).filter((e) => !skip.test(e.name));

// ---- Output ------------------------------------------------------------------

type Section = { title: string; about: string; entries: Entry[] };

function render(sections: Section[]): string {
  const textIds = new Map<string, number>();
  const texts: { body: string; users: string[] }[] = [];
  const norm = (s: string) => s.replace(/\r\n?/g, "\n").replace(/[ \t]+$/gm, "").trim();
  const idsOf = (e: Entry) =>
    e.texts.map((f) => {
      const body = norm(readFileSync(f, "utf8"));
      let id = textIds.get(body);
      if (id === undefined) {
        id = texts.length + 1;
        textIds.set(body, id);
        texts.push({ body, users: [] });
      }
      const who = `${e.name} ${e.version}`;
      if (!texts[id - 1].users.includes(who)) texts[id - 1].users.push(who);
      return id;
    });

  const out: string[] = [];
  const rule = "=".repeat(78);
  out.push(
    "THIRD-PARTY SOFTWARE IN TIFFIN",
    "",
    "Tiffin is free software under the GNU Affero General Public License v3.0 only",
    "(AGPL-3.0-only, the LICENSE file); the SDK, starters, templates, tracker and",
    "build glue that end up inside your apps are Apache-2.0 (README, Licensing).",
    "",
    "Tiffin includes the software below, each under its own licence. Each entry",
    "names its licence and the numbers of its licence texts, which are at the end:",
    "each distinct text once, with every package that carries it. NOTICE files of",
    "Apache-2.0 components and the source of MPL-2.0 components come before them.",
    "",
    "Generated by scripts/notices.ts (make notices). Do not edit by hand.",
  );
  sections.forEach((s, i) => {
    out.push("", rule, `${i + 1}. ${s.title}`, rule, "", ...wrap(s.about), "");
    const width = Math.min(60, Math.max(...s.entries.map((e) => e.name.length + e.version.length + 1)));
    for (const e of s.entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))) {
      const ids = idsOf(e);
      const head = `${e.name} ${e.version}`;
      out.push(`${head.padEnd(width)}  ${e.licence}${ids.length ? `  [text ${ids.join(", ")}]` : ""}`);
      if (e.author) out.push(`    author: ${e.author}`);
      if (e.note) out.push(`    ${e.note}`);
    }
  });

  const all = sections.flatMap((s) => s.entries);
  const n = sections.length;
  out.push("", rule, `${n + 1}. Source code of the MPL-2.0 components`, rule, "");
  out.push(...wrap("These are compiled into the tiffin binary unmodified. The source code of each module, exactly as built, is at:"), "");
  const seen = new Set<string>();
  for (const e of all.filter((e) => e.source).sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))) {
    const [mod, ...urls] = e.source!;
    if (seen.has(mod)) continue;
    seen.add(mod);
    out.push(`  ${mod}`, ...urls.map((u) => `    ${u}`));
  }

  out.push("", rule, `${n + 2}. NOTICE files`, rule);
  const notices = new Map<string, string[]>();
  for (const e of all) for (const f of e.notices) {
    const body = norm(readFileSync(f, "utf8"));
    notices.set(body, [...(notices.get(body) ?? []), `${e.name} ${e.version}`]);
  }
  for (const [body, users] of [...notices].sort((a, b) => (a[1][0] < b[1][0] ? -1 : 1))) {
    out.push("", `---- NOTICE of ${[...new Set(users)].join(", ")} ----`, "", body);
  }

  out.push("", rule, `${n + 3}. Licence texts`, rule);
  texts.forEach((t, i) => {
    out.push("", `---- [text ${i + 1}] ${t.users.join(", ")} ----`, "", t.body);
  });
  return out.join("\n") + "\n";
}

function wrap(text: string, first = "", rest = first, width = 78): string[] {
  const lines: string[] = [];
  let line = first;
  for (const w of text.split(" ")) {
    if (line.trim() && (line + " " + w).length > width) {
      lines.push(line);
      line = rest + w;
    } else line = line.trim() ? `${line} ${w}` : line + w;
  }
  lines.push(line);
  return lines;
}

const go = await goEntries();
const dashboard = jsEntries(await dashboardDirs(), undefined, (d) => (/\/tailwindcss$/.test(d) ? "its base styles are in the dashboard's CSS" : undefined));
const auth = jsEntries(await authEngineDirs());
const sdk = jsEntries(sdkDirs());
const data: Entry[] = [
  {
    name: "isbot patterns",
    version: "(internal/mod/analytics/enrich/isbot-patterns.json)",
    licence: "Unlicense",
    texts: [join(root, "internal", "mod", "analytics", "enrich", "isbot-LICENSE")],
    notices: [],
  },
  {
    name: "uap-core regexes",
    version: "(compiled into github.com/ua-parser/uap-go)",
    licence: "Apache-2.0",
    texts: [apache],
    notices: [],
    note: "https://github.com/ua-parser/uap-core",
  },
];

const text = render([
  {
    title: "Go modules compiled into the tiffin binary",
    about: `Every package linked into ./cmd/tiffin for ${targets.join(", ")}, as reported by ${GO_LICENSES}.`,
    entries: go,
  },
  {
    title: "JavaScript and fonts in the dashboard",
    about: "Bundled into the web dashboard (internal/dashboard/dist), which the binary embeds and serves.",
    entries: dashboard,
  },
  {
    title: "JavaScript in the auth engine",
    about: "Bundled into the auth engine (internal/mod/auth/engine/tiffin-auth.js.gz), which the binary embeds and runs on the box with Bun.",
    entries: auth,
  },
  {
    title: "Packages the SDK depends on",
    about: "@shiptiffin/sdk (embedded for tiffin sdk add) bundles no third-party code; a project that uses it installs these with it.",
    entries: sdk,
  },
  { title: "Other data in the binary", about: "Data files compiled into the tiffin binary.", entries: data },
]);
writeFileSync(join(root, "THIRD_PARTY_NOTICES"), text);
console.log(`THIRD_PARTY_NOTICES: ${go.length} Go packages, ${dashboard.length + auth.length + sdk.length} JS packages, ${(text.length / 1024).toFixed(0)} KB`);
