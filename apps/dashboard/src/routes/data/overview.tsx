import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Copy, Eye, EyeOff, GitBranch, History, Play, Plus, RotateCcw, Table2 } from "lucide-react";
import { Toggle } from "radix-ui";
import { useState, type ReactNode } from "react";
import { mod, mq, type PgInfo, type PgTable } from "@/api/modules";
import { ActorMark } from "@/components/actor";
import { CopyButton } from "@/components/copy";
import { Reading, Rows, Section } from "@/components/data-parts";
import { Empty, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, int, num } from "@/lib/format";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";
import { dq, editWords, problemToast, slugOf } from "./api";
import { useBranch, useSetSearch } from "./view";

type Form = "url" | "psql" | "env";
const DOTS = "••••••••";

/** The connection as each form writes it: a URL, a psql command, a line for .env. */
function written(url: string, form: Form) {
  return form === "psql" ? `psql "${url}"` : form === "env" ? `DATABASE_URL="${url}"` : url;
}

/** Postgres's major version and minor, as people say it: "17.4". */
const pgVersion = (v?: string) => (v ?? "").match(/^\d+(\.\d+)?/)?.[0] ?? "";

/**
 * The Database's home: how to connect, what's in it, and what you can do
 * next. Like a project dashboard on a hosted database, sized for one box.
 */
export function DataOverview({ project }: { project: string }) {
  const branch = useBranch();
  const info = useQuery(mq.pg(project));
  const tables = useQuery(mq.tables(project, branch || undefined));
  const snaps = useQuery(mq.snapshots(project));
  const copies = useQuery(mq.branches(project));
  const saved = useQuery(dq.queries(project));
  const { can } = useMe();
  const setSearch = useSetSearch();
  const writer = can("apply:irreversible");

  const list = tables.data ?? [];
  const mine = list.filter((t) => !t.managed && (t.kind === "table" || t.kind === "partitioned"));
  const managed = list.filter((t) => t.managed);
  const views = list.filter((t) => !t.managed && t.kind !== "table" && t.kind !== "partitioned");
  const newest = (snaps.data ?? [])[0];
  const i = info.data;
  const size = bytesParts(i?.sizeBytes);
  const search = (branch ? { branch } : {}) as never;

  return (
    <div className="grid gap-x-12 gap-y-10 xl:grid-cols-[minmax(0,1fr)_19rem]">
      <div className="min-w-0">
        <Connection project={project} info={i} failed={info.isError} writer={writer} branch={branch} />

        <div className="mt-8 grid grid-cols-2 gap-x-8 gap-y-6 border-b border-rule pb-7 sm:grid-cols-4">
          <Reading label="Size" value={i ? size.value : "–"} unit={i ? size.unit : undefined} sub={i ? "Tables, indexes and history" : undefined} />
          <Reading
            label="Tables"
            value={tables.isSuccess ? int(mine.length) : "–"}
            sub={tables.isSuccess ? [views.length ? count(views.length, "view") : "", managed.length ? `${int(managed.length)} kept by Tiffin` : ""].filter(Boolean).join(", ") || "Your own tables" : undefined}
          />
          <Reading label="Connections" value={i ? int(i.connections) : "–"} unit={i ? "open" : undefined} sub={i ? "To production, right now" : undefined} />
          <Reading
            label="Restore point"
            value={<span className="block pt-1 text-[0.9375rem] leading-[1.375rem] tracking-normal">{newest ? relative(newest.at) : snaps.isSuccess ? "None yet" : "–"}</span>}
            sub={newest ? `Before ${newest.reason.replace("an SQL write", "SQL that changed data")}` : snaps.isSuccess ? "Taken before anything risky" : undefined}
          />
        </div>

        <Section
          className="mt-9"
          id="db-tables"
          label={branch ? `Tables in ${branch}` : "Tables"}
          aside={
            mine.length > 0 ? (
              <Link to="/projects/$project/data/tables/$table" params={{ project, table: slugOf(mine[0]) }} search={search} className="hover:text-ink">
                Open the table editor
              </Link>
            ) : undefined
          }
        >
          {tables.isError && <ProblemNote error={tables.error} />}
          {tables.isPending && <Skeleton className="h-56" />}
          {tables.isSuccess && mine.length === 0 && views.length === 0 && (
            <Empty title="No tables yet">
              Make one here, or let your app's migrations make them. They show up the moment they exist.
              {writer && (
                <div className="mt-4">
                  <Button variant="primary" onClick={() => setSearch({ new: "table" }, false)}>
                    <Plus />
                    New table
                  </Button>
                </div>
              )}
            </Empty>
          )}
          {tables.isSuccess && mine.length + views.length > 0 && <TableSizes project={project} list={[...mine, ...views]} search={search} />}
        </Section>
      </div>

      <aside className="flex min-w-0 flex-col gap-9">
        <Section id="db-do" label="Quick actions">
          <Rows>
            {writer && (
              <Action icon={<Plus />} title="New table" sub="Name it, pick columns, link it to others" onClick={() => setSearch({ new: "table" }, false)} />
            )}
            <Action
              icon={<Play />}
              title="Run SQL"
              sub={saved.data?.length ? `${count(saved.data.length, "saved query", "saved queries")} to start from` : "Changes take a restore point first"}
              to="/projects/$project/data/sql"
              project={project}
              search={search}
            />
            <Action
              icon={<GitBranch />}
              title="Make a copy"
              sub={copies.data?.length ? `${count(copies.data.length, "copy", "copies")} so far; each takes a second` : "A writable copy, in about a second"}
              to="/projects/$project/data/branches"
              project={project}
              search={search}
            />
            <Action
              icon={<RotateCcw />}
              title="Go back in time"
              sub={snaps.data?.length ? `${count(snaps.data.length, "restore point")} from the last 7 days` : "Restore points show up here"}
              to="/projects/$project/data/restore"
              project={project}
              search={search}
            />
          </Rows>
        </Section>
        <Details info={i} failed={info.isError} />
        <RecentEdits project={project} />
      </aside>
    </div>
  );
}

