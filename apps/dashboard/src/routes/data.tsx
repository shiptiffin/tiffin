import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Database, Eye, GitBranch, KeyRound, Play, Plus, RotateCcw, Table2, Timer, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { q as core } from "@/api/queries";
import { mod, mq, type PgBranchCreated, type PgSnapshot, type PgStatement, type PgTable } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { Empty, Page, PageHeader, Skeleton, Tabs, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { RiskMark } from "@/components/risk";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { bytes, ms, num } from "@/lib/format";
import { useMe } from "@/lib/me";
import { expiry, full, relative, within } from "@/lib/time";

// ------------------------------------------------------------------ shared

function useServices(project: string) {
  const p = useQuery(core.project(project));
  const has = (a: string) => (p.data?.resources ?? []).some((r) => r.address === a);
  return { postgres: has("service/postgres"), valkey: has("service/valkey"), loaded: p.isSuccess };
}

export function DataTabs({ project }: { project: string }) {
  const s = useServices(project);
  const items = [
    ...(s.postgres || !s.loaded
      ? [
          { to: "/projects/$project/data", params: { project }, label: "Tables", exact: true },
          { to: "/projects/$project/data/sql", params: { project }, label: "SQL" },
          { to: "/projects/$project/data/branches", params: { project }, label: "Branches" },
        ]
      : []),
    ...(s.valkey ? [{ to: "/projects/$project/data/kv", params: { project }, label: "Key-value" }] : []),
  ];
  return <Tabs items={items} />;
}

export function DataHeader({ project, title, lede, actions }: { project: string; title: ReactNode; lede?: ReactNode; actions?: ReactNode }) {
  return (
    <PageHeader
      eyebrow={
        <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
          {project}
        </Link>
      }
      title={title}
      lede={lede}
      actions={actions}
    >
      <DataTabs project={project} />
    </PageHeader>
  );
}

const ident = (s: string) => `"${s.replace(/"/g, '""')}"`;
const qualified = (t: Pick<PgTable, "schema" | "name">) => (t.schema === "public" ? ident(t.name) : `${ident(t.schema)}.${ident(t.name)}`);
const tableSlug = (t: Pick<PgTable, "schema" | "name">) => (t.schema === "public" ? t.name : `${t.schema}.${t.name}`);

// ------------------------------------------------------------------ overview

export function DataPage({ project }: { project: string }) {
  useTitle(`${project} · Data`);
  const info = useQuery(mq.pg(project));
  const tables = useQuery(mq.tables(project));
  const snaps = useQuery(mq.snapshots(project));
  const s = useServices(project);

  if (info.isError && notOnBox(info.error)) return <NotOnBox what="Databases" />;
  if (s.loaded && !s.postgres)
    return (
      <Page wide>
        <DataHeader project={project} title="Data" />
        <Empty className="mt-10" icon={<Database />} title="This project has no Postgres yet">
          Add <code className="font-mono text-ink">services: {"{ postgres: {} }"}</code> to tiffin.config.ts, then plan and apply.
        </Empty>
      </Page>
    );

  const pg = info.data;
  const list = tables.data ?? [];
  // The app's own tables first; the auth service's tables are managed for it.
  const mine = list.filter((t) => t.schema !== "auth");
  const managed = list.filter((t) => t.schema === "auth");
  const totalRows = mine.reduce((n, t) => n + (t.rowEstimate ?? 0), 0);
  return (
    <Page wide>
      <DataHeader
        project={project}
        title="Data"
        lede={
          pg ? (
            <>
              Postgres {pg.version.split(" ")[0]} · database <code className="font-mono text-ink">{pg.database}</code>
              {(pg.extensions ?? []).length > 0 && <> · {(pg.extensions ?? []).map((e) => e.split("@")[0]).join(", ")}</>}
            </>
          ) : undefined
        }
        actions={
          <Button asChild variant="primary">
            <Link to="/projects/$project/data/sql" params={{ project }}>
              <Play />
              Run SQL
            </Link>
          </Button>
        }
      />

      {info.isError && <ProblemNote className="mt-8" error={info.error} />}
      <dl className="mt-8 grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-4">
        <Stat label="Size" value={pg ? bytes(pg.sizeBytes) : "…"} />
        <Stat label="Rows, about" value={tables.isSuccess ? num(totalRows) : "…"} sub={`in ${mine.length} of your tables and views`} />
        <Stat label="Branches" value={pg ? num(pg.branches) : "…"} sub="copies made in milliseconds" />
        <Stat label="Snapshots" value={pg ? num(pg.snapshots) : "…"} sub="kept for 7 days" />
      </dl>

      <section className="mt-10" aria-labelledby="tables">
        <h2 id="tables" className="display-italic mb-3 text-xl text-ink">
          Tables
        </h2>
        {tables.isPending && <Skeleton className="h-40" />}
        {tables.isError && <ProblemNote error={tables.error} />}
        {tables.isSuccess && list.length === 0 && (
          <Empty icon={<Table2 />} title="No tables yet">
            Your app's migrations create them. Or try <code className="font-mono text-ink">CREATE TABLE</code> in the SQL console with writes on.
          </Empty>
        )}
        {mine.length > 0 && <TableList project={project} list={mine} />}
        {list.length > 0 && mine.length === 0 && (
          <p className="rounded-xl border border-dashed border-rule-strong px-4 py-5 text-base text-ink-3">
            None of your own yet. Your app's migrations create them; sign-in's tables are below.
          </p>
        )}
        {managed.length > 0 && (
          <details className="group/managed mt-4 overflow-hidden rounded-xl border border-rule">
            <summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-3 text-base text-ink-2 transition-colors hover:bg-hover/50 sm:px-5 [&::-webkit-details-marker]:hidden">
              <ChevronRight className="size-4 text-ink-3 transition-transform group-open/managed:rotate-90" />
              <span className="min-w-0 flex-1">
                Sign-in tables{" "}
                <span className="hidden text-ink-3 sm:inline">
                  · managed by Tiffin's auth, in the <code className="font-mono">auth</code> schema
                </span>
              </span>
              <span className="shrink-0 font-mono text-xs text-ink-3 tnum">
                {managed.length} tables
                <span className="hidden sm:inline"> · ~{num(managed.reduce((n, t) => n + (t.rowEstimate ?? 0), 0))} rows</span>
              </span>
            </summary>
            <p className="border-t border-rule px-4 py-2.5 text-sm text-ink-3 sm:px-5">
              Users, sessions and organizations live here. Read them freely; change them through the Users page or the auth API, not by hand.
            </p>
            <TableList project={project} list={managed} flush />
          </details>
        )}
      </section>

      <div className="mt-12 grid gap-10 lg:grid-cols-2">
        <Connect project={project} />
        <Snapshots project={project} list={snaps.data ?? []} />
      </div>
    </Page>
  );
}

function TableList({ project, list, flush }: { project: string; list: PgTable[]; flush?: boolean }) {
  return (
    <ul className={cn("divide-y divide-rule overflow-hidden", flush ? "border-t border-rule" : "rounded-xl border border-rule bg-raised/60")}>
      {list.map((t, k) => (
        <li key={t.schema + t.name} className="animate-rise" style={{ animationDelay: `${Math.min(k, 8) * 30}ms` }}>
          <Link
            to="/projects/$project/data/tables/$table"
            params={{ project, table: tableSlug(t) }}
            className="group grid grid-cols-[1.5rem_minmax(0,1fr)_auto] items-center gap-x-3 px-4 py-3 transition-colors hover:bg-hover/50 sm:grid-cols-[1.5rem_minmax(0,1fr)_7rem_6rem_1rem] sm:px-5"
          >
            <Table2 className={cn("size-4", t.kind === "table" ? "text-ink-3" : "text-ink-4")} />
            <span className="min-w-0">
              <span className="flex items-center gap-2">
                <span className="truncate font-mono text-[0.875rem] text-ink">{tableSlug(t)}</span>
                {t.kind !== "table" && <span className="rounded-full bg-hover px-1.5 py-px text-xs text-ink-3">{t.kind.replace("-", " ")}</span>}
                {t.rls && <span className="rounded-full bg-rev-wash px-1.5 py-px text-xs text-rev">row security</span>}
              </span>
              <span className="mt-0.5 block truncate font-mono text-xs text-ink-3">{(t.columns ?? []).map((c) => c.name).join(" · ")}</span>
            </span>
            <span className="text-right font-mono text-sm text-ink-2 tnum">
              {t.rowEstimate === null || t.kind !== "table" ? "" : t.rowEstimate === 0 ? <span className="text-ink-3">empty</span> : `~${num(t.rowEstimate)}`}
            </span>
            <span className="hidden text-right font-mono text-xs text-ink-3 tnum sm:block">{t.sizeBytes ? bytes(t.sizeBytes) : ""}</span>
            <ChevronRight className="hidden size-4 text-ink-4 transition-transform group-hover:translate-x-0.5 sm:block" />
          </Link>
        </li>
      ))}
    </ul>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="bg-raised px-4 py-4">
      <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</dt>
      <dd className="display mt-1.5 text-2xl text-ink tnum">{value}</dd>
      {sub && <dd className="mt-0.5 text-xs text-ink-3">{sub}</dd>}
    </div>
  );
}

function Connect({ project }: { project: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <section aria-labelledby="connect">
      <h2 id="connect" className="display-italic mb-3 text-xl text-ink">
        Connect
      </h2>
      <div className="rounded-xl border border-rule bg-raised/60 p-5">
        <p className="text-base text-ink-2">
          Apps on the box get <code className="font-mono text-ink">DATABASE_URL</code> automatically. For psql or a GUI on this computer, reveal the
          URL.
        </p>
        {!url && (
          <Button
            size="sm"
            className="mt-4"
            onClick={async () => {
              setErr(null);
              try {
                setUrl((await mod.pgConnection(project)).databaseUrl);
              } catch (e) {
                setErr(e);
              }
            }}
          >
            <Eye />
            Show the connection URL
          </Button>
        )}
        {err ? <ProblemNote className="mt-3" error={err} /> : null}
        {url && <Command className="mt-4" cmd={`psql "${url}"`} />}
      </div>
    </section>
  );
}

function Snapshots({ project, list }: { project: string; list: PgSnapshot[] }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [restoring, setRestoring] = useState<PgSnapshot | null>(null);
  const [done, setDone] = useState<string | null>(null);
  return (
    <section aria-labelledby="snaps">
      <h2 id="snaps" className="display-italic mb-1 text-xl text-ink">
        Snapshots
      </h2>
      <p className="mb-3 text-sm text-ink-3">Taken by themselves before anything risky: SQL writes, dropped extensions, restores.</p>
      {done && <p className="mb-3 rounded-lg bg-rev-wash px-3 py-2 text-sm text-ink">{done}</p>}
      {list.length === 0 ? (
        <p className="rounded-xl border border-dashed border-rule-strong px-4 py-5 text-base text-ink-3">None yet.</p>
      ) : (
        <ul className="max-h-[22rem] divide-y divide-rule overflow-y-auto rounded-xl border border-rule bg-raised/60">
          {list.map((s) => (
            <li key={s.id} className="flex items-center gap-3 px-4 py-2.5">
              <span className="min-w-0 flex-1">
                <span className="block text-base text-ink">{s.reason.charAt(0).toUpperCase() + s.reason.slice(1)}</span>
                <span className="block text-xs text-ink-3" title={full(s.at)}>
                  {relative(s.at)} · {bytes(s.sizeBytes)}
                  {s.branch ? ` · branch ${s.branch}` : ""}
                  {within(s.expiresAt, 86_400_000) ? ` · goes ${expiry(s.expiresAt)}` : ""}
                </span>
              </span>
              {can("apply:irreversible") && (
                <Button variant="ghost" size="sm" onClick={() => setRestoring(s)}>
                  <RotateCcw />
                  Restore
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <HazardDialog<{ overwrites: string; takenAt: string; database: string }, { restored: string; before?: string }>
        open={!!restoring}
        onOpenChange={(o) => !o && setRestoring(null)}
        title="Restore this snapshot?"
        word={project}
        action="Replace the database"
        run={(confirm) => mod.restoreSnapshot(project, restoring!.id, confirm)}
        renderPreview={(p) => (
          <div className="rounded-lg border border-irr-rule bg-irr-wash px-4 py-3 text-base text-ink">
            <p>{p.overwrites.charAt(0).toUpperCase() + p.overwrites.slice(1)}.</p>
            <p className="mt-2 text-sm text-ink-2">Taken {relative(p.takenAt)}. The current contents are snapshotted first, so you can come back.</p>
          </div>
        )}
        onDone={(r) => {
          setDone(`Restored. What was there before is snapshot ${r.before ?? "(none)"}.`);
          qc.invalidateQueries({ queryKey: ["snapshots", project] });
          qc.invalidateQueries({ queryKey: ["tables", project] });
        }}
      />
    </section>
  );
}

// ------------------------------------------------------------------ table viewer

const PAGE = 50;

export function TablePage({ project, table, page = 1 }: { project: string; table: string; page?: number }) {
  useTitle(`${table} · ${project}`);
  const navigate = useNavigate();
  const tables = useQuery(mq.tables(project));
  const t = (tables.data ?? []).find((x) => tableSlug(x) === table);
  const pk = (t?.columns ?? []).filter((c) => c.primary).map((c) => ident(c.name));
  const sql = t ? `SELECT * FROM ${qualified(t)}${pk.length ? ` ORDER BY ${pk.join(", ")}` : ""} LIMIT ${PAGE} OFFSET ${(page - 1) * PAGE}` : "";
  const rows = useQuery({
    queryKey: ["rows", project, table, page],
    queryFn: () => mod.sql(project, { sql, limit: PAGE }),
    enabled: !!t,
    placeholderData: (d) => d,
  });
  const res = rows.data?.results?.[0];
  const total = t?.rowEstimate ?? null;
  const pages = total ? Math.max(1, Math.ceil(total / PAGE)) : null;
  const to = (p: number) => navigate({ to: "/projects/$project/data/tables/$table", params: { project, table }, search: p > 1 ? { page: p } : {} });

  return (
    <Page full>
      <DataHeader
        project={project}
        title={<span className="font-mono text-[0.85em] tracking-tight">{table}</span>}
        lede={
          t ? `${t.kind === "table" ? "Table" : t.kind.replace("-", " ")} · about ${num(t.rowEstimate ?? 0)} rows · ${bytes(t.sizeBytes)}` : undefined
        }
        actions={
          <Button asChild>
            <Link to="/projects/$project/data/sql" params={{ project }} search={{}}>
              <Play />
              Query it
            </Link>
          </Button>
        }
      />
      {tables.isSuccess && !t && <ProblemNote className="mt-8" error={new Error(`There's no table called ${table}.`)} />}
      {t && (
        <div className="mt-6 flex flex-wrap gap-1.5">
          {(t.columns ?? []).map((c) => (
            <span
              key={c.name}
              className="inline-flex items-center gap-1.5 rounded-md border border-rule bg-raised/60 px-2 py-1 font-mono text-xs"
              title={c.default ? `default ${c.default}` : undefined}
            >
              {c.primary && <KeyRound className="size-3 text-brass-ink" aria-label="primary key" />}
              <span className="text-ink">{c.name}</span>
              <span className="text-ink-4">
                {c.type}
                {c.nullable ? "?" : ""}
              </span>
            </span>
          ))}
        </div>
      )}
      <div className="mt-4">
        {rows.isError && <ProblemNote error={rows.error} />}
        {res ? (
          <ResultGrid r={res} columnsTypes={Object.fromEntries((t?.columns ?? []).map((c) => [c.name, c.type]))} />
        ) : (
          <Skeleton className="h-64" />
        )}
      </div>
      <div className="mt-4 flex items-center justify-between text-sm text-ink-3">
        <span className="tnum">
          Rows {(page - 1) * PAGE + 1}–{(page - 1) * PAGE + (res?.rowCount ?? 0)}
          {total !== null ? ` of about ${num(total)}` : ""}
        </span>
        <span className="flex gap-1">
          <Button size="icon-sm" variant="ghost" disabled={page <= 1} onClick={() => to(page - 1)} aria-label="Previous page">
            <ChevronLeft />
          </Button>
          <span className="grid h-7 place-items-center px-2 font-mono text-xs tnum">
            {page}
            {pages ? ` / ${pages}` : ""}
          </span>
          <Button size="icon-sm" variant="ghost" disabled={(res?.rowCount ?? 0) < PAGE} onClick={() => to(page + 1)} aria-label="Next page">
            <ChevronRight />
          </Button>
        </span>
      </div>
    </Page>
  );
}

const NUMERIC = /^(int|numeric|float|real|double|decimal|bigint|smallint|serial|money)/;

/** Rows as a grid. Every value is text: nothing a row contains is ever rendered as HTML. */
export function ResultGrid({ r, columnsTypes }: { r: PgStatement; columnsTypes?: Record<string, string> }) {
  const cols = r.columns ?? [];
  const rows = r.rows ?? [];
  const [open, setOpen] = useState<string | null>(null);
  if (cols.length === 0)
    return (
      <p className="rounded-xl border border-rule bg-raised/60 px-4 py-3 font-mono text-sm text-ink-2">
        {r.command} · {num(r.rowCount)} {r.rowCount === 1 ? "row" : "rows"}
      </p>
    );
  return (
    <Untrusted label="Rows from your database, shown as plain text">
      <div className="max-h-[60vh] overflow-auto">
        <table className="w-full border-separate border-spacing-0 font-mono text-[0.78rem]">
          <thead className="sticky top-0 z-[1] bg-paper-sunk">
            <tr>
              <th className="w-10 border-b border-rule px-3 py-2 text-right font-normal text-ink-4">#</th>
              {cols.map((c) => (
                <th
                  key={c.name}
                  className={cn(
                    "border-b border-rule px-3 py-2 text-left font-normal whitespace-nowrap",
                    NUMERIC.test(columnsTypes?.[c.name] ?? c.type ?? "") && "text-right",
                  )}
                >
                  <span className="text-ink">{c.name}</span> <span className="text-ink-4">{columnsTypes?.[c.name] ?? c.type}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i} className="group">
                <td className="border-b border-rule/60 px-3 py-1.5 text-right text-ink-4 tnum group-hover:bg-hover/50">{i + 1}</td>
                {(row ?? []).map((v, j) => {
                  const id = `${i}:${j}`;
                  const numeric =
                    typeof v === "number" ||
                    (typeof v === "string" &&
                      NUMERIC.test(columnsTypes?.[cols[j]?.name] ?? cols[j]?.type ?? ""));
                  const text = cell(v);
                  const long = text.length > 60;
                  return (
                    <td
                      key={j}
                      className={cn(
                        "max-w-[28rem] border-b border-rule/60 px-3 py-1.5 align-top group-hover:bg-hover/50",
                        numeric && "text-right tnum",
                        v === null ? "text-ink-4 italic" : typeof v === "object" ? "text-out" : "text-ink-2",
                        open === id ? "break-all whitespace-pre-wrap" : "truncate whitespace-nowrap",
                        long && "cursor-pointer",
                      )}
                      title={long && open !== id ? "Click to expand" : undefined}
                      onClick={() => long && setOpen(open === id ? null : id)}
                    >
                      {text}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {r.truncated && <p className="border-t border-rule px-3 py-1.5 text-xs text-ink-3">More rows than shown. Add a LIMIT or narrow the query.</p>}
    </Untrusted>
  );
}

function cell(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

// ------------------------------------------------------------------ SQL console

const HISTORY = "tiffin.sql.history";

function loadHistory(project: string): string[] {
  try {
    return JSON.parse(localStorage.getItem(`${HISTORY}.${project}`) ?? "[]");
  } catch {
    return [];
  }
}

export function SqlPage({ project }: { project: string }) {
  useTitle(`${project} · SQL`);
  const qc = useQueryClient();
  const tables = useQuery(mq.tables(project));
  const branches = useQuery(mq.branches(project));
  const { can } = useMe();
  const [sql, setSql] = useState(() => loadHistory(project)[0] ?? "SELECT now();");
  const [write, setWrite] = useState(false);
  const [branch, setBranch] = useState("");
  const [history, setHistory] = useState(() => loadHistory(project));
  const ta = useRef<HTMLTextAreaElement>(null);
  const run = useMutation({
    mutationFn: () => mod.sql(project, { sql, write, branch: branch || undefined, limit: 500 }),
    onSuccess: (r) => {
      const h = [sql, ...history.filter((x) => x !== sql)].slice(0, 12);
      setHistory(h);
      try {
        localStorage.setItem(`${HISTORY}.${project}`, JSON.stringify(h));
      } catch {
        /* storage blocked */
      }
      if (!r.readOnly) {
        qc.invalidateQueries({ queryKey: ["tables", project] });
        qc.invalidateQueries({ queryKey: ["snapshots", project] });
        qc.invalidateQueries({ queryKey: ["rows", project] });
      }
    },
  });
  const examples = useMemo(() => (tables.data ?? []).slice(0, 4).map((t) => `SELECT * FROM ${qualified(t)} LIMIT 20;`), [tables.data]);
  const writer = can("apply:irreversible");

  return (
    <Page full>
      <DataHeader project={project} title="SQL" lede="Read-only unless you say otherwise. Results are rows your apps wrote, shown as plain text." />
      <div className="mt-8 grid gap-6 xl:grid-cols-[minmax(0,1fr)_16rem]">
        <div className="min-w-0">
          <div className={cn("overflow-hidden rounded-xl border bg-raised transition-colors", write ? "border-irr-rule" : "border-rule")}>
            {write && <div aria-hidden className="h-1 bg-[repeating-linear-gradient(135deg,var(--irr)_0_8px,transparent_8px_14px)] opacity-60" />}
            <textarea
              ref={ta}
              value={sql}
              onChange={(e) => setSql(e.target.value)}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                  e.preventDefault();
                  if (sql.trim()) run.mutate();
                }
              }}
              spellCheck={false}
              aria-label="SQL"
              rows={7}
              className="block min-h-40 w-full resize-y bg-transparent px-4 py-3 font-mono text-[0.8125rem] leading-6 text-ink outline-none"
            />
            <div className="flex flex-wrap items-center gap-3 border-t border-rule bg-paper-sunk/60 px-3 py-2">
              <label className="flex items-center gap-2 text-sm text-ink-2">
                <span className="text-ink-3">on</span>
                <select
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  className="h-7 rounded-md border border-rule bg-paper px-2 font-mono text-xs text-ink outline-none focus-visible:border-brass"
                  aria-label="Database"
                >
                  <option value="">main</option>
                  {(branches.data ?? []).map((b) => (
                    <option key={b.name} value={b.name}>
                      {b.name}
                    </option>
                  ))}
                </select>
              </label>
              <label
                className={cn("flex items-center gap-2 text-sm", writer ? "cursor-pointer text-ink-2" : "text-ink-4")}
                title={writer ? undefined : "Writes need apply:irreversible"}
              >
                <button
                  type="button"
                  role="switch"
                  aria-checked={write}
                  disabled={!writer}
                  onClick={() => setWrite((w) => !w)}
                  className={cn("relative h-5 w-9 rounded-full transition-colors", write ? "bg-irr" : "bg-rule-strong")}
                >
                  <span
                    className={cn("absolute top-0.5 left-0.5 size-4 rounded-full bg-raised shadow transition-transform", write && "translate-x-4")}
                  />
                </button>
                Allow writes
              </label>
              <span className="ml-auto hidden text-xs text-ink-4 sm:inline">
                <kbd className="kbd">⌘</kbd> <kbd className="kbd">↵</kbd> to run
              </span>
              <Button variant={write ? "danger" : "primary"} size="sm" disabled={!sql.trim() || run.isPending} onClick={() => run.mutate()}>
                <Play />
                {run.isPending ? "Running…" : write ? "Run with writes" : "Run"}
              </Button>
            </div>
          </div>
          {write && (
            <p className="mt-2 flex items-start gap-2 text-sm text-ink-2">
              <RiskMark tier="irreversible" className="mt-0.5" />
              Writes can change or delete data. The database is snapshotted first, so you can restore it from Data for 7 days.
            </p>
          )}

          <div className="mt-6 flex flex-col gap-4">
            {run.isError && <ProblemNote error={run.error} />}
            {run.data && (
              <>
                <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-ink-3">
                  <span className="inline-flex items-center gap-1.5">
                    <Timer className="size-3.5" />
                    {ms(run.data.durationMs)}
                  </span>
                  <span>on {run.data.database}</span>
                  <span>{run.data.readOnly ? "read-only" : "with writes"}</span>
                  {run.data.snapshot && (
                    <span className="rounded-full bg-rev-wash px-2 py-px text-xs text-rev">
                      snapshot {run.data.snapshot.slice(0, 14)}… taken first
                    </span>
                  )}
                </p>
                {(run.data.results ?? []).map((r, i) => (
                  <ResultGrid key={i} r={r} />
                ))}
              </>
            )}
            {!run.data && !run.isError && (
              <p className="rounded-xl border border-dashed border-rule-strong px-4 py-8 text-center text-base text-ink-3">Results show up here.</p>
            )}
          </div>
        </div>

        <aside className="flex flex-col gap-6">
          {examples.length > 0 && (
            <section>
              <h2 className="mb-2 text-2xs font-medium tracking-wider text-ink-3 uppercase">Start from</h2>
              <ul className="flex flex-col gap-1">
                {examples.map((x) => (
                  <li key={x}>
                    <button
                      onClick={() => (setSql(x), ta.current?.focus())}
                      className="w-full truncate rounded-md px-2 py-1.5 text-left font-mono text-xs text-ink-2 hover:bg-hover hover:text-ink"
                    >
                      {x}
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
          {history.length > 0 && (
            <section>
              <h2 className="mb-2 text-2xs font-medium tracking-wider text-ink-3 uppercase">Recent</h2>
              <ul className="flex flex-col gap-1">
                {history.map((x) => (
                  <li key={x}>
                    <button
                      onClick={() => (setSql(x), ta.current?.focus())}
                      className="w-full truncate rounded-md px-2 py-1.5 text-left font-mono text-xs text-ink-3 hover:bg-hover hover:text-ink"
                      title={x}
                    >
                      {x}
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </aside>
      </div>
    </Page>
  );
}

// ------------------------------------------------------------------ branches

export function BranchesPage({ project }: { project: string }) {
  useTitle(`${project} · Branches`);
  const qc = useQueryClient();
  const list = useQuery(mq.branches(project));
  const { can } = useMe();
  const [name, setName] = useState("");
  const [from, setFrom] = useState("");
  const [made, setMade] = useState<PgBranchCreated | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => mod.createBranch(project, name.trim(), from || undefined),
    onSuccess: (r) => {
      setMade(r);
      setName("");
      qc.invalidateQueries({ queryKey: ["branches", project] });
      qc.invalidateQueries({ queryKey: ["pg", project] });
    },
  });
  useEffect(() => {
    if (!made) return;
    const t = setTimeout(() => setMade(null), 12_000);
    return () => clearTimeout(t);
  }, [made]);
  const valid = /^[a-z][a-z0-9-]{0,39}$/.test(name.trim());

  return (
    <Page wide>
      <DataHeader
        project={project}
        title="Branches"
        lede="A full copy of the database for a preview, a migration rehearsal or an agent's experiment. The data disk clones files instead of copying them, so it takes milliseconds."
      />
      {can("apply:reversible") && (
        <form
          className="mt-8 flex flex-col gap-3 rounded-xl border border-rule bg-raised/60 p-5 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) create.mutate();
          }}
        >
          <div className="flex flex-1 flex-col gap-1.5">
            <Label htmlFor="b-name">New branch</Label>
            <Input
              id="b-name"
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase())}
              placeholder="try-new-pricing"
              className="font-mono"
              autoComplete="off"
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="b-from">From</Label>
            <select
              id="b-from"
              value={from}
              onChange={(e) => setFrom(e.target.value)}
              className="h-9 rounded-md border border-rule bg-paper px-2.5 font-mono text-sm text-ink outline-none focus-visible:border-brass"
            >
              <option value="">main</option>
              {(list.data ?? []).map((b) => (
                <option key={b.name} value={b.name}>
                  {b.name}
                </option>
              ))}
            </select>
          </div>
          <Button type="submit" variant="primary" disabled={!valid || create.isPending}>
            <GitBranch />
            {create.isPending ? "Cloning…" : "Create branch"}
          </Button>
        </form>
      )}
      {create.isError && <ProblemNote className="mt-3" error={create.error} />}
      {made && (
        <div className="mt-4 flex animate-pop items-center gap-4 rounded-xl border border-rev/40 bg-rev-wash px-5 py-4">
          <span className="display text-3xl text-rev tnum">{ms(made.cloneMs)}</span>
          <p className="text-base text-ink">
            <span className="font-medium">{made.name}</span> is a full copy of {made.from} ({bytes(made.sizeBytes)}).{" "}
            <span className="text-ink-2">
              Writes to {made.from} paused for {ms(made.blockedMs)}; {ms(made.totalMs)} end to end.
            </span>
          </p>
        </div>
      )}
      <ul className="mt-8 divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        <li className="flex items-center gap-3 px-5 py-3.5">
          <Database className="size-4 text-ink-3" />
          <span className="flex-1">
            <span className="font-mono text-[0.875rem] text-ink">main</span>
            <span className="ml-2 text-sm text-ink-3">what your apps use</span>
          </span>
        </li>
        {(list.data ?? []).map((b) => (
          <li key={b.name} className="flex items-center gap-3 px-5 py-3.5">
            <GitBranch className="size-4 text-ink-3" />
            <span className="min-w-0 flex-1">
              <span className="font-mono text-[0.875rem] text-ink">{b.name}</span>
              <span className="mt-0.5 block text-sm text-ink-3">
                from {b.from} · {relative(b.createdAt)} · {bytes(b.sizeBytes)} · <code className="font-mono text-xs">{b.database}</code>
              </span>
            </span>
            <Button asChild variant="ghost" size="sm">
              <Link to="/projects/$project/data/sql" params={{ project }}>
                Query
              </Link>
            </Button>
            {can("apply:irreversible") && (
              <Button variant="ghost" size="icon-sm" aria-label={`Delete ${b.name}`} onClick={() => setDeleting(b.name)} className="hover:text-irr">
                <Trash2 />
              </Button>
            )}
          </li>
        ))}
      </ul>
      {list.isSuccess && (list.data ?? []).length === 0 && (
        <p className="mt-4 text-base text-ink-3">No branches yet. Make one above; it costs almost nothing until it diverges.</p>
      )}
      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete branch ${deleting}?`}
        body="Its database is dropped. Main is not affected."
        action="Delete branch"
        run={() => mod.deleteBranch(project, deleting!)}
        done={() => {
          qc.invalidateQueries({ queryKey: ["branches", project] });
          qc.invalidateQueries({ queryKey: ["pg", project] });
        }}
      />
      <p className="mt-6 flex items-center gap-2 text-sm text-ink-3">
        <Plus className="size-3.5" /> Agents can do the same:{" "}
        <code className="font-mono text-ink-2">tiffin branches create {project} --name try-it</code>
      </p>
    </Page>
  );
}
