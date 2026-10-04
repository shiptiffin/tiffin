import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Eye, GitBranch, KeyRound, Play, RotateCcw, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { q as core } from "@/api/queries";
import { mod, mq, type PgBranchCreated, type PgSnapshot, type PgStatement, type PgTable } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Command } from "@/components/copy";
import { MiniSelect, PartsBar, Reading, Readings, Rows, Section, TypeWord, jsonLine } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { Crumbs, Empty, Page, PageHeader, Skeleton, Tabs, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { RiskDots } from "@/components/risk-dots";
import { SegMeter } from "@/components/seg-meter";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, countWords, int, ms, NNBSP, num, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayKey, full, relative } from "@/lib/time";

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
  ];
  return <Tabs items={items} />;
}

/** "shop › Data", the title, one line, the Data tabs. */
export function DataHeader({
  project,
  title,
  lede,
  actions,
  sub,
  tabs = true,
}: {
  project: string;
  title: ReactNode;
  lede?: ReactNode;
  actions?: ReactNode;
  sub?: string;
  /** The Database tabs (Tables, SQL, Branches); the Cache page has none. */
  tabs?: boolean;
}) {
  return (
    <PageHeader
      eyebrow={
        <Crumbs
          items={[
            { label: project, to: "/projects/$project", params: { project }, mono: true },
            ...(sub ? [{ label: sub, to: "/projects/$project/data", params: { project } }] : []),
          ]}
        />
      }
      title={title}
      lede={lede}
      actions={actions}
    >
      {tabs && <DataTabs project={project} />}
    </PageHeader>
  );
}

const ident = (s: string) => `"${s.replace(/"/g, '""')}"`;
const qualified = (t: Pick<PgTable, "schema" | "name">) => (t.schema === "public" ? ident(t.name) : `${ident(t.schema)}.${ident(t.name)}`);
const tableSlug = (t: Pick<PgTable, "schema" | "name">) => (t.schema === "public" ? t.name : `${t.schema}.${t.name}`);
const kindWord = (k: PgTable["kind"]) => (k === "table" ? "" : k === "materialized-view" ? "materialized view" : k);

/** Each project's database role may open this many connections (internal/mod/postgres/reconcile.go). */
const ROLE_CONNECTIONS = 80;

// ------------------------------------------------------------------ overview

