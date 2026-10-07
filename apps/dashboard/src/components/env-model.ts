import type { Change, Manifest, SecretInfo } from "@/api/client";
import { change, type StagedEdit } from "@/lib/staged";

/**
 * The project's environment variables as one list, the way a person thinks
 * of them: a name, a value (or a secret's dots), the apps it reaches.
 *
 * Plain values live in the manifest: `env` reaches every app, `apps.<app>.env`
 * one app. Secrets are stored apart, encrypted, and reach every app. When a
 * name is set in more than one place, an app's own value beats the
 * all-apps one, and a secret beats both (platform.ProjectEnv).
 */

export type SetEdit = Extract<StagedEdit, { kind: "set" }>;

/** Where a plain value is written: "all" is the manifest's top-level env. */
export type Scope = "all" | string[];

export type EnvRow = {
  id: string;
  key: string;
  secret: boolean;
  /** Plain values only; a secret's value never comes back from the box. */
  value?: string;
  scope: Scope;
  /** Manifest paths that hold this row (plain values). */
  paths: string[][];
  /** When it was last set and by whom, if the box says (secrets; plain values from History). */
  at?: string;
  by?: { kind: string; id: string; name?: string };
  /** A change on its way: the new value, or undefined while it's being removed. */
  staged?: { removing: boolean; to?: string };
  /** Being added right now (not in the manifest yet). */
  adding?: boolean;
  /** Something else wins over this one for some apps. */
  shadowedBy?: string;
};

/** Environment variable names the box accepts: capitals, digits and underscores, not starting with a digit. */
export const ENV_KEY = /^[A-Z_][A-Z0-9_]{0,127}$/;

/** Frameworks write these into browser code at build time (manifest.BuildInlined). */
const BUILD_INLINED = ["NEXT_PUBLIC_", "VITE_", "PUBLIC_"];
export const builtIn = (k: string) => BUILD_INLINED.some((p) => k.startsWith(p));

/** Names the box sets for apps itself; prefixes end in "_" (manifest.boxEnv). */
const BOX_ENV = [
  "PORT",
  "DATABASE_URL",
  "DIRECT_DATABASE_URL",
  "DATABASE_POOL_MAX",
  "REDIS_URL",
  "VALKEY_PREFIX",
  "SMTP_URL",
  "EMAIL_FROM",
  "SENTRY_DSN",
  "S3_",
  "AWS_",
  "TIFFIN_",
  "OTEL_",
  "UPSTASH_REDIS_REST_",
  "KV_REST_API_",
  "NEXT_PUBLIC_SENTRY_DSN",
  "NEXT_PUBLIC_TIFFIN_",
];
export const setByBox = (k: string) => BOX_ENV.some((b) => k === b || (b.endsWith("_") && k.startsWith(b)));

/** Bits of entropy per character: random keys score high, words and URLs low. */
function entropy(v: string): number {
  const n = new Map<string, number>();
  for (const c of v) n.set(c, (n.get(c) ?? 0) + 1);
  let h = 0;
  for (const c of n.values()) h -= (c / v.length) * Math.log2(c / v.length);
  return h;
}

/**
 * Whether a variable looks like a key or a password, so Add and the paste
 * import start with Secret on: by its name (…_KEY, …_SECRET, …TOKEN…,
 * …PASSWORD…, …_DSN, PRIVATE) or its value (a URL with a password in it,
 * sk-…, ghp_…, a long random string). Never for browser variables
 * (NEXT_PUBLIC_*…): those ship to browsers, so they can't be secret.
 */
export function looksSecret(k: string, v = ""): boolean {
  if (builtIn(k)) return false;
  if (/(SECRET|TOKEN|PASSWORD|PASSWD|PRIVATE|CREDENTIAL)/.test(k) || /(^|_)(KEY|DSN|PASS|PWD)$/.test(k) || /_KEY_/.test(k)) return true;
  const val = v.trim();
  if (/^[a-z][a-z0-9+.-]*:\/\/[^/\s:@]+:[^/\s@]+@/i.test(val)) return true;
  if (/^(sk|rk)[-_]|^(ghp|gho|ghs|ghu|github_pat|glpat|xox[abpr]|re)_|^AKIA[0-9A-Z]{12}|^SG\.|^-----BEGIN/.test(val)) return true;
  return val.length >= 24 && !/[\s/:]/.test(val) && entropy(val) > 3.6;
}

/** One variable read from .env text; `dup` when a later line sets the same name (the last one wins). */
export type Parsed = { k: string; v: string; valid: boolean; dup: boolean };

const findClose = (s: string, q: string) => {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && q === '"') i++;
    else if (s[i] === q) return i;
  }
  return -1;
};

/**
 * Reads .env text the way dotenv does: comments, blank lines, `export `,
 * single, double and backtick quotes, values that run over several lines
 * inside quotes, \n escapes in double quotes, and `# comments` after an
 * unquoted value. Names are upper-cased; names the box can't take stay in
 * the list, marked, so the person sees why they're skipped.
 */
