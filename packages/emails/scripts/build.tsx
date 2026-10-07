// Compiles every template in src/emails to what the senders embed:
//
//   box family -> internal/mod/email/templates/box/<id>.html.tmpl  (Go html/template)
//                 internal/mod/email/templates/box/<id>.txt.tmpl   (Go text/template: subject + plain text)
//                 internal/mod/email/templates/gen.go              (typed data structs + one func per email)
//   app family -> packages/auth-engine/src/templates.gen.ts        (one function per email, no imports)
//
// React renders each template once with placeholder props (src/ui/compile.tsx);
// this script turns the placeholders into template actions. Nothing here runs
// when mail is sent.
//
//   bun scripts/build.tsx           write the files
//   bun scripts/build.tsx --check   fail if the committed files are stale
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { render, toPlainText } from "react-email";
import { appVars } from "../src/ui/app";
import { boxVars } from "../src/ui/box";
import { brass } from "../src/ui/brand";
import { RAW, placeholderProps, setMode, type EmailSpec, type Var } from "../src/ui/compile";

const root = resolve(import.meta.dir, "..", "..", "..");
const goDir = join(root, "internal/mod/email/templates");
const tsOut = join(root, "packages/auth-engine/src/templates.gen.ts");
const check = process.argv.includes("--check");

type Mod = { spec: EmailSpec<Record<string, Var>>; default: (p: Record<string, unknown>) => React.ReactElement };

// ---- 1. render ----------------------------------------------------------

export type Compiled = { spec: EmailSpec<Record<string, Var>>; html: string; text: string; subject: string };

// Headings keep their case; links marked tf-plain show their words only (their words are the address).
const textSelectors = [
  ...["h1", "h2", "h3", "h4", "h5", "h6"].map((selector) => ({ selector, options: { uppercase: false } })),
  { selector: "a.tf-plain", format: "anchor", options: { ignoreHref: true } },
];

export async function compileAll(): Promise<Compiled[]> {
  const dir = join(import.meta.dir, "..", "src/emails");
  const out: Compiled[] = [];
  for (const f of readdirSync(dir).filter((f) => f.endsWith(".tsx")).sort()) {
    const mod = (await import(join(dir, f))) as Mod;
    const { spec } = mod;
    if (`${spec.id}.tsx` !== f) throw new Error(`${f}: spec.id is "${spec.id}"`);
    const props = placeholderProps(spec.vars);
    const C = mod.default;
    setMode("html");
    const html = cleanHtml(await render(<C {...props} />));
    const subject = String(spec.subject(props as never));
    setMode("text");
    const text = cleanText(toPlainText(await render(<C {...props} />), { selectors: textSelectors }));
    setMode("live");
    out.push({ spec, html, text, subject });
  }
  return out;
}

function cleanHtml(h: string): string {
  return h
    .replace(/<!--(\$|\/\$|html|head|body| )-->/g, "") // React's markers
    .replace(/<style><\/style>/g, "")
    .replace(/%%!(\w+)%%/g, (_, n: keyof typeof RAW) => {
      if (!(n in RAW)) throw new Error(`unknown raw snippet ${n}`);
      return RAW[n];
    });
}

/**
 * Lines that hold only markers fold into the markers, followed by the widest
 * line break around them, so a branch that isn't taken leaves no blank lines
 * behind. Senders collapse runs of blank lines after filling in values
 * (text() in the generated code), so a stray extra break never shows.
 */