export function DataPage({ project }: { project: string }) {
  useTitle(`${project} · Database`);
  const info = useQuery(mq.pg(project));
  const tables = useQuery(mq.tables(project));
  const snaps = useQuery(mq.snapshots(project));
  const s = useServices(project);

  if (info.isError && notOnBox(info.error)) return <NotOnBox what="Databases" />;
  if (s.loaded && !s.postgres)
    return (
      <Page wide>
        <DataHeader project={project} title="Database" />
        <Empty className="mt-10" title="This project has no Postgres yet">
          Add <code className="font-mono text-ink">services: {"{ postgres: {} }"}</code> to tiffin.config.ts, then plan and apply.
        </Empty>
      </Page>
    );

  const pg = info.data;
  const list = tables.data ?? [];
  // The app's own tables first; the sign-in service's tables are managed for it.
  const mine = list.filter((t) => t.schema !== "auth");
  const managed = list.filter((t) => t.schema === "auth");
  const sum = (ts: PgTable[], f: (t: PgTable) => number) => ts.reduce((n, t) => n + f(t), 0);
  const totalRows = sum(
    mine.filter((t) => t.kind === "table"),
    (t) => t.rowEstimate ?? 0,
  );
  const mineBytes = sum(mine, (t) => t.sizeBytes);
  const authBytes = sum(managed, (t) => t.sizeBytes);
  const nTables = mine.filter((t) => t.kind === "table").length;
  const nViews = mine.length - nTables;
  const newest = (snaps.data ?? [])[0];

  return (
    <Page wide>
      <DataHeader
        project={project}
        title="Database"
        lede={
          pg ? (
            <>
              Postgres {pg.version.split(" ")[0]}, database <code className="ident text-ink">{pg.database}</code>
              {(pg.extensions ?? []).length > 0 && <> with {(pg.extensions ?? []).map((e) => e.split("@")[0]).join(", ")}</>}. Apps get{" "}
              <code className="ident text-ink">DATABASE_URL</code> on their own.
            </>
          ) : undefined
        }
        actions={
          <Button asChild variant="secondary">
            <Link to="/projects/$project/data/sql" params={{ project }}>
              <Play />
              Run SQL
            </Link>
          </Button>
        }
      />

      {info.isError && <ProblemNote className="mt-8" error={info.error} />}
      <Readings className="grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr_1fr]">
        <Reading
          className="col-span-2 lg:col-span-1"
          label="Size"
          value={pg ? bytesParts(pg.sizeBytes).value : "–"}
          unit={pg ? bytesParts(pg.sizeBytes).unit : undefined}
        >
          {pg && tables.isSuccess && (
            <PartsBar
              className="mt-2.5"
              total={pg.sizeBytes}
              label={`Database size: your tables ${bytes(mineBytes)}, sign-in ${bytes(authBytes)}, Postgres's own catalog the rest`}
              parts={[
                { key: "mine", value: mineBytes, tone: "var(--ink-2)", name: `your tables ${bytes(mineBytes)}` },
                { key: "auth", value: authBytes, tone: "var(--part-3)", name: `sign-in ${bytes(authBytes)}` },
                { key: "pg", value: Math.max(0, pg.sizeBytes - mineBytes - authBytes), tone: "var(--part-4)", name: "Postgres's own catalog" },
              ]}
            />
          )}
        </Reading>
        <Reading label="Connections" value={pg ? int(pg.connections) : "–"} unit={`of ${ROLE_CONNECTIONS}`}>
          <SegMeter
            className="mt-2.5"
            label="Connections open"
            value={pg?.connections ?? 0}
            max={ROLE_CONNECTIONS}
            warnAt={0.75}
            fullAt={0.95}
            scale={["0", "40", "80"]}
            valueText={`${pg?.connections ?? 0} of ${ROLE_CONNECTIONS}`}
          />
        </Reading>
        <Reading
          label="Rows, about"
          value={tables.isSuccess ? num(totalRows) : "–"}
          sub={tables.isSuccess ? `in ${countWords(nTables, "table")}${nViews ? ` and ${countWords(nViews, "view")}` : ""}` : undefined}
        />
        <Reading
          label="Branches"
          value={pg ? int(pg.branches) : "–"}
          unit={pg ? (pg.branches === 1 ? "branch" : "branches") : undefined}
          sub={
            pg ? (
              <Link to="/projects/$project/data/branches" params={{ project }} className="hover:text-ink">
                {count(pg.snapshots, "snapshot")}
                {newest ? `, newest ${relative(newest.at)}` : ""}
              </Link>
            ) : undefined
          }
        />
      </Readings>

      <Section className="mt-10" id="tables" label="Your tables" aside={mine.length > 0 ? "row counts are Postgres's estimate" : undefined}>
        {tables.isPending && <Skeleton className="h-40" />}
        {tables.isError && <ProblemNote error={tables.error} />}
        {tables.isSuccess && list.length === 0 && (
          <Empty title="No tables yet">
            Your app's migrations create them. Or try <code className="font-mono text-ink">CREATE TABLE</code> in the SQL console with writes on.
          </Empty>
        )}
        {mine.length > 0 && <TableList project={project} list={mine} />}
        {list.length > 0 && mine.length === 0 && (
          <p className="border-y border-rule py-4 text-base text-ink-3">
            None of your own yet. Your app's migrations create them; sign-in's tables are below.
          </p>
        )}
      </Section>

      {managed.length > 0 && (
        <details className="group/managed mt-8">
          <summary className="flex cursor-pointer list-none items-center gap-2 py-1 [&::-webkit-details-marker]:hidden">
            <ChevronRight className="size-3.5 text-ink-3 transition-transform duration-[var(--dur-state)] group-open/managed:rotate-90" />
            <span className="label">Auth (managed by Tiffin)</span>
            <span className="ml-auto text-sm text-ink-3 tnum">
              {count(managed.length, "table")}, about {num(sum(managed, (t) => t.rowEstimate ?? 0))} rows
            </span>
          </summary>
          <p className="mt-1.5 mb-2.5 max-w-[44rem] text-sm text-ink-3">
            People, sessions and organizations, in the <code className="ident">auth</code> schema. Read them freely; change them on the Users page or
            through the sign-in API, not by hand.
          </p>
          <TableList project={project} list={managed} />
        </details>
      )}

      <Connect project={project} className="mt-12" />
    </Page>
  );
}

