import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { Check, ChevronDown, ChevronRight, GitBranch, Lock, Plus, RotateCcw, Search, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { q as core } from "@/api/queries";
import { mod, mq, type PgBranchCreated, type PgSnapshot, type PgTable } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Rows, Section } from "@/components/data-parts";
import { Select } from "@/components/ui/choice";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { Crumbs, Empty, Page, PageHeader, Skeleton, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { Input } from "@/components/ui/input";
import { ConnectButton } from "@/components/connect";
import { useCommand, useKeyHelp } from "@/lib/shortcuts";
import { cn } from "@/lib/cn";
import { bytes, count, int, ms, NNBSP, num, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { clock, dayKey, full, relative } from "@/lib/time";
import { parseSlug, slugOf } from "./data/api";
import { SchemaMap } from "./data/schema-view";
import { SqlPanel } from "./data/sql-page";
import { TableForm } from "./data/table-form";
import { TableView } from "./data/table-view";
import { useBranch, useDataSearch, useSetSearch } from "./data/view";
import "./data/data.css";

// ------------------------------------------------------------------ the frame

function useServices(project: string) {
  const p = useQuery(core.project(project));
  const has = (a: string) => (p.data?.resources ?? []).some((r) => r.address === a);
  return { postgres: has("service/postgres"), valkey: has("service/valkey"), loaded: p.isSuccess };
}

/** The Database tabs; each keeps the copy you're looking at. */
function DataTabs({ project, branch }: { project: string; branch: string }) {
  const search = (branch ? { branch } : {}) as never;
  const items: Array<{ to: string; label: string; exact?: boolean; also?: string }> = [
    { to: "/projects/$project/data", label: "Tables", exact: true, also: "/projects/$project/data/tables/$table" },
    { to: "/projects/$project/data/sql", label: "SQL" },
    { to: "/projects/$project/data/schema", label: "Schema" },
    { to: "/projects/$project/data/branches", label: "Copies" },
    { to: "/projects/$project/data/restore", label: "Restore points" },
  ];
  return (
    <nav className="mt-7 -mb-px flex gap-1 overflow-x-auto border-b border-rule [scrollbar-width:none]" aria-label="Database">
      {items.map((t) => (
        <TabLink key={t.to} to={t.to} also={t.also} exact={t.exact} project={project} search={search}>
          {t.label}
        </TabLink>
      ))}
    </nav>
  );
}

function TabLink({ to, also, exact, project, search, children }: { to: string; also?: string; exact?: boolean; project: string; search: never; children: ReactNode }) {
  const cls =
    "relative flex h-10 shrink-0 items-center gap-2 px-3 text-[0.875rem] text-ink-3 transition-colors first:pl-0 first:after:left-0 hover:text-ink data-[status=active]:font-[550] data-[status=active]:text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full after:bg-transparent data-[status=active]:after:bg-ink";
  const path = useRouterState({ select: (s) => s.location.pathname });
  const onAlso = !!also && path.startsWith(`/projects/${project}/data/tables/`);
  return (
    <Link
      to={to as "/"}
      params={{ project } as never}
      search={search}
      activeOptions={{ exact: !!exact, includeSearch: false }}
      data-status={onAlso ? "active" : undefined}
      aria-current={onAlso ? "page" : undefined}
      className={cls}
    >
      {children}
    </Link>
  );
}

/** "shop › Database", the title, one line, the Database tabs and the copy you're looking at. */
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
  /** The Database tabs (Tables, SQL, Schema, Copies, Restore points); the KV page has none. */
  tabs?: boolean;
}) {
  const branch = useBranch();
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
      {tabs && <DataTabs project={project} branch={branch} />}
    </PageHeader>
  );
}

