import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError, request, type Approval, type Change, type Tier } from "@/api/client";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { dayWords, splitIntent, tokenWho, useApprovalsByChange } from "@/components/ledger-parts";
import { WorkflowRow } from "@/components/ledger-workflow";
import { TiffinMark } from "@/components/logo";
import { Page } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { RiskDots } from "@/components/risk-dots";
import { SignedEntry } from "@/components/signed-entry";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { asTier, intentWords, opCounts, splitRequester, tierCopy } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { useEnamels } from "@/lib/enamel";
import { countWords, words } from "@/lib/format";
import { mcpCommand } from "@/lib/mcp";
import { clock, dayKey, dayLabel, relative } from "@/lib/time";
import { useWaitingWorkflowApprovals } from "@/lib/wf";
import { actorShown } from "@/lib/who";

export type ActivitySearch = { project?: string; risk?: Tier; who?: "people" | "agents" };

/** Entries per request; earlier ones arrive as you scroll (GET /v1/changes?before=<id>). */
const LIMIT = 100;

type LedgerPage = { items: Change[]; end: boolean; legacy?: boolean };

/**
 * The change log, a page at a time, newest first. A box from before the
 * cursor existed answers 422 to `before` (or ignores it and repeats the first
 * page): then the history simply ends where the first page does.
 */
function useLedger(project?: string) {
  return useInfiniteQuery({
    queryKey: ["changes", project ?? "", "ledger"],
    initialPageParam: "",
    queryFn: async ({ pageParam }): Promise<LedgerPage> => {
      const qs = new URLSearchParams({ limit: String(LIMIT) });
      if (project) qs.set("project", project);
      if (pageParam) qs.set("before", pageParam);
      try {
        const items = (await request<Change[] | null>("GET", `/v1/changes?${qs}`)) ?? [];
        return { items, end: items.length < LIMIT };
      } catch (e) {
        if (pageParam && e instanceof ApiError && (e.status === 422 || e.status === 400)) return { items: [], end: true, legacy: true };
        throw e;
      }
    },
    getNextPageParam: (last, pages) => {
      if (last.end || last.items.length === 0) return undefined;
      // An old box ignores `before` and sends the first page again.
      if (pages.length > 1 && pages[0].items[0]?.id === last.items[0]?.id) return undefined;
      return last.items[last.items.length - 1].id;
    },
  });
}

/** Every loaded entry once, in order. */
function flatten(pages: LedgerPage[]): { all: Change[]; legacy: boolean } {
  const seen = new Set<string>();
  const all: Change[] = [];
  let legacy = false;
  pages.forEach((p, n) => {
    if (p.legacy) legacy = true;
    if (n > 0 && p.items[0] && p.items[0].id === pages[0].items[0]?.id) legacy = true;
    for (const c of p.items) {
      if (seen.has(c.id)) continue;
      seen.add(c.id);
      all.push(c);
    }
  });
  return { all, legacy };
}

const isAgent = (c: Change) => c.actor.kind === "agent";

