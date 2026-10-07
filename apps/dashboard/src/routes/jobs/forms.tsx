// The Jobs forms, each the inside of a dialog: a schedule (presets, a time
// zone, the next runs as the box computes them, an app route or a URL), a
// queue's settings, a test job with a JSON payload, a workflow run. Schedules
// and queues live in tiffin.config.ts, so saving goes through change() (plan,
// apply, History, Undo); a test job and a run are plain API calls.
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Minus, Plus, Trash2 } from "lucide-react";
import { useId, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, type Manifest } from "@/api/client";
import { jobsApi, jq, type QueueStats } from "@/api/jobs";
import { mod2 } from "@/api/modules";
import { q as api } from "@/api/queries";
import { cronHuman } from "@/components/jobs-words";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Breaker } from "@/components/breaker";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem, Select } from "@/components/ui/choice";
import { DialogBody, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { useDebounced } from "@/lib/debounced";
import { change } from "@/lib/staged";
import { jsonError } from "./json";
import { shortURL } from "./shared";

type Cron = NonNullable<Manifest["crons"]>[string];
type Queue = NonNullable<Manifest["queues"]>[string];
type FormProps = { project: string; name?: string; done: () => void };

const field =
  "h-9 w-full min-w-0 rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.84375rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger disabled:opacity-60";

const slugOk = (s: string) => /^[a-z][a-z0-9-]{0,39}$/.test(s) && !s.endsWith("-");
const urlOk = (s: string) => {
  try {
    const u = new URL(s);
    return (u.protocol === "https:" || u.protocol === "http:") && !!u.hostname && !u.username && !u.password && !u.hash;
  } catch {
    return false;
  }
};

/** A labelled control with a note or an error under it. */
function Field({ label, note, error, children, htmlFor }: { label: string; note?: ReactNode; error?: string | false; children: ReactNode; htmlFor?: string }) {
  const id = useId();
  return (
    <div className="grid gap-1">
      <label htmlFor={htmlFor} className="text-xs font-[550] text-ink-2" id={id}>
        {label}
      </label>
      {children}
      {error ? (
        <span className="text-xs text-danger" role="alert">
          {error}
        </span>
      ) : note ? (
        <span className="text-xs text-ink-3">{note}</span>
      ) : null}
    </div>
  );
}

/** Two or more choices as one segmented control (arrow keys move between them). */
function Segmented<T extends string>({ label, value, onChange, items }: { label: string; value: T; onChange: (v: T) => void; items: Array<{ value: T; label: string; disabled?: boolean }> }) {
  return (
    <RadioGroup value={value} onValueChange={(v) => onChange(v as T)} aria-label={label} className="flex flex-wrap gap-1.5" orientation="horizontal">
      {items.map((it) => (
        <RadioItem
          key={it.value}
          value={it.value}
          disabled={it.disabled}
          className={cn(
            "h-8 rounded-full border px-3 text-[0.8125rem] transition-colors duration-[var(--dur-state)] outline-none focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] disabled:opacity-45",
            value === it.value ? "border-brass bg-brass-wash font-[550] text-ink" : "border-rule-2 text-ink-2 hover:border-rule-3 hover:text-ink",
          )}
        >
          {it.label}
        </RadioItem>
      ))}
    </RadioGroup>
  );
}

/** A number with − and + beside it; 0 can read as "no limit". */
function Stepper({ id, value, onChange, min, max, step = 1, zero, label }: { id?: string; value: number; onChange: (n: number) => void; min: number; max: number; step?: number; zero?: string; label: string }) {
  const clamp = (n: number) => Math.max(min, Math.min(max, Math.round(n)));
  return (
    <div className="flex items-center gap-1.5">
      <Button type="button" size="icon-sm" variant="secondary" aria-label={`Fewer: ${label}`} disabled={value <= min} onClick={() => onChange(clamp(value - step))}>
        <Minus />
      </Button>
      <input
        id={id}
        inputMode="numeric"
        value={value === 0 && zero ? "" : String(value)}
        placeholder={zero}
        onChange={(e) => {
          const n = Number(e.target.value.replace(/[^\d]/g, "") || 0);
          onChange(clamp(n));
        }}
        aria-label={label}
        className={cn(field, "w-24 text-center tnum")}
      />
      <Button type="button" size="icon-sm" variant="secondary" aria-label={`More: ${label}`} disabled={value >= max} onClick={() => onChange(clamp(value + step))}>
        <Plus />
      </Button>
    </div>
  );
}

function JsonBox({ id, value, onChange, error, rows = 7, label }: { id: string; value: string; onChange: (v: string) => void; error: string | null; rows?: number; label: string }) {
  return (
    <>
      <textarea
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={rows}
        spellCheck={false}
        aria-label={label}
        aria-invalid={!!error}
        aria-describedby={error ? `${id}-err` : undefined}
        className={cn(field, "h-auto resize-y py-2 font-mono text-[0.78rem] leading-5")}
      />
      {error && (
        <span id={`${id}-err`} className="text-xs text-danger" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

function Frame({ title, lede, children, footer, onSubmit }: { title: string; lede?: ReactNode; children: ReactNode; footer: ReactNode; onSubmit: () => void }) {
  return (
    <form
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        onSubmit();
      }}
      className="flex min-h-0 flex-col"
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        {lede && <DialogDescription>{lede}</DialogDescription>}
      </DialogHeader>
      <DialogBody className="grid gap-5">{children}</DialogBody>
      <DialogFooter>{footer}</DialogFooter>
    </form>
  );
}

// ------------------------------------------------------------------ schedules

type Preset = "minutes" | "hourly" | "daily" | "weekdays" | "custom";
type When = { preset: Preset; every: number; minute: number; time: string; raw: string };

/** Reads an expression back into the preset it came from, or Custom. */
export function parseWhen(expr: string): When {
  const base: When = { preset: "custom", every: 15, minute: 0, time: "09:00", raw: expr };
  const s = expr.trim();
  const pad = (n: string) => n.padStart(2, "0");
  let m: RegExpMatchArray | null;
  if ((m = s.match(/^\*\/(\d+) \* \* \* \*$/))) return { ...base, preset: "minutes", every: Number(m[1]) };
  if (s === "* * * * *") return { ...base, preset: "minutes", every: 1 };
  if (s === "@hourly") return { ...base, preset: "hourly", minute: 0 };
  if ((m = s.match(/^(\d{1,2}) \* \* \* \*$/))) return { ...base, preset: "hourly", minute: Number(m[1]) };
  if (s === "@daily") return { ...base, preset: "daily", time: "00:00" };
  if ((m = s.match(/^(\d{1,2}) (\d{1,2}) \* \* \*$/))) return { ...base, preset: "daily", time: `${pad(m[2])}:${pad(m[1])}` };
  if ((m = s.match(/^(\d{1,2}) (\d{1,2}) \* \* 1-5$/))) return { ...base, preset: "weekdays", time: `${pad(m[2])}:${pad(m[1])}` };
  return base;
}

export function whenExpr(w: When): string {
  const [h, mm] = w.time.split(":").map((x) => Number(x) || 0);
  switch (w.preset) {
    case "minutes":
      return w.every === 1 ? "* * * * *" : `*/${w.every} * * * *`;
    case "hourly":
      return `${w.minute} * * * *`;
    case "daily":
      return `${mm} ${h} * * *`;
    case "weekdays":
      return `${mm} ${h} * * 1-5`;
    default:
      return w.raw.trim();
  }
}

const myZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
let zoneList: string[] | undefined;
const zones = () => (zoneList ??= ["UTC", ...(typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [])]);

function nextFmt(tz: string) {
  try {
    return new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz });
  } catch {
    return new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" });
  }
}

/** "every weekday at 09:00 (Europe/London)". */
export function scheduleSentence(expr: string, tz: string) {
  const c = cronHuman(expr);
  const zone = tz && tz !== "UTC" ? tz : "UTC";
  return c.exact ? `${c.words}${c.timeOfDay ? `, ${zone} time` : ""}` : `on the schedule ${expr} (${zone})`;
}

/** Waits until a new schedule exists on the box (its change applied), then runs it once. */
async function runWhenReady(project: string, name: string): Promise<string | null> {
  for (let i = 0; i < 40; i++) {
    const list = await mod2.crons(project).catch(() => []);
    if (list.some((c) => c.name === name)) return (await jobsApi.triggerCron(project, name)).job;
    await new Promise((r) => setTimeout(r, 500));
  }
  return null;
}

export function ScheduleForm({ project, name: editing, done }: FormProps) {
  const navigate = useNavigate();
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const crons = useQuery(jq.crons(project));
  const m = manifest.data?.manifest;
  const apps = Object.keys(m?.apps ?? {});
  const existing: Cron | undefined = editing ? m?.crons?.[editing] : undefined;
  const live = editing ? crons.data?.find((c) => c.name === editing) : undefined;
  if (!m) return <div className="h-96" aria-busy />;
  if (editing && !existing && live?.origin === "vercel.json")
    return (
      <Frame title={`The ${editing} schedule`} footer={<Button onClick={done}>Close</Button>} onSubmit={done}>
        <p className="text-[0.9375rem] text-ink-2">
          This schedule comes from <code className="ident">{live.app}</code>’s vercel.json, so it changes with that app’s next deploy. Pause it here, or move it into
          tiffin.config.ts to edit it.
        </p>
      </Frame>
    );
  return <ScheduleFields key={editing ?? ""} project={project} manifest={m} apps={apps} editing={editing && existing ? editing : undefined} existing={existing} done={done} navigate={navigate} />;
}

function ScheduleFields({
  project,
  manifest,
  apps,
  editing,
  existing,
  done,
  navigate,
}: {
  project: string;
  manifest: Manifest;
  apps: string[];
  editing?: string;
  existing?: Cron;
  done: () => void;
  navigate: ReturnType<typeof useNavigate>;
}) {
  const uid = useId();
  const [name, setName] = useState(editing ?? "");
  const [when, setWhen] = useState<When>(() => (existing ? parseWhen(existing.schedule) : { preset: "weekdays", every: 15, minute: 0, time: "09:00", raw: "0 9 * * 1-5" }));
  const [tz, setTz] = useState(existing ? (existing.timezone ?? "UTC") : myZone());
  const [target, setTarget] = useState<"app" | "url">(existing ? (existing.url ? "url" : "app") : apps.length ? "app" : "url");
  const [app, setApp] = useState(existing?.app ?? Object.entries(manifest.apps ?? {}).find(([, a]) => a.role === "worker")?.[0] ?? apps[0] ?? "");
  const [path, setPath] = useState(existing?.path ?? "");
  const [url, setUrl] = useState(existing?.url ?? "");
  const [skip, setSkip] = useState(!existing?.overlap);
  const [timeout, setTimeoutS] = useState(existing?.timeoutSeconds ?? 0);
  const [more, setMore] = useState(!!existing?.overlap || !!existing?.timeoutSeconds);
  const [running, setRunning] = useState(false);

  const expr = whenExpr(when);
  const zoneOk = zones().includes(tz);
  const shown = useDebounced({ expr, tz }, 250);
  const preview = useQuery({
    queryKey: ["schedule-preview", project, shown.expr, shown.tz],
    queryFn: () => jobsApi.preview(project, shown.expr, shown.tz === "UTC" ? "" : shown.tz),
    enabled: !!shown.expr && zones().includes(shown.tz),
    retry: false,
    staleTime: 60_000,
  });
  const taken = Object.keys(manifest.crons ?? {});
  const nameErr = editing ? false : !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : taken.includes(name) ? `There’s already a schedule called ${name}.` : false;
  const defPath = `/cron/${name || "name"}`;
  const p = path || defPath;
  const urlErr = target === "url" && url && !urlOk(url) ? "A full address, like https://hooks.example.com/digest (no user name or #part)." : false;
  const pathErr = target === "app" && path && !path.startsWith("/") ? "Starts with /." : false;
  const ok =
    !!name && !nameErr && !!expr && zoneOk && !preview.isError && (target === "url" ? !!url && !urlErr : !!app && !pathErr) && (timeout === 0 || (timeout >= 5 && timeout <= 3600));

  const spec = (): Cron => {
    const c: Cron = { schedule: expr };
    if (target === "url") c.url = url.trim();
    else {
      c.app = app;
      if (path && path !== `/cron/${name}`) c.path = path;
    }
    if (tz && tz !== "UTC") c.timezone = tz;
    if (!skip) c.overlap = true;
    if (timeout) c.timeoutSeconds = timeout;
    return c;
  };
  const targetWords = target === "url" ? shortURL(url) : `${app} at ${p}`;
  const save = () => {
    if (!ok) return;
    const said = scheduleSentence(expr, tz);
    change(
      project,
      {
        kind: "set",
        path: ["crons", name],
        from: existing,
        to: spec(),
        what: editing ? `Change the ${name} schedule: ${said}, calling ${targetWords}` : `Add the ${name} schedule: ${said}, calling ${targetWords}`,
        undo: editing ? `${name} goes back to ${scheduleSentence(existing!.schedule, existing!.timezone ?? "UTC")}` : `the ${name} schedule is removed`,
      },
      { immediate: true },
    );
    done();
  };
  const runOnce = async () => {
    if (!ok) return;
    setRunning(true);
    try {
      if (!editing) save();
      const job = editing ? (await jobsApi.triggerCron(project, name)).job : await runWhenReady(project, name);
      if (!job) throw new Error("the schedule wasn’t ready in time; try Run now in a moment");
      toast({ title: `Started ${name} once.`, detail: "Its regular runs stay as they are.", action: { label: "Watch it", run: () => void navigate({ to: "/projects/$project/jobs/$id", params: { project, id: job } }) } });
      if (editing) done();
    } catch (e) {
      toast({ title: `Couldn’t run ${name}.`, detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e instanceof Error ? e.message : e), tone: "danger" });
    } finally {
      setRunning(false);
    }
  };
  const remove = () => {
    change(project, { kind: "set", path: ["crons", name], from: existing, to: undefined, what: `Remove the ${name} schedule`, undo: `the ${name} schedule comes back` }, { immediate: true });
    done();
  };
  const fmt = nextFmt(zoneOk ? tz : "UTC");
  const next = preview.data?.next ?? [];

  return (
    <Frame
      title={editing ? `Edit ${editing}` : "New schedule"}
      lede={editing ? undefined : "The box calls an app route or a web address on a schedule, and keeps every run."}
      onSubmit={save}
      footer={
        <>
          {editing && (
            <Button type="button" variant="danger-quiet" className="sm:mr-auto" onClick={remove}>
              <Trash2 /> Delete schedule
            </Button>
          )}
          <Button type="button" variant="ghost" onClick={done}>
            Cancel
          </Button>
          <Button type="button" onClick={() => void runOnce()} disabled={!ok || running}>
            {running ? "Starting…" : "Run once now"}
          </Button>
          <Button type="submit" variant="primary" disabled={!ok}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_minmax(0,15rem)] md:gap-x-7">
        <div className="grid min-w-0 content-start gap-4">
          {!editing && (
            <Field label="Name" htmlFor={`${uid}-name`} error={nameErr} note="Lowercase, like nightly-digest. It names the schedule in History and the CLI.">
              <input
                id={`${uid}-name`}
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
                placeholder="morning-digest"
                spellCheck={false}
                aria-invalid={!!nameErr}
                className={cn(field, "ident")}
              />
            </Field>
          )}
          <div className="grid gap-2">
            <span className="text-xs font-[550] text-ink-2" id={`${uid}-when`}>
              When
            </span>
            <Segmented
              label="When"
              value={when.preset}
              onChange={(preset) => setWhen((w) => ({ ...w, preset, raw: preset === "custom" ? whenExpr(w) : w.raw }))}
              items={[
                { value: "minutes", label: "Every N minutes" },
                { value: "hourly", label: "Every hour" },
                { value: "daily", label: "Every day at…" },
                { value: "weekdays", label: "Weekdays at…" },
                { value: "custom", label: "Custom" },
              ]}
            />
            <div className="flex flex-wrap items-center gap-2 text-[0.84375rem] text-ink-2">
              {when.preset === "minutes" && (
                <div className="flex items-center gap-2">
                  Every
                  <Select
                    aria-label="Every how many minutes"
                    value={String(when.every)}
                    onValueChange={(v) => setWhen({ ...when, every: Number(v) })}
                    className="w-20"
                    options={[1, 2, 5, 10, 15, 20, 30].map((n) => ({ value: String(n), label: n }))}
                  />
                  {when.every === 1 ? "minute" : "minutes"}
                </div>
              )}
              {when.preset === "hourly" && (
                <div className="flex items-center gap-2">
                  At minute
                  <Select
                    aria-label="At minute"
                    value={String(when.minute)}
                    onValueChange={(v) => setWhen({ ...when, minute: Number(v) })}
                    className="w-20"
                    options={Array.from({ length: 12 }, (_, i) => i * 5).map((n) => ({ value: String(n), label: `:${String(n).padStart(2, "0")}` }))}
                  />
                </div>
              )}
              {(when.preset === "daily" || when.preset === "weekdays") && (
                <label className="flex items-center gap-2">
                  At
                  <input type="time" value={when.time} step={60} onChange={(e) => setWhen({ ...when, time: e.target.value || "00:00" })} className={cn(field, "w-32 tnum")} />
                </label>
              )}
              {when.preset === "custom" && (
                <input
                  value={when.raw}
                  onChange={(e) => setWhen({ ...when, raw: e.target.value })}
                  aria-label="Cron expression"
                  aria-invalid={preview.isError}
                  placeholder="0 9 * * 1-5"
                  spellCheck={false}
                  className={cn(field, "ident")}
                />
              )}
            </div>
            <p className="text-xs text-ink-3">
              <code className="ident text-[0.72rem] text-ink-2">{expr || "…"}</code>
              {when.preset === "custom" && " · minute, hour, day of month, month, day of week"}
            </p>
          </div>
          <Field label="Time zone" htmlFor={`${uid}-tz`} error={!zoneOk && "Pick a time zone from the list, such as Europe/London or UTC."}>
            <input id={`${uid}-tz`} list={`${uid}-zones`} value={tz} onChange={(e) => setTz(e.target.value)} spellCheck={false} aria-invalid={!zoneOk} className={field} />
            <datalist id={`${uid}-zones`}>
              {zones().map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </Field>
          <div className="grid gap-2">
            <span className="text-xs font-[550] text-ink-2">Calls</span>
            <Segmented
              label="Calls"
              value={target}
              onChange={setTarget}
              items={[
                { value: "app", label: "An app route", disabled: apps.length === 0 },
                { value: "url", label: "A URL" },
              ]}
            />
            {target === "app" ? (
              <div className="grid gap-2 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]">
                <Select aria-label="App" value={app} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} />
                <input value={path} onChange={(e) => setPath(e.target.value)} placeholder={defPath} aria-label="Path on the app" aria-invalid={!!pathErr} spellCheck={false} className={cn(field, "ident")} />
                {pathErr && <span className="text-xs text-danger sm:col-span-2">{pathErr}</span>}
              </div>
            ) : (
              <>
                <input
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://hooks.example.com/digest"
                  aria-label="Web address to call"
                  aria-invalid={!!urlErr}
                  inputMode="url"
                  spellCheck={false}
                  className={cn(field, "ident")}
                />
                {urlErr ? <span className="text-xs text-danger">{urlErr}</span> : apps.length === 0 && <span className="text-xs text-ink-3">This project has no apps, so a schedule calls a web address.</span>}
              </>
            )}
          </div>
        </div>

        <div className="grid content-start gap-2 md:border-l md:border-rule md:pl-6">
          <span className="text-xs font-[550] text-ink-2">Next runs</span>
          {!zoneOk || !expr ? (
            <p className="text-[0.84375rem] text-ink-3">Pick when it runs.</p>
          ) : preview.isError ? (
            <p className="text-[0.84375rem] text-danger" role="alert">
              {preview.error instanceof ApiError ? (preview.error.problem.detail ?? preview.error.message) : "That schedule doesn’t read."}
            </p>
          ) : (
            <ol className="grid gap-1 text-[0.875rem] text-ink tnum" aria-live="polite" aria-busy={preview.isFetching}>
              {(next.length ? next : Array.from({ length: 5 }, () => "")).map((n, i) =>
                n ? (
                  <li key={n}>{fmt.format(new Date(n))}</li>
                ) : (
                  <li key={i} className="h-5 w-32 animate-pulse rounded bg-paper-sunk" />
                ),
              )}
            </ol>
          )}
          <p className="mt-1 text-xs text-ink-3">
            {zoneOk && tz !== myZone() ? `In ${tz} time. ` : ""}Signed with your project’s key, retried with backoff
            {skip ? ", skipped if the last run is still going." : "."}
          </p>
        </div>
      </div>

      <details open={more} onToggle={(e) => setMore(e.currentTarget.open)} className="group">
        <summary className="cursor-pointer text-[0.8125rem] font-[550] text-ink-2 select-none hover:text-ink">More options</summary>
        <div className="mt-3 grid gap-4 sm:grid-cols-2">
          <div className="flex items-start gap-3">
            <Breaker label="Skip a run while the last one is still going" state={skip ? "on" : "off"} onFlip={(n) => setSkip(n === "on")} />
            <span className="text-[0.84375rem] text-ink-2">
              Skip a run while the last one is still going
              <span className="block text-xs text-ink-3">Off: runs can overlap.</span>
            </span>
          </div>
          <Field label="Give up on a call after (seconds)" note={timeout ? undefined : "60 seconds, then it is retried."}>
            <Stepper value={timeout} onChange={setTimeoutS} min={0} max={3600} step={5} zero="60" label="Timeout in seconds" />
          </Field>
        </div>
      </details>
    </Frame>
  );
}

