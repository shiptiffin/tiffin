import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Clock, Database, FolderPlus, GitBranch, Inbox, KeyRound, LayoutTemplate } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import type { Manifest } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { cronWords } from "@/lib/format";
import { serviceNames, stage, type StagedEdit } from "@/lib/staged";
import { checkGitUrl, frameworkName, rememberNextDeploy, slugify, starterLine, starterOrder, startersQuery, starterThumb, type Starter } from "@/lib/starters";
import { toast } from "./toast";

type Kind = "app" | "bucket" | "queue" | "cron" | "env";
const ALL_SERVICES = ["postgres", "valkey", "storage", "email", "auth", "analytics"] as const;

/**
 * "Add" on a project: every new part stages a change (nothing applies from
 * here); the staged bar and the plan tray take it from there. Services stage
 * at once; the rest ask for a name and the one or two things they need.
 */
export function AddMenu({ project, manifest, routes, className }: { project: string; manifest?: Manifest; routes: string[]; className?: string }) {
  const [open, setOpen] = useState<Kind | null>(null);
  const services = (manifest?.services ?? {}) as Record<string, unknown>;
  const off = ALL_SERVICES.filter((s) => !(s in services));
  const staged = (what: string) => toast({ title: <>Staged: {what}.</>, detail: "Review it with anything else you stage, then apply." });
  return (
    <>
      <Menu>
        <MenuTrigger asChild>
          <Button variant="secondary" size="lg" className={className} disabled={!manifest}>
            Add <ChevronDown className="-mr-1 text-ink-3" />
          </Button>
        </MenuTrigger>
        <MenuContent align="end" className="min-w-60">
          <MenuItem onSelect={() => setOpen("app")}>
            <LayoutTemplate /> App…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("bucket")}>
            <FolderPlus /> Bucket…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("queue")}>
            <Inbox /> Queue…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("cron")}>
            <Clock /> Schedule…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("env")}>
            <KeyRound /> Environment variable…
          </MenuItem>
          {off.length > 0 && (
            <>
              <MenuSeparator />
              <MenuLabel>Services</MenuLabel>
              {off.map((s) => (
                <MenuItem
                  key={s}
                  onSelect={() => {
                    stage(project, { kind: "service", service: s, from: "off", to: "on" });
                    staged(`add ${serviceNames[s] ?? s} to ${project}`);
                  }}
                >
                  <Database /> {serviceNames[s] ?? s}
                </MenuItem>
              ))}
            </>
          )}
        </MenuContent>
      </Menu>
      <Dialog open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        <DialogContent className="max-w-lg">
          {open === "app" && manifest && <AddApp project={project} manifest={manifest} routes={routes} done={() => setOpen(null)} />}
          {open === "bucket" && manifest && <AddBucket project={project} manifest={manifest} done={() => setOpen(null)} />}
          {open === "queue" && manifest && <AddQueue project={project} manifest={manifest} done={() => setOpen(null)} />}
          {open === "cron" && manifest && <AddCron project={project} manifest={manifest} done={() => setOpen(null)} />}
          {open === "env" && manifest && <AddEnv project={project} manifest={manifest} done={() => setOpen(null)} />}
        </DialogContent>
      </Dialog>
    </>
  );
}

const field =
  "h-9 w-full rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.84375rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger";

function Field({ label, note, error, group, children }: { label: string; note?: ReactNode; error?: string | false; group?: boolean; children: ReactNode }) {
  const Tag = group ? "div" : "label";
  return (
    <Tag className="block" role={group ? "group" : undefined} aria-label={group ? label : undefined}>
      <span className="mb-1 block text-xs font-[550] text-ink-2">{label}</span>
      {children}
      {error ? <span className="mt-1 block text-xs text-danger">{error}</span> : note ? <span className="mt-1 block text-xs text-ink-3">{note}</span> : null}
    </Tag>
  );
}