export function parseDotenv(text: string): Parsed[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const found: Array<{ k: string; v: string }> = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim().replace(/^export\s+/, "");
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const k = line.slice(0, eq).trim().toUpperCase();
    const rest = line.slice(eq + 1).trimStart();
    const q = rest[0];
    let v: string;
    if (q === '"' || q === "'" || q === "`") {
      let body = rest.slice(1);
      let end = findClose(body, q);
      while (end < 0 && i + 1 < lines.length) {
        body += "\n" + lines[++i];
        end = findClose(body, q);
      }
      v = end < 0 ? body : body.slice(0, end);
      if (q === '"') v = v.replace(/\\([nrt"\\])/g, (_, c: string) => ({ n: "\n", r: "\r", t: "\t" })[c] ?? c);
    } else v = rest.replace(/\s+#.*$/, "").trim();
    found.push({ k, v });
  }
  return found.map((f, i) => ({ ...f, valid: ENV_KEY.test(f.k), dup: found.slice(i + 1).some((g) => g.k === f.k) }));
}

/** A value as one .env line: quoted when it has to be. */
export function dotenvLine(k: string, v: string): string {
  if (v === "" || /^[\w@%+=:,./-]*$/.test(v)) return `${k}=${v}`;
  return `${k}="${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n")}"`;
}

/** The variables as a .env file: plain values, secrets as empty lines with a note, one app's values under a heading. */
export function toDotenv(project: string, rows: EnvRow[]): string {
  const out = [`# ${project}: environment variables, copied from the dashboard`, ""];
  const whole = rows.filter((r) => r.scope === "all" && !r.adding);
  for (const r of whole) {
    if (r.secret) out.push(`# ${r.key} is a secret: its value is hidden and can't be copied.`, `${r.key}=`);
    else out.push(dotenvLine(r.key, r.value ?? ""));
  }
  const apps = [...new Set(rows.flatMap((r) => (r.scope === "all" ? [] : r.scope)))].sort();
  for (const a of apps) {
    out.push("", `# Only for ${a}`);
    for (const r of rows) if (r.scope !== "all" && r.scope.includes(a) && !r.adding) out.push(dotenvLine(r.key, r.value ?? ""));
  }
  return out.join("\n") + "\n";
}

export const sameScope = (a: Scope, b: Scope) => (a === "all" || b === "all" ? a === b : a.length === b.length && a.every((x) => b.includes(x)));

/** "Whole project", "web", "web and api". */
export function scopeWords(s: Scope): string {
  if (s === "all") return "Whole project";
  if (s.length <= 2) return s.join(" and ");
  return `${s.slice(0, -1).join(", ")} and ${s[s.length - 1]}`;
}

type Touch = { at: string; by: Change["actor"] };

/** When each plain value last changed, from the project's History: "env/KEY" and "app/<app>/KEY". */
export function lastTouched(changes: Change[] | undefined): Map<string, Touch> {
  const out = new Map<string, Touch>();
  const note = (k: string, c: Change) => {
    const prev = out.get(k);
    if (!prev || prev.at < c.at) out.set(k, { at: c.at, by: c.actor });
  };
  for (const c of changes ?? []) {
    for (const op of c.plan?.ops ?? []) {
      const [kind, name] = [op.address.split("/")[0], op.address.split("/").slice(1).join("/")];
      if (kind === "env") note(`env/${name}`, c);
      else if (kind === "app") {
        const before = ((op.before as { env?: Record<string, string> } | null)?.env ?? {}) as Record<string, string>;
        const after = ((op.after as { env?: Record<string, string> } | null)?.env ?? {}) as Record<string, string>;
        for (const k of new Set([...Object.keys(before), ...Object.keys(after)])) if (before[k] !== after[k]) note(`app/${name}/${k}`, c);
      } else if (kind === "project") {
        const after = ((op.after as { env?: Record<string, string> } | null)?.env ?? {}) as Record<string, string>;
        for (const k of Object.keys(after)) note(`env/${k}`, c);
      }
    }
  }
  return out;
}

const pathKey = (p: string[]) => p.join("/");

/** Every variable of the project as rows, sorted by name, with changes on their way folded in. */
export function envRows(man: Manifest | undefined, secrets: SecretInfo[] | undefined, pending: StagedEdit[], touched: Map<string, Touch>): EnvRow[] {
  const sets = pending.filter((e): e is SetEdit => e.kind === "set" && isEnvPath(e.path));
  const staged = new Map(sets.map((e) => [pathKey(e.path), e]));
  const rows: EnvRow[] = [];
  const top = (man?.env ?? {}) as Record<string, string>;
  const apps = (man?.apps ?? {}) as Record<string, { env?: Record<string, string> | null }>;

  for (const [k, v] of Object.entries(top)) {
    const path = ["env", k];
    const s = staged.get(pathKey(path));
    const t = touched.get(`env/${k}`);
    rows.push({ id: `all:${k}`, key: k, secret: false, value: v, scope: "all", paths: [path], at: t?.at, by: t?.by, staged: s ? { removing: s.to === undefined, to: s.to as string | undefined } : undefined });
  }
  // One app's values: the same name and value on several apps is one row.
  const byKV = new Map<string, EnvRow>();
  for (const [app, spec] of Object.entries(apps)) {
    for (const [k, v] of Object.entries(spec.env ?? {})) {
      const path = ["apps", app, "env", k];
      const s = staged.get(pathKey(path));
      const t = touched.get(`app/${app}/${k}`);
      const id = `${k}\u0000${v}\u0000${s ? String(s.to) : ""}`;
      const row = byKV.get(id);
      if (row) {
        (row.scope as string[]).push(app);
        row.paths.push(path);
        if (t && (!row.at || row.at < t.at)) {
          row.at = t.at;
          row.by = t.by;
        }
      } else {
        const r: EnvRow = { id: `app:${app}:${k}`, key: k, secret: false, value: v, scope: [app], paths: [path], at: t?.at, by: t?.by, staged: s ? { removing: s.to === undefined, to: s.to as string | undefined } : undefined };
        byKV.set(id, r);
        rows.push(r);
      }
    }
  }
  for (const s of secrets ?? []) rows.push({ id: `secret:${s.name}`, key: s.name, secret: true, scope: "all", paths: [], at: s.updatedAt, by: { kind: "token", id: s.updatedBy } });

  // Values being added right now: paths that aren't in the manifest yet.
  const known = new Set(rows.flatMap((r) => r.paths.map(pathKey)));
  const adds = new Map<string, EnvRow>();
  for (const e of sets) {
    if (known.has(pathKey(e.path)) || e.to === undefined) continue;
    const key = e.path[e.path.length - 1];
    const scope: Scope = e.path[0] === "env" ? "all" : [e.path[1]];
    const id = `adding:${key}:${String(e.to)}:${scope === "all" ? "all" : "apps"}`;
    const prev = adds.get(id);
    if (prev && prev.scope !== "all" && scope !== "all") {
      prev.scope.push(...scope);
      prev.paths.push(e.path);
    }
    else adds.set(id, { id, key, secret: false, value: String(e.to), scope, paths: [e.path], adding: true });
  }
  rows.push(...adds.values());

  // What wins: a secret over both, an app's own value over the all-apps one.
  const secretNames = new Set((secrets ?? []).map((s) => s.name));
  for (const r of rows) {
    if (r.secret) continue;
    if (secretNames.has(r.key)) r.shadowedBy = "A secret with this name replaces it.";
    else if (r.scope === "all") {
      const own = Object.entries(apps)
        .filter(([, a]) => a.env && r.key in a.env)
        .map(([n]) => n);
      if (own.length) r.shadowedBy = `${scopeWords(own)} ${own.length === 1 ? "has its" : "have their"} own value.`;
    }
  }
  return rows.sort((a, b) => a.key.localeCompare(b.key) || (a.secret === b.secret ? 0 : a.secret ? 1 : -1) || (a.scope === "all" ? -1 : 1));
}

export const isEnvPath = (p: string[]) => (p[0] === "env" && p.length === 2) || (p[0] === "apps" && p[2] === "env" && p.length === 4);

/** The manifest paths a plain value goes to for a scope. */
export const pathsFor = (key: string, scope: Scope): string[][] => (scope === "all" ? [["env", key]] : scope.map((a) => ["apps", a, "env", key]));

/** The value at a manifest path, if any. */
export function valueAt(man: Manifest | undefined, path: string[]): string | undefined {
  let at: unknown = man;
  for (const k of path) {
    if (!at || typeof at !== "object") return undefined;
    at = (at as Record<string, unknown>)[k];
  }
  return typeof at === "string" ? at : undefined;
}

/**
 * Writes a plain value: sets it at the scope's paths and removes it from the
 * paths it leaves (`from`). Each path is one edit; they go as one change.
 */
export function savePlain(project: string, man: Manifest | undefined, key: string, value: string, scope: Scope, from: string[][] = []) {
  const to = pathsFor(key, scope);
  const keep = new Set(to.map(pathKey));
  const where = scope === "all" ? `every app in ${project}` : scopeWords(scope);
  for (const p of to) {
    const was = valueAt(man, p);
    const app = p[0] === "apps" ? p[1] : undefined;
    change(
      project,
      {
        kind: "set",
        path: p,
        from: was,
        to: value,
        what: was === undefined ? `Add ${key} to ${app ?? where}` : `Set ${key} to “${value}” for ${app ?? where}`,
        undo: was === undefined ? `${key} is removed from ${app ?? where}` : `${key} goes back to “${was}”`,
      },
      { immediate: true },
    );
  }
  for (const p of from) if (!keep.has(pathKey(p))) removePath(project, man, key, p);
}

/** Removes a plain value from one manifest path. */
export function removePath(project: string, man: Manifest | undefined, key: string, p: string[]) {
  const was = valueAt(man, p);
  if (was === undefined) return;
  const where = p[0] === "apps" ? p[1] : project;
  change(project, { kind: "set", path: p, from: was, to: undefined, what: `Remove ${key} from ${where}`, undo: `${key} comes back as “${was}”` }, { immediate: true });
}

