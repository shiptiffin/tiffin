import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, Pencil, Search, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api, notOnBox } from "@/api/client";
import { mod3 } from "@/api/modules";
import { q } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { SetByTiffin } from "@/components/env-box";
import { EnvDialog } from "@/components/env-dialog";
import { EnvImport } from "@/components/env-import";
import { builtIn, envRows, lastTouched, removePath, scopeWords, toDotenv, type EnvRow } from "@/components/env-model";
import { useTitle } from "@/components/favicon";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Working } from "@/components/project-rows";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox, Select } from "@/components/ui/choice";
import { actorWords } from "@/lib/actors";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { count } from "@/lib/format";
import { useMe } from "@/lib/me";
import { rememberProject } from "@/lib/recent";
import { undoChange, usePending } from "@/lib/staged";
import { full, relative } from "@/lib/time";
import { actorShown } from "@/lib/who";

/** Columns: select, name, value, scope, changed, actions. Phones stack them. */
const COLS = "sm:grid-cols-[1.25rem_minmax(0,14rem)_minmax(0,1fr)_minmax(0,8.5rem)_8.5rem_7.5rem]";
const DOTS = "••••••••";

/**
 * A project's environment variables in one table, the Vercel way: plain
 * values (kept in its config, so History and Undo work; masked until you
 * reveal one), secrets (encrypted on the box, write-only: never shown or
 * copied, only replaced), which part of the project each reaches, and when
 * it last changed. Add one, paste a .env, copy them out as a .env, select
 * several to delete. Below, what Tiffin sets for every app, read-only.
 */