function Shell({
  title,
  lede,
  children,
  submit,
  ok,
  done,
  onSubmit,
}: {
  title: string;
  lede: ReactNode;
  children: ReactNode;
  submit: string;
  ok: boolean;
  done: () => void;
  onSubmit: () => void;
}) {
  return (
    <form
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        if (!ok) return;
        onSubmit();
        done();
      }}
      className="flex min-h-0 flex-col"
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{lede}</DialogDescription>
      </DialogHeader>
      <DialogBody className="grid gap-4">{children}</DialogBody>
      <DialogFooter>
        <span className="mr-auto text-xs text-ink-3 max-sm:hidden">Stages a change. Nothing happens until you apply it.</span>
        <Button type="button" variant="ghost" onClick={done}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ok}>
          {submit}
        </Button>
      </DialogFooter>
    </form>
  );
}

const slugOk = (s: string) => /^[a-z][a-z0-9-]{0,39}$/.test(s) && !s.endsWith("-");
const say = (what: string) => toast({ title: <>Staged: {what}.</>, detail: "Review it with anything else you stage, then apply." });
const set = (project: string, e: Omit<Extract<StagedEdit, { kind: "set" }>, "kind">) => stage(project, { kind: "set", ...e });

// ───────────────────────── app ─────────────────────────

function AddApp({ project, manifest, routes, done }: { project: string; manifest: Manifest; routes: string[]; done: () => void }) {
  const starters = useQuery(startersQuery);
  const list = [...(starters.data ?? [])].sort((a, b) => starterOrder.indexOf(a.id) - starterOrder.indexOf(b.id));
  const [pick, setPick] = useState<string>("hono-postgres");
  const [git, setGit] = useState({ url: "", ref: "", path: "", framework: "next" });
  const starter: Starter | undefined = list.find((s) => s.id === pick);
  const apps = Object.keys(manifest.apps ?? {});
  const base = pick === "git" ? "web" : (starter?.app ?? "web");
  const free = (n: string) => !apps.includes(n);
  const suggestion = free(base) ? base : [2, 3, 4, 5].map((i) => `${base}-${i}`).find(free) ?? "";
  const [typed, setTyped] = useState<string | null>(null);
  const name = typed ?? suggestion;
  const nameErr = !name ? "Give it a name." : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : !free(name) ? `${project} already has an app called ${name}.` : false;
  const gitErr = pick === "git" ? (checkGitUrl(git.url).ok ? false : ((checkGitUrl(git.url) as { why: string }).why ?? false)) : false;
  // An app answers at its name unless another project already does; then at project-name.
  const route = routes.includes(name) ? `${project}-${name}` : undefined;
  const framework = pick === "git" ? git.framework : (starter?.framework ?? "bun");
  const needs = pick === "git" ? [] : (starter?.services ?? []).filter((s) => !((manifest.services ?? {}) as Record<string, unknown>)[s]);
  return (
    <Shell
      title={`Add an app to ${project}`}
      lede="Built on the box from a starter or a public repository. Once the change is applied, its page offers the first deploy."
      submit={`Stage ${name || "the app"}`}
      ok={!nameErr && !gitErr && (pick === "git" || !!starter)}
      done={done}
      onSubmit={() => {
        const spec: Record<string, unknown> =
          pick === "git" ? { framework } : { ...(structuredClone(Object.values(starter!.fragment.apps)[0] ?? {}) as Record<string, unknown>), framework };
        if (route) spec.routes = [route];
        set(project, { path: ["apps", name], to: spec, what: `Add the ${name} app (${frameworkName(framework)}) to ${project}`, undo: `${name} is removed again` });
        for (const s of needs) stage(project, { kind: "service", service: s, from: "off", to: "on" });
        if (pick === "git") rememberNextDeploy(project, name, { git: { url: git.url.trim(), ref: git.ref, path: git.path } });
        else rememberNextDeploy(project, name, { template: starter!.id });
        say(`add ${name} to ${project}${needs.length ? ` with ${needs.map((s) => serviceNames[s] ?? s).join(", ")}` : ""}`);
      }}
    >
      <div role="radiogroup" aria-label="Build it from" className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {list.map((s) => (
          <PickTile key={s.id} picked={pick === s.id} onPick={() => setPick(s.id)} thumb={starterThumb[s.id]} title={s.name} line={starterLine[s.id]} />
        ))}
        <PickTile picked={pick === "git"} onPick={() => setPick("git")} icon={<GitBranch />} title="Git URL" line="A public https repository." />
      </div>
      {pick === "git" && (
        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_8rem]">
          <Field label="Repository" error={!!git.url && gitErr}>
            <input value={git.url} onChange={(e) => setGit({ ...git, url: e.target.value })} placeholder="https://github.com/owner/repo" spellCheck={false} className={cn(field, "ident")} />
          </Field>
          <Field label="Framework">
            <select value={git.framework} onChange={(e) => setGit({ ...git, framework: e.target.value })} className={field}>
              {["next", "hono", "bun", "static"].map((f) => (
                <option key={f} value={f}>
                  {frameworkName(f)}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Branch, tag or commit">
            <input value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value })} placeholder="default branch" spellCheck={false} className={cn(field, "ident")} />
          </Field>
          <Field label="Folder">
            <input value={git.path} onChange={(e) => setGit({ ...git, path: e.target.value })} placeholder="the top" spellCheck={false} className={cn(field, "ident")} />
          </Field>
        </div>
      )}
      <Field
        label="App name"
        error={nameErr}
        note={
          <>
            Answers at <span className="ident text-ink-2">{route ?? name}.…</span>
            {needs.length > 0 && <> · also adds {needs.map((s) => serviceNames[s] ?? s).join(", ")}</>}
          </>
        }
      >
        <input value={name} onChange={(e) => setTyped(slugify(e.target.value))} spellCheck={false} autoComplete="off" aria-invalid={!!nameErr} className={cn(field, "ident")} />
      </Field>
    </Shell>
  );
}