/** Pick the copy (preview branch) every Database tab looks at: production or one of its copies. */
function CopyPicker({ project }: { project: string }) {
  const branch = useBranch();
  const set = useSetSearch();
  const list = useQuery(mq.branches(project));
  const copies = list.data ?? [];
  if (!branch && copies.length === 0) return null;
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="secondary" aria-label={`Looking at ${branch ? `the copy ${branch}` : "production"}. Switch`}>
          <GitBranch className={branch ? "text-brass-ink" : undefined} />
          <span className="max-w-[9rem] truncate">{branch || "production"}</span>
          <ChevronDown className="text-ink-3" />
        </Button>
      </MenuTrigger>
      <MenuContent align="end" className="w-64">
        <MenuLabel>Look at</MenuLabel>
        <MenuItem onSelect={() => set({ branch: undefined, f: undefined, s: undefined })}>
          <span className="flex-1">production</span>
          {!branch && <Check className="!text-ink" />}
        </MenuItem>
        {copies.map((b) => (
          <MenuItem key={b.name} onSelect={() => set({ branch: b.name, f: undefined, s: undefined })}>
            <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem]">{b.name}</span>
            <span className="text-xs text-ink-3">{relative(b.createdAt)}</span>
            {branch === b.name && <Check className="!text-ink" />}
          </MenuItem>
        ))}
        <MenuSeparator />
        <MenuItem asChild>
          <Link to="/projects/$project/data/branches" params={{ project }}>
            <GitBranch />
            Make or delete copies
          </Link>
        </MenuItem>
      </MenuContent>
    </Menu>
  );
}

/** Every Database page: the head, a note when looking at a copy, and the page; or why there's no database. */
function DataShell({ project, children, wide }: { project: string; children: ReactNode; wide?: boolean }) {
  const info = useQuery(mq.pg(project));
  const tables = useQuery(mq.tables(project));
  const s = useServices(project);
  const branch = useBranch();
  const { can } = useMe();
  const search = useDataSearch();
  const setSearch = useSetSearch();
  const canMake = can("apply:irreversible");
  useCommand(canMake ? { id: "new-table", label: "New table", keywords: ["create", "database"], run: () => setSearch({ new: "table" }, false) } : null);
  useKeyHelp("Database: table", TABLE_KEYS);
  useKeyHelp("Database: SQL", SQL_KEYS);
  useKeyHelp("Database: schema", SCHEMA_KEYS);

  if (info.isError && notOnBox(info.error)) return <NotOnBox what="Databases" />;
  if (s.loaded && !s.postgres)
    return (
      <Page wide>
        <DataHeader project={project} title={PARTS.postgres.name} tabs={false} />
        <Empty className="mt-10" title="This project has no database yet">
          Add it from the project's overview, or add <code className="font-mono text-ink">services: {"{ postgres: {} }"}</code> to tiffin.config.ts and apply.
        </Empty>
      </Page>
    );
  const own = (tables.data ?? []).filter((t) => !t.managed && (t.kind === "table" || t.kind === "partitioned")).length;
  return (
    <Page full={!wide} wide={wide}>
      <DataHeader
        project={project}
        title={PARTS.postgres.name}
        lede={
          info.data ? (
            <span className="tnum">
              {bytes(info.data.sizeBytes)} · {count(own, "table")}
              <span className="text-ink-3"> · {PARTS.postgres.sub}</span>
            </span>
          ) : undefined
        }
        actions={
          <>
            <CopyPicker project={project} />
            <ConnectButton part="database" project={project} />
            {canMake && (
              <Button variant="primary" onClick={() => setSearch({ new: "table" }, false)}>
                <Plus />
                New table
              </Button>
            )}
          </>
        }
      />
      {branch && (
        <p className="mt-4 flex items-center gap-2 rounded-md border border-brass/50 bg-brass-wash px-3 py-2 text-sm text-ink">
          <GitBranch className="size-3.5 shrink-0 text-brass-ink" aria-hidden />
          <span>
            You're looking at the copy <span className="font-mono">{branch}</span>. Changes here don't touch production.
          </span>
        </p>
      )}
      {info.isError && <ProblemNote className="mt-6" error={info.error} />}
      <div className="mt-6">{children}</div>
      {search.new === "table" && <TableForm project={project} branch={branch} onClose={() => setSearch({ new: undefined })} />}
    </Page>
  );
}

