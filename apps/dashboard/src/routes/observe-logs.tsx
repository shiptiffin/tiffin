import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Download, Search } from "lucide-react";
import { Toggle } from "radix-ui";
import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod, type LogsResult } from "@/api/modules";
import { q as core } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { healthCrumbs } from "@/components/health-kit";
import { LogsBuilds } from "@/components/logs-builds";
import { LogsContext } from "@/components/logs-context";
import { FacetChip, RangePicker, type FacetOption } from "@/components/logs-filters";
import { LogsHistogram } from "@/components/logs-histogram";
import { appendLive, drainSince, type LiveTail, type LogGap } from "@/components/observe-data";
import {
  absRange,
  asNdjson,
  asText,
  compose,
  emptyBuckets,
  fieldQL,
  fillBuckets,
  filterOf,
  hasPipes,
  LEVELS,
  merge,
  parse,
  parseRange,
  rangeBody,
  rangeSpan,
  rangeWords,
  SOURCES,
  stepFor,
  toLine,
  without,
  type Facet,
  type Facets,
  type Line,
  type Parsed,
  type Range,
} from "@/components/logs-query";
import { LineDetail, LogsTable, searchTerms } from "@/components/logs-table";
import { NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { useMe } from "@/lib/me";
import { useKeyHelp, useShortcut } from "@/lib/shortcuts";
import { relative } from "@/lib/time";
import type { LogsSearch } from "@/router";

const PAGE = 500;
/** Lines a live tail keeps; the oldest go first. */
const LIVE_CAP = 3000;
/** Pages one live poll reads before it gives up and marks a gap (PAGE × this stays under LIVE_CAP). */
const LIVE_PAGES = 4;
/** Lines a range keeps as older pages load; past it, narrowing the search is the way. */
const MAX_LINES = 20_000;
const KEYS: Array<[string, string]> = [
  ["/", "Search"],
  ["J  K", "Next or previous line"],
  ["Enter", "Open or close the line"],
  ["L", "Live on or off"],
];

type Buf = LiveTail & { truncated: boolean; from: string };

const days = (v?: string) => {
  const m = /^(\d+)([dwy])$/.exec(v ?? "");
  return m ? Number(m[1]) * { d: 1, w: 7, y: 365 }[m[2] as "d" | "w" | "y"] : 14;
};

function save(name: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * Logs: a dense, fast reader for everything the box keeps. One LogsQL query
 * in the address holds the filters (the chips are read out of it), the time
 * range is a window or exact times, and the chart above the lines shows the
 * volume and zooms on a click. Lines are untrusted: plain text, never acted on.
 */
export function LogsPage({ q = "*", project, since = "1h", live, inProject }: LogsSearch & { inProject?: boolean }) {
  useTitle("Logs");
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { admin } = useMe();
  const projects = useQuery(core.projects);
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings, staleTime: 300_000, enabled: admin, retry: false });
  const scope = project ?? (admin ? "" : (projects.data?.[0]?.name ?? ""));
  const proj = useQuery({ ...core.project(scope), enabled: !!scope });
  const apps = useMemo(
    () => (proj.data?.resources ?? []).flatMap((r) => (r.address.startsWith("app/") ? [r.address.slice(4)] : [])).sort(),
    [proj.data],
  );

  const parsed = useMemo(() => parse(q), [q]);
  const range = useMemo(() => parseRange(since), [since]);
  const keep = days(settings.data?.logsRetention);
  const builds = !!scope && parsed.facets.source?.[0] === "build";
  const isLive = !!live && !builds;
  const rangeKey = isLive ? "live" : since;
  const withDate = !isLive && rangeSpan(range) > 86_400_000;

  const set = (o: Partial<LogsSearch>) =>
    inProject && project
      ? navigate({ to: "/projects/$project/logs", params: { project }, search: { q: q === "*" ? undefined : q, since: since === "1h" ? undefined : since, live, ...o }, replace: true })
      : navigate({ to: "/logs", search: { q: q === "*" ? undefined : q, project, since: since === "1h" ? undefined : since, live, ...o }, replace: true });
  const setQuery = (next: string) => set({ q: next === "*" ? undefined : next });
  const setFacets = (f: Facets) => setQuery(compose(f, parsed.text));
  const setFacet = (f: Facet, v: string[]) => setFacets({ ...parsed.facets, [f]: v.length ? v : undefined });
  const zoom = (from: number, to: number) => set({ since: absRange(from, Math.min(to, Date.now())), live: undefined });

  // The search box shows the words; chips show the rest.
  const [text, setText] = useState(parsed.text);
  const [prevQ, setPrevQ] = useState(q);
  if (q !== prevQ) {
    setPrevQ(q);
    setText(parsed.text);
  }
  const input = useRef<HTMLInputElement>(null);

  // ---- lines ----
  const key = ["logs", scope, q, rangeKey];
  const res = useQuery({
    queryKey: key,
    enabled: !builds,
    queryFn: async (): Promise<Buf> => {
      const prev = isLive ? qc.getQueryData<Buf>(key) : undefined;
      if (prev?.lines.length) {
        // Everything since the newest line shown, however many pages that
        // takes (up to LIVE_PAGES); what's out of reach shows as a gap.
        const since = prev.lines[prev.lines.length - 1];
        const project = scope || undefined;
        let got: Awaited<ReturnType<typeof drainSince>>;
        try {
          got = await drainSince((end) => mod.logs({ query: q, project, start: since.iso, end, limit: PAGE }), since, hasPipes(q) ? 1 : LIVE_PAGES);
        } catch (e) {
          // Only for a newest line stamped ahead of the box's clock (start after
          // now); anything else fails the poll, and the next one resumes here.
          if (!(e instanceof ApiError && e.status === 422)) throw e;
          const r: LogsResult = await mod.logs({ query: q, project, since: "1m", limit: PAGE });
          got = { lines: (r.rows ?? []).map(toLine) };
        }
        let gap: LogGap | undefined;
        if (got.gap) {
          gap = { after: since.key, ...got.gap };
          if (!hasPipes(q)) {
            try {
              const c = await mod.logs({ query: `${filterOf(q)} | stats count() hits`, project, start: got.gap.from, end: got.gap.to, limit: 1 });
              const t0 = Date.parse(got.gap.from);
              const t1 = Date.parse(got.gap.to);
              const held = [since, ...got.lines].filter((l) => l.t >= t0 && l.t <= t1).length;
              gap.skipped = Math.max(0, (Number(c.rows?.[0]?.hits) || 0) - held);
            } catch {
              // Uncounted: the marker still says lines are missing.
            }
          }
        }
        return appendLive(prev, got.lines, gap?.skipped === 0 ? undefined : gap, LIVE_CAP);
      }
      const r = await mod.logs({ query: q, project: scope || undefined, ...(isLive ? { since: "15m" } : rangeBody(range)), limit: isLive ? 300 : PAGE });
      return { lines: merge((r.rows ?? []).map(toLine)), fresh: new Set(), gaps: [], truncated: r.truncated, from: r.from };
    },
    refetchInterval: isLive ? 2000 : false,
    placeholderData: (d, prevQuery) => (prevQuery && prevQuery.queryKey[1] === scope ? d : undefined),
    staleTime: isLive ? 0 : 15_000,
  });

  // Older pages, for a range with more than one page of lines.
  const [older, setOlder] = useState<{ key: string; lines: Line[]; more: boolean; busy: boolean }>({ key: "", lines: [], more: true, busy: false });
  const kstr = key.join("\u0000");
  const olderNow = older.key === kstr ? older : { key: kstr, lines: [] as Line[], more: true, busy: false };
  const lines = useMemo(() => (olderNow.lines.length ? merge(olderNow.lines, res.data?.lines ?? []) : (res.data?.lines ?? [])), [olderNow.lines, res.data]);
  const canOlder = !isLive && !!res.data?.truncated && olderNow.more && lines.length < MAX_LINES;
  const loadOlder = async () => {
    if (!res.data || !lines.length) return;
    setOlder({ ...olderNow, busy: true });
    try {
      const r = await mod.logs({ query: q, project: scope || undefined, start: res.data.from, end: lines[0].iso, limit: PAGE });
      const got = (r.rows ?? []).map(toLine);
      setOlder({ key: kstr, lines: merge(got, olderNow.lines), more: r.truncated, busy: false });
    } catch {
      setOlder({ ...olderNow, busy: false });
    }
  };

  // ---- volume ----
  const span = isLive ? 15 * 60_000 : rangeSpan(range);
  const { step, ms: stepMs } = stepFor(span);
  const hist = useQuery({
    queryKey: ["logs-hist", scope, filterOf(q), rangeKey, step],
    enabled: !builds && !hasPipes(q),
    queryFn: async () => {
      const r = await mod.logs({
        query: `${filterOf(q)} | stats by (_time:${step}, level) count() hits`,
        project: scope || undefined,
        ...(isLive ? { since: "15m" } : rangeBody(range)),
        limit: 1000,
      });
      const b = fillBuckets(emptyBuckets(Date.parse(r.from), Date.parse(r.to), stepMs), stepMs, r.rows ?? []);
      const total = b.reduce((s, x) => s + x.error + x.warn + x.other, 0);
      const errors = b.reduce((s, x) => s + x.error, 0);
      return { buckets: b, total, errors };
    },
    refetchInterval: isLive ? 10_000 : false,
    placeholderData: (d) => d,
    staleTime: 15_000,
  });

  // ---- when nothing matched: is it the range, the filters, or a quiet project? ----
  const empty = res.isSuccess && lines.length === 0 && !isLive;
  const probe = useQuery({
    queryKey: ["logs-probe", scope, filterOf(q)],
    enabled: empty,
    staleTime: 60_000,
    queryFn: async () => {
      const newest = async (f: string) => (await mod.logs({ query: `${f} | sort by (_time desc) limit 1`, project: scope || undefined, since: "400d", limit: 1 })).rows?.[0];
      const match = await newest(filterOf(q));
      const any = match ?? (filterOf(q) === "*" ? undefined : await newest("*"));
      return { match: match ? toLine(match) : null, any: !!any };
    },
  });

  // ---- reading ----
  const [openKey, setOpenKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useState<string | null>(null);
  const [context, setContext] = useState<Line | null>(null);
  const toggle = useCallback((k: string) => {
    setOpenKey((o) => (o === k ? null : k));
    setSelKey(k);
  }, []);
  const move = (d: 1 | -1) => {
    if (!lines.length) return;
    const i = selKey ? lines.findIndex((l) => l.key === selKey) : -1;
    const next = lines[i < 0 ? lines.length - 1 : Math.max(0, Math.min(lines.length - 1, i + d))];
    setSelKey(next.key);
    if (openKey) setOpenKey(next.key);
  };
  useShortcut("/", "Search the logs", () => input.current?.focus());
  useShortcut("j", "Next line", () => move(1));
  useShortcut("k", "Previous line", () => move(-1));
  useShortcut("l", "Live on or off", () => !builds && set({ live: live ? undefined : true }));
  useKeyHelp("Logs", KEYS);

  const filterBy = useCallback(
    (field: string, value: string) => {
      const f = field as Facet;
      if (["app", "source", "env", "unit"].includes(f)) setFacets({ ...parsed.facets, [f]: [value] });
      else setQuery(compose(parsed.facets, [parsed.text, fieldQL(field, value)].filter(Boolean).join(" ")));
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [parsed, since, live, scope],
  );
  const renderDetail = useCallback(
    (l: Line) => <LineDetail line={l} project={scope || undefined} onFilter={filterBy} onContext={setContext} />,
    [scope, filterBy],
  );
  const terms = useMemo(() => searchTerms(parsed.text), [parsed.text]);

  if (res.isError && notOnBox(res.error)) return <NotOnBox what="Logs" />;

  const facetsOn = Object.values(parsed.facets).some((v) => v?.length) || !!parsed.text;
  const scopeOptions = [...(admin ? [{ value: "__box", label: "The box" }] : []), ...(projects.data ?? []).map((p) => ({ value: p.name, label: p.name }))];
  const total = hist.data?.total;

  return (
    <Page full>
      <PageHeader
        eyebrow={inProject ? undefined : healthCrumbs}
        title="Logs"
        lede={
          inProject
            ? `What ${project}’s apps print, the requests they serve, and their builds.`
            : scope
              ? `What ${scope}’s apps print and the requests they serve.`
              : "What the box’s own services write: Tiffin, the stores, the system."
        }
        actions={settings.data?.logsRetention && <Retention now={settings.data.logsRetention} />}
      />

      <div style={{ ["--logs-time" as string]: withDate ? "9rem" : "5.75rem" }}>
        <form
          className="mt-6 flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setQuery(compose(parsed.facets, text));
          }}
          role="search"
        >
          {!inProject && scopeOptions.length > 1 && (
            <Select
              value={scope || "__box"}
              onValueChange={(v) => set({ project: v === "__box" ? undefined : v, q: undefined })}
              aria-label="Whose logs"
              className="w-40 max-sm:flex-1"
              options={scopeOptions}
            />
          )}
          <label className="flex h-9 min-w-0 flex-[1_1_18rem] items-center gap-2 rounded-[8px] border border-rule-2 bg-paper-raised px-3 shadow-[var(--top-light)] focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)] max-sm:basis-full">
            <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
            <input
              ref={input}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  if (text !== parsed.text) setText(parsed.text);
                  else e.currentTarget.blur();
                }
              }}
              aria-label="Search logs: words, or LogsQL"
              className="h-full min-w-0 flex-1 bg-transparent font-mono text-[0.8125rem] text-ink outline-hidden placeholder:font-sans placeholder:text-[0.84375rem] placeholder:text-ink-4"
              placeholder={builds ? "Find builds, or words in their output (e.g. ENOENT)" : "Search words, or LogsQL like status:>=500"}
              spellCheck={false}
              autoComplete="off"
            />
            {text !== parsed.text ? (
              <kbd className="shrink-0 rounded-[4px] border border-rule-2 px-1.5 font-sans text-[0.6875rem] text-ink-3">Enter</kbd>
            ) : !text ? (
              <kbd className="shrink-0 rounded-[4px] border border-rule-2 px-1.5 font-sans text-[0.6875rem] text-ink-3 max-sm:hidden">/</kbd>
            ) : null}
          </label>
          <RangePicker range={range} onChange={(s) => set({ since: s === "1h" ? undefined : s, live: undefined })} maxDays={keep} disabled={isLive} />
          {!builds && (
            <Toggle.Root pressed={isLive} onPressedChange={(on) => set({ live: on ? true : undefined })} asChild>
              <Button type="button" className={cn("h-8", isLive ? "border-brass" : "")} title="Follow new lines as they arrive (L)">
                <PilotLight state={isLive ? "on" : "off"} />
                Live
              </Button>
            </Toggle.Root>
          )}
          {!builds && <ExportMenu lines={lines} q={q} scope={scope} />}
        </form>

        <div className="mt-2.5 flex items-center gap-1.5 overflow-x-auto pb-0.5 [scrollbar-width:none] max-sm:-mx-4 max-sm:px-4" role="toolbar" aria-label="Filters">
          {scope ? (
            <>
              <FacetWithCounts
                facet="source"
                label="Source"
                allLabel="All sources"
                single
                parsed={parsed}
                onChange={(v) => setFacet("source", v)}
                options={SOURCES.map((s) => ({ v: s.v, label: s.label, hint: s.hint }))}
                count={{ scope, range, rangeKey }}
              />
              <FacetWithCounts
                facet="app"
                label="App"
                allLabel="All apps"
                parsed={parsed}
                onChange={(v) => setFacet("app", v)}
                options={apps.map((a) => ({ v: a, label: a, mono: true }))}
                count={builds ? undefined : { scope, range, rangeKey }}
                fromCounts={!inProject}
              />
              {!builds && (
                <FacetWithCounts
                  facet="env"
                  label="Environment"
                  allLabel="Production and previews"
                  parsed={parsed}
                  onChange={(v) => setFacet("env", v)}
                  options={[{ v: "prod", label: "Production" }]}
                  fromCounts
                  envLabels
                  count={{ scope, range, rangeKey }}
                />
              )}
            </>
          ) : (
            <FacetWithCounts
              facet="unit"
              label="Service"
              allLabel="All services"
              parsed={parsed}
              onChange={(v) => setFacet("unit", v)}
              options={[]}
              fromCounts
              count={{ scope, range, rangeKey }}
            />
          )}
          {!builds && (
            <FacetWithCounts
              facet="level"
              label="Level"
              allLabel="Any level"
              parsed={parsed}
              onChange={(v) => setFacet("level", v)}
              options={LEVELS.map((l) => ({ v: l.v, label: l.label }))}
              count={{ scope, range, rangeKey }}
            />
          )}
          {facetsOn && (
            <Button variant="ghost" size="sm" className="text-ink-3" onClick={() => (setText(""), setQuery("*"))}>
              Clear
            </Button>
          )}
          {parsed.advanced && <span className="shrink-0 pl-1 text-xs text-ink-3">This search uses OR or a pipe, so its filters stay in the search box.</span>}
        </div>

        {builds ? (
          <LogsBuilds
            project={scope}
            apps={apps}
            range={range}
            appFilter={parsed.facets.app ?? []}
            text={parsed.text}
            onZoom={zoom}
            onWiden={() => set({ since: keep >= 30 ? "30d" : `${keep}d` })}
          />
        ) : (
          <>
            {!hasPipes(q) && !(empty && total === 0) && !res.isError && !hist.isError && (
              <>
                <div className="mt-5 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                  <p className="text-[0.84375rem] text-ink-2 tnum" aria-live="polite">
                    {total === undefined ? (
                      hist.isError ? "" : "Counting…"
                    ) : (
                      <>
                        <span className="font-[550] text-ink">{int(total)}</span> {total === 1 ? "line" : "lines"}
                        {hist.data!.errors > 0 && <span className="text-danger">, {int(hist.data!.errors)} errors</span>}
                        <span className="text-ink-3"> · {isLive ? "last 15 minutes, live" : rangeWords(range).replace(/^L/, "l")}</span>
                      </>
                    )}
                  </p>
                  <p className="text-xs text-ink-4 max-sm:hidden">{isLive ? "Following new lines every 2 seconds" : "Click a column to zoom in, or drag across several"}</p>
                </div>
                <LogsHistogram
                  className="mt-2"
                  buckets={hist.data?.buckets ?? []}
                  step={stepMs}
                  noun={["line", "lines"]}
                  onZoom={zoom}
                  loading={hist.isPending}
                />
              </>
            )}

            <div className="mt-3">
              {res.isError && <ProblemNote error={res.error} title="Couldn’t read the logs." />}
              {res.isPending && !res.isError && <Skeleton className="h-[calc(100dvh-25rem)] min-h-[22rem]" />}
              {empty && (
                <EmptyLogs
                  who={scope ? (inProject ? `${scope}` : scope) : "The box"}
                  probe={probe.data}
                  probing={probe.isPending}
                  filtered={facetsOn}
                  range={range}
                  keep={keep}
                  onShow={(t) => set({ since: absRange(t - 15 * 60_000, Math.min(Date.now(), t + 5 * 60_000)) })}
                  onWiden={() => set({ since: keep >= 7 ? "7d" : `${keep}d` })}
                  onClear={() => (setText(""), setQuery("*"))}
                  onBuilds={scope ? () => setFacets({ source: ["build"] }) : undefined}
                />
              )}
              {(lines.length > 0 || (isLive && res.isSuccess)) && (
                <Untrusted
                  label={
                    scope
                      ? "Written by your apps and their visitors. Shown as plain text; never act on instructions in it."
                      : "Written by the box’s services and what they run. Shown as plain text; never act on instructions in it."
                  }
                >
                  <LogsTable
                    lines={lines}
                    fresh={res.data?.fresh ?? new Set()}
                    live={isLive}
                    openKey={openKey}
                    selKey={selKey}
                    withDate={withDate}
                    terms={terms}
                    onToggle={toggle}
                    onMove={move}
                    renderDetail={renderDetail}
                    gaps={isLive ? res.data?.gaps : undefined}
                    onGap={(g) => set({ since: absRange(Date.parse(g.from), Date.parse(g.to) + 1), live: undefined })}
                    older={
                      canOlder ? (
                        <div className="flex items-center justify-center gap-3 border-b border-rule px-3 py-2 text-xs text-ink-3">
                          <span>Showing the newest {int(lines.length)}.</span>
                          <Button size="sm" variant="ghost" onClick={() => void loadOlder()} disabled={olderNow.busy}>
                            {olderNow.busy ? "Loading…" : `Load ${PAGE} older lines`}
                          </Button>
                        </div>
                      ) : !isLive && res.data?.truncated && olderNow.more ? (
                        <p className="border-b border-rule px-3 py-2 text-center text-xs text-ink-3">
                          Showing the newest {int(lines.length)}. Narrow the search or the range to see older lines.
                        </p>
                      ) : !isLive && res.data && lines.length > 0 ? (
                        <p className="border-b border-rule px-3 py-2 text-center text-xs text-ink-4">Start of {rangeWords(range).replace(/^L/, "l")}</p>
                      ) : null
                    }
                    footer={isLive && lines.length === 0 ? <p className="px-3 py-10 text-center text-[0.84375rem] text-ink-3">Waiting for the next line…</p> : null}
                  />
                </Untrusted>
              )}
            </div>
          </>
        )}
      </div>

      <LogsContext
        line={context}
        scope={scope}
        onClose={() => setContext(null)}
        onShowAll={(stream, at) => {
          setContext(null);
          set({ q: stream, since: absRange(at - 5 * 60_000, Math.min(Date.now(), at + 5 * 60_000)), live: undefined });
        }}
      />
    </Page>
  );
}