function PickTile({ picked, onPick, thumb, icon, title, line }: { picked: boolean; onPick: () => void; thumb?: string; icon?: ReactNode; title: string; line?: string }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={picked}
      onClick={onPick}
      className={cn(
        "flex flex-col overflow-hidden rounded-[9px] border bg-paper text-left transition-[border-color] duration-[var(--dur-state)]",
        picked ? "border-brass shadow-[0_0_0_1px_var(--brass)]" : "border-rule-2 hover:border-rule-3",
      )}
    >
      <span className="grid aspect-[16/9] place-items-center bg-paper-sunk text-ink-3 [&_svg]:size-5">{thumb ? <img src={thumb} alt="" className="size-full object-contain p-1" /> : icon}</span>
      <span className="border-t border-rule px-2.5 py-2">
        <span className="block text-[0.8125rem] font-[550] text-ink">{title}</span>
        {line && <span className="line-clamp-2 block text-[0.71875rem] leading-4 text-ink-3">{line}</span>}
      </span>
    </button>
  );
}

// ───────────────────────── bucket, queue, schedule, env ─────────────────────────

function AddBucket({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [name, setName] = useState("");
  const [pub, setPub] = useState(false);
  const existing = Object.keys(manifest.services?.storage?.buckets ?? {});
  const err = !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : existing.includes(name) ? `${project} already has a bucket called ${name}.` : false;
  return (
    <Shell
      title={`Add a bucket to ${project}`}
      lede="S3-compatible storage for files your apps write. Private buckets serve files only through signed links."
      submit={name ? `Stage ${name}` : "Stage the bucket"}
      ok={!!name && !err}
      done={done}
      onSubmit={() => {
        set(project, { path: ["services", "storage", "buckets", name], to: { public: pub }, what: `Add the ${name} bucket (${pub ? "public" : "private"}) to ${project}`, undo: `${name} goes to the trash` });
        say(`add the ${name} bucket`);
      }}
    >
      <Field label="Name" error={err} note={!manifest.services?.storage ? "Adds Storage to the project too." : undefined}>
        <input autoFocus value={name} onChange={(e) => setName(slugify(e.target.value))} placeholder="uploads" spellCheck={false} className={cn(field, "ident")} />
      </Field>
      <label className="flex items-start gap-2.5 text-sm text-ink-2">
        <input type="checkbox" checked={pub} onChange={(e) => setPub(e.target.checked)} className="mt-0.5 size-4 accent-[var(--brass)]" />
        <span>
          <b className="font-[550] text-ink">Public.</b> Anyone with a file’s address can read it. Use it for images and downloads, never for people’s uploads.
        </span>
      </label>
    </Shell>
  );
}

function AppSelect({ manifest, value, onChange }: { manifest: Manifest; value: string; onChange: (v: string) => void }) {
  const apps = Object.keys(manifest.apps ?? {});
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} className={field}>
      {apps.map((a) => (
        <option key={a} value={a}>
          {a}
        </option>
      ))}
    </select>
  );
}