// ------------------------------------------------------------------ queues

const periods = [
  { s: 1, label: "a second" },
  { s: 60, label: "a minute" },
  { s: 3600, label: "an hour" },
  { s: 86400, label: "a day" },
];

export function QueueForm({ project, name: editing, done }: FormProps) {
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const stats = useQuery(jq.stats(project));
  const m = manifest.data?.manifest;
  if (!m) return <div className="h-96" aria-busy />;
  const declared = editing ? m.queues?.[editing] : undefined;
  const live = editing ? stats.data?.find((s) => s.name === editing) : undefined;
  return <QueueFields key={editing ?? ""} project={project} manifest={m} editing={editing} declared={declared} live={live} done={done} />;
}

function QueueFields({ project, manifest, editing, declared, live, done }: { project: string; manifest: Manifest; editing?: string; declared?: Queue; live?: QueueStats; done: () => void }) {
  const uid = useId();
  const apps = Object.keys(manifest.apps ?? {});
  const base: Partial<Queue> = declared ?? (live ? { app: live.app, url: live.url, path: live.path, concurrency: live.concurrency, keyConcurrency: live.keyConcurrency, rateLimit: live.rateLimit, ratePeriodSeconds: live.ratePeriodSeconds, maxAttempts: live.maxAttempts, leaseSeconds: live.leaseSeconds } : {});
  const [name, setName] = useState(editing ?? "");
  const [target, setTarget] = useState<"app" | "url">(base.url ? "url" : apps.length ? "app" : "url");
  const [app, setApp] = useState(base.app || (Object.entries(manifest.apps ?? {}).find(([, a]) => a.role === "worker")?.[0] ?? apps[0] ?? ""));
  const [path, setPath] = useState(base.path && base.path !== `/queues/${editing}` ? base.path : "");
  const [url, setUrl] = useState(base.url ?? "");
  const [concurrency, setConcurrency] = useState(base.concurrency ?? 0);
  const [keyConc, setKeyConc] = useState(base.keyConcurrency ?? 0);
  const [rate, setRate] = useState(base.rateLimit ?? 0);
  const [period, setPeriod] = useState(base.ratePeriodSeconds || 60);
  const [attempts, setAttempts] = useState(base.maxAttempts || 10);
  const [lease, setLease] = useState(base.leaseSeconds || 60);
  const taken = Object.keys(manifest.queues ?? {});
  const nameErr = editing ? false : !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : taken.includes(name) ? `There’s already a queue called ${name}.` : manifest.topics?.[name] ? `${name} is a topic.` : false;
  const urlErr = target === "url" && url && !urlOk(url) ? "A full address, like https://hooks.example.com/orders." : false;
  const ok = !!name && !nameErr && (target === "url" ? !!url && !urlErr : !!app && (!path || path.startsWith("/")));
  const spec = (): Queue => {
    // The normalized form (as the box stores it), so an unchanged queue plans nothing.
    const where = target === "url" ? { url: url.trim() } : { app, path: path || `/queues/${name}` };
    return { ...where, concurrency, keyConcurrency: keyConc, rateLimit: rate, ratePeriodSeconds: rate ? period : 0, maxAttempts: attempts, leaseSeconds: lease };
  };
  const save = () => {
    if (!ok) return;
    const where = target === "url" ? shortURL(url) : `${app} at ${path || `/queues/${name}`}`;
    change(
      project,
      {
        kind: "set",
        path: ["queues", name],
        from: declared,
        to: spec(),
        what: editing && declared ? `Change the ${name} queue, delivering to ${where}` : editing ? `Declare ${name} in tiffin.config.ts, delivering to ${where}` : `Add the ${name} queue, delivering to ${where}`,
        undo: declared ? `${name} goes back to its earlier settings` : `the ${name} queue is removed, with any jobs still waiting in it`,
      },
      { immediate: true },
    );
    done();
  };
  const remove = () => {
    change(project, { kind: "set", path: ["queues", name], from: declared, to: undefined, what: `Remove the ${name} queue`, undo: `the ${name} queue comes back, empty` }, { immediate: true });
    done();
  };
  return (
    <Frame
      title={editing ? `${editing} settings` : "New queue"}
      lede={editing ? undefined : "Jobs sent to a queue are delivered one by one to an app route or a web address, retried with backoff, and kept when they keep failing."}
      onSubmit={save}
      footer={
        <>
          {editing && declared && (
            <Button type="button" variant="danger-quiet" className="sm:mr-auto" onClick={remove}>
              <Trash2 /> Delete queue…
            </Button>
          )}
          <Button type="button" variant="ghost" onClick={done}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!ok}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      {!editing && (
        <Field label="Name" htmlFor={`${uid}-name`} error={nameErr}>
          <input
            id={`${uid}-name`}
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
            placeholder="emails"
            spellCheck={false}
            aria-invalid={!!nameErr}
            className={cn(field, "ident")}
          />
        </Field>
      )}
      {editing && !declared && <p className="text-[0.84375rem] text-ink-2">Saving declares {editing} in tiffin.config.ts, so its settings are kept with the project.</p>}
      <div className="grid gap-2">
        <span className="text-xs font-[550] text-ink-2">Delivers to</span>
        <Segmented
          label="Delivers to"
          value={target}
          onChange={setTarget}
          items={[
            { value: "app", label: "An app route", disabled: apps.length === 0 },
            { value: "url", label: "A URL" },
          ]}
        />
        {target === "app" ? (
          <div className="grid gap-2 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]">
            <Select aria-label="App" value={app} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} />
            <input value={path} onChange={(e) => setPath(e.target.value)} placeholder={`/queues/${name || "name"}`} aria-label="Path on the app" spellCheck={false} className={cn(field, "ident")} />
          </div>
        ) : (
          <>
            <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.example.com/orders" aria-label="Web address to call" aria-invalid={!!urlErr} spellCheck={false} className={cn(field, "ident")} />
            {urlErr && <span className="text-xs text-danger">{urlErr}</span>}
          </>
        )}
      </div>
      <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
        <Field label="Run at most, at once" note={concurrency ? `${int(concurrency)} at a time` : "No limit"}>
          <Stepper value={concurrency} onChange={setConcurrency} min={0} max={1000} zero="No limit" label="Jobs at once" />
        </Field>
        <Field label="At once per key" note={keyConc ? "Per send key, e.g. per customer" : "No limit per key"}>
          <Stepper value={keyConc} onChange={setKeyConc} min={0} max={1000} zero="No limit" label="Jobs at once per key" />
        </Field>
        <Field label="Start at most" note={rate ? `${int(rate)} per key, ${periods.find((x) => x.s === period)?.label ?? `${period} s`}` : "No rate limit"}>
          <div className="flex flex-wrap items-center gap-2">
            <Stepper value={rate} onChange={setRate} min={0} max={10000} zero="No limit" label="Jobs started per period" />
            <Select
              aria-label="Per"
              value={String(period)}
              onValueChange={(v) => setPeriod(Number(v))}
              disabled={!rate}
              className="w-32"
              options={periods.map((x) => ({ value: String(x.s), label: `per ${x.label.replace(/^an? /, "")}` }))}
            />
          </div>
        </Field>
        <Field label="Tries before it gives up" note="Then the job waits in Failed.">
          <Stepper value={attempts} onChange={setAttempts} min={1} max={100} label="Tries" />
        </Field>
        <Field label={target === "url" ? "Give up on a call after (seconds)" : "Seconds without an answer or heartbeat"} note={target === "url" ? "Each call’s timeout." : "Long jobs heartbeat to keep going."}>
          <Stepper value={lease} onChange={setLease} min={5} max={3600} step={5} label="Seconds" />
        </Field>
      </div>
    </Frame>
  );
}