// The keys the Database pages handle themselves, for the `?` sheet.
const TABLE_KEYS: Array<[string, string]> = [
  ["↑ ↓ ← →", "Move between cells"],
  ["↵", "Edit the cell; ↵ again saves"],
  ["esc", "Cancel the edit, or clear the selection"],
  ["⇥", "While editing: save and move right"],
  ["⌥ ↵", "Open the linked row"],
  ["Space", "Select the row (on its checkbox), or flip true/false"],
  ["⌘ A", "Select every loaded row"],
  ["⌘ C", "Copy the cell, or the selected rows"],
  ["⌘ V", "Paste cells from a spreadsheet (a summary comes first)"],
  ["⌫", "Delete the selected rows"],
  ["Home End", "First or last column; with ⌘, first or last row"],
];
const SQL_KEYS: Array<[string, string]> = [
  ["⌘ ↵", "Run"],
  ["Ctrl Space", "Suggest tables and columns"],
];
const SCHEMA_KEYS: Array<[string, string]> = [
  ["+ −", "Zoom"],
  ["0", "Fit the diagram"],
];

// ------------------------------------------------------------------ tables

/** The Tables tab: your tables on the left, the open one on the right. */
export function DataPage({ project }: { project: string }) {
  useTitle(`${project} · Database`);
  return (
    <DataShell project={project}>
      <TablesLayout project={project} />
    </DataShell>
  );
}

export function TablePage({ project, table }: { project: string; table: string }) {
  useTitle(`${table} · ${project}`);
  return (
    <DataShell project={project}>
      <TablesLayout project={project} slug={table} />
    </DataShell>
  );
}

function TablesLayout({ project, slug }: { project: string; slug?: string }) {
  const branch = useBranch();
  const tables = useQuery(mq.tables(project, branch || undefined));
  const list = tables.data ?? [];
  const mine = list.filter((t) => !t.managed);
  const open = slug ? parseSlug(slug) : mine[0] ? { schema: mine[0].schema, name: mine[0].name } : null;
  const exists = !slug || list.some((t) => t.schema === open?.schema && t.name === open?.name);
  const { can } = useMe();
  const setSearch = useSetSearch();

  return (
    <div className="grid gap-x-8 gap-y-4 lg:grid-cols-[13.5rem_minmax(0,1fr)]">
      <TableNav project={project} list={list} loaded={tables.isSuccess} current={open ? slugOf(open) : undefined} branch={branch} />
      <div className="min-w-0">
        {tables.isError && <ProblemNote error={tables.error} />}
        {tables.isSuccess && list.length === 0 && (
          <Empty title="No tables yet">
            Make one here, or let your app's migrations make them.
            {can("apply:irreversible") && (
              <div className="mt-4">
                <Button variant="primary" onClick={() => setSearch({ new: "table" }, false)}>
                  <Plus />
                  New table
                </Button>
              </div>
            )}
          </Empty>
        )}
        {tables.isSuccess && !exists && <ProblemNote error={new Error(`There's no table called ${slug}${branch ? ` in the copy ${branch}` : ""}.`)} />}
        {tables.isPending && <Skeleton className="h-[max(20rem,calc(100dvh-20rem))]" />}
        {open && exists && tables.isSuccess && <TableView key={`${branch}:${slugOf(open)}`} project={project} schema={open.schema} name={open.name} branch={branch} />}
      </div>
    </div>
  );
}