function cleanText(t: string): string {
  const parts = t.replace(/^\s+|\s+$/g, "").split(/(\n+)/); // content, sep, content, sep, ...
  const isMarkers = (x: string) => /^[ \t]*(%%[#:/][^%\n]*%%[ \t]*)+$/.test(x);
  const out: string[] = [];
  for (let i = 0; i < parts.length; i += 2) {
    const line = parts[i]!;
    if (!isMarkers(line)) {
      out.push(line);
      if (i + 1 < parts.length) out.push(parts[i + 1]!);
      continue;
    }
    // a run of marker lines
    let nl = out.length && /^\n+$/.test(out.at(-1)!) ? out.pop()!.length : 0;
    let markers = line.trim();
    let j = i;
    while (j + 2 < parts.length && isMarkers(parts[j + 2]!)) {
      nl = Math.max(nl, parts[j + 1]!.length);
      markers += parts[j + 2]!.trim();
      j += 2;
    }
    if (j + 1 < parts.length) nl = Math.max(nl, parts[j + 1]!.length);
    out.push(markers.replace(/[ \t]+/g, ""), "\n".repeat(j + 1 < parts.length ? nl : 0));
    i = j;
  }
  return out.join("").replace(/\n{3,}/g, "\n\n") + "\n";
}

// ---- 2. parse -----------------------------------------------------------

type Cond = { name: string; eq?: string } | null; // null: else
type Node = { t: "s"; s: string } | { t: "v"; name: string; ctx: Ctx } | { t: "if"; branches: { cond: Cond; body: Node[] }[] };
type Ctx = "text" | "attr" | "url" | "style" | "rawtext";

const TOKEN = /%%([=#:/])([^%]*)%%/g;

function parse(src: string, where: string, html: boolean): Node[] {
  const rootList: Node[] = [];
  const stack: { node: Extract<Node, { t: "if" }>; list: Node[] }[] = [];
  let list = rootList;
  let last = 0;
  const ctx = html ? new HtmlScanner() : null;
  for (const m of src.matchAll(TOKEN)) {
    const s = src.slice(last, m.index);
    if (s) list.push({ t: "s", s });
    ctx?.feed(s);
    last = m.index! + m[0].length;
    const [, op, arg] = m as unknown as [string, string, string];
    if (op === "=") {
      list.push({ t: "v", name: arg, ctx: ctx ? ctx.context() : "text" });
    } else if (op === "#") {
      const [name, eq] = arg.split("=") as [string, string | undefined];
      const node: Extract<Node, { t: "if" }> = { t: "if", branches: [{ cond: { name, eq }, body: [] }] };
      list.push(node);
      stack.push({ node, list });
      list = node.branches[0]!.body;
      ctx?.push();
    } else if (op === ":") {
      const top = stack.at(-1);
      if (!top) throw new Error(`${where}: else without if`);
      const [name, eq] = arg ? (arg.split("=") as [string, string | undefined]) : [];
      const b = { cond: name ? { name, eq } : null, body: [] as Node[] };
      top.node.branches.push(b);
      list = b.body;
      ctx?.branch();
    } else if (op === "/") {
      const top = stack.pop();
      if (!top) throw new Error(`${where}: end without if`);
      list = top.list;
      ctx?.pop(where);
    }
  }
  if (stack.length) throw new Error(`${where}: unclosed if`);
  const s = src.slice(last);
  if (s) list.push({ t: "s", s });
  if (/%%/.test(JSON.stringify(rootList.filter((n) => n.t === "s")))) throw new Error(`${where}: stray %% left in output`);
  return rootList;
}

/** Just enough of an HTML tokenizer to know where a placeholder sits. React writes well-formed, double-quoted markup. */
class HtmlScanner {
  st: "text" | "tag" | "attr" | "comment" | "rawtext" = "text";
  attr = "";
  tag = "";
  buf = "";
  saved: string[] = [];
  feed(s: string) {
    for (let i = 0; i < s.length; i++) {
      const c = s[i]!;
      switch (this.st) {
        case "text":
          if (s.startsWith("<!--", i)) {
            this.st = "comment";
            i += 3;
          } else if (c === "<") {
            this.st = "tag";
            this.buf = "";
            this.tag = "";
          }
          break;
        case "comment":
          if (s.startsWith("-->", i)) {
            this.st = "text";
            i += 2;
          }
          break;
        case "tag":
          if (c === '"') {
            this.attr = (this.buf.match(/([\w:-]+)=$/)?.[1] ?? "").toLowerCase();
            this.st = "attr";
          } else if (c === ">") {
            this.tag = (this.buf.match(/^\/?([\w-]+)/)?.[1] ?? "").toLowerCase();
            this.st = this.tag === "style" || this.tag === "script" ? "rawtext" : "text";
          } else this.buf += c;
          break;
        case "attr":
          if (c === '"') {
            this.st = "tag";
            this.buf += '""';
          }
          break;
        case "rawtext":
          if (s.startsWith("</", i)) {
            this.st = "tag";
            this.buf = "";
          }
          break;
      }
    }
  }
  context(): Ctx {
    if (this.st === "text") return "text";
    if (this.st === "attr") return this.attr === "href" || this.attr === "src" ? "url" : this.attr === "style" ? "style" : "attr";
    if (this.st === "rawtext") return "rawtext";
    throw new Error(`placeholder inside <${this.tag || "?"}> markup (${this.st})`);
  }
  key() {
    return this.st === "attr" ? `attr|${this.attr}` : `${this.st}|`;
  }
  push() {
    this.saved.push(this.key());
  }
  branch() {
    const k = this.saved.at(-1)!;
    [this.st, this.attr] = k.split("|") as [typeof this.st, string];
  }
  pop(where: string) {
    const k = this.saved.pop()!;
    if (k !== this.key()) throw new Error(`${where}: a conditional changes the HTML context (${k} -> ${this.key()})`);
  }
}

function walk(nodes: Node[], f: (n: Node) => void) {
  for (const n of nodes) {
    f(n);
    if (n.t === "if") n.branches.forEach((b) => walk(b.body, f));
  }
}

function validate(c: Compiled, parts: Record<string, Node[]>) {
  const used = new Set<string>();
  for (const [part, nodes] of Object.entries(parts)) {
    walk(nodes, (n) => {
      const where = `${c.spec.id}.${part}`;
      const check = (name: string) => {
        if (!(name in c.spec.vars)) throw new Error(`${where}: unknown variable ${name}`);
        used.add(name);
        return c.spec.vars[name]!;
      };
      if (n.t === "v") {
        const v = check(n.name);
        if (v.kind === "flag" || v.kind === "enum") throw new Error(`${where}: ${n.name} is a ${v.kind}; use when/choose`);
        if (n.ctx === "url" && v.kind !== "url") throw new Error(`${where}: ${n.name} is used as a link; declare it v.url`);
        if (n.ctx === "style" && v.kind !== "color") throw new Error(`${where}: ${n.name} is in a style; only v.color may be`);
        if (n.ctx === "rawtext") throw new Error(`${where}: ${n.name} inside <style>`);
        if (v.kind === "color" && n.ctx !== "style") throw new Error(`${where}: colour ${n.name} outside a style`);
      }
      if (n.t === "if")
        for (const b of n.branches) {
          if (!b.cond) continue;
          const v = check(b.cond.name);
          if (b.cond.eq !== undefined && (v.kind !== "enum" || !v.values.includes(b.cond.eq)))
            throw new Error(`${where}: ${b.cond.name} can't be "${b.cond.eq}"`);
          if (b.cond.eq === undefined && v.kind === "enum") throw new Error(`${where}: test ${b.cond.name} against a value`);
        }
    });
  }
  // The shared vars (app name, accent...) are passed to every email of a family, used or not.
  const shared = new Set(Object.keys(c.spec.family === "app" ? appVars : boxVars));
  for (const k of Object.keys(c.spec.vars)) if (!used.has(k) && !shared.has(k)) throw new Error(`${c.spec.id}: variable ${k} is never used`);
}

// ---- 3. Go --------------------------------------------------------------

const goName = (n: string) => n.replace(/^ip$/, "IP").replace(/^url$/, "URL").replace(/Url$/, "URL").replace(/^\w/, (c) => c.toUpperCase());
const pascal = (id: string) => id.replace(/(^|-)(\w)/g, (_, __, c: string) => c.toUpperCase());
const camel = (id: string) => id.replace(/-(\w)/g, (_, c: string) => c.toUpperCase());

function goTemplate(nodes: Node[], html: boolean): string {
  const out: string[] = [];
  const cond = (c: NonNullable<Cond>) => (c.eq !== undefined ? `eq .${goName(c.name)} ${JSON.stringify(c.eq)}` : `.${goName(c.name)}`);
  for (const n of nodes) {
    if (n.t === "s") {
      if (n.s.includes("{{")) throw new Error("static text contains {{");
      // html/template drops comments, so Outlook's conditional comments go in as trusted constants.
      out.push(html ? n.s.replace(/<!--[\s\S]*?-->/g, (c) => `{{comment \`${c}\`}}`) : n.s);
    } else if (n.t === "v") out.push(`{{.${goName(n.name)}}}`);
    else
      n.branches.forEach((b, i) => {
        out.push(i === 0 ? `{{if ${cond(b.cond!)}}}` : b.cond ? `{{else if ${cond(b.cond)}}}` : "{{else}}");
        out.push(goTemplate(b.body, html));
        if (i === n.branches.length - 1) out.push("{{end}}");
      });
  }
  return out.join("");
}

const goGen = (all: Compiled[]) => {
  const box = all.filter((c) => c.spec.family === "box");
  const lines = [
    "// Code generated by packages/emails (bun run build). DO NOT EDIT.",
    "// The templates are React Email components in packages/emails/src/emails.",
    "",
    "package templates",
    "",
    'import "fmt"',
    "",
  ];
  for (const c of box) {
    const T = pascal(c.spec.id);
    lines.push(`// ${T}Data fills ${c.spec.id}: ${c.spec.title}.`, `type ${T}Data struct {`);
    for (const [k, v] of Object.entries(c.spec.vars)) {
      const doc = v.kind === "enum" ? `${v.doc}: ${v.values.join(", ")}` : v.doc;
      lines.push(`\t${goName(k)} ${v.kind === "flag" ? "bool" : "string"} // ${doc}`);
    }
    lines.push("}", "", `// ${T} renders ${c.spec.id}: ${c.spec.title}.`, `func ${T}(d ${T}Data) (*Email, error) {`);
    for (const [k, v] of Object.entries(c.spec.vars)) {
      if (v.kind !== "enum") continue;
      lines.push(
        `\tswitch d.${goName(k)} {`,
        `\tcase ${v.values.map((x) => JSON.stringify(x)).join(", ")}:`,
        "\tdefault:",
        `\t\treturn nil, fmt.Errorf("${c.spec.id}: ${goName(k)} is %q, want one of ${v.values.join(", ")}", d.${goName(k)})`,
        "\t}",
      );
    }
    lines.push(`\treturn render(${JSON.stringify(c.spec.id)}, d)`, "}", "");
  }
  lines.push("// Names lists every box email.", `var Names = []string{${box.map((c) => JSON.stringify(c.spec.id)).join(", ")}}`, "");
  return lines.join("\n");
};

/** samples_gen_test.go: every box email filled with its PreviewProps, for the Go tests. */
const goSamples = (all: Compiled[]) => {
  const lines = ["// Code generated by packages/emails (bun run build). DO NOT EDIT.", "", "package templates", "", "var samples = map[string]func() (*Email, error){"];
  for (const c of all.filter((c) => c.spec.family === "box")) {
    const T = pascal(c.spec.id);
    lines.push(`\t${JSON.stringify(c.spec.id)}: func() (*Email, error) {`, `\t\treturn ${T}(${T}Data{`);
    for (const [k, v] of Object.entries(c.spec.vars)) {
      const val = (c.spec.preview as Record<string, unknown>)[k];
      lines.push(`\t\t\t${goName(k)}: ${v.kind === "flag" ? String(!!val) : JSON.stringify(String(val ?? ""))},`);
    }
    lines.push("\t\t})", "\t},");
  }
  lines.push("}", "");
  return lines.join("\n");
};

// ---- 4. TypeScript -------------------------------------------------------

/** Long static chunks shared by several emails (the head, the preheader padding, the footer) are written once. */
const shared = new Map<string, string>();
function chunk(s: string): string {
  const name = shared.get(s);
  return name ?? JSON.stringify(s);
}
function countChunks(nodes: Node[], seen: Map<string, number>) {
  walk(nodes, (n) => {
    if (n.t === "s" && n.s.length >= 200) seen.set(n.s, (seen.get(n.s) ?? 0) + 1);
  });
}

function tsExpr(nodes: Node[], part: "html" | "text" | "subject"): string {
  const bits: string[] = [];
  for (const n of nodes) {
    if (n.t === "s") bits.push(chunk(n.s));
    else if (n.t === "v") {
      if (part !== "html") bits.push(`s(v.${n.name})`);
      else bits.push(n.ctx === "url" ? `u(v.${n.name})` : n.ctx === "style" ? `c(v.${n.name})` : `e(v.${n.name})`);
    } else {
      let expr = "";
      for (let i = n.branches.length - 1; i >= 0; i--) {
        const b = n.branches[i]!;
        const body = `(${tsExpr(b.body, part) || '""'})`;
        if (!b.cond) expr = body;
        else {
          const test = b.cond.eq !== undefined ? `v.${b.cond.name} === ${JSON.stringify(b.cond.eq)}` : `!!v.${b.cond.name}`;
          expr = `(${test} ? ${body} : ${expr || '""'})`;
        }
      }
      bits.push(expr);
    }
  }
  return bits.join(" + ");
}

const TS_HEAD = `// Code generated by packages/emails (bun run build). DO NOT EDIT.
// The templates are React Email components in packages/emails/src/emails;
// this file is what they compile to: plain functions, no imports, no React.
// Every value is escaped for where it lands: HTML text and attributes are
// entity-escaped, links must be http(s) or mailto, colours must be #rrggbb.

export type Email = { subject: string; html: string; text: string };

const ENT: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
/** Escapes text for HTML. */
const e = (x: unknown): string => String(x ?? "").replace(/[&<>"']/g, (ch) => ENT[ch]!);
/** A link: http(s) or mailto only, then escaped. */
const u = (x: unknown): string => {
  const t = String(x ?? "").trim();
  if (!/^(https?:\\/\\/|mailto:)/i.test(t)) throw new Error(\`email template: refusing link \${JSON.stringify(t.slice(0, 40))}\`);
  return e(t);
};
/** A colour: #rrggbb only. */
const c = (x: unknown): string => {
  const t = String(x ?? "").trim();
  if (!/^#[0-9a-f]{6}$/i.test(t)) throw new Error(\`email template: refusing colour \${JSON.stringify(t.slice(0, 20))}\`);
  return t;
};
/** Plain text: no escaping, but no stray carriage returns. */
const s = (x: unknown): string => String(x ?? "").replace(/\\r\\n?/g, "\\n");
/** One line, for the subject. */
const line = (x: string): string => x.replace(/\\s+/g, " ").trim();
/** The plain-text part: no runs of blank lines where a branch wasn't taken. */
const txt = (x: string): string => x.replace(/[ \\t]+\\n/g, "\\n").replace(/\\n{3,}/g, "\\n\\n").trim() + "\\n";
`;

function tsGen(all: Compiled[], parsed: Map<string, Record<string, Node[]>>): string {
  const app = all.filter((c) => c.spec.family === "app");
  const out = [TS_HEAD];
  const seen = new Map<string, number>();
  for (const c of app) countChunks(parsed.get(c.spec.id)!.html!, seen);
  shared.clear();
  for (const [s, n] of seen) if (n > 1) shared.set(s, `H${shared.size}`);
  if (shared.size) {
    out.push("", "// Markup several emails share.");
    for (const [s, name] of shared) out.push(`const ${name} = ${JSON.stringify(s)};`);
  }
  const field = (k: string, v: Var) => {
    const type = v.kind === "flag" ? "boolean" : v.kind === "enum" ? v.values.map((x) => JSON.stringify(x)).join(" | ") : "string";
    return [`  /** ${v.doc} */`, `  ${k}: ${type};`];
  };
  out.push(
    "",
    "/** The button when the app sets no accent of its own: the dashboard's brass (--brass in tokens.css). */",
    `export const BRASS = ${JSON.stringify(brass.light.accent)};`,
    "/** The dashboard's text on brass (--on-brass): the dark choice for text on any accent. */",
    `export const ON_BRASS = ${JSON.stringify(brass.light.onAccent)};`,
  );
  out.push("", "/** What every app email carries: the app's own brand, and who it went to. */", "export type AppBrand = {");
  for (const [k, v] of Object.entries(appVars)) out.push(...field(k, v));
  out.push("};");
  for (const c of app) {
    const T = pascal(c.spec.id);
    const fn = camel(c.spec.id);
    out.push("", `/** ${c.spec.title}. */`, `export type ${T}Vars = AppBrand & {`);
    for (const [k, v] of Object.entries(c.spec.vars)) if (!(k in appVars)) out.push(...field(k, v));
    out.push("};", "", `/** ${c.spec.title}. */`, `export function ${fn}(v: ${T}Vars): Email {`);
    for (const [k, v] of Object.entries(c.spec.vars))
      if (v.kind === "enum")
        out.push(`  if (!${JSON.stringify(v.values)}.includes(v.${k})) throw new Error(\`${c.spec.id}: ${k} is \${JSON.stringify(v.${k})}\`);`);
    const p = parsed.get(c.spec.id)!;
    out.push(
      `  return {`,
      `    subject: line(${tsExpr(p.subject!, "subject")}),`,
      `    html: ${tsExpr(p.html!, "html")},`,
      `    text: txt(${tsExpr(p.text!, "text")}),`,
      `  };`,
      "}",
    );
  }
  out.push("", `/** Every app email, by id. */`, `export const appEmails = { ${app.map((c) => `${JSON.stringify(c.spec.id)}: ${camel(c.spec.id)}`).join(", ")} } as const;`, "");
  return out.join("\n");
}

function gofmt(src: string): string {
  const p = Bun.spawnSync(["gofmt"], { stdin: Buffer.from(src) });
  if (p.exitCode !== 0) throw new Error("gofmt: " + p.stderr.toString());
  return p.stdout.toString();
}

// ---- 5. write -----------------------------------------------------------

export async function build() {
  const all = await compileAll();
  const parsed = new Map<string, Record<string, Node[]>>();
  const files = new Map<string, string>();
  for (const c of all) {
    const parts = { html: parse(c.html, `${c.spec.id}.html`, true), text: parse(c.text, `${c.spec.id}.text`, false), subject: parse(c.subject, `${c.spec.id}.subject`, false) };
    validate(c, parts);
    parsed.set(c.spec.id, parts);
    if (c.spec.family === "box") {
      const head = `{{/* Code generated by packages/emails from src/emails/${c.spec.id}.tsx. DO NOT EDIT. */}}`;
      files.set(join(goDir, "box", `${c.spec.id}.html.tmpl`), `${head}{{define ${JSON.stringify(c.spec.id + ".html")}}}${goTemplate(parts.html, true)}{{end}}\n`);
      files.set(
        join(goDir, "box", `${c.spec.id}.txt.tmpl`),
        `${head}{{define ${JSON.stringify(c.spec.id + ".subject")}}}${goTemplate(parts.subject, false)}{{end}}` +
          `{{define ${JSON.stringify(c.spec.id + ".text")}}}${goTemplate(parts.text, false)}{{end}}\n`,
      );
    }
  }
  files.set(join(goDir, "gen.go"), gofmt(goGen(all)));
  files.set(join(goDir, "samples_gen_test.go"), gofmt(goSamples(all)));
  files.set(tsOut, tsGen(all, parsed));

  const stale: string[] = [];
  for (const [path, body] of files) {
    const now = existsSync(path) ? readFileSync(path, "utf8") : null;
    if (now === body) continue;
    if (check) stale.push(path);
    else {
      mkdirSync(join(path, ".."), { recursive: true });
      writeFileSync(path, body);
    }
  }
  if (check && stale.length) {
    console.error(`stale (run bun run build in packages/emails):\n  ${stale.join("\n  ")}`);
    process.exit(1);
  }
  for (const c of all) console.log(`${c.spec.family.padEnd(3)} ${c.spec.id.padEnd(16)} html ${(Buffer.byteLength(c.html) / 1024).toFixed(1)} KB  text ${Buffer.byteLength(c.text)} B`);
  return { all, parsed };
}

if (import.meta.main) await build();