// ------------------------------------------------------------------ a test job

const delays = [
  { s: 0, label: "Now" },
  { s: 10, label: "In 10 seconds" },
  { s: 60, label: "In a minute" },
  { s: 300, label: "In 5 minutes" },
  { s: 3600, label: "In an hour" },
];

export function SendJobForm({ project, name: preset, done }: FormProps) {
  const uid = useId();
  const navigate = useNavigate();
  const stats = useQuery(jq.stats(project));
  const topics = useQuery(jq.topics(project));
  const targets = useMemo(() => (stats.data ?? []).filter((x) => !x.name.startsWith("_") && (x.topic || x.app || x.url)), [stats.data]);
  const [queue, setQueue] = useState(preset ?? "");
  const chosen = queue || targets.find((t) => !t.topic)?.name || targets[0]?.name || "";
  const [payload, setPayload] = useState('{\n  "test": true\n}');
  const [delay, setDelay] = useState(0);
  const [key, setKey] = useState("");
  const [priority, setPriority] = useState<"low" | "normal" | "high">("normal");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const bad = jsonError(payload);
  const isTopic = targets.find((t) => t.name === chosen)?.topic;
  const subs = topics.data?.find((t) => t.name === chosen)?.subscriptions?.length ?? 0;
  const send = async () => {
    if (!chosen || bad) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await jobsApi.send(project, {
        name: chosen,
        payload: payload.trim() ? JSON.parse(payload) : undefined,
        delaySeconds: delay || undefined,
        key: key.trim() || undefined,
        priority,
      });
      const ids = r.jobs ?? [];
      done();
      toast({ title: r.topic ? `Published to ${chosen}: ${int(ids.length)} ${ids.length === 1 ? "job" : "jobs"}.` : `Sent ${ids[0]} to ${chosen}.`, detail: delay ? `It runs ${delays.find((d) => d.s === delay)?.label.toLowerCase()}.` : undefined });
      if (ids.length === 1) void navigate({ to: "/projects/$project/jobs/$id", params: { project, id: ids[0] } });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Frame
      title="Send a test job"
      lede="It goes through the queue like any job your apps send: signed, retried, and watched live."
      onSubmit={() => void send()}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={done}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!chosen || !!bad || busy}>
            {busy ? "Sending…" : isTopic ? `Publish to ${chosen}` : chosen ? `Send to ${chosen}` : "Send"}
          </Button>
        </>
      }
    >
      {targets.length === 0 ? (
        <p className="text-[0.9375rem] text-ink-2">No queue has a target yet. Make one with New queue, then send it a job.</p>
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Queue" htmlFor={`${uid}-q`} note={isTopic ? `A topic: one job for each of its ${int(subs)} ${subs === 1 ? "subscriber" : "subscribers"}.` : undefined}>
              <Select
                id={`${uid}-q`}
                value={chosen}
                onValueChange={setQueue}
                className="font-mono"
                options={targets.map((t) => ({ value: t.name, label: `${t.name}${t.topic ? " (topic)" : ""}` }))}
              />
            </Field>
            <Field label="When" htmlFor={`${uid}-d`}>
              <Select id={`${uid}-d`} value={String(delay)} onValueChange={(v) => setDelay(Number(v))} options={delays.map((d) => ({ value: String(d.s), label: d.label }))} />
            </Field>
          </div>
          <Field label="Payload (JSON)" htmlFor={`${uid}-p`}>
            <JsonBox id={`${uid}-p`} value={payload} onChange={setPayload} error={bad} label="Payload (JSON)" />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Key (optional)" htmlFor={`${uid}-k`} note="Per-key limits apply to it, e.g. a customer ID.">
              <input id={`${uid}-k`} value={key} onChange={(e) => setKey(e.target.value)} maxLength={200} spellCheck={false} className={cn(field, "ident")} />
            </Field>
            <div className="grid gap-1">
              <span className="text-xs font-[550] text-ink-2">Priority</span>
              <Segmented
                label="Priority"
                value={priority}
                onChange={setPriority}
                items={[
                  { value: "low", label: "Low" },
                  { value: "normal", label: "Normal" },
                  { value: "high", label: "High" },
                ]}
              />
            </div>
          </div>
          {err !== null && <ProblemNote error={err} />}
        </>
      )}
    </Frame>
  );
}