function TableList({ project, list }: { project: string; list: PgTable[] }) {
  return (
    <Rows>
      <li aria-hidden className="hidden grid-cols-[13rem_minmax(0,1fr)_6.5rem_5rem_1rem] gap-x-6 py-2 sm:grid">
        <span className="label">Name</span>
        <span className="label">Columns</span>
        <span className="label text-right">Rows</span>
        <span className="label text-right">On disk</span>
        <span />
      </li>
      {list.map((t) => (
        <li key={t.schema + t.name}>
          <Link
            to="/projects/$project/data/tables/$table"
            params={{ project, table: tableSlug(t) }}
            className="group grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-6 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk sm:-mx-3 sm:grid-cols-[13rem_minmax(0,1fr)_6.5rem_5rem_1rem] sm:px-3"
          >
            <span className="flex min-w-0 items-baseline gap-2">
              <span className="truncate font-mono text-[0.84375rem] text-ink">{tableSlug(t)}</span>
              {t.kind !== "table" && <TypeWord>{kindWord(t.kind)}</TypeWord>}
            </span>
            <span
              className="col-start-1 row-start-2 truncate text-xs text-ink-3 sm:col-start-2 sm:row-start-1 sm:text-sm"
              title={(t.columns ?? []).map((c) => `${c.name} ${c.type}`).join(", ")}
            >
              {(t.columns ?? []).map((c) => c.name).join(", ")}
              {t.rls && <span className="text-ink-2"> · row security on</span>}
            </span>
            <span className="text-right text-sm text-ink-2 tnum sm:text-base">
              {t.kind !== "table" || t.rowEstimate === null ? (
                ""
              ) : t.rowEstimate === 0 ? (
                <span className="text-ink-3">empty</span>
              ) : (
                num(t.rowEstimate)
              )}
            </span>
            <span className="hidden text-right text-sm text-ink-3 tnum sm:block">{t.sizeBytes ? bytes(t.sizeBytes) : ""}</span>
            <ChevronRight className="hidden size-4 self-center text-ink-4 transition-transform group-hover:translate-x-0.5 group-hover:text-ink-2 sm:block" />
          </Link>
        </li>
      ))}
    </Rows>
  );
}

function Connect({ project, className }: { project: string; className?: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <Section id="connect" label="Connect from this computer" className={className}>
      <div className="flex flex-col gap-3 border-t border-rule pt-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-base text-ink-2">
          For psql or a database app, reveal the connection URL. It carries the password, so mind who's watching.
        </p>
        {!url && (
          <Button
            size="sm"
            className="self-start sm:self-auto"
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
            Show the URL
          </Button>
        )}
      </div>
      {err ? <ProblemNote className="mt-3" error={err} /> : null}
      {url && <Command className="mt-3" cmd={`psql "${url}"`} />}
    </Section>
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
  const last = (res?.rowCount ?? 0) < PAGE;
  const to = (p: number) => navigate({ to: "/projects/$project/data/tables/$table", params: { project, table }, search: p > 1 ? { page: p } : {} });
  const types = useMemo(() => Object.fromEntries((t?.columns ?? []).map((c) => [c.name, c.type])), [t]);
  const keys = useMemo(() => new Set((t?.columns ?? []).filter((c) => c.primary).map((c) => c.name)), [t]);

  // [ and ] page through, like a pager.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || (e.target as HTMLElement)?.closest?.("input,textarea,select,[contenteditable]")) return;
      if (e.key === "[" && page > 1) to(page - 1);
      if (e.key === "]" && !last) to(page + 1);
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  });

  const from = (page - 1) * PAGE;
  return (
    <Page full>
      <DataHeader
        project={project}
        sub="Database"
        title={table}
        lede={
          t
            ? `${t.kind === "table" ? "Table" : kindWord(t.kind).replace(/^./, (c) => c.toUpperCase())}${
                t.kind === "table" ? `, about ${num(t.rowEstimate ?? 0)} rows` : ""
              }, ${bytes(t.sizeBytes)} on disk. ${countWords((t.columns ?? []).length, "column", "columns", true)}${
                pk.length
                  ? `, keyed by ${(t.columns ?? [])
                      .filter((c) => c.primary)
                      .map((c) => c.name)
                      .join(" and ")}`
                  : ""
              }.`
            : undefined
        }
        actions={
          <Button asChild>
            <Link
              to="/projects/$project/data/sql"
              params={{ project }}
              search={(t ? { sql: `${sql.replace(/ OFFSET \d+$/, "").replace(/ LIMIT \d+$/, "")} LIMIT 100;` } : {}) as never}
            >
              <Play />
              Query it
            </Link>
          </Button>
        }
      />
      {tables.isSuccess && !t && <ProblemNote className="mt-8" error={new Error(`There's no table called ${table}.`)} />}
      <div className="mt-6">
        {rows.isError && <ProblemNote error={rows.error} />}
        {res ? (
          <ResultGrid r={res} columnsTypes={types} keys={keys} offset={from} tall className={cn(rows.isPlaceholderData && "opacity-60")} />
        ) : (
          t && <Skeleton className="h-[min(32rem,calc(100dvh-20rem))]" />
        )}
      </div>
      {t && (
        <div className="mt-3 flex h-8 items-center justify-between gap-3 text-sm text-ink-3">
          <span className="tnum">
            {res && res.rowCount > 0 ? (
              <>
                Rows {int(from + 1)}–{int(from + res.rowCount)}
                {total !== null && t.kind === "table" ? (
                  <span className="max-sm:hidden"> of about {num(Math.max(total, from + res.rowCount))}</span>
                ) : (
                  ""
                )}
              </>
            ) : res ? (
              "No rows here."
            ) : (
              ""
            )}
          </span>
          <span className="flex items-center gap-1">
            <span className="mr-2 hidden text-xs text-ink-4 lg:inline">
              <kbd className="kbd">[</kbd> <kbd className="kbd">]</kbd> to page
            </span>
            <Button size="icon-sm" variant="ghost" disabled={page <= 1} onClick={() => to(page - 1)} aria-label="Previous page">
              <ChevronLeft />
            </Button>
            <span className="min-w-[4.5rem] text-center text-sm text-ink-2 tnum">
              {int(page)}
              {pages ? <span className="text-ink-3"> of {int(Math.max(pages, page))}</span> : ""}
            </span>
            <Button size="icon-sm" variant="ghost" disabled={last} onClick={() => to(page + 1)} aria-label="Next page">
              <ChevronRight />
            </Button>
          </span>
        </div>
      )}
    </Page>
  );
}

