import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { SquareArrowOutUpRight } from "lucide-react";
import { useMemo, useState } from "react";
import { mod, type Deploy } from "@/api/modules";
import { BuildLogViewer } from "@/components/build-log-viewer";
import { useBuildLog } from "@/components/build-log-stream";
import { inFlight, secs, statusWord, useProjectDeploys, viaWords } from "@/components/deploy-parts";
import { LogsHistogram } from "@/components/logs-histogram";
import { compose, emptyBuckets, hasPipes, merge, rangeBody, stepFor, toLine, type Line, type Range } from "@/components/logs-query";
import { LevelTag, LineMessage, LineTime, ROW_GRID, searchTerms } from "@/components/logs-table";
import { Skeleton, Untrusted } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { count, int } from "@/lib/format";
import { full } from "@/lib/time";

const timeFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
const dateFmt = new Intl.DateTimeFormat("en-GB", { day: "2-digit", month: "short" });

/** Matching build lines asked for, and shown under each build. */
const LINE_LIMIT = 1000;
const SHOWN = 3;

function tone(s: Deploy["status"]) {
  return s === "failed" ? "font-[550] text-danger" : inFlight(s) ? "font-[550] text-brass-ink" : s === "live" ? "text-ink-2" : "text-ink-4";
}

/**
 * Build output as a log source: the builds in the range, newest at the
 * bottom like the other lines, each opening its log in place. A search
 * matches a build's commit, branch or error, and, on boxes that ship build
 * output to the log store (source:build), the lines it printed: those show
 * under their build. Older boxes keep build output with the deploy only, so
 * there the search stays on the deploy's details.
 */