export function EnvVarsPage({ project }: { project: string }) {
  useTitle(`${project} · Environment Variables`);
  useEffect(() => rememberProject(project), [project]);
  const qc = useQueryClient();
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const secrets = useQuery({ ...q.secrets(project), retry: false });
  const changes = useQuery({ ...q.changes(project), staleTime: 15_000 });
  const names = useQuery({ ...q.tokenNames, retry: false });
  const pending = usePending(project);
  const { can, name: myName } = useMe();
  const writer = can("apply:reversible");
  const [adding, setAdding] = useState(false);
  const [importing, setImporting] = useState<string | null>(null);
  const [editing, setEditing] = useState<EnvRow | null>(null);
  const [deleting, setDeleting] = useState<EnvRow[] | null>(null);
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<"name" | "changed">("name");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [shownValues, setShownValues] = useState<Set<string>>(new Set());
  const undoIds = useRef<string[]>([]);

  const man = m.data?.manifest;
  const apps = useMemo(() => Object.keys(man?.apps ?? {}).sort(), [man]);
  const running = useMemo(
    () =>
      Object.entries(man?.apps ?? {})
        .filter(([, a]) => a.framework !== "static")
        .map(([n]) => n)
        .sort(),
    [man],
  );
  const secretsOn = !notOnBox(secrets.error);
  const touched = useMemo(() => lastTouched(changes.data), [changes.data]);
  const rows = useMemo(() => envRows(man, secrets.data, pending, touched), [man, secrets.data, pending, touched]);
  const secretNames = useMemo(() => (secrets.data ?? []).map((s) => s.name), [secrets.data]);
  const own = useMemo(() => new Set(rows.map((r) => r.key)), [rows]);
  const needle = filter.trim().toLowerCase();
  const shown = useMemo(() => {
    const list = needle ? rows.filter((r) => r.key.toLowerCase().includes(needle) || (!r.secret && (r.value ?? "").toLowerCase().includes(needle))) : rows;
    return sort === "changed" ? [...list].sort((a, b) => (b.at ?? "").localeCompare(a.at ?? "") || a.key.localeCompare(b.key)) : list;
  }, [rows, needle, sort]);
  const selectable = shown.filter((r) => !r.adding && !r.staged);
  const selected = rows.filter((r) => picked.has(r.id));
  const allPicked = selectable.length > 0 && selectable.every((r) => picked.has(r.id));

  const restart = useMutation({
    mutationFn: () => Promise.all(running.map((a) => mod3.restart(project, a))),
    onSuccess: () => {
      for (const a of running) void qc.invalidateQueries({ queryKey: ["runtime", project, a] });
      toast({ title: `Restarting ${scopeWords(running)}.`, detail: "One copy at a time, so they keep answering." });
    },
    onError: (e) => toast({ title: "Couldn’t restart the apps.", detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });

  if (m.isError && notOnBox(m.error)) return <NotOnBox what="Environment Variables" />;

  // Named as History names them, and "you" for you.
  const who = (by?: EnvRow["by"]) => {
    if (!by) return undefined;
    const n = by.kind === "token" ? names.data?.get(by.id)?.who : by.kind === "agent" ? actorWords(by) : actorShown(by, names.data);
    return n && myName && n === myName ? "you" : (n ?? "someone");
  };
  const toggle = (set: Set<string>, id: string) => {
    const next = new Set(set);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  };
  const copyAll = async () => {
    if (await copyText(toDotenv(project, rows)))
      toast({
        title: `Copied ${count(rows.length, "variable")} as a .env file.`,
        detail: secretNames.length
          ? `Secrets come out as empty lines: their values never leave the box. tiffin secrets list ${project} shows their names; tiffin pull writes the plain values into tiffin.config.ts.`
          : "tiffin pull writes the same plain values into tiffin.config.ts.",
      });
  };

  const dialogProps = { project, manifest: man, apps, secretsOn, secretNames };
  const loaded = !!m.data;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Environment Variables" }]} />}
        title="Environment Variables"
        lede={`Settings, API keys and passwords the apps in ${project} read when they start.`}
        actions={
          writer && (
            <>
              <Button size="lg" onClick={() => setImporting("")} disabled={!loaded}>
                Paste .env
              </Button>
              <Button variant="primary" size="lg" onClick={() => setAdding(true)} disabled={!loaded}>
                Add variable
              </Button>
            </>
          )
        }
      />
      <p className="mt-4 max-w-[48rem] text-[0.8125rem] leading-5 text-ink-3">
        A change applies when you save it: the apps restart with it, one copy at a time, no deploy needed. Values named <span className="ident text-[0.75rem]">NEXT_PUBLIC_*</span>,{" "}
        <span className="ident text-[0.75rem]">VITE_*</span> or <span className="ident text-[0.75rem]">PUBLIC_*</span> are built into browser code, so changing one rebuilds the web apps.
      </p>

      {m.isError && <ProblemNote className="mt-6" error={m.error} title="The project’s config can’t be read." />}
      {secrets.isError && secretsOn && <ProblemNote className="mt-6" error={secrets.error} title="Secrets can’t be listed right now." />}

      {loaded && rows.length === 0 ? (
        <div className="mt-8 rounded-[12px] border border-dashed border-rule-3 px-6 py-10 text-center">
          <p className="text-[0.9375rem] font-[550] text-ink">No variables yet</p>
          <p className="mx-auto mt-1 max-w-[30rem] text-[0.875rem] text-ink-3">Add your API keys and settings one by one, or paste a whole .env file and pick which ones are secrets.</p>
          {writer && (
            <div className="mt-4 flex flex-wrap justify-center gap-2">
              <Button variant="primary" onClick={() => setImporting("")}>
                Paste a .env
              </Button>
              <Button onClick={() => setAdding(true)}>Add variable</Button>
            </div>
          )}
        </div>
      ) : (
        <>
          <div className="mt-8 flex flex-wrap items-center gap-2">
            <label className="flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)] sm:w-80">
              <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Find a variable"
                aria-label="Find a variable"
                spellCheck={false}
                className="min-w-0 flex-1 bg-transparent text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4"
              />
            </label>
            <Select
              size="sm"
              aria-label="Sort"
              value={sort}
              onValueChange={(v) => setSort(v === "changed" ? "changed" : "name")}
              options={[
                { value: "name", label: "By name" },
                { value: "changed", label: "Last changed" },
              ]}
              className="w-36"
            />
            <span className="flex-1" />
            <Button size="md" variant="ghost" onClick={() => void copyAll()} disabled={!loaded || rows.length === 0}>
              Copy as .env
            </Button>
            {writer && running.length > 0 && (
              <Button size="md" variant="ghost" onClick={() => restart.mutate()} disabled={restart.isPending}>
                {restart.isPending ? "Restarting…" : "Restart apps"}
              </Button>
            )}
          </div>

          {writer && selected.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-3 rounded-[8px] bg-paper-sunk px-3 py-2 text-[0.8125rem] text-ink-2">
              <span className="tnum">{count(selected.length, "variable")} selected</span>
              <Button size="sm" variant="danger-quiet" onClick={() => setDeleting(selected)}>
                <Trash2 />
                Delete {selected.length}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())}>
                Clear
              </Button>
            </div>
          )}

          <div className="mt-3 border-b border-rule" role="table" aria-label="Environment variables">
            <div role="row" className={cn("hidden items-center gap-x-4 border-b border-rule pb-2 sm:grid", COLS)}>
              <span role="columnheader" className="flex">
                {writer && (
                  <Checkbox
                    aria-label={allPicked ? "Unselect all" : "Select all"}
                    checked={allPicked ? true : picked.size > 0 ? "indeterminate" : false}
                    onCheckedChange={() => setPicked(allPicked ? new Set() : new Set(selectable.map((r) => r.id)))}
                    disabled={selectable.length === 0}
                  />
                )}
              </span>
              {["Name", "Value", "Scope", "Changed"].map((h) => (
                <span key={h} role="columnheader" className="label text-ink-3">
                  {h}
                </span>
              ))}
              <span role="columnheader" className="sr-only">
                Actions
              </span>
            </div>
            {!loaded && !m.isError && (
              <div className="grid gap-2 py-3">
                <Skeleton className="h-9" />
                <Skeleton className="h-9" />
              </div>
            )}
            {loaded && rows.length > 0 && shown.length === 0 && <p className="py-6 text-[0.875rem] text-ink-3">Nothing matches “{filter}”.</p>}
            <div className="divide-y divide-rule max-sm:border-t max-sm:border-rule">
              {shown.map((r) => (
                <Row
                  key={r.id}
                  r={r}
                  who={who(r.by)}
                  writer={writer}
                  picked={picked.has(r.id)}
                  onPick={() => setPicked((s) => toggle(s, r.id))}
                  revealed={shownValues.has(r.id)}
                  onReveal={() => setShownValues((s) => toggle(s, r.id))}
                  onEdit={() => setEditing(r)}
                  onDelete={() => setDeleting([r])}
                />
              ))}
            </div>
          </div>
        </>
      )}

      <SetByTiffin project={project} manifest={man} own={own} />

      <EnvDialog
        {...dialogProps}
        open={adding}
        onOpenChange={setAdding}
        onPasteMany={(text) => {
          setAdding(false);
          setImporting(text);
        }}
      />
      <EnvDialog {...dialogProps} row={editing ?? undefined} open={!!editing} onOpenChange={(o) => !o && setEditing(null)} />
      <EnvImport {...dialogProps} initial={importing ?? ""} open={importing !== null} onOpenChange={(o) => !o && setImporting(null)} />
      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={deleting?.length === 1 ? `Delete ${deleting[0].key}?` : `Delete ${count(deleting?.length ?? 0, "variable")}?`}
        body={`${deleteWho(project, deleting)} without ${deleting?.length === 1 ? "it" : "them"}. You can undo this from History.`}
        action={deleting?.length === 1 ? `Delete ${deleting[0].key}` : `Delete ${count(deleting?.length ?? 0, "variable")}`}
        run={async () => {
          const list = deleting ?? [];
          undoIds.current = [];
          for (const r of list) if (r.secret) undoIds.current.push((await api.deleteSecret(project, r.key))?.change ?? "");
          for (const r of list) if (!r.secret) for (const p of r.paths) removePath(project, man, r.key, p);
        }}
        done={() => {
          const gone = (deleting ?? []).filter((r) => r.secret);
          setPicked(new Set());
          if (!gone.length) return; // plain values: the change's own toast says so, with Undo
          void qc.invalidateQueries({ queryKey: ["secrets", project] });
          void qc.invalidateQueries({ queryKey: ["changes"] });
          toast({
            title: gone.length === 1 ? <>Deleted the secret {gone[0].key}.</> : <>Deleted {count(gone.length, "secret")}.</>,
            detail: `Apps in ${project} restart without ${gone.length === 1 ? "it" : "them"}.${gone.length > 1 ? " History can undo each one." : ""}`,
            action: gone.length === 1 && undoIds.current[0] ? { label: "Undo", run: () => undoChange(undoIds.current[0]) } : undefined,
          });
        }}
      />
    </Page>
  );
}