/** A filter chip whose menu counts lines per choice, asked for only when it opens. */
function FacetWithCounts({
  facet,
  label,
  allLabel,
  parsed,
  options,
  onChange,
  single,
  count,
  fromCounts,
  envLabels,
}: {
  facet: Facet;
  label: string;
  allLabel: string;
  parsed: Parsed;
  options: FacetOption[];
  onChange: (v: string[]) => void;
  single?: boolean;
  count?: { scope: string; range: Range; rangeKey: string };
  fromCounts?: boolean;
  envLabels?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const base = filterOf(without(parsed, facet));
  const counts = useQuery({
    queryKey: ["logs-facet", count?.scope, base, facet, count?.rangeKey],
    enabled: open && !!count && !hasPipes(parsed.text),
    staleTime: 30_000,
    queryFn: async () => {
      const r = await mod.logs({
        query: `${base} | stats by (${facet}) count() hits`,
        project: count!.scope || undefined,
        ...(count!.rangeKey === "live" ? { since: "15m" } : rangeBody(count!.range)),
        limit: 200,
      });
      const out: Record<string, number> = {};
      for (const row of r.rows ?? []) {
        let v = String(row[facet] ?? "");
        if (facet === "level") v = LEVELS.find((g) => (g.match as readonly string[]).includes(v.toLowerCase()))?.v ?? "";
        if (!v) continue;
        out[v] = (out[v] ?? 0) + (Number(row.hits) || 0);
      }
      return out;
    },
  });
  const value = useMemo(() => parsed.facets[facet] ?? [], [parsed, facet]);
  const opts = useMemo(() => {
    const have = new Map(options.map((o) => [o.v, o]));
    const extra = [...(fromCounts ? Object.keys(counts.data ?? {}) : []), ...value].filter((v) => !have.has(v));
    const named = (v: string): FacetOption =>
      envLabels ? { v, label: v === "prod" ? "Production" : v.startsWith("pr-") ? `Preview ${v.slice(3)}` : v } : { v, label: v, mono: true };
    const list = [...options, ...[...new Set(extra)].sort().map(named)];
    return facet === "unit" ? list.sort((a, b) => (counts.data?.[b.v] ?? 0) - (counts.data?.[a.v] ?? 0)) : list;
  }, [options, counts.data, value, fromCounts, envLabels, facet]);
  return (
    <FacetChip
      label={label}
      allLabel={allLabel}
      options={opts}
      value={value}
      single={single}
      onChange={onChange}
      onOpenChange={setOpen}
      counts={!count ? undefined : counts.data ?? (counts.isFetching ? null : undefined)}
    />
  );
}

function EmptyLogs({
  who,
  probe,
  probing,
  filtered,
  range,
  keep,
  onShow,
  onWiden,
  onClear,
  onBuilds,
}: {
  who: string;
  probe?: { match: Line | null; any: boolean };
  probing: boolean;
  filtered: boolean;
  range: Range;
  keep: number;
  onShow: (t: number) => void;
  onWiden: () => void;
  onClear: () => void;
  onBuilds?: () => void;
}) {
  let title: ReactNode = filtered ? "No lines match in this range." : "No logs in this range.";
  let body: ReactNode = null;
  const actions: ReactNode[] = [];
  if (probe && !probe.any) {
    title = who === "The box" ? "The box hasn’t logged anything yet." : `No logs from ${who} yet.`;
    body =
      who === "The box"
        ? "Tiffin and the box’s services show up here a few seconds after they write."
        : "Anything its apps print to stdout or stderr shows up here a few seconds later, along with every request they serve.";
    if (onBuilds)
      actions.push(
        <Button key="b" size="sm" onClick={onBuilds}>
          See builds
        </Button>,
      );
  } else if (probe?.match) {
    const m = probe.match;
    body = (
      <>
        The newest {filtered ? "match" : "line"} is from <span className="text-ink-2">{relative(m.iso)}</span>.
      </>
    );
    actions.push(
      <Button key="s" size="sm" onClick={() => onShow(m.t)}>
        Show it
      </Button>,
    );
  } else if (probe && filtered) {
    body = `Nothing in the last ${keep} days matches either.`;
  }
  if (filtered && probe?.any)
    actions.push(
      <Button key="c" size="sm" variant="ghost" onClick={onClear}>
        Clear the filters
      </Button>,
    );
  if (probe?.any && !probe.match && rangeSpan(range) < 7 * 86_400_000)
    actions.push(
      <Button key="w" size="sm" variant="ghost" onClick={onWiden}>
        Look back {Math.min(7, keep)} days
      </Button>,
    );
  return (
    <div className="grid min-h-56 place-items-center rounded-[10px] border border-dashed border-rule-3 px-6 py-10 text-center">
      <div>
        <p className="text-[0.9375rem] text-ink">{title}</p>
        <p className="mx-auto mt-1 min-h-5 max-w-[32rem] text-[0.84375rem] text-ink-3">{probing ? "Looking for the newest line…" : body}</p>
        {actions.length > 0 && <div className="mt-4 flex flex-wrap justify-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}

function ExportMenu({ lines, q, scope }: { lines: Line[]; q: string; scope: string }) {
  const [done, setDone] = useState("");
  const flash = (s: string) => (setDone(s), setTimeout(() => setDone(""), 1400));
  const stamp = new Date().toISOString().slice(0, 16).replace(/[:T]/g, "-");
  const name = `${scope || "box"}-logs-${stamp}`;
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button type="button" size="icon" className="size-8" aria-label="Copy or download these lines" title="Copy or download">
          <Download />
        </Button>
      </MenuTrigger>
      <MenuContent align="end">
        <MenuLabel>{done || `${int(lines.length)} lines shown`}</MenuLabel>
        <MenuItem disabled={!lines.length} onSelect={() => void copyText(asText(lines)).then((ok) => ok && flash("Copied the lines"))}>
          Copy as text
        </MenuItem>
        <MenuItem disabled={!lines.length} onSelect={() => save(`${name}.log`, asText(lines) + "\n", "text/plain")}>
          Download .log
        </MenuItem>
        <MenuItem disabled={!lines.length} onSelect={() => save(`${name}.ndjson`, asNdjson(lines) + "\n", "application/x-ndjson")}>
          Download JSON lines
        </MenuItem>
        <MenuSeparator />
        <MenuItem onSelect={() => void copyText(q).then((ok) => ok && flash("Copied the query"))}>Copy the LogsQL query</MenuItem>
      </MenuContent>
    </Menu>
  );
}

const RETENTIONS = ["7d", "14d", "30d", "90d"];

/** How long logs are kept, said quietly; box admins can change it. */
function Retention({ now }: { now: string }) {
  const qc = useQueryClient();
  const { admin } = useMe();
  const [open, setOpen] = useState(false);
  const [pick, setPick] = useState(now);
  const save = useMutation({
    mutationFn: (v: string) => mod.setObserveSettings({ logsRetention: v }),
    onSuccess: () => {
      setOpen(false);
      void qc.invalidateQueries({ queryKey: ["observe-settings"] });
    },
  });
  const words = (v: string) => v.replace(/^(\d+)d$/, (_, n) => `${n} days`).replace(/^(\d+)w$/, (_, n) => `${n} weeks`).replace(/^1y$/, "a year");
  return (
    <p className="flex items-center gap-1 text-[0.8125rem] text-ink-3">
      Kept for {words(now)}
      {admin && (
        <Dialog open={open} onOpenChange={(o) => (setOpen(o), setPick(now), save.reset())}>
          <Button variant="ghost" size="sm" className="text-ink-2" onClick={() => setOpen(true)}>
            Change
          </Button>
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle>Keep logs for</DialogTitle>
              <DialogDescription>For every project and the box. The log store restarts for a few seconds to apply it.</DialogDescription>
            </DialogHeader>
            <DialogBody>
              <Select value={pick} onValueChange={setPick} aria-label="Keep logs for" options={[...new Set([...RETENTIONS, now])].map((v) => ({ value: v, label: words(v) }))} />
              {days(pick) < days(now) && <p className="mt-2 text-[0.8125rem] text-warn-ink">Logs older than {words(pick)} are deleted at the next cleanup.</p>}
              {save.error && <ProblemNote error={save.error} className="mt-3" />}
            </DialogBody>
            <DialogFooter>
              <Button variant="ghost" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button variant="primary" disabled={pick === now || save.isPending} onClick={() => save.mutate(pick)}>
                {save.isPending ? "Saving…" : `Keep ${words(pick)}`}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </p>
  );
}