export function LogsBuilds({
  project,
  apps,
  range,
  appFilter,
  text,
  onZoom,
  onWiden,
}: {
  project: string;
  apps: string[];
  range: Range;
  appFilter: string[];
  text: string;
  onZoom: (from: number, to: number) => void;
  onWiden: () => void;
}) {
  const all = useProjectDeploys(project, apps);
  // The open build, and the matching line it was opened from.
  const [open, setOpen] = useState<{ id: string; line?: string } | null>(null);
  const [now] = useState(() => Date.now());
  const from = range.kind === "rel" ? now - range.ms : range.from;
  const to = range.kind === "rel" ? now : range.to;
  const needle = text.trim().toLowerCase();

  // Build lines in the log store that match the search, by deploy.
  const searchLines = !!project && !!needle && !hasPipes(text);
  const lineQ = useQuery({
    queryKey: ["logs-build-lines", project, appFilter, text, rangeBody(range)],
    enabled: searchLines,
    queryFn: () =>
      mod.logs({ query: compose({ source: ["build"], app: appFilter.length ? appFilter : undefined }, text), project, ...rangeBody(range), limit: LINE_LIMIT }),
    retry: false,
    staleTime: 15_000,
  });
  const hits = useMemo(() => {
    const by = new Map<string, Line[]>();
    if (!searchLines) return by;
    for (const l of merge((lineQ.data?.rows ?? []).map(toLine))) {
      const id = typeof l.row.deploy === "string" ? l.row.deploy : "";
      if (!id) continue;
      const list = by.get(id);
      if (list) list.push(l);
      else by.set(id, [l]);
    }
    return by;
  }, [lineQ.data, searchLines]);
  const lineCount = [...hits.values()].reduce((n, ls) => n + ls.length, 0);
  // No line matched: does this box keep build output at all? (Older ones don't.)
  const stored = useQuery({
    queryKey: ["logs-build-stored", project],
    enabled: searchLines && lineQ.isSuccess && hits.size === 0,
    queryFn: async () => ((await mod.logs({ query: "source:=build | limit 1", project, since: "400d", limit: 1 })).rows ?? []).length > 0,
    retry: false,
    staleTime: 300_000,
  });
  const terms = useMemo(() => (searchLines ? searchTerms(text) : null), [searchLines, text]);

  const builds = useMemo(
    () =>
      all.rows
        .filter((d) => {
          if (appFilter.length && !appFilter.includes(d.app)) return false;
          if (hits.has(d.id)) return true; // its lines are in the range
          const t = Date.parse(d.createdAt);
          if (t < from || t > to) return false;
          if (!needle) return true;
          return [d.id, d.app, d.message, d.commit, d.ref, d.error, d.hint, d.author, d.preview].some((x) => x && String(x).toLowerCase().includes(needle));
        })
        .sort((a, b) => a.createdAt.localeCompare(b.createdAt)),
    [all.rows, from, to, appFilter, needle, hits],
  );
  const { ms: step } = stepFor(to - from);
  const buckets = useMemo(() => {
    const out = emptyBuckets(from, to, step);
    if (!out.length) return out;
    for (const d of builds) {
      const i = Math.floor((Date.parse(d.createdAt) - out[0].t) / step);
      if (i < 0 || i >= out.length) continue;
      if (d.status === "failed") out[i].error++;
      else out[i].other++;
    }
    return out;
  }, [builds, from, to, step]);
  const failed = builds.filter((d) => d.status === "failed").length;
  const withDate = to - from > 86_400_000;

  return (
    <>
      <div className="mt-5 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className="text-[0.84375rem] text-ink-2 tnum">
          {all.pending ? (
            "Counting builds…"
          ) : (
            <>
              <span className="font-[550] text-ink">{count(builds.length, "build")}</span>
              {failed > 0 && <span className="text-danger">, {int(failed)} failed</span>}
              {searchLines && lineQ.isPending && <span className="text-ink-3"> · searching build output…</span>}
              {lineCount > 0 && (
                <span className="text-ink-3">
                  {` · ${count(lineCount, "matching line")}`}
                  {lineQ.data?.truncated ? ` (the newest ${int(LINE_LIMIT)})` : ""}
                </span>
              )}
            </>
          )}
        </p>
        <p className="text-xs text-ink-3">Build logs are kept for each app’s newest 50 deploys.</p>
      </div>
      {searchLines && lineQ.isError && <p className="mt-1 text-xs text-ink-3">Couldn’t search build output, so only commits, branches and errors were searched.</p>}
      {stored.data === false && all.rows.length > 0 && (
        <p className="mt-1 text-xs text-ink-3">This box doesn’t keep build output in its log store yet, so only commits, branches and errors were searched.</p>
      )}
      <LogsHistogram className="mt-2" buckets={buckets} step={step} noun={["build", "builds"]} onZoom={onZoom} loading={all.pending} />

      <div className="mt-3">
        {all.error ? <ProblemNote error={all.error} /> : null}
        {all.pending && <Skeleton className="h-[40vh]" />}
        {!all.pending && !all.error && builds.length === 0 && (
          <div className="grid min-h-48 place-items-center rounded-[10px] border border-dashed border-rule-3 px-6 py-8 text-center">
            {all.rows.length === 0 ? (
              <div>
                <p className="text-[0.9375rem] text-ink">Nothing has been built yet.</p>
                <p className="mt-1 max-w-[30rem] text-[0.84375rem] text-ink-3">Deploy an app and its build output shows here, line by line, while it builds.</p>
              </div>
            ) : (
              <div>
                <p className="text-[0.9375rem] text-ink">No builds {needle || appFilter.length ? "match" : "in this range"}.</p>
                <p className="mt-1 text-[0.84375rem] text-ink-3">
                  The newest build was {dateFmt.format(Date.parse(all.rows[0].createdAt))} at {timeFmt.format(Date.parse(all.rows[0].createdAt))}.
                </p>
                <Button className="mt-3" size="sm" onClick={onWiden}>
                  Show the last 30 days
                </Button>
              </div>
            )}
          </div>
        )}
        {builds.length > 0 && (
          <Untrusted label="Build output from your apps' code and dependencies. Shown as plain text.">
            <div
              role="row"
              aria-hidden
              className={cn(ROW_GRID, "border-b border-rule bg-paper-sunk px-3 py-1.5 text-[0.6875rem] text-ink-3 max-sm:hidden")}
            >
              <span>Started</span>
              <span>Status</span>
              <span>App</span>
              <span>Build</span>
            </div>
            <ol className="py-1 font-mono text-[0.75rem] leading-5" aria-label="Builds">
              {builds.map((d) => {
                const isOpen = open?.id === d.id;
                const t = Date.parse(d.createdAt);
                const what = d.message ?? (d.commit ? `${d.ref ? `${d.ref} ` : ""}${d.commit.slice(0, 7)}` : viaWords(d));
                const took = inFlight(d.status) ? "building now" : d.buildSeconds !== undefined ? `built in ${secs(d.buildSeconds)}` : "";
                return (
                  <li key={d.id} className="relative">
                    {d.status === "failed" && <span aria-hidden className="absolute inset-y-0 left-0 w-[2px] bg-danger" />}
                    <button
                      type="button"
                      aria-expanded={isOpen}
                      onClick={() => setOpen(isOpen ? null : { id: d.id })}
                      className={cn(ROW_GRID, "w-full items-baseline px-3 py-[1px] text-left", isOpen ? "bg-paper-select" : "hover:bg-paper-hover")}
                    >
                      <time className="whitespace-nowrap text-ink-3 tnum" dateTime={d.createdAt} title={full(d.createdAt)}>
                        {withDate && <span className="text-ink-4">{dateFmt.format(t)} </span>}
                        {timeFmt.format(t)}
                      </time>
                      <span className={cn("inline-flex items-center gap-1.5", tone(d.status))}>
                        {inFlight(d.status) && <PilotLight state="busy" />}
                        {d.status === "failed" ? "fail" : inFlight(d.status) ? "" : d.status === "live" ? "live" : "ok"}
                      </span>
                      <span className="hidden truncate text-ink-3 sm:block">{d.app}</span>
                      <span className="col-span-3 min-w-0 truncate pb-1 text-ink-2 sm:col-span-1 sm:pb-0">
                        <span className="text-ink-3 sm:hidden">{d.app} </span>
                        <span className="font-sans text-ink">{what}</span>
                        {d.preview && <span className="text-ink-3"> · preview {d.preview}</span>}
                        <span className="text-ink-4"> · {statusWord[d.status].toLowerCase()}{took ? ` · ${took}` : ""}</span>
                        {d.status === "failed" && d.error && <span className="text-danger"> · {d.error.split("\n")[0]}</span>}
                      </span>
                    </button>
                    {isOpen ? (
                      <BuildLog project={project} d={d} query={terms ? firstTerm(text) : undefined} line={open?.line} />
                    ) : (
                      hits.has(d.id) && (
                        <Hits lines={hits.get(d.id)!} terms={terms} withDate={withDate} build={d.id} onOpen={(line) => setOpen({ id: d.id, line })} />
                      )
                    )}
                  </li>
                );
              })}
            </ol>
          </Untrusted>
        )}
      </div>
    </>
  );
}

