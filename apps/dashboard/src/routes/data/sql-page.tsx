import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bookmark, Download, Play, TriangleAlert, X } from "lucide-react";
import { lazy, Suspense, useMemo, useState } from "react";
import { mod, mq, type PgStatement } from "@/api/modules";
import { Rows, Section } from "@/components/data-parts";
import { Select } from "@/components/ui/choice";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch, SwitchThumb } from "@/components/ui/switch";
import { cn } from "@/lib/cn";
import { count, int, ms } from "@/lib/format";
import { loadHistory, saveHistory } from "@/lib/command-history";
import { useMe } from "@/lib/me";
import { db, dq, problemToast } from "./api";
import { NUMERIC, toCSV } from "./format";
import { DataGrid, widthFor, type GridCol } from "./grid";
import type { SqlSchema } from "./sql-editor";

const SqlEditor = lazy(() => import("./sql-editor"));


const strip = (sql: string) =>
  sql
    .replace(/--[^\n]*/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/'(?:[^']|'')*'/g, "''");

/** The first keyword of a statement that would change something (the server refuses it without changes allowed). */
export function writeVerb(sql: string): string | null {
  const m = strip(sql)
    .trim()
    .match(/^(?:with\b[\s\S]*?\)\s*)?(insert|update|delete|drop|alter|create|truncate|grant|revoke|comment|vacuum|reindex|cluster|copy)\b/i);
  return m ? m[1].toUpperCase() : null;
}