export function ActivityPage({ search }: { search: ActivitySearch }) {
  const { project, risk, who } = search;
  useTitle(project ? `${project} · Ledger` : "Ledger");
  const changes = useLedger(project);
  const pending = useQuery({ ...q.pending, retry: false });
  const names = useQuery({ ...q.tokenNames, retry: false });
  const projects = useQuery(q.projects);
  const wf = useWaitingWorkflowApprovals();
  const signedBy = useApprovalsByChange();
  const projectNames = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const enamels = useEnamels(projectNames);

  if (changes.isPending) return <Skeleton />;
  if (changes.isError)
    return (
      <Page>
        <ProblemNote error={changes.error} title="Couldn’t load the Ledger" />
      </Page>
    );

  const { all, legacy } = flatten(changes.data.pages);
  const first = changes.data.pages[0]?.items ?? [];
  const waiting = (pending.data ?? []).filter((a) => !project || a.project === project);
  const flows = wf.filter((w) => !project || w.project === project);
  if (all.length === 0 && waiting.length === 0) return project ? <NoChangesIn project={project} /> : <FirstRun />;

  const list = all.filter((c) => (!risk || asTier(c.plan.risk) === risk) && (!who || (who === "agents") === isAgent(c)));
  const byId = new Map(all.map((c) => [c.id, c]));

  return (
    <Page>
      <header>
        <p className="label mb-2">Ledger{project ? ` · ${project}` : ""}</p>
        <Headline all={first} project={project} />
      </header>

      <Controls search={search} all={all} projects={projectNames} enamels={enamels} />

      {(waiting.length > 0 || flows.length > 0) && <Waiting approvals={waiting} workflows={flows} />}

      {list.length === 0 && !changes.hasNextPage ? (
        <p className="mt-10 text-[0.9375rem] text-ink-2">
          {risk ? `No ${tierCopy[risk].label.toLowerCase()} changes here.` : who === "agents" ? "No agent has changed anything here." : "Nobody has changed anything here."}{" "}
          <Link to="/ledger" search={{ project }} className="text-brass-ink hover:underline hover:underline-offset-4">
            Show every entry
          </Link>
        </p>
      ) : (
        <Entries
          key={`${project}|${risk}|${who}`}
          list={list}
          more={{ next: !!changes.hasNextPage, busy: changes.isFetchingNextPage, fetch: () => void changes.fetchNextPage(), legacy, error: changes.isFetchNextPageError }}
          showProject={!project}
          byId={byId}
          signedBy={signedBy}
          names={names.data}
          enamels={enamels}
        />
      )}
    </Page>
  );
}

// ───────────────────────── headline ─────────────────────────

const weekday = new Intl.DateTimeFormat("en-GB", { weekday: "long" });
const dayMonth = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long" });

/** One sentence for the period: how many, where, since when; then who and how risky. */
function Headline({ all, project }: { all: Change[]; project?: string }) {
  if (all.length === 0) return <h1 className="sentence text-ink">Nothing is written down yet.</h1>;
  const projects = new Set(all.map((c) => c.project)).size;
  const agents = all.filter(isAgent).length;
  const irr = all.filter((c) => asTier(c.plan.risk) === "irreversible").length;
  const out = all.filter((c) => asTier(c.plan.risk) === "outbound").length;
  const oldest = all[all.length - 1].at;
  const now = new Date();
  const ageDays = (now.getTime() - new Date(oldest).getTime()) / 86_400_000;
  const since = dayKey(oldest) === dayKey(now.toISOString()) ? " today" : ageDays < 6 ? ` since ${weekday.format(new Date(oldest))}` : ` since ${dayMonth.format(new Date(oldest))}`;
  const where = project ? (
    <>
      {" "}
      to {project}
    </>
  ) : (
    <> across {countWords(projects, "project")}</>
  );
  const whoWords = agents === 0 ? "All by people." : agents === all.length ? "All by agents." : `Agents made ${words(agents)} of them.`;
  const risk = irr > 0 ? `${words(irr, true)} ${irr === 1 ? "was" : "were"} irreversible.` : out > 0 ? `${words(out, true)} reached outside the box.` : "Every one can be undone.";
  return (
    <h1 className="sentence max-w-[44rem] text-ink">
      {countWords(all.length, "change", "changes", true)}
      {where}
      {since}. <span className="text-ink-3">
        {whoWords} {risk}
      </span>
    </h1>
  );
}

// ───────────────────────── controls ─────────────────────────