/** The connection string, its password hidden until asked for, in the form you need. */
function Connection({ project, info, failed, writer, branch }: { project: string; info?: PgInfo; failed: boolean; writer: boolean; branch: string }) {
  const [form, setForm] = useState<Form>("url");
  const [shown, setShown] = useState(false);
  const real = useQuery({ queryKey: ["pg-connection", project], queryFn: () => mod.pgConnection(project), enabled: shown && writer, staleTime: 5 * 60_000 });
  const masked = info ? `postgresql://${info.role}:${DOTS}@${info.host || "127.0.0.1:5432"}/${info.database}?sslmode=disable` : "";
  const url = shown && real.data ? real.data.databaseUrl : masked;

  /** Copies with the password: fetched (and logged on the box) only now. */
  const copy = async () => {
    try {
      const c = real.data ?? (await mod.pgConnection(project));
      if (await copyText(written(c.databaseUrl, form))) toast({ title: form === "url" ? "Copied the connection string" : form === "psql" ? "Copied the psql command" : "Copied the .env line", detail: "It includes the password." });
    } catch (e) {
      problemToast(e, "Couldn't get the connection string.");
    }
  };

  return (
    <section aria-labelledby="db-connect" className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 pt-3.5 pb-3">
        <h2 id="db-connect" className="text-base font-[550] text-ink">
          Connection string
        </h2>
        <Segmented<Form>
          label="Form"
          value={form}
          onChange={setForm}
          options={[
            { value: "url", label: "URL" },
            { value: "psql", label: "psql" },
            { value: "env", label: ".env" },
          ]}
        />
      </div>
      <div className="flex items-start gap-2 border-y border-rule bg-paper-sunk/60 px-4 py-3">
        {info ? (
          <code className="min-w-0 flex-1 font-mono text-[0.8125rem] leading-5 break-all text-ink" aria-label="Connection string" data-testid="db-connection">
            {written(url, form)
              .split(DOTS)
              .flatMap((part, k) => (k === 0 ? [part] : [<span key={k} className="tracking-[0.08em] text-ink-3" aria-label="password hidden">{DOTS}</span>, part]))}
          </code>
        ) : failed ? (
          <p className="flex-1 text-sm text-ink-3">The database didn't answer, so its connection string can't be shown.</p>
        ) : (
          <Skeleton className="h-5 flex-1" />
        )}
        {writer && info && (
          <span className="-my-1 flex shrink-0 items-center gap-0.5">
            <Toggle.Root
              pressed={shown}
              onPressedChange={setShown}
              aria-label="Show the password"
              title={shown ? "Hide the password" : "Show the password"}
              className="grid size-7 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink data-[state=on]:text-ink"
            >
              {shown ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
            </Toggle.Root>
            <button
              type="button"
              onClick={() => void copy()}
              aria-label="Copy with the password"
              title="Copy with the password"
              className="grid size-7 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink"
            >
              <Copy className="size-3.5" />
            </button>
          </span>
        )}
      </div>
      {real.isError && <ProblemNote className="mx-4 mt-3" error={real.error} />}
      <div className="grid gap-x-6 gap-y-1.5 px-4 py-3 text-sm text-ink-3 sm:grid-cols-2">
        <p>
          Your apps already have it as <code className="ident text-ink-2">DATABASE_URL</code>
          {branch ? <>; previews get their copy's own.</> : "."}
        </p>
        <p className="flex min-w-0 items-center gap-1 sm:justify-end">
          <span className="shrink-0">From your computer:</span>
          <code className="ident truncate text-ink-2">tiffin db tunnel {project}</code>
          <CopyButton value={`tiffin db tunnel ${project}`} label="Copy the tunnel command" className="-my-1 size-6" />
        </p>
        {!writer && <p className="sm:col-span-2">The password needs full access to the project.</p>}
        {writer && shown && <p className="sm:col-span-2">Seeing or copying the password is written to the box's audit log.</p>}
      </div>
    </section>
  );
}

/** Your tables by size, each with its rows and a bar of its share of the largest. */
function TableSizes({ project, list, search }: { project: string; list: PgTable[]; search: never }) {
  const [all, setAll] = useState(false);
  const sorted = [...list].sort((a, b) => b.sizeBytes - a.sizeBytes || a.name.localeCompare(b.name));
  const shown = all ? sorted : sorted.slice(0, 8);
  const max = Math.max(1, sorted[0]?.sizeBytes ?? 1);
  return (
    <>
      <ul className="divide-y divide-rule border-y border-rule">
        {shown.map((t) => (
          <li key={slugOf(t)}>
            <Link
              to="/projects/$project/data/tables/$table"
              params={{ project, table: slugOf(t) }}
              search={search}
              className="group grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 py-2.5 transition-colors hover:bg-paper-hover sm:grid-cols-[minmax(0,1fr)_6rem_minmax(5rem,9rem)_4.5rem] sm:px-2"
            >
              <span className="flex min-w-0 items-baseline gap-2">
                <Table2 aria-hidden className="relative top-[2px] size-3.5 shrink-0 text-ink-4" />
                <span className="truncate font-mono text-[0.84375rem] text-ink">{slugOf(t)}</span>
                {t.kind !== "table" && t.kind !== "partitioned" && <span className="shrink-0 text-xs text-ink-3">{t.kind === "materialized-view" ? "materialized view" : t.kind}</span>}
                {t.rls && <span className="shrink-0 text-xs text-ink-3">row security on</span>}
              </span>
              <span className="text-right text-sm text-ink-2 tnum">
                {t.rowEstimate === null ? "" : `${t.rowEstimate < 10_000 ? int(t.rowEstimate) : num(t.rowEstimate)} ${t.rowEstimate === 1 ? "row" : "rows"}`}
                {t.sizeBytes > 0 && <span className="text-ink-3 sm:hidden"> · {bytes(t.sizeBytes)}</span>}
              </span>
              <span aria-hidden className="hidden h-1.5 overflow-hidden rounded-full bg-paper-sunk sm:block">
                {t.sizeBytes > 0 && <span className="block h-full rounded-full bg-ink-3/70 transition-colors group-hover:bg-ink-2" style={{ width: `${Math.max(2, (t.sizeBytes / max) * 100)}%` }} />}
              </span>
              <span className="hidden text-right text-sm text-ink-3 tnum sm:block">{t.sizeBytes ? bytes(t.sizeBytes) : "–"}</span>
            </Link>
          </li>
        ))}
      </ul>
      {sorted.length > shown.length && (
        <button type="button" onClick={() => setAll(true)} className="mt-2 text-sm text-ink-3 hover:text-ink">
          Show {int(sorted.length - shown.length)} more
        </button>
      )}
    </>
  );
}

/** One thing to do next: an icon, what it is, one line on it. */
function Action({
  icon,
  title,
  sub,
  to,
  project,
  search,
  onClick,
}: {
  icon: ReactNode;
  title: string;
  sub: string;
  to?: string;
  project?: string;
  search?: never;
  onClick?: () => void;
}) {
  const body = (
    <>
      <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-md border border-rule-2 bg-paper-raised text-ink-2 [&_svg]:size-3.5">{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block text-base text-ink">{title}</span>
        <span className="block text-sm text-ink-3">{sub}</span>
      </span>
      {to && <ArrowUpRight aria-hidden className="mt-1 size-3.5 shrink-0 text-ink-4 transition-colors group-hover:text-ink" />}
    </>
  );
  const cls = "group flex w-full items-start gap-3 py-2.5 text-left transition-colors hover:bg-paper-hover sm:px-1.5";
  return (
    <li>
      {to ? (
        <Link to={to as "/"} params={{ project } as never} search={search} className={cls}>
          {body}
        </Link>
      ) : (
        <button type="button" onClick={onClick} className={cls}>
          {body}
        </button>
      )}
    </li>
  );
}

/** What the database is: version, names, where it listens, extensions. */
function Details({ info, failed }: { info?: PgInfo; failed: boolean }) {
  if (!info)
    return (
      <Section id="db-details" label="Details">
        {failed ? <p className="border-y border-rule py-3 text-sm text-ink-3">Not known while the database doesn't answer.</p> : <Skeleton className="h-36" />}
      </Section>
    );
  const ext = (info.extensions ?? []).map((e) => e.split("@")[0]).filter((e) => e !== "plpgsql");
  const rows: Array<[string, ReactNode]> = [
    ["Postgres", pgVersion(info.version)],
    ["Database", <code className="ident">{info.database}</code>],
    ["Role", <code className="ident">{info.role}</code>],
    ["Listens on", <code className="ident">{info.host || "127.0.0.1:5432"}</code>],
    ["Socket", <code className="ident">{info.socketDir}</code>],
  ];
  return (
    <Section id="db-details" label="Details">
      <dl className="divide-y divide-rule border-y border-rule">
        {rows.map(([k, v]) => (
          <div key={k} className="flex items-baseline justify-between gap-4 py-2">
            <dt className="shrink-0 text-sm text-ink-3">{k}</dt>
            <dd className="min-w-0 truncate text-right text-sm text-ink-2">{v}</dd>
          </div>
        ))}
        <div className="py-2.5">
          <dt className="text-sm text-ink-3">Extensions</dt>
          <dd className="mt-1.5 flex flex-wrap gap-1.5">
            {ext.length === 0 ? (
              <span className="text-sm text-ink-3">None beyond the defaults</span>
            ) : (
              ext.map((e) => (
                <code key={e} className="rounded-[4px] border border-rule-2 px-1.5 py-px font-mono text-[0.75rem] text-ink-2">
                  {e}
                </code>
              ))
            )}
          </dd>
        </div>
      </dl>
    </Section>
  );
}

/** The last few row edits made in the table editor, in words, with who. */
function RecentEdits({ project }: { project: string }) {
  const edits = useQuery(dq.edits(project));
  const list = (edits.data ?? []).slice(0, 5);
  if (edits.isError || (edits.isSuccess && list.length === 0)) return null;
  return (
    <Section
      id="db-edits"
      label="Recent edits"
      aside={
        <Link to="/projects/$project/history" params={{ project }} className="inline-flex items-center gap-1 hover:text-ink">
          <History className="size-3" aria-hidden />
          History
        </Link>
      }
    >
      {edits.isPending ? (
        <Skeleton className="h-32" />
      ) : (
        <ul className="divide-y divide-rule border-y border-rule">
          {list.map((e) => (
            <li key={e.id} className="flex items-start gap-2.5 py-2.5">
              <ActorMark actor={e.actor} className="mt-px" />
              <span className="min-w-0 flex-1">
                <span className={cn("block text-sm", e.undoneBy ? "text-ink-3 line-through" : "text-ink")}>{editWords(e)}</span>
                <span className="block text-xs text-ink-3">
                  {e.actor.name || e.actor.id} · {relative(e.at)}
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