function TableNav({ project, list, loaded, current, branch }: { project: string; list: PgTable[]; loaded: boolean; current?: string; branch: string }) {
  const [q, setQ] = useState("");
  const navigate = useNavigate();
  const shown = q ? list.filter((t) => slugOf(t).toLowerCase().includes(q.toLowerCase())) : list;
  const mine = shown.filter((t) => !t.managed);
  const managed = shown.filter((t) => t.managed);
  const search = (branch ? { branch } : {}) as never;
  const item = (t: PgTable) => {
    const s = slugOf(t);
    const on = s === current;
    return (
      <li key={s}>
        <Link
          to="/projects/$project/data/tables/$table"
          params={{ project, table: s }}
          search={search}
          aria-current={on ? "page" : undefined}
          className={cn(
            "flex h-8 items-center gap-2 rounded-[6px] px-2.5 text-[0.84375rem] transition-colors",
            on ? "bg-paper-press font-[550] text-ink" : "text-ink-2 hover:bg-paper-hover hover:text-ink",
          )}
        >
          <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem]">{t.managed ? s : t.name}</span>
          {t.kind !== "table" && t.kind !== "partitioned" ? (
            <span className="shrink-0 text-xs text-ink-3">{t.kind === "materialized-view" ? "mat. view" : t.kind}</span>
          ) : (
            t.rowEstimate !== null && <span className="shrink-0 text-xs text-ink-3 tnum">{short(t.rowEstimate)}</span>
          )}
        </Link>
      </li>
    );
  };
  return (
    <>
      {/* A phone gets a picker instead of the list. */}
      <div className="flex items-center gap-2 text-sm text-ink-3 lg:hidden">
        Table
        <Select
          value={current ?? ""}
          onValueChange={(v) => navigate({ to: "/projects/$project/data/tables/$table", params: { project, table: v }, search })}
          className="flex-1 font-mono"
          aria-label="Table"
          options={list.map((t) => ({ value: slugOf(t), label: `${slugOf(t)}${t.managed ? " (managed)" : ""}` }))}
        />
      </div>
      <nav aria-label="Tables" className="hidden min-w-0 lg:block">
        {list.length > 10 && (
          <div className="mb-2 flex h-8 items-center gap-2 rounded-md border border-rule bg-paper px-2.5 focus-within:border-brass">
            <Search className="size-3.5 shrink-0 text-ink-3" aria-hidden />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a table" aria-label="Find a table" className="w-full bg-transparent text-sm text-ink outline-none placeholder:text-ink-4" />
          </div>
        )}
        <h2 className="label mb-1 px-2.5">Your tables</h2>
        {!loaded ? (
          <Skeleton className="h-40" />
        ) : mine.length === 0 ? (
          <p className="px-2.5 py-1 text-sm text-ink-3">{q ? "None match." : "None yet."}</p>
        ) : (
          <ul className="flex flex-col">{mine.map(item)}</ul>
        )}
        {managed.length > 0 && (
          <details className="group/m mt-4" open={managed.some((t) => slugOf(t) === current)}>
            <summary className="flex cursor-pointer list-none items-center gap-1.5 px-2.5 py-1 [&::-webkit-details-marker]:hidden">
              <ChevronRight className="size-3 text-ink-3 transition-transform group-open/m:rotate-90" />
              <span className="label">Managed by Tiffin</span>
              <Lock className="ml-auto size-3 text-ink-4" aria-label="read-only" />
            </summary>
            <p className="mt-1 mb-1.5 px-2.5 text-xs text-ink-3">Sign-in and the box's own tables. Read-only here.</p>
            <ul className="flex flex-col">{managed.map(item)}</ul>
          </details>
        )}
      </nav>
    </>
  );
}

const short = (n: number) => (n < 10_000 ? int(n) : num(n));

// ------------------------------------------------------------------ SQL and schema

export function SqlPage({ project }: { project: string }) {
  useTitle(`${project} · SQL`);
  const branch = useBranch();
  // Other pages hand over a query in the URL (?sql=…).
  const [handed] = useState(() => new URLSearchParams(location.search).get("sql") ?? undefined);
  return (
    <DataShell project={project}>
      <SqlPanel key={branch} project={project} branch={branch} handed={handed} />
    </DataShell>
  );
}

export function SchemaPage({ project }: { project: string }) {
  useTitle(`${project} · Schema`);
  const branch = useBranch();
  const tables = useQuery(mq.tables(project, branch || undefined));
  return (
    <DataShell project={project}>
      {tables.isError && <ProblemNote error={tables.error} />}
      {tables.isPending ? <Skeleton className="h-[max(24rem,calc(100dvh-20rem))]" /> : tables.data && <SchemaMap project={project} branch={branch} tables={tables.data} />}
    </DataShell>
  );
}

// ------------------------------------------------------------------ copies and restore points