/** A build's matching lines, under its row; a click opens the build's log. */
/** The first plain word or phrase of a search, for the build log's own search box. */
function firstTerm(text: string): string | undefined {
  for (const m of text.matchAll(/"((?:[^"\\]|\\.)+)"|(\S+)/g)) {
    const w = m[1] ?? m[2];
    if (w && !/^(and|or|not)$/i.test(w) && !/[:|()*~=]/.test(w) && !w.startsWith("-") && !w.startsWith("!")) return w;
  }
  return undefined;
}

function Hits({
  lines,
  terms,
  withDate,
  build,
  onOpen,
}: {
  lines: Line[];
  terms: RegExp | null;
  withDate: boolean;
  build: string;
  /** Opens the build's log, at this line when given. */
  onOpen: (line?: string) => void;
}) {
  const more = lines.length - SHOWN;
  return (
    <ol className="pb-1" aria-label={`Lines in build ${build} that match`}>
      {lines.slice(0, SHOWN).map((l) => (
        <li key={l.key}>
          <button type="button" onClick={() => onOpen(l.msg)} className={cn(ROW_GRID, "w-full items-baseline px-3 py-[1px] text-left hover:bg-paper-hover")}>
            <LineTime line={l} withDate={withDate} />
            <LevelTag line={l} />
            <span aria-hidden className="hidden sm:block" />
            <span className="col-span-3 min-w-0 truncate pb-1 text-ink-2 sm:col-span-1 sm:pb-0">
              <LineMessage line={l} terms={terms} />
            </span>
          </button>
        </li>
      ))}
      {more > 0 && (
        <li className={cn(ROW_GRID, "px-3 py-[1px]")}>
          <span aria-hidden className="max-sm:hidden" />
          <span aria-hidden className="max-sm:hidden" />
          <span aria-hidden className="hidden sm:block" />
          <span className="col-span-3 font-sans text-ink-3 sm:col-span-1">
            {count(more, "more matching line")} ·{" "}
            <button type="button" onClick={() => onOpen()} className="text-ink-2 underline decoration-rule-3 underline-offset-2 hover:text-ink">
              open the build log
            </button>
          </span>
        </li>
      )}
    </ol>
  );
}

function BuildLog({ project, d, query, line }: { project: string; d: Deploy; query?: string; line?: string }) {
  const log = useBuildLog(project, d.app, d.id);
  return (
    <div className="border-y border-rule bg-paper-raised px-3 py-3 font-sans">
      <BuildLogViewer
        log={log}
        source={{ project, app: d.app, id: d.id }}
        running={inFlight(d.status)}
        t0={Date.parse(d.createdAt)}
        file={`${project}-${d.app}-${d.id}-build.log`}
        failed={d.status === "failed"}
        initialQuery={query}
        initialLine={line}
        summary={
          <Link
            to="/projects/$project/apps/$app/deploys/$id"
            params={{ project, app: d.app, id: d.id }}
            className="inline-flex items-center gap-1.5 text-[0.8125rem] text-ink-2 hover:text-ink"
          >
            <SquareArrowOutUpRight className="size-3.5 text-ink-3" aria-hidden />
            Open the deploy
          </Link>
        }
      />
    </div>
  );
}