function Controls({ search, all, projects, enamels }: { search: ActivitySearch; all: Change[]; projects: string[]; enamels: ReturnType<typeof useEnamels> }) {
  const navigate = useNavigate();
  const set = (patch: Partial<ActivitySearch>) => navigate({ to: "/ledger", search: { ...search, ...patch }, replace: true });
  const tierCount = (t: Tier) => all.filter((c) => asTier(c.plan.risk) === t).length;
  const toggle = "inline-flex h-7 items-center gap-1.5 rounded-[6px] px-2 text-[0.8125rem] text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink aria-pressed:bg-paper-sunk aria-pressed:text-ink disabled:pointer-events-none disabled:opacity-40";
  return (
    <div className="mt-7 flex flex-wrap items-center gap-x-5 gap-y-2 border-y border-rule py-1.5" role="toolbar" aria-label="Filter the Ledger">
      <div className="flex items-center" role="group" aria-label="Who">
        {(
          [
            [undefined, "Everyone"],
            ["people", "People"],
            ["agents", "Agents"],
          ] as const
        ).map(([v, label]) => (
          <button key={label} type="button" className={toggle} aria-pressed={search.who === v} onClick={() => set({ who: v })}>
            {label}
          </button>
        ))}
      </div>
      <span aria-hidden className="h-4 w-px bg-rule-2 max-sm:hidden" />
      <div className="flex items-center" role="group" aria-label="Risk">
        {(["reversible", "outbound", "irreversible"] as const).map((t) => {
          const n = tierCount(t);
          const on = search.risk === t;
          return (
            <button
              key={t}
              type="button"
              className={toggle}
              aria-pressed={on}
              disabled={n === 0 && !on}
              title={tierCopy[t].blurb}
              onClick={() => set({ risk: on ? undefined : t })}
            >
              <RiskDots tier={t} label={false} />
              <span className={cn(t === "irreversible" && n > 0 && "text-danger")}>{tierCopy[t].label}</span>
              <span className="text-xs text-ink-3 tnum">{n}</span>
            </button>
          );
        })}
      </div>
      <span aria-hidden className="h-4 w-px bg-rule-2 max-sm:hidden" />
      {projects.length > 1 || search.project ? (
        <Menu>
          <MenuTrigger asChild>
            <button type="button" className={cn(toggle, "data-[state=open]:bg-paper-sunk")} aria-label={`Project: ${search.project ?? "all"}`}>
              {search.project ? (
                <>
                  <EnamelSwatch enamel={enamels[search.project] ?? "indigo"} />
                  <span className="text-ink">{search.project}</span>
                </>
              ) : (
                "Every project"
              )}
              <ChevronDown className="size-3.5" />
            </button>
          </MenuTrigger>
          <MenuContent align="start" className="min-w-44">
            <MenuRadioGroup value={search.project ?? ""} onValueChange={(v) => set({ project: v || undefined })}>
              <MenuRadioItem value="">Every project</MenuRadioItem>
              <MenuSeparator />
              {projects.map((p) => (
                <MenuRadioItem key={p} value={p}>
                  <EnamelSwatch enamel={enamels[p] ?? "indigo"} />
                  {p}
                </MenuRadioItem>
              ))}
            </MenuRadioGroup>
          </MenuContent>
        </Menu>
      ) : null}
    </div>
  );
}

// ───────────────────────── waiting ─────────────────────────

function Waiting({ approvals, workflows }: { approvals: Approval[]; workflows: ReturnType<typeof useWaitingWorkflowApprovals> }) {
  const n = approvals.length + workflows.length;
  return (
    <section aria-labelledby="waiting" className="mt-8 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-raised">
      <header className="flex items-baseline justify-between px-4 pt-3 sm:px-5">
        <h2 id="waiting" className="label !text-brass-ink">
          Waiting for you
        </h2>
        {n > 1 && <span className="text-xs text-ink-3 tnum">{n}</span>}
      </header>
      <div className="divide-y divide-rule px-4 sm:px-5">
        {approvals.map((a) => {
          const r = splitRequester(a.requester);
          return (
            <div key={a.id} className="flex flex-col gap-x-6 sm:flex-row sm:items-center">
              <SignedEntry
                className="min-w-0 flex-1"
                time={clock(a.createdAt)}
                timeNote="asked"
                actor={{ kind: "agent", name: r.name, session: r.session }}
                intent={splitIntent(intentWords({ intent: a.intent, plan: a.plan })).head}
                to="/approvals/$id"
                params={{ id: a.id }}
                counts={opCounts(a.plan.ops)}
                tier={asTier(a.plan.risk)}
                extra={
                  <span>
                    {a.project} · expires {relative(a.expiresAt)}
                  </span>
                }
              />
              <Button asChild variant="primary" size="md" className="mb-3 self-start max-sm:ml-[56px] sm:mb-0 sm:self-center">
                <Link to="/approvals/$id" params={{ id: a.id }}>
                  Review
                </Link>
              </Button>
            </div>
          );
        })}
        {workflows.map((w) => (
          <WorkflowRow key={w.project + w.id} w={w} compact />
        ))}
      </div>
    </section>
  );
}