/** What in this SQL loses data for good: a DROP, a TRUNCATE, a DELETE or UPDATE with no WHERE. */
export function hazards(sql: string): string[] {
  const out: string[] = [];
  for (const raw of strip(sql).split(";")) {
    const s = raw.trim().replace(/\s+/g, " ");
    if (!s) continue;
    let m: RegExpMatchArray | null;
    if ((m = s.match(/^drop (table|schema|view|materialized view|database|type) (?:if exists )?([^\s(]+)/i))) out.push(`DROP ${m[1].toUpperCase()} removes ${m[2]} and everything in it`);
    else if ((m = s.match(/^truncate (?:table )?([^\s;]+)/i))) out.push(`TRUNCATE empties ${m[1]}`);
    else if ((m = s.match(/^delete from ([^\s;]+)/i)) && !/\bwhere\b/i.test(s)) out.push(`DELETE with no WHERE removes every row of ${m[1]}`);
    else if ((m = s.match(/^update ([^\s;]+) set /i)) && !/\bwhere\b/i.test(s)) out.push(`UPDATE with no WHERE changes every row of ${m[1]}`);
    else if ((m = s.match(/^alter table ([^\s;]+) .*\bdrop (?:column )?(?!constraint|default|not)([^\s,;]+)/i))) out.push(`ALTER TABLE drops ${m[2]} from ${m[1]}, with its values`);
  }
  return out;
}

/** The highest $n the SQL uses, outside strings and comments. */
const paramCount = (sql: string) => Math.max(0, ...[...strip(sql).matchAll(/\$(\d+)/g)].map((m) => Number(m[1])).filter((n) => n <= 32));

/** The SQL tab: a real editor, saved queries, history, and results in the table grid. */
export function SqlPanel({ project, branch, handed }: { project: string; branch: string; handed?: string }) {
  const qc = useQueryClient();
  const tables = useQuery(mq.tables(project, branch || undefined));
  const saved = useQuery(dq.queries(project));
  const { can } = useMe();
  const [sql, setSql] = useState(() => handed ?? loadHistory("sql", project)[0] ?? "SELECT now();");
  const [write, setWrite] = useState(false);
  const [history, setHistory] = useState(() => loadHistory("sql", project));
  const [params, setParams] = useState<string[]>([]);
  const [timeout, setTimeoutS] = useState(30);
  const [openName, setOpenName] = useState<string | null>(null);
  const [naming, setNaming] = useState(false);
  const writer = can("apply:irreversible");
  const nParams = paramCount(sql);

  const schema: SqlSchema = useMemo(() => {
    const ns: Record<string, Record<string, string[]>> = {};
    for (const t of tables.data ?? []) (ns[t.schema] ??= {})[t.name] = (t.columns ?? []).map((c) => c.name);
    return ns;
  }, [tables.data]);

  const run = useMutation({
    mutationFn: (text: string) =>
      (write ? mod.sqlWrite : mod.sql)(project, {
        sql: text,
        branch: branch || undefined,
        limit: 1000,
        timeoutSeconds: timeout,
        ...(nParams ? { params: Array.from({ length: nParams }, (_, i) => paramValue(params[i] ?? "")) } : {}),
      }),
    onSuccess: (r, text) => {
      const h = [text, ...history.filter((x) => x !== text)].slice(0, 20);
      setHistory(h);
      saveHistory("sql", project, h);
      if (!r.readOnly) {
        for (const k of ["tables", "snapshots", "pg-rows", "pg-table", "pg"]) void qc.invalidateQueries({ queryKey: [k, project] });
      }
    },
  });
  const go = () => sql.trim() && !run.isPending && run.mutate(sql);
  const verb = writeVerb(sql);
  const danger = hazards(sql);

  const save = useMutation({
    mutationFn: (name: string) => db.saveQuery(project, name, sql),
    onSuccess: (q) => {
      setOpenName(q.name);
      setNaming(false);
      void qc.invalidateQueries({ queryKey: ["pg-queries", project] });
      toast({ title: `Saved “${q.name}”` });
    },
  });
  const forget = async (name: string, text: string) => {
    try {
      await db.deleteQuery(project, name);
      void qc.invalidateQueries({ queryKey: ["pg-queries", project] });
      if (openName === name) setOpenName(null);
      toast({
        title: `Deleted “${name}”`,
        action: { label: "Undo", run: () => db.saveQuery(project, name, text).then(() => qc.invalidateQueries({ queryKey: ["pg-queries", project] })) },
      });
    } catch (e) {
      problemToast(e);
    }
  };

  const examples = (tables.data ?? [])
    .filter((t) => !t.managed)
    .slice(0, 4)
    .map((t) => `SELECT * FROM ${t.schema === "public" ? t.name : `${t.schema}.${t.name}`} LIMIT 20;`);

  return (
    <div className="grid gap-x-10 gap-y-8 xl:grid-cols-[minmax(0,1fr)_17rem]">
      <div className="min-w-0">
        <div className={cn("overflow-hidden rounded-[10px] border bg-paper-raised transition-colors duration-[var(--dur-state)]", write ? "border-danger-rule" : "border-rule-2")}>
          {openName && (
            <div className="flex items-center gap-2 border-b border-rule px-3.5 py-1.5 text-sm text-ink-3">
              <Bookmark className="size-3.5" aria-hidden />
              <span className="text-ink-2">{openName}</span>
              <button type="button" className="ml-auto text-xs hover:text-ink" onClick={() => setOpenName(null)}>
                Close
              </button>
            </div>
          )}
          <div className="h-[13rem] min-h-[8rem] resize-y overflow-hidden sm:h-[15rem]">
            <Suspense fallback={<Skeleton className="m-3 h-[calc(100%-1.5rem)] opacity-50" />}>
              <SqlEditor value={sql} onChange={setSql} onRun={go} schema={schema} wrap={typeof window !== "undefined" && window.innerWidth < 640} />
            </Suspense>
          </div>
          {nParams > 0 && (
            <div className="flex flex-wrap gap-2 border-t border-rule px-3.5 py-2.5">
              {Array.from({ length: nParams }, (_, i) => (
                <label key={i} className="flex items-center gap-1.5 font-mono text-xs text-ink-3">
                  ${i + 1}
                  <Input
                    value={params[i] ?? ""}
                    onChange={(e) => setParams((p) => Object.assign([...p], { [i]: e.target.value }))}
                    placeholder="null"
                    className="h-7 w-32 font-mono text-[0.78125rem]"
                    aria-label={`Value for $${i + 1}`}
                  />
                </label>
              ))}
            </div>
          )}
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-rule bg-paper-sunk/60 px-3 py-2">
            <label className={cn("flex items-center gap-2 text-sm", writer ? "cursor-pointer text-ink-2" : "text-ink-3")} title={writer ? undefined : "Changes need a key with full access"}>
              <Switch
                checked={write}
                onCheckedChange={setWrite}
                disabled={!writer}
                className="relative h-[18px] w-8 rounded-full border border-rule-3 bg-paper-press transition-colors duration-[var(--dur-state)] data-[state=checked]:border-danger data-[state=checked]:bg-danger"
              >
                <SwitchThumb className="absolute top-[1px] left-[1px] size-3.5 rounded-full bg-paper-raised shadow-[0_1px_1px_oklch(0.2_0.01_60/0.25)] transition-transform duration-[var(--dur-state)] ease-[var(--ease-out)] data-[state=checked]:translate-x-3.5" />
              </Switch>
              Allow changes
            </label>
            <div className="flex items-center gap-1.5 text-sm text-ink-3">
              Stop after
              <Select
                size="sm"
                value={String(timeout)}
                onValueChange={(v) => setTimeoutS(Number(v))}
                aria-label="Time limit"
                className="w-24"
                options={[10, 30, 60, 300].map((s) => ({ value: String(s), label: s < 60 ? `${s} s` : `${s / 60} min` }))}
              />
            </div>
            <Button size="sm" variant="ghost" onClick={() => (openName && !naming ? save.mutate(openName) : setNaming(true))} disabled={!sql.trim() || save.isPending} title={openName ? `Save over “${openName}”` : "Save this query"}>
              <Bookmark />
              {openName ? "Save" : "Save…"}
            </Button>
            <span className="ml-auto hidden text-xs text-ink-3 sm:inline">
              <kbd className="kbd">⌘</kbd> <kbd className="kbd">↵</kbd> runs
            </span>
            <Button variant={write ? "danger" : "primary"} size="sm" disabled={!sql.trim() || run.isPending} onClick={go} className="max-sm:ml-auto">
              <Play />
              {run.isPending ? "Running…" : write ? "Run with changes" : "Run"}
            </Button>
          </div>
        </div>
        {write && danger.length > 0 ? (
          <div role="alert" className="mt-2.5 flex gap-2.5 rounded-md border border-danger-rule bg-danger-wash px-3 py-2 text-sm text-ink">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
            <div>
              {danger.map((d) => (
                <p key={d}>{d}.</p>
              ))}
              <p className="mt-0.5 text-ink-2">A restore point is taken first; bring it back under Restore points for 7 days.</p>
            </div>
          </div>
        ) : write ? (
          <p className="mt-2.5 text-sm text-ink-2">Changes can add, change or delete data. A restore point of the database is taken first, so you can go back for 7 days.</p>
        ) : (
          verb && (
            <p className="mt-2.5 text-sm text-ink-3">
              <span className="font-mono text-ink-2">{verb}</span> changes data, so it only runs with Allow changes on{writer ? "" : ", which your key can't do"}.
              {danger.length > 0 && <span className="text-danger"> {danger[0]}.</span>}
            </p>
          )
        )}

        <div className="mt-7 flex flex-col gap-5">
          {run.isError && <ProblemNote error={run.error} />}
          {run.data && (
            <>
              <p className="text-sm text-ink-3 tnum" aria-live="polite">
                {(run.data.results ?? []).length === 1 && (run.data.results?.[0].columns ?? []).length > 0 && (
                  <span className="text-ink-2">{count(run.data.results![0].rowCount, "row")} </span>
                )}
                in {ms(run.data.durationMs)}, {run.data.readOnly ? "read only" : "with changes"}
                {branch ? (
                  <>
                    , on the copy <span className="font-mono text-ink-2">{branch}</span>
                  </>
                ) : (
                  ""
                )}
                .
                {run.data.snapshot && <span className="text-ink-2"> A restore point was taken first.</span>}
              </p>
              {(run.data.results ?? []).map((r, i) => (
                <ResultTable key={i} r={r} />
              ))}
            </>
          )}
          {!run.data && !run.isError && (
            <p className="border-y border-dashed border-rule-3 py-8 text-center text-base text-ink-3">Results show up here, with the row count and how long it took.</p>
          )}
        </div>
      </div>

      <aside className="flex min-w-0 flex-col gap-8">
        <Section id="saved" label="Saved">
          {(saved.data ?? []).length === 0 ? (
            <p className="border-y border-rule py-3 text-sm text-ink-3">{saved.isPending ? "…" : "Queries you save show up here, for everyone on the project."}</p>
          ) : (
            <Rows>
              {(saved.data ?? []).map((q) => (
                <li key={q.name} className="group flex items-center gap-1">
                  <button
                    type="button"
                    onClick={() => {
                      setSql(q.sql);
                      setOpenName(q.name);
                    }}
                    title={q.sql}
                    className={cn("min-w-0 flex-1 truncate py-2 text-left text-base transition-colors hover:text-ink", openName === q.name ? "text-ink" : "text-ink-2")}
                  >
                    {q.name}
                  </button>
                  <button
                    type="button"
                    aria-label={`Delete the saved query ${q.name}`}
                    onClick={() => void forget(q.name, q.sql)}
                    className="grid size-6 shrink-0 place-items-center rounded-[5px] text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 hover:bg-paper-hover hover:text-ink focus-visible:opacity-100 max-sm:opacity-100"
                  >
                    <X className="size-3.5" />
                  </button>
                </li>
              ))}
            </Rows>
          )}
        </Section>
        {history.length > 0 && (
          <Section id="recent" label="Recent" aside="on this computer">
            <QueryList
              items={history}
              onPick={(x) => {
                setSql(x);
                setOpenName(null);
              }}
              quiet
            />
          </Section>
        )}
        {examples.length > 0 && (
          <Section id="start" label="Start from">
            <QueryList
              items={examples}
              onPick={(x) => {
                setSql(x);
                setOpenName(null);
              }}
            />
          </Section>
        )}
      </aside>

      <SaveDialog open={naming} initial={openName ?? ""} busy={save.isPending} error={save.error} onClose={() => setNaming(false)} onSave={(n) => save.mutate(n)} />
    </div>
  );
}

/** A param as typed: numbers and true/false stay text too (Postgres casts), an empty box is NULL. */
const paramValue = (s: string) => (s === "" ? null : s);

/** One statement's result: rows in the grid (read-only), or what it changed. */
export function ResultTable({ r }: { r: PgStatement }) {
  const cols: GridCol[] = useMemo(
    () =>
      (r.columns ?? []).map((c, i) => {
        const numeric = NUMERIC.test(c.type);
        const category = numeric ? "number" : c.type === "bool" ? "bool" : c.type.startsWith("json") ? "json" : c.type === "timestamptz" || c.type === "timestamp" ? "timestamp" : "text";
        return { name: c.name, index: i, type: c.type, category, numeric, editable: false, width: widthFor(category, c.name, c.type) };
      }),
    [r.columns],
  );
  const rows = useMemo(() => (r.rows ?? []).map((x) => x ?? []), [r.rows]);
  if (cols.length === 0)
    return (
      <p className="border-y border-rule py-3 text-base text-ink-2">
        <span className="font-mono text-ink">{r.command.split(" ")[0]}</span>
        <span className="text-ink-3"> · {count(r.rowCount, "row")} affected</span>
      </p>
    );
  return (
    <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised">
      <DataGrid cols={cols} rows={rows} rowKey={(_, i) => String(i)} label="Query results" height={Math.min(rows.length * 32 + 46, 520)} />
      <div className="flex items-center gap-3 border-t border-rule px-3 py-1">
        <p className="min-w-0 flex-1 text-xs text-ink-3">
          {r.truncated ? `More than ${int(rows.length)} rows matched; only these came back. Add a LIMIT or narrow the query.` : count(rows.length, "row")}
        </p>
        <Button
          size="sm"
          variant="ghost"
          className="h-6 text-xs"
          disabled={rows.length === 0}
          onClick={() => {
            const url = URL.createObjectURL(new Blob([toCSV(cols.map((c) => c.name), rows)], { type: "text/csv;charset=utf-8" }));
            const a = document.createElement("a");
            a.href = url;
            a.download = "query.csv";
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 10_000);
          }}
        >
          <Download />
          Download CSV
        </Button>
      </div>
    </div>
  );
}