const workerFirst = (m: Manifest) => Object.entries(m.apps ?? {}).find(([, a]) => a.role === "worker")?.[0] ?? Object.keys(m.apps ?? {})[0] ?? "";

function AddQueue({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [name, setName] = useState("");
  const [app, setApp] = useState(workerFirst(manifest));
  const [path, setPath] = useState("");
  const existing = Object.keys(manifest.queues ?? {});
  const err = !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : existing.includes(name) ? `There’s already a queue called ${name}.` : false;
  const p = path || (name ? `/jobs/${name}` : "");
  const noApps = Object.keys(manifest.apps ?? {}).length === 0;
  return (
    <Shell
      title={`Add a queue to ${project}`}
      lede="Jobs you send are delivered to an app as HTTP requests, retried with backoff, and kept when they keep failing."
      submit={name ? `Stage ${name}` : "Stage the queue"}
      ok={!!name && !err && !!app && p.startsWith("/")}
      done={done}
      onSubmit={() => {
        set(project, { path: ["queues", name], to: { app, path: p }, what: `Add the ${name} queue, delivered to ${app} at ${p}`, undo: `the ${name} queue is removed` });
        say(`add the ${name} queue`);
      }}
    >
      {noApps && <p className="text-sm text-warn-ink">A queue delivers to an app. Add an app first.</p>}
      <Field label="Name" error={err}>
        <input autoFocus value={name} onChange={(e) => setName(slugify(e.target.value))} placeholder="emails" spellCheck={false} className={cn(field, "ident")} />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Delivered to">
          <AppSelect manifest={manifest} value={app} onChange={setApp} />
        </Field>
        <Field label="At the path">
          <input value={p} onChange={(e) => setPath(e.target.value)} spellCheck={false} className={cn(field, "ident")} />
        </Field>
      </div>
    </Shell>
  );
}

const schedules = [
  { v: "*/10 * * * *", l: "Every 10 minutes" },
  { v: "0 * * * *", l: "Every hour" },
  { v: "0 2 * * *", l: "Every day at 02:00 UTC" },
  { v: "0 9 * * 1", l: "Mondays at 09:00 UTC" },
];

/** "every hour", "on Mondays at 09:00 UTC", or the expression itself. */
function whenWords(expr: string) {
  const preset = schedules.find((s) => s.v === expr);
  if (preset) return preset.l.charAt(0).toLowerCase() + preset.l.slice(1).replace(/^mondays/, "on Mondays");
  const w = cronWords(expr);
  return w === expr ? `on the schedule “${expr}”` : w;
}

function AddCron({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [name, setName] = useState("");
  const [app, setApp] = useState(workerFirst(manifest));
  const [schedule, setSchedule] = useState("0 2 * * *");
  const [path, setPath] = useState("");
  const existing = Object.keys(manifest.crons ?? {});
  const err = !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : existing.includes(name) ? `There’s already a schedule called ${name}.` : false;
  const fields = schedule.trim().split(/\s+/).length === 5;
  const p = path || (name ? `/cron/${name}` : "");
  return (
    <Shell
      title={`Add a schedule to ${project}`}
      lede="The box calls an app’s path on a schedule, like cron, and keeps every run in the queue’s history."
      submit={name ? `Stage ${name}` : "Stage the schedule"}
      ok={!!name && !err && !!app && fields && p.startsWith("/")}
      done={done}
      onSubmit={() => {
        const when = whenWords(schedule.trim());
        set(project, { path: ["crons", name], to: { schedule: schedule.trim(), app, path: p }, what: `Call ${app} at ${p} ${when}`, undo: `the ${name} schedule is removed` });
        say(`call ${app} at ${p} ${when}`);
      }}
    >
      <Field label="Name" error={err}>
        <input autoFocus value={name} onChange={(e) => setName(slugify(e.target.value))} placeholder="nightly" spellCheck={false} className={cn(field, "ident")} />
      </Field>
      <Field group label="When" note={fields ? whenWords(schedule.trim()) : undefined} error={!fields && "Five fields: minute hour day month weekday."}>
        <div className="flex flex-wrap gap-1.5">
          {schedules.map((s) => (
            <button
              key={s.v}
              type="button"
              onClick={() => setSchedule(s.v)}
              className={cn("h-7 rounded-full border px-2.5 text-xs", schedule === s.v ? "border-brass bg-brass-wash text-ink" : "border-rule-2 text-ink-2 hover:border-rule-3")}
            >
              {s.l}
            </button>
          ))}
        </div>
        <input value={schedule} onChange={(e) => setSchedule(e.target.value)} spellCheck={false} className={cn(field, "ident mt-2")} aria-label="Cron expression" />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Calls">
          <AppSelect manifest={manifest} value={app} onChange={setApp} />
        </Field>
        <Field label="At the path">
          <input value={p} onChange={(e) => setPath(e.target.value)} spellCheck={false} className={cn(field, "ident")} />
        </Field>
      </div>
    </Shell>
  );
}

function AddEnv({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const existing = manifest.env ?? {};
  const valid = /^[A-Z_][A-Z0-9_]{0,127}$/.test(key);
  const replacing = key in existing;
  return (
    <Shell
      title={`Add an environment variable to ${project}`}
      lede={
        <>
          Plain settings every app in {project} reads, kept in <span className="ident">tiffin.config.ts</span>. Keys and passwords go in Secrets instead.
        </>
      }
      submit={replacing ? `Stage the new ${key}` : key ? `Stage ${key}` : "Stage the variable"}
      ok={valid}
      done={done}
      onSubmit={() => {
        set(project, {
          path: ["env", key],
          from: existing[key],
          to: value,
          what: replacing ? `Set ${key} to “${value}” in ${project}` : `Add ${key}=“${value}” to ${project}`,
          undo: replacing ? `${key} goes back to “${existing[key]}”` : `${key} is removed`,
        });
        say(`${replacing ? "change" : "add"} ${key}`);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-[minmax(0,13rem)_minmax(0,1fr)]">
        <Field label="Name" error={!!key && !valid && "Capitals, digits and underscores, like LOG_LEVEL."}>
          <input
            autoFocus
            value={key}
            onChange={(e) => setKey(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
            placeholder="LOG_LEVEL"
            spellCheck={false}
            className={cn(field, "ident")}
          />
        </Field>
        <Field label="Value" note={replacing ? `Now “${existing[key]}”.` : undefined}>
          <input value={value} onChange={(e) => setValue(e.target.value)} placeholder="info" spellCheck={false} className={cn(field, "ident")} />
        </Field>
      </div>
    </Shell>
  );
}