// ------------------------------------------------------------------ a workflow run

export function StartRunForm({ project, done }: FormProps) {
  const uid = useId();
  const navigate = useNavigate();
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const runs = useQuery(jq.runs(project));
  const apps = Object.keys(manifest.data?.manifest.apps ?? {});
  const known = [...new Set((runs.data ?? []).map((r) => r.workflow))];
  const lastApp = (wf: string) => runs.data?.find((r) => r.workflow === wf)?.app;
  const [workflow, setWorkflow] = useState("");
  const [app, setApp] = useState("");
  const [input, setInput] = useState("{\n}");
  const [idem, setIdem] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const bad = jsonError(input);
  const chosenApp = app || lastApp(workflow) || apps[0] || "";
  const start = async () => {
    if (!workflow.trim() || !chosenApp || bad) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await jobsApi.startRun(project, { workflow: workflow.trim(), app: chosenApp, input: input.trim() ? JSON.parse(input) : undefined, id: idem.trim() || undefined });
      done();
      toast({ title: `Started ${r.workflow}.`, detail: `Pinned to ${r.app}’s current release.` });
      void navigate({ to: "/projects/$project/jobs/$id", params: { project, id: r.id } });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Frame
      title="Start a workflow run"
      lede="Runs a workflow your app defines with workflow.define, with the input you give it."
      onSubmit={() => void start()}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={done}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!workflow.trim() || !chosenApp || !!bad || busy}>
            {busy ? "Starting…" : "Start the run"}
          </Button>
        </>
      }
    >
      {apps.length === 0 ? (
        <p className="text-[0.9375rem] text-ink-2">Workflows run inside an app, and this project has none yet.</p>
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Workflow" htmlFor={`${uid}-w`} note={known.length ? undefined : "Its name in workflow.define(\"…\")."}>
              <input id={`${uid}-w`} list={`${uid}-known`} autoFocus value={workflow} onChange={(e) => setWorkflow(e.target.value)} placeholder="monthly-report" spellCheck={false} className={cn(field, "ident")} />
              <datalist id={`${uid}-known`}>
                {known.map((w) => (
                  <option key={w} value={w} />
                ))}
              </datalist>
            </Field>
            <Field label="In the app" htmlFor={`${uid}-a`}>
              <Select id={`${uid}-a`} value={chosenApp} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} />
            </Field>
          </div>
          <Field label="Input (JSON)" htmlFor={`${uid}-i`}>
            <JsonBox id={`${uid}-i`} value={input} onChange={setInput} error={bad} label="Input (JSON)" />
          </Field>
          <Field label="Run ID (optional)" htmlFor={`${uid}-id`} note="Starting again with the same ID opens the run it started, instead of a second one.">
            <input id={`${uid}-id`} value={idem} onChange={(e) => setIdem(e.target.value)} maxLength={200} spellCheck={false} className={cn(field, "ident")} />
          </Field>
          {err !== null && <ProblemNote error={err} />}
        </>
      )}
    </Frame>
  );
}