// ───────────────────────── entries ─────────────────────────

function Entries({
  list,
  more,
  showProject,
  byId,
  signedBy,
  names,
  enamels,
}: {
  list: Change[];
  more: { next: boolean; busy: boolean; fetch: () => void; legacy: boolean; error: boolean };
  showProject: boolean;
  byId: Map<string, Change>;
  signedBy: Map<string, Approval>;
  names: Parameters<typeof tokenWho>[1];
  enamels: ReturnType<typeof useEnamels>;
}) {
  const [sel, setSel] = useState(-1);
  const root = useRef<HTMLDivElement>(null);
  const tail = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const { next, busy, fetch } = more;

  // Earlier history as the end of the list comes near (also when a filter leaves too few rows to scroll).
  useEffect(() => {
    const el = tail.current;
    if (!el || !next || busy || more.error) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && fetch(), { rootMargin: "900px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [next, busy, fetch, more.error]);

  // j / k move through the entries, ↵ opens one; typing elsewhere is left alone.
  const move = useCallback(
    (to: number) => {
      const i = Math.max(0, Math.min(list.length - 1, to));
      if (i >= list.length - 5 && next && !busy) fetch();
      setSel(i);
      requestAnimationFrame(() => {
        const row = root.current?.querySelector<HTMLElement>(`[data-entry="${i}"]`);
        row?.querySelector<HTMLElement>("a")?.focus({ preventScroll: true });
        row?.scrollIntoView({ block: "nearest" });
      });
    },
    [list.length, next, busy, fetch],
  );
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      if (document.querySelector("[role=dialog],[role=menu]")) return;
      if (e.key === "j" || (e.key === "ArrowDown" && sel >= 0)) {
        e.preventDefault();
        move(sel + 1);
      } else if (e.key === "k" || (e.key === "ArrowUp" && sel >= 0)) {
        e.preventDefault();
        move(sel < 0 ? 0 : sel - 1);
      } else if (e.key === "Enter" && sel >= 0 && !(t && t.closest("a,button"))) {
        e.preventDefault();
        void navigate({ to: "/changes/$id", params: { id: list[sel].id } });
      } else if (e.key === "Escape" && sel >= 0) {
        setSel(-1);
        (document.activeElement as HTMLElement | null)?.blur();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [sel, move, navigate, list]);

  const days: Array<{ key: string; label: string; sub: string; items: Array<{ c: Change; i: number }> }> = [];
  list.forEach((c, i) => {
    const k = dayKey(c.at);
    const last = days[days.length - 1];
    if (last?.key === k) last.items.push({ c, i });
    else days.push({ key: k, ...dayHeading(c.at), items: [{ c, i }] });
  });
  const oldest = list[list.length - 1] as Change | undefined;

  return (
    <div ref={root} className="mt-6">
      {days.map((d, n) => (
        <section key={d.key} aria-labelledby={`day-${d.key}`} className="pt-5">
          <div className="flex items-baseline gap-2 pb-1">
            <h2 id={`day-${d.key}`} className="flex items-baseline gap-2">
              <span className="day text-ink">{d.label}</span>
              <span className="text-xs text-ink-3">{d.sub}</span>
            </h2>
            {n === 0 && (
              <p className="ml-auto hidden items-center gap-1.5 text-xs text-ink-3 lg:flex" aria-hidden>
                <kbd className="kbd">j</kbd>
                <kbd className="kbd">k</kbd>
                to move
                <kbd className="kbd ml-2">↵</kbd>
                to open
              </p>
            )}
          </div>
          <div className="divide-y divide-rule border-y border-rule">
            {d.items.map(({ c, i }) => (
              <Entry
                key={c.id}
                c={c}
                index={i}
                selected={i === sel}
                onPick={() => setSel(i)}
                showProject={showProject}
                undo={c.undoneBy ? byId.get(c.undoneBy) : undefined}
                undid={c.undoOf ? byId.get(c.undoOf) : undefined}
                approval={signedBy.get(c.id)}
                names={names}
                enamel={enamels[c.project]}
              />
            ))}
          </div>
        </section>
      ))}
      <div ref={tail} className="mt-10 mb-2 flex min-h-8 flex-col items-center gap-3 text-center text-sm text-ink-3">
        {next ? (
          <Button variant="ghost" size="sm" onClick={fetch} disabled={busy}>
            {busy ? "Reading earlier entries…" : more.error ? "Couldn’t read earlier entries. Try again" : "Show earlier entries"}
          </Button>
        ) : more.legacy ? (
          <p>These are the latest {countWords(list.length, "entry", "entries")}. This box can’t page further back yet; pick a project above to see more of one.</p>
        ) : oldest ? (
          <p className="flex items-center gap-2">
            <TiffinMark className="size-4 text-ink-4" />
            That’s everything. The Ledger starts on {dayWords(oldest.at)}.
          </p>
        ) : null}
      </div>
    </div>
  );
}

function Entry({
  c,
  index,
  selected,
  onPick,
  showProject,
  undo,
  undid,
  approval,
  names,
  enamel,
}: {
  c: Change;
  index: number;
  selected: boolean;
  onPick: () => void;
  showProject: boolean;
  undo?: Change;
  undid?: Change;
  approval?: Approval;
  names: Parameters<typeof tokenWho>[1];
  enamel?: ReturnType<typeof useEnamels>[string];
}) {
  const tier = asTier(c.plan.risk);
  const agent = isAgent(c);
  let signature: ReactNode;
  if (approval) signature = `signed by ${tokenWho(approval.decidedBy, names)} · passkey${approval.decidedAt ? ` · ${clock(approval.decidedAt)}` : ""}`;
  else if (agent) signature = "within its grant";
  const extra: ReactNode[] = [];
  if (c.undoneBy)
    extra.push(
      <Link key="u" to="/changes/$id" params={{ id: c.undoneBy }} className="text-ink-2 underline decoration-rule-3 underline-offset-[3px] hover:text-ink hover:decoration-ink-3">
        undone{undo ? ` at ${clock(undo.at)}` : ""}
      </Link>,
    );
  if (c.undoOf)
    extra.push(
      <Link key="o" to="/changes/$id" params={{ id: c.undoOf }} className="text-ink-3 underline decoration-rule-3 underline-offset-[3px] hover:text-ink">
        {undid ? `undid the ${clock(undid.at)} entry` : "the entry it undid"}
      </Link>,
    );
  if (showProject)
    extra.push(
      <span key="p" className="inline-flex items-center gap-1.5">
        {enamel && <EnamelSwatch enamel={enamel} size={6} />}
        {c.project}
      </span>,
    );
  return (
    <div
      data-entry={index}
      data-sel={selected ? "" : undefined}
      onFocusCapture={onPick}
      className="relative transition-colors duration-[var(--dur-state)] data-[sel]:bg-paper-sunk/70 before:absolute before:top-3.5 before:bottom-3.5 before:-left-3 before:w-[2px] before:rounded-full before:bg-transparent data-[sel]:before:bg-brass"
    >
      <SignedEntry
        time={clock(c.at)}
        actor={{ kind: c.actor.kind, name: actorShown(c.actor, names), session: c.actor.session, model: c.actor.model || undefined }}
        intent={splitIntent(intentWords(c)).head}
        to="/changes/$id"
        params={{ id: c.id }}
        counts={opCounts(c.plan.ops)}
        tier={tier}
        muted={!!c.undoneBy}
        signature={signature}
        extra={extra.length ? <>{extra}</> : undefined}
        className="!py-2"
      />
    </div>
  );
}

const shortDay = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long" });
const yearDay = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", year: "numeric" });
/** "Today" · "Saturday 3 October"; "Thursday" · "1 October"; older years carry the year. */
function dayHeading(iso: string): { label: string; sub: string } {
  const l = dayLabel(iso);
  const d = new Date(iso);
  if (l === "Today" || l === "Yesterday") return { label: l, sub: dayWords(iso) };
  return { label: weekday.format(d), sub: d.getFullYear() === new Date().getFullYear() ? shortDay.format(d) : yearDay.format(d) };
}

// ───────────────────────── empty and loading ─────────────────────────

function Skeleton() {
  return (
    <Page>
      <div className="h-3 w-16 rounded bg-paper-sunk" />
      <div className="mt-3 h-8 w-[28rem] max-w-full animate-pulse rounded-md bg-paper-sunk" />
      <div className="mt-7 h-10 border-y border-rule" />
      <div className="mt-8 space-y-5">
        {[0, 1, 2, 3, 4].map((k) => (
          <div key={k} className="grid grid-cols-[44px_1fr] gap-3">
            <div className="h-3 w-9 rounded bg-paper-sunk" />
            <div className="space-y-2">
              <div className="h-3 w-24 rounded bg-paper-sunk" />
              <div className="h-4 w-3/4 rounded bg-paper-sunk" />
            </div>
          </div>
        ))}
      </div>
    </Page>
  );
}

function NoChangesIn({ project }: { project: string }) {
  return (
    <Page>
      <p className="label mb-2">Ledger · {project}</p>
      <h1 className="sentence text-ink">Nothing has changed in {project} yet.</h1>
      <p className="mt-3 max-w-[36rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
        Entries are written the moment someone applies a plan.{" "}
        <Link to="/ledger" search={{}} className="text-brass-ink hover:underline hover:underline-offset-4">
          Read every project’s
        </Link>
        .
      </p>
    </Page>
  );
}

function FirstRun() {
  const steps = [
    { t: "Describe your app", d: "Writes a starter tiffin.config.ts in this folder.", cmd: "tiffin init" },
    { t: "See what would change", d: "A dry run. Nothing happens, and you get a plan hash.", cmd: "tiffin plan" },
    { t: "Apply exactly that plan", d: "Paste the hash. If anything moved since, Tiffin refuses.", cmd: 'tiffin apply --confirm <hash> -m "Set up my app"' },
    { t: "Ship it", d: "Builds on the box and switches traffic once it’s healthy. Prints your URL.", cmd: "tiffin deploy" },
  ];
  return (
    <Page>
      <p className="label mb-2">Ledger</p>
      <h1 className="sentence text-ink">Nothing is written down yet.</h1>
      <p className="mt-3 max-w-[38rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
        Every change to this box, by you or an agent, is planned first, applied with its plan’s hash and written down here, signed and undoable. Make the first
        one from your terminal:
      </p>
      <ol className="mt-8 divide-y divide-rule border-y border-rule">
        {steps.map((s, k) => (
          <li key={s.t} className="grid grid-cols-[44px_minmax(0,1fr)] gap-x-3 py-4">
            <span className="day pt-px text-ink-3">{k + 1}</span>
            <div className="min-w-0">
              <h2 className="text-[0.9375rem] font-[550] text-ink">{s.t}</h2>
              <p className="mt-0.5 text-sm text-ink-2">{s.d}</p>
              <Command cmd={s.cmd} className="mt-2.5" />
            </div>
          </li>
        ))}
        <li className="grid grid-cols-[44px_minmax(0,1fr)] gap-x-3 py-4">
          <span className="day pt-px text-ink-3">or</span>
          <div className="min-w-0">
            <h2 className="text-[0.9375rem] font-[550] text-ink">Let an agent do it</h2>
            <p className="mt-0.5 text-sm text-ink-2">
              Give it its own token from{" "}
              <Link to="/tokens" search={{ create: true }} className="text-brass-ink hover:underline hover:underline-offset-4">
                Access
              </Link>
              ; its entries are written in graphite, with its model and session.
            </p>
            <Command cmd={mcpCommand()} className="mt-2.5" />
          </div>
        </li>
      </ol>
    </Page>
  );
}