export function BranchesPage({ project }: { project: string }) {
  useTitle(`${project} · Copies`);
  const qc = useQueryClient();
  const list = useQuery(mq.branches(project));
  const info = useQuery(mq.pg(project));
  const { can } = useMe();
  const setSearch = useSetSearch();
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
  const valid = /^[a-z][a-z0-9-]{0,18}$/.test(name.trim());
  const branches = list.data ?? [];

  return (
    <DataShell project={project} wide>
      <p className="max-w-[44rem] text-base text-ink-2">
        A copy is a full, writable copy of the database that takes about a second to make: the disk shares the files instead of copying them. Previews of your apps
        get one each; make your own to try something.
      </p>
      {made && (
        <div className="mt-6 flex max-w-[44rem] animate-pop items-center gap-5 rounded-[10px] border border-rule-2 bg-paper-raised px-5 py-4 shadow-[var(--top-light)]">
          <p className="reading shrink-0 text-ink">
            {ms(made.cloneMs).split(NNBSP)[0]}
            <span className="u text-[0.8125rem] text-ink-3">&#8239;{ms(made.cloneMs).split(NNBSP)[1]}</span>
          </p>
          <p className="text-base text-ink-2">
            <span className="font-mono text-ink">{made.name}</span> is a full copy of {made.from === "main" ? "production" : made.from}, {bytes(made.sizeBytes)}.{" "}
            <button type="button" className="text-ink underline decoration-rule-3 underline-offset-4" onClick={() => setSearch({ branch: made.name }, false)}>
              Look at it
            </button>
          </p>
        </div>
      )}
      <Section className="mt-8" id="copies" label="Copies" aside={branches.length > 0 ? count(branches.length + 1, "database") : undefined}>
        <Rows>
          <li className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-baseline gap-x-3 py-3 sm:grid-cols-[1.25rem_minmax(0,1fr)_6rem_10rem]">
            <span aria-hidden className="size-[7px] self-center justify-self-center rounded-full bg-ink-2" />
            <span className="min-w-0">
              <span className="font-mono text-[0.84375rem] text-ink">production</span>
              <span className="ml-2 text-sm text-ink-3">what your apps use</span>
            </span>
            <span className="text-right text-sm text-ink-2 tnum">{info.data ? bytes(info.data.sizeBytes) : ""}</span>
            <span className="hidden sm:block" />
          </li>
          {branches.map((b) => (
            <li key={b.name} className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-baseline gap-x-3 py-3 sm:grid-cols-[1.25rem_minmax(0,1fr)_6rem_10rem]">
              <GitBranch aria-hidden className="relative top-0.5 size-3.5 justify-self-center text-ink-3" />
              <span className="min-w-0">
                <span className="font-mono text-[0.84375rem] text-ink">{b.name}</span>
                <span className="mt-0.5 block truncate text-sm text-ink-3">
                  {b.preview ? `for the preview ${b.preview}, ` : ""}from {b.from === "main" ? "production" : b.from}, {relative(b.createdAt)}
                  <span className="sm:hidden">, {bytes(b.sizeBytes)}</span>
                </span>
              </span>
              <span className="hidden text-right text-sm text-ink-2 tnum sm:block">{bytes(b.sizeBytes)}</span>
              <span className="flex justify-end gap-1 self-center">
                <Button asChild variant="ghost" size="sm">
                  <Link to="/projects/$project/data" params={{ project }} search={{ branch: b.name } as never}>
                    Open
                  </Link>
                </Button>
                {can("apply:irreversible") && (
                  <Button variant="ghost" size="icon-sm" aria-label={`Delete ${b.name}`} title={`Delete ${b.name}`} onClick={() => setDeleting(b.name)} className="hover:text-danger">
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
              aria-label="New copy's name"
              className="h-8 font-mono text-sm sm:max-w-[16rem]"
              autoComplete="off"
            />
            <div className="flex items-center gap-2 text-sm text-ink-3">
              from
              <Select
                id="b-from"
                size="sm"
                value={from || "__prod"}
                onValueChange={(v) => setFrom(v === "__prod" ? "" : v)}
                aria-label="Copy from"
                className="w-auto min-w-[7rem] font-mono"
                options={[{ value: "__prod", label: "production" }, ...branches.map((b) => ({ value: b.name, label: b.name }))]}
              />
            </div>
            <Button type="submit" variant="primary" disabled={!valid || create.isPending} className="self-start sm:ml-1 sm:self-auto">
              <GitBranch />
              {create.isPending ? "Copying…" : valid ? `Copy ${from || "production"} as ${name.trim()}` : "Make a copy"}
            </Button>
          </form>
        )}
        {create.isError && <ProblemNote className="mt-3" error={create.error} />}
        <p className="mt-3 text-sm text-ink-3">
          It costs almost nothing until it changes. Agents can do the same: <code className="ident text-ink-2">tiffin branches create {project} --name try-it</code>
        </p>
      </Section>
      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete the copy ${deleting}?`}
        body="Its database is dropped, with anything changed in it. Production is not affected."
        action="Delete copy"
        run={() => mod.deleteBranch(project, deleting!)}
        done={() => {
          qc.invalidateQueries({ queryKey: ["branches", project] });
          qc.invalidateQueries({ queryKey: ["pg", project] });
        }}
      />
    </DataShell>
  );
}

export function RestorePage({ project }: { project: string }) {
  useTitle(`${project} · Restore points`);
  const snaps = useQuery(mq.snapshots(project));
  return (
    <DataShell project={project} wide>
      <Snapshots project={project} list={snaps.data ?? []} loaded={snaps.isSuccess} />
    </DataShell>
  );
}

function Snapshots({ project, list, loaded }: { project: string; list: PgSnapshot[]; loaded: boolean }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [restoring, setRestoring] = useState<PgSnapshot | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const shown = all ? list : list.slice(0, 12);
  const today = dayKey(new Date().toISOString());
  return (
    <Section id="snaps" label="Restore points" aside="taken before anything risky, kept for 7 days">
      {done && <p className="mb-3 text-base text-ink">{done}</p>}
      {loaded && list.length === 0 ? (
        <p className="border-y border-rule py-4 text-base text-ink-3">None yet. One is taken before SQL that changes data, before deleting many rows or a table, and before a restore.</p>
      ) : (
        <Rows>
          {shown.map((s) => (
            <li key={s.id} className="group grid grid-cols-[3.25rem_minmax(0,1fr)_auto_auto] items-center gap-x-3 py-1.5 sm:grid-cols-[3.25rem_minmax(0,1fr)_6rem_7rem] sm:gap-x-4">
              <time dateTime={s.at} title={full(s.at)} className="text-sm text-ink-3 tnum">
                {dayKey(s.at) === today ? clock(s.at) : new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" }).format(new Date(s.at))}
              </time>
              <span className="min-w-0 truncate text-base text-ink">
                {s.reason.charAt(0).toUpperCase() + s.reason.slice(1).replace("an SQL write", "SQL that changed data")}
                {s.branch && <span className="text-ink-3"> on {s.branch}</span>}
              </span>
              <span className="text-right text-sm text-ink-3 tnum">{bytes(s.sizeBytes)}</span>
              {can("apply:irreversible") && (
                <span className="flex justify-end">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setRestoring(s)}
                    aria-label={`Restore the database to ${full(s.at)}`}
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
        <button type="button" onClick={() => setAll(true)} className="mt-2 text-sm text-ink-3 hover:text-ink">
          Show {words(list.length - shown.length)} more
        </button>
      )}
      <HazardDialog<{ overwrites: string; takenAt: string; database: string }, { restored: string; before?: string }>
        open={!!restoring}
        onOpenChange={(o) => !o && setRestoring(null)}
        title="Go back to this restore point?"
        word={project}
        action="Replace the database"
        run={(confirm) => mod.restoreSnapshot(project, restoring!.id, confirm)}
        renderPreview={(p) => (
          <div className="rounded-lg border border-danger-rule bg-danger-wash px-4 py-3 text-base text-ink">
            <p>Everything in the database is replaced by how it was {relative(p.takenAt)}.</p>
            <p className="mt-2 text-sm text-ink-2">A restore point of what's there now is taken first, so you can come back.</p>
          </div>
        )}
        onDone={() => {
          setDone("Restored. What was there before is the newest restore point.");
          qc.invalidateQueries({ queryKey: ["snapshots", project] });
          qc.invalidateQueries({ queryKey: ["tables", project] });
          qc.invalidateQueries({ queryKey: ["pg-rows", project] });
        }}
      />
    </Section>
  );
}