const NUMERIC = /^(int|numeric|float|real|double|decimal|bigint|smallint|serial|money|oid)/;

/**
 * Rows as a grid. Every value is text: nothing a row contains is ever
 * rendered as HTML. Numbers right-aligned in tabular figures (raw digits, so
 * what you copy is what's stored); JSON on one readable line, expanded on
 * click; the header stays put while you scroll.
 */
export function ResultGrid({
  r,
  columnsTypes,
  keys,
  offset = 0,
  tall,
  className,
}: {
  r: PgStatement;
  columnsTypes?: Record<string, string>;
  keys?: Set<string>;
  offset?: number;
  tall?: boolean;
  className?: string;
}) {
  const cols = r.columns ?? [];
  const rows = r.rows ?? [];
  const [open, setOpen] = useState<string | null>(null);
  if (cols.length === 0)
    return (
      <p className="border-y border-rule py-3 text-base text-ink-2">
        <span className="font-mono text-ink">{r.command.split(" ")[0]}</span>
        <span className="text-ink-3"> · {count(r.rowCount, "row")} affected</span>
      </p>
    );
  const typeOf = (j: number) => columnsTypes?.[cols[j]?.name] ?? cols[j]?.type ?? "";
  return (
    <Untrusted label="Rows from your database, shown as plain text" className={className}>
      <div className={cn("overflow-auto overscroll-contain", tall ? "max-h-[max(20rem,calc(100dvh-19rem))]" : "max-h-[60vh]")}>
        <table className="w-full border-separate border-spacing-0 font-mono text-[0.78125rem] leading-[1.1875rem]">
          <thead className="sticky top-0 z-[1]">
            <tr>
              <th className="w-10 border-b border-rule-2 bg-paper-sunk px-3 py-1.5 text-right align-bottom font-normal text-ink-4">#</th>
              {cols.map((c, j) => {
                const numeric = NUMERIC.test(typeOf(j));
                return (
                  <th
                    key={c.name + j}
                    scope="col"
                    className={cn(
                      "border-b border-rule-2 bg-paper-sunk px-3 py-1.5 align-bottom font-normal whitespace-nowrap",
                      numeric ? "text-right" : "text-left",
                    )}
                  >
                    <span className={cn("flex items-center gap-1 text-ink", numeric && "justify-end")}>
                      {keys?.has(c.name) && <KeyRound className="size-3 text-brass-ink" aria-label="primary key" />}
                      {c.name}
                    </span>
                    <span className="block text-[0.6875rem] text-ink-4">{typeOf(j)}</span>
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i} className="group">
                <td className="border-b border-rule px-3 py-1.5 text-right align-top text-ink-4 tnum group-hover:bg-paper-press/60">
                  {int(offset + i + 1)}
                </td>
                {(row ?? []).map((v, j) => {
                  const id = `${i}:${j}`;
                  const numeric = typeof v === "number" || (typeof v === "string" && NUMERIC.test(typeOf(j)));
                  const isJson = v !== null && typeof v === "object";
                  const expanded = open === id;
                  const text = cell(v, expanded);
                  const long = text.length > 48 || isJson;
                  return (
                    <td
                      key={j}
                      className={cn(
                        "max-w-[26rem] border-b border-rule px-3 py-1.5 align-top group-hover:bg-paper-press/60",
                        numeric && "text-right tnum",
                        v === null ? "text-ink-4" : isJson ? "text-ink-2" : "text-ink",
                        expanded ? "whitespace-pre-wrap [overflow-wrap:anywhere]" : "truncate whitespace-nowrap",
                        long && "cursor-pointer",
                      )}
                      title={long && !expanded ? "Click to see all of it" : typeof v === "string" && TS.test(v) ? v : undefined}
                      onClick={() => long && setOpen(expanded ? null : id)}
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

const TS = /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(\.\d+)?([+-]\d{2}(?::?\d{2})?|Z)$/;
const tsFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
function cell(v: unknown, expanded = false): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "object") return expanded ? JSON.stringify(v, null, 2) : jsonLine(v);
  // A timestamptz reads in the viewer's clock, to the second; expanding shows Postgres's own text.
  if (!expanded && typeof v === "string" && TS.test(v)) {
    const m = v.match(TS)!;
    const d = new Date(`${m[1]}T${m[2]}${m[3] ?? ""}${m[4] === "Z" ? "Z" : m[4].length === 3 ? `${m[4]}:00` : m[4].replace(/^([+-]\d{2})(\d{2})$/, "$1:$2")}`);
    if (!Number.isNaN(d.getTime())) return tsFmt.format(d);
  }
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

/** The first keyword of a statement that would change something (the server refuses it without writes on). */
function writeVerb(sql: string): string | null {
  const body = sql
    .replace(/--[^\n]*/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .trim();
  const m = body.match(
    /^(?:with\b[\s\S]*?\)\s*)?(insert|update|delete|drop|alter|create|truncate|grant|revoke|comment|vacuum|reindex|cluster|copy)\b/i,
  );
  return m ? m[1].toUpperCase() : null;
}

export function SqlPage({ project }: { project: string }) {
  useTitle(`${project} · SQL`);
  const qc = useQueryClient();
  const tables = useQuery(mq.tables(project));
  const branches = useQuery(mq.branches(project));
  const { can } = useMe();
  // Other pages hand over a query or a branch in the URL (?sql=…, ?branch=…).
  const [handed] = useState(() => new URLSearchParams(location.search));
  const [sql, setSql] = useState(() => handed.get("sql") ?? loadHistory(project)[0] ?? "SELECT now();");
  const [write, setWrite] = useState(false);
  const [branch, setBranch] = useState(() => handed.get("branch") ?? "");
  const [history, setHistory] = useState(() => loadHistory(project));
  const ta = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const run = useMutation({
    mutationFn: (text: string) => (write ? mod.sqlWrite : mod.sql)(project, { sql: text, branch: branch || undefined, limit: 500 }),
    onSuccess: (r, text) => {
      const h = [text, ...history.filter((x) => x !== text)].slice(0, 12);
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
        qc.invalidateQueries({ queryKey: ["pg", project] });
      }
    },
  });
  const examples = useMemo(
    () =>
      (tables.data ?? [])
        .filter((t) => t.schema !== "auth")
        .slice(0, 4)
        .map((t) => `SELECT * FROM ${qualified(t)} LIMIT 20;`),
    [tables.data],
  );
  const writer = can("apply:irreversible");
  const verb = writeVerb(sql);
  const lines = Math.max(sql.split("\n").length, 8);
  const go = () => sql.trim() && !run.isPending && run.mutate(sql);

  return (
    <Page full>
      <DataHeader
        project={project}
        title="Database"
        lede="Read-only unless you turn writes on. Results are rows your apps wrote, shown as plain text."
      />
      <div className="mt-8 grid gap-x-10 gap-y-8 xl:grid-cols-[minmax(0,1fr)_17rem]">
        <div className="min-w-0">
          <div
            className={cn(
              "overflow-hidden rounded-[10px] border bg-paper-raised transition-colors duration-[var(--dur-state)]",
              write ? "border-danger-rule" : "border-rule-2",
            )}
          >
            <div className="flex max-h-[24rem] min-h-[10.5rem]">
              <div
                ref={gutter}
                aria-hidden
                className="w-10 shrink-0 overflow-hidden border-r border-rule bg-paper-sunk/60 py-3 pr-2.5 text-right font-mono text-[0.75rem] leading-6 text-ink-4 select-none tnum max-sm:hidden"
              >
                {Array.from({ length: lines }, (_, i) => (
                  <div key={i}>{i + 1}</div>
                ))}
              </div>
              <textarea
                ref={ta}
                value={sql}
                onChange={(e) => setSql(e.target.value)}
                onScroll={(e) => gutter.current && (gutter.current.scrollTop = e.currentTarget.scrollTop)}
                onKeyDown={(e) => {
                  if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                    e.preventDefault();
                    go();
                  }
                }}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
                aria-label="SQL"
                rows={7}
                wrap="off"
                // On a phone the statement wraps (no line numbers) so a DELETE is never scrolled out of sight.
                className="block min-w-0 flex-1 resize-none bg-transparent px-3.5 py-3 font-mono text-[0.8125rem] leading-6 text-ink outline-none max-sm:!whitespace-pre-wrap max-sm:[overflow-wrap:anywhere]"
              />
            </div>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-rule bg-paper-sunk/60 px-3 py-2">
              <label className="flex items-center gap-2 text-sm text-ink-3">
                on
                <MiniSelect value={branch} onChange={(e) => setBranch(e.target.value)} aria-label="Database" className="min-w-[7rem]">
                  <option value="">main</option>
                  {(branches.data ?? []).map((b) => (
                    <option key={b.name} value={b.name}>
                      {b.name}
                    </option>
                  ))}
                </MiniSelect>
              </label>
              <label
                className={cn("flex items-center gap-2 text-sm", writer ? "cursor-pointer text-ink-2" : "text-ink-4")}
                title={writer ? undefined : "Writes need a token that may destroy data (apply:irreversible)"}
              >
                <button
                  type="button"
                  role="switch"
                  aria-checked={write}
                  disabled={!writer}
                  onClick={() => setWrite((w) => !w)}
                  className={cn(
                    "relative h-[18px] w-8 rounded-full border transition-colors duration-[var(--dur-state)]",
                    write ? "border-danger bg-danger" : "border-rule-3 bg-paper-press",
                  )}
                >
                  <span
                    className={cn(
                      "absolute top-[1px] left-[1px] size-3.5 rounded-full bg-paper-raised shadow-[0_1px_1px_oklch(0.2_0.01_60/0.25)] transition-transform duration-[var(--dur-state)] ease-[var(--ease-out)]",
                      write && "translate-x-3.5",
                    )}
                  />
                </button>
                Allow writes
              </label>
              <span className="ml-auto hidden text-xs text-ink-3 sm:inline">
                <kbd className="kbd">⌘</kbd> <kbd className="kbd">↵</kbd> runs
              </span>
              <Button
                variant={write ? "danger" : "primary"}
                size="sm"
                disabled={!sql.trim() || run.isPending}
                onClick={go}
                className="max-sm:ml-auto"
              >
                <Play />
                {run.isPending ? "Running…" : write ? "Run with writes" : "Run"}
              </Button>
            </div>
          </div>
          {write ? (
            <p className="mt-2.5 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm text-ink-2">
              <RiskDots tier="irreversible" />
              Writes can change or delete data. The database is snapshotted first; restore it under Branches for 7 days.
            </p>
          ) : (
            verb && (
              <p className="mt-2.5 text-sm text-ink-3">
                <span className="font-mono text-ink-2">{verb}</span> changes data, so it only runs with writes on
                {writer ? "" : ", which your token can't do"}.
              </p>
            )
          )}

          <div className="mt-7 flex flex-col gap-5">
            {run.isError && <ProblemNote error={run.error} />}
            {run.data && (
              <>
                <p className="text-sm text-ink-3 tnum">
                  {(run.data.results ?? []).length === 1 && (run.data.results?.[0].columns ?? []).length > 0 && (
                    <span className="text-ink-2">{count(run.data.results![0].rowCount, "row")} </span>
                  )}
                  in {ms(run.data.durationMs)}, {run.data.readOnly ? "read-only" : "with writes"}, on{" "}
                  <span className="font-mono text-ink-2">{run.data.database}</span>.
                  {run.data.snapshot && (
                    <span className="text-ink-2">
                      {" "}
                      Snapshot <span className="font-mono">{run.data.snapshot.slice(0, 14)}…</span> taken first.
                    </span>
                  )}
                </p>
                {(run.data.results ?? []).map((r, i) => (
                  <ResultGrid key={i} r={r} />
                ))}
              </>
            )}
            {!run.data && !run.isError && (
              <p className="border-y border-dashed border-rule-3 py-8 text-center text-base text-ink-3">
                Results show up here, with the row count and how long it took.
              </p>
            )}
          </div>
        </div>

        <aside className="flex min-w-0 flex-col gap-8">
          {examples.length > 0 && (
            <Section id="start" label="Start from">
              <QueryList items={examples} onPick={(x) => (setSql(x), ta.current?.focus())} />
            </Section>
          )}
          {history.length > 0 && (
            <Section id="recent" label="Recent">
              <QueryList items={history} onPick={(x) => (setSql(x), ta.current?.focus())} quiet />
            </Section>
          )}
        </aside>
      </div>
    </Page>
  );
}

function QueryList({ items, onPick, quiet }: { items: string[]; onPick: (s: string) => void; quiet?: boolean }) {
  return (
    <Rows>
      {items.map((x) => (
        <li key={x}>
          <button
            onClick={() => onPick(x)}
            title={x}
            className={cn(
              "block w-full truncate py-2 text-left font-mono text-[0.75rem] transition-colors duration-[var(--dur-state)] hover:text-ink",
              quiet ? "text-ink-3" : "text-ink-2",
            )}
          >
            {x.replace(/\s+/g, " ")}
          </button>
        </li>
      ))}
    </Rows>
  );
}

// ------------------------------------------------------------------ branches and snapshots

export function BranchesPage({ project }: { project: string }) {
  useTitle(`${project} · Branches`);
  const qc = useQueryClient();
  const list = useQuery(mq.branches(project));
  const info = useQuery(mq.pg(project));
  const snaps = useQuery(mq.snapshots(project));
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
    const t = setTimeout(() => setMade(null), 20_000);
    return () => clearTimeout(t);
  }, [made]);
  const valid = /^[a-z][a-z0-9-]{0,39}$/.test(name.trim());
  const branches = list.data ?? [];

  return (
    <Page wide>
      <DataHeader
        project={project}
        title="Database"
        lede="A branch is a full, writable copy of the database that takes milliseconds to make: the data disk clones its files (a reflink) instead of copying them."
      />

      {made && (
        <div className="mt-8 flex max-w-[44rem] animate-pop items-center gap-5 rounded-[10px] border border-rule-2 bg-paper-raised px-5 py-4 shadow-[var(--top-light)]">
          <p className="reading shrink-0 text-ink">
            {ms(made.cloneMs).split(NNBSP)[0]}
            <span className="u text-[0.8125rem] text-ink-3">&#8239;{ms(made.cloneMs).split(NNBSP)[1]}</span>
          </p>
          <p className="text-base text-ink-2">
            <span className="font-mono text-ink">{made.name}</span> is a full copy of {made.from}, {bytes(made.sizeBytes)}. Writes to {made.from}{" "}
            paused for {ms(made.blockedMs)}; {ms(made.totalMs)} end to end.
          </p>
        </div>
      )}

      <Section className="mt-10" id="branches" label="Branches" aside={branches.length > 0 ? count(branches.length + 1, "database") : undefined}>
        <Rows>
          <li className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-baseline gap-x-3 py-3 sm:grid-cols-[1.25rem_minmax(0,1fr)_6rem_10rem]">
            <span aria-hidden className="size-[7px] self-center justify-self-center rounded-full bg-ink-2" />
            <span className="min-w-0">
              <span className="font-mono text-[0.84375rem] text-ink">main</span>
              <span className="ml-2 text-sm text-ink-3">what your apps use</span>
            </span>
            <span className="text-right text-sm text-ink-2 tnum">{info.data ? bytes(info.data.sizeBytes) : ""}</span>
            <span className="hidden sm:block" />
          </li>
          {branches.map((b) => (
            <li
              key={b.name}
              className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-baseline gap-x-3 py-3 sm:grid-cols-[1.25rem_minmax(0,1fr)_6rem_10rem]"
            >
              <GitBranch aria-hidden className="relative top-0.5 size-3.5 justify-self-center text-ink-3" />
              <span className="min-w-0">
                <span className="font-mono text-[0.84375rem] text-ink">{b.name}</span>
                <span className="mt-0.5 block truncate text-sm text-ink-3">
                  from {b.from}, {relative(b.createdAt)}
                  <span className="sm:hidden">, {bytes(b.sizeBytes)}</span>
                  <span className="max-sm:hidden">
                    {" "}
                    · <code className="ident">{b.database}</code>
                  </span>
                </span>
              </span>
              <span className="hidden text-right text-sm text-ink-2 tnum sm:block">{bytes(b.sizeBytes)}</span>
              <span className="flex justify-end gap-1 self-center">
                <Button asChild variant="ghost" size="sm">
                  <Link to="/projects/$project/data/sql" params={{ project }} search={{ branch: b.name } as never}>
                    Query
                  </Link>
                </Button>
                {can("apply:irreversible") && (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Delete ${b.name}`}
                    title={`Delete ${b.name}`}
                    onClick={() => setDeleting(b.name)}
                    className="hover:text-danger"
                  >
                    <Trash2 />
                  </Button>
                )}
              </span>
            </li>
          ))}
        </Rows>
        {can("apply:reversible") && (
          <form
            className="mt-4 flex flex-col gap-2 sm:flex-row sm:items-center"
            onSubmit={(e) => {
              e.preventDefault();
              if (valid) create.mutate();
            }}
          >
            <Input
              id="b-name"
              value={name}
              onChange={(e) => setName(e.target.value.toLowerCase())}
              placeholder="try-new-pricing"
              aria-label="New branch name"
              className="h-8 font-mono text-sm sm:max-w-[16rem]"
              autoComplete="off"
            />
            <label className="flex items-center gap-2 text-sm text-ink-3">
              from
              <MiniSelect id="b-from" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="Copy from" className="min-w-[7rem]">
                <option value="">main</option>
                {branches.map((b) => (
                  <option key={b.name} value={b.name}>
                    {b.name}
                  </option>
                ))}
              </MiniSelect>
            </label>
            <Button type="submit" variant="primary" disabled={!valid || create.isPending} className="self-start sm:ml-1 sm:self-auto">
              <GitBranch />
              {create.isPending ? "Copying…" : valid ? `Branch ${from || "main"} as ${name.trim()}` : "Create branch"}
            </Button>
          </form>
        )}
        {create.isError && <ProblemNote className="mt-3" error={create.error} />}
        <p className="mt-3 text-sm text-ink-3">
          It costs almost nothing until it diverges. Agents can do the same:{" "}
          <code className="ident text-ink-2">tiffin branches create {project} --name try-it</code>
        </p>
      </Section>

      <Snapshots project={project} list={snaps.data ?? []} loaded={snaps.isSuccess} />

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
    </Page>
  );
}

function Snapshots({ project, list, loaded }: { project: string; list: PgSnapshot[]; loaded: boolean }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [restoring, setRestoring] = useState<PgSnapshot | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const shown = all ? list : list.slice(0, 6);
  const today = dayKey(new Date().toISOString());
  return (
    <Section className="mt-12" id="snaps" label="Snapshots" aside="taken before anything risky, kept for 7 days">
      {done && <p className="mb-3 text-base text-ink">{done}</p>}
      {loaded && list.length === 0 ? (
        <p className="border-y border-rule py-4 text-base text-ink-3">
          None yet. One is taken before the first SQL write, restore or dropped extension.
        </p>
      ) : (
        <Rows>
          {shown.map((s) => (
            <li
              key={s.id}
              className="group grid grid-cols-[3.25rem_minmax(0,1fr)_auto_auto] items-center gap-x-3 py-1.5 sm:grid-cols-[3.25rem_minmax(0,1fr)_6rem_7rem] sm:gap-x-4"
            >
              <time dateTime={s.at} title={full(s.at)} className="text-sm text-ink-3 tnum">
                {dayKey(s.at) === today ? clock(s.at) : new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" }).format(new Date(s.at))}
              </time>
              <span className="min-w-0 truncate text-base text-ink">
                {s.reason.charAt(0).toUpperCase() + s.reason.slice(1)}
                {s.branch && <span className="text-ink-3"> on {s.branch}</span>}
              </span>
              <span className="text-right text-sm text-ink-3 tnum">{bytes(s.sizeBytes)}</span>
              {can("apply:irreversible") && (
                <span className="flex justify-end">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setRestoring(s)}
                    aria-label={`Restore the snapshot from ${clock(s.at)}`}
                    className="max-sm:w-7 max-sm:px-0 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100"
                  >
                    <RotateCcw />
                    <span className="max-sm:sr-only">Restore</span>
                  </Button>
                </span>
              )}
            </li>
          ))}
        </Rows>
      )}
      {list.length > shown.length && (
        <button onClick={() => setAll(true)} className="mt-2 text-sm text-ink-3 hover:text-ink">
          Show {words(list.length - shown.length)} more
        </button>
      )}
      <HazardDialog<{ overwrites: string; takenAt: string; database: string }, { restored: string; before?: string }>
        open={!!restoring}
        onOpenChange={(o) => !o && setRestoring(null)}
        title="Restore this snapshot?"
        word={project}
        action="Replace the database"
        run={(confirm) => mod.restoreSnapshot(project, restoring!.id, confirm)}
        renderPreview={(p) => (
          <div className="rounded-lg border border-danger-rule bg-danger-wash px-4 py-3 text-base text-ink">
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
    </Section>
  );
}