function QueryList({ items, onPick, quiet }: { items: string[]; onPick: (s: string) => void; quiet?: boolean }) {
  return (
    <Rows>
      {items.map((x) => (
        <li key={x}>
          <button
            type="button"
            onClick={() => onPick(x)}
            title={x}
            className={cn("block w-full truncate py-2 text-left font-mono text-[0.75rem] transition-colors duration-[var(--dur-state)] hover:text-ink", quiet ? "text-ink-3" : "text-ink-2")}
          >
            {x.replace(/\s+/g, " ")}
          </button>
        </li>
      ))}
    </Rows>
  );
}

function SaveDialog({ open, initial, busy, error, onClose, onSave }: { open: boolean; initial: string; busy: boolean; error: unknown; onClose: () => void; onSave: (n: string) => void }) {
  const [name, setName] = useState(initial);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-sm">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) onSave(name.trim());
          }}
        >
          <DialogHeader>
            <DialogTitle>Save this query</DialogTitle>
            <DialogDescription>Saved on the box for everyone on the project, and for agents (db queries).</DialogDescription>
          </DialogHeader>
          <div className="px-6 pb-5">
            <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Best sellers this month" aria-label="Name" maxLength={80} />
            {error ? <ProblemNote className="mt-3" error={error} /> : null}
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!name.trim() || busy}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