/** "The apps in shop restart", "web restarts", "web and api restart". */
function deleteWho(project: string, rows: EnvRow[] | null): string {
  const scopes = (rows ?? []).map((r) => (r.secret ? "all" : r.scope));
  if (scopes.length === 0 || scopes.some((s) => s === "all")) return `The apps in ${project} restart`;
  const apps = [...new Set(scopes.flat())].sort();
  return `${scopeWords(apps)} ${apps.length === 1 ? "restarts" : "restart"}`;
}

function Row({
  r,
  who,
  writer,
  picked,
  onPick,
  revealed,
  onReveal,
  onEdit,
  onDelete,
}: {
  r: EnvRow;
  who?: string;
  writer: boolean;
  picked: boolean;
  onPick: () => void;
  revealed: boolean;
  onReveal: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const busy = !!r.staged || !!r.adding;
  const value = (r.staged && !r.staged.removing ? r.staged.to : r.value) ?? "";
  const when = r.at ? `${r.secret ? "Set " : ""}${r.secret ? relative(r.at) : capital(relative(r.at))}` : undefined;
  const scope = r.secret || r.scope === "all" ? "Whole project" : scopeWords(r.scope);
  return (
    <div role="row" className={cn("grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-1 py-2.5", COLS, picked ? "bg-paper-select" : "", busy ? "opacity-80" : "")}>
      <span role="cell" className="flex h-7 items-center max-sm:hidden">
        {writer && <Checkbox aria-label={`Select ${r.key}`} checked={picked} onCheckedChange={onPick} disabled={busy} />}
      </span>

      <div role="cell" className="min-w-0">
        <span className="ident block truncate text-[0.8125rem] leading-7 text-ink" title={r.key}>
          {r.key}
        </span>
        {builtIn(r.key) && (
          <span className="block text-xs text-ink-3" title="Built into browser code: anyone can read it, and changing it rebuilds the web apps.">
            In browser code
          </span>
        )}
      </div>

      <div role="cell" className="min-w-0 max-sm:col-start-1 max-sm:row-start-2">
        {r.secret ? (
          <span className="flex h-7 min-w-0 items-center gap-2">
            <span className="shrink-0 rounded-[5px] border border-rule-2 px-1.5 text-[0.6875rem] leading-[1.125rem] font-[550] text-ink-2">Secret</span>
            <span className="truncate text-[0.8125rem] text-ink-3">Encrypted, never shown</span>
          </span>
        ) : (
          <span className="flex h-7 min-w-0 items-center gap-0.5">
            <span className={cn("ident mr-1 min-w-0 truncate text-[0.75rem] leading-5", revealed ? (value ? "text-ink-2" : "text-ink-4") : "tracking-[0.1em] text-ink-3")} title={revealed ? value : undefined}>
              {revealed ? value || "(empty)" : DOTS}
            </span>
            <button
              type="button"
              onClick={onReveal}
              aria-label={revealed ? `Hide ${r.key}` : `Show ${r.key}`}
              aria-pressed={revealed}
              className="grid size-7 shrink-0 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink"
            >
              {revealed ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
            </button>
            <CopyButton value={value} label={`Copy ${r.key}`} />
          </span>
        )}
        {busy && (
          <span className="block text-xs">
            <Working>{r.staged?.removing ? "Removing…" : r.adding ? "Adding…" : "Saving…"}</Working>
          </span>
        )}
        {r.shadowedBy && !busy && <span className="block text-xs text-ink-3">{r.shadowedBy}</span>}
      </div>

      <span role="cell" className="truncate text-[0.8125rem] leading-7 text-ink-2 max-sm:hidden">
        {scope}
      </span>
      <span role="cell" className="min-w-0 pt-1 text-[0.8125rem] leading-5 text-ink-3 max-sm:hidden" title={r.at ? full(r.at) : undefined}>
        {when ? (
          <>
            <span className="block truncate">{when}</span>
            {who && <span className="block truncate text-xs">by {who}</span>}
          </>
        ) : (
          <span className="text-ink-4" aria-label="Not known">
            —
          </span>
        )}
      </span>

      {/* Phones: scope and when, under the value. */}
      <span className="col-start-1 text-xs text-ink-3 sm:hidden">
        {scope}
        {when ? ` · ${when.toLowerCase()}` : ""}
        {when && who ? ` by ${who}` : ""}
      </span>

      <div role="cell" className="flex h-7 items-center justify-end gap-0.5 max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1">
        {writer && !busy && (
          <>
            {r.secret ? (
              <Button variant="ghost" size="sm" onClick={onEdit} aria-label={`Replace the value of ${r.key}`}>
                Replace value
              </Button>
            ) : (
              <Button variant="ghost" size="icon-sm" aria-label={`Edit ${r.key}`} title="Edit" onClick={onEdit}>
                <Pencil />
              </Button>
            )}
            <Button variant="ghost" size="icon-sm" aria-label={`Delete ${r.key}`} title="Delete" onClick={onDelete}>
              <Trash2 />
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
