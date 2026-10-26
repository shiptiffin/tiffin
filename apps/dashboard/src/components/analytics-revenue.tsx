import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { isProblem } from "@/api/client";
import { mod2, type RevenueCount, type RevenueQuery, type RevenueReport, type StripeStatus } from "@/api/modules";
import { change, countryName, DeltaBadge, Panel, type Delta } from "@/components/analytics-kit";
import { customPer, DAY, HOUR, periods, RangePicker, type Per } from "@/components/analytics-toolbar";
import { SeriesTable, TimeSeries, type Series } from "@/components/charts/time-series";
import { Confirm } from "@/components/confirm";
import { Skeleton } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { ViewToggle } from "@/components/usage-charts";
import type { Attribution } from "@/lib/attribution";
import { cn } from "@/lib/cn";
import { int, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { money } from "@/lib/money";
import { relative } from "@/lib/time";
import type { AnalyticsSearch } from "@/routes/analytics-search";

/**
 * Revenue: which sources bring paying customers (Analytics › Revenue).
 *
 * Needs marketing attribution on. Not connected: three steps to connect
 * Stripe with a restricted key. Connected: the import of the last 12 months
 * (polled while it runs), anything that keeps revenue incomplete (with
 * Repair when the webhook points at an old address), six numbers against
 * the days before, revenue over time, revenue by source, campaign, landing
 * page and country under the first- or last-touch model, the payments not
 * matched yet (Match again), and the connection itself (Disconnect).
 *
 * Amounts arrive in the account currency's smallest unit (lib/money.ts).
 * Sources, pages, customer ids and reasons come from visitors and Stripe:
 * they are rendered as text, never as markup.
 */

export const stripeQuery = (project: string) => ({ queryKey: ["analytics-stripe", project], queryFn: () => mod2.stripe(project), retry: false, staleTime: 30_000 });

const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
const dateFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long" });
const link = "font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink";

export function RevenueView({
  project,
  attribution,
  search,
  go,
  onAttribution,
}: {
  project: string;
  attribution: Attribution | undefined;
  search: AnalyticsSearch;
  go: (patch: Partial<AnalyticsSearch>, replace?: boolean) => void;
  onAttribution: () => void;
}) {
  const on = !!attribution?.on;
  const st = useQuery({
    ...stripeQuery(project),
    enabled: on,
    // While the last 12 months come in, watch them arrive.
    refetchInterval: (q) => (q.state.data?.backfill?.state === "running" ? 2000 : false),
  });
  const s = st.data;
  const importing = s?.connected && s.backfill.state === "running";

  const custom = !!search.from;
  const preset = periods.find((x) => x.v === (search.period ?? "7d")) ?? periods[3];
  const per: Per = custom ? customPer(search.from!, search.to) : preset;
  const compare = search.compare !== "off";
  const touch = search.touch ?? "last";
  const rq: RevenueQuery = { ...(custom ? { from: search.from, to: search.to } : { period: preset.v as RevenueQuery["period"] }), touch, limit: 25 };
  const r = useQuery({
    queryKey: ["analytics-revenue", project, rq],
    queryFn: () => mod2.revenue(project, rq),
    enabled: on && !!s,
    placeholderData: keepPreviousData,
    refetchInterval: importing ? 3000 : 60_000,
    retry: false,
  });

  if (attribution === undefined) return <Loading />;
  if (!on)
    return (
      <div className="mt-8 max-w-[44rem]">
        <p className="state-sentence text-ink">Revenue needs marketing attribution.</p>
        <p className="mt-2 text-[0.9375rem] text-ink-2">It links each payment to the account that paid and to the source that brought it, so it has to be on first.</p>
        <Button variant="primary" size="lg" className="mt-5" onClick={onAttribution}>
          Go to Attribution
        </Button>
      </div>
    );
  if (st.isError) return <ProblemNote className="mt-6 max-w-[46rem]" error={st.error} title="Couldn’t read the Stripe connection" />;
  if (!s) return <Loading />;

  // revenue() from the app counts while Stripe isn't connected.
  const fromApp = !s.connected && r.data?.provider === "app" && r.data.totals.payments + r.data.previous.payments > 0;

  return (
    <div className="mt-5">
      {!s.connected && <ConnectStripe project={project} status={s} />}
      {(s.connected || fromApp) && (
        <>
          <div className={cn("flex flex-wrap items-center gap-2", !s.connected && "mt-10")}>
            <RangePicker
              per={per}
              from={search.from}
              to={search.to}
              compare={compare}
              onPeriod={(v) => go({ period: v, from: undefined, to: undefined })}
              onDays={(from, to) => go({ from, to, period: undefined })}
              onCompare={(c) => go({ compare: c ? undefined : "off" }, true)}
            />
            <Segmented
              label="Credit revenue to"
              value={touch}
              onChange={(t) => go({ touch: t === "first" ? "first" : undefined }, true)}
              options={[
                { value: "last", label: "Last touch", name: "Last touch: the visit the payment was made in" },
                { value: "first", label: "First touch", name: "First touch: where the customer first came from" },
              ]}
            />
            <span className="ml-auto flex items-center gap-2 text-[0.8125rem] text-ink-3">
              {s.connected ? (
                <>
                  {s.mode === "test" && <span className="inline-flex h-6 items-center rounded-[6px] border border-rule-2 px-2 text-[0.75rem] font-[550] text-ink-2">Test mode</span>}
                  <span>Stripe{s.accountCurrency ? `, in ${s.accountCurrency.toUpperCase()}` : ""}</span>
                </>
              ) : (
                <span>From revenue() in your app</span>
              )}
            </span>
          </div>
          {s.connected && <Health project={project} s={s} />}
          {r.isError ? (
            <ProblemNote className="mt-5 max-w-[46rem]" error={r.error} title="Couldn’t load revenue" />
          ) : !r.data ? (
            <Loading />
          ) : (
            <div className={cn("transition-opacity duration-200", r.isPlaceholderData && "opacity-60")} aria-busy={r.isFetching}>
              <Report project={project} d={r.data} per={per} compare={compare} />
            </div>
          )}
          {s.connected && <Connection project={project} s={s} />}
        </>
      )}
    </div>
  );
}

function Loading() {
  return (
    <div className="mt-6" aria-busy aria-label="Loading revenue">
      <Skeleton className="h-[22rem] rounded-[12px]" />
      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-64 rounded-[12px]" />
        <Skeleton className="h-64 rounded-[12px]" />
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ connect

/** "Charges: Read" … grouped as Read: Charges, … and Write: Webhook Endpoints. */
function byAccess(perms: string[]) {
  const read: string[] = [];
  const write: string[] = [];
  for (const p of perms) {
    const [what, access] = p.split(/:\s*/);
    (access === "Write" ? write : read).push(what);
  }
  return { read, write };
}

function Step({ n, title, children, last }: { n: number; title: string; children: ReactNode; last?: boolean }) {
  return (
    <li className="relative grid grid-cols-[1.5rem_minmax(0,1fr)] gap-x-3.5">
      {!last && <span aria-hidden className="absolute top-7 bottom-0 left-[0.71875rem] w-px bg-rule-2" />}
      <span aria-hidden className="grid size-6 place-items-center rounded-full border border-rule-2 bg-paper-raised text-[0.75rem] font-[550] text-ink-2 tnum">
        {n}
      </span>
      <div className={cn("min-w-0", !last && "pb-7")}>
        <h3 className="pt-0.5 text-[0.875rem] font-[550] text-ink">{title}</h3>
        <div className="mt-1 text-[0.84375rem] text-ink-2">{children}</div>
      </div>
    </li>
  );
}

function ConnectStripe({ project, status }: { project: string; status: StripeStatus }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [key, setKey] = useState("");
  const { read, write } = byAccess(status.permissions ?? []);
  const connect = useMutation({
    mutationFn: () => mod2.stripeConnect(project, key.trim()),
    onSuccess: (s) => {
      setKey("");
      qc.setQueryData(["analytics-stripe", project], s);
      void qc.invalidateQueries({ queryKey: ["analytics-revenue", project] });
      void qc.invalidateQueries({ queryKey: ["analytics-privacy", project] });
      toast({ title: `Connected Stripe to ${project}. Importing the last 12 months.` });
    },
  });
  const looksLikeKey = /^(rk|sk)_(live|test)_\S{8,}$/.test(key.trim());
  return (
    <section aria-labelledby="connect-stripe" className="max-w-[46rem]">
      <p className="state-sentence text-ink">Connect Stripe to see which sources bring paying customers.</p>
      <p className="mt-2 max-w-[38rem] text-[0.875rem] text-ink-3">Each payment is linked to the account that paid, or to the visit it was paid in, and so to the source that brought it.</p>
      <h2 id="connect-stripe" className="sr-only">
        Connect Stripe
      </h2>
      <ol className="mt-7">
        <Step n={1} title="Create a restricted key">
          <p>
            In Stripe, open API keys, choose Create restricted key, then Powering an integration you built and Choose your own. Set these rows, and leave
            everything else at None.{" "}
            <a href="https://dashboard.stripe.com/apikeys" target="_blank" rel="noreferrer" className={cn(link, "inline-flex items-center gap-0.5 whitespace-nowrap")}>
              Open API keys
              <ArrowUpRight aria-hidden className="size-3.5" />
            </a>
          </p>
          <dl className="mt-3 grid gap-x-8 gap-y-2 rounded-[10px] border border-rule-2 bg-paper-sunk/60 px-4 py-3 text-[0.8125rem] sm:grid-cols-[auto_minmax(0,1fr)]">
            <dt className="font-[550] text-ink">Read</dt>
            <dd className="text-ink-2">{read.join(" · ")}</dd>
            <dt className="font-[550] text-ink">Write</dt>
            <dd className="text-ink-2">{write.join(" · ")}</dd>
          </dl>
        </Step>
        <Step n={2} title="Paste it here">
          <form
            className="mt-1 flex flex-wrap gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (looksLikeKey) connect.mutate();
            }}
          >
            <input
              aria-label="Stripe restricted key"
              value={key}
              onChange={(e) => (setKey(e.target.value), connect.reset())}
              placeholder="rk_live_…"
              type="password"
              autoComplete="off"
              spellCheck={false}
              disabled={!can("apply:reversible")}
              className="h-9 min-w-0 flex-[1_1_16rem] rounded-[8px] border border-rule-2 bg-paper-raised px-3 font-mono text-[0.8125rem] text-ink placeholder:text-ink-4 focus-visible:border-rule-3 focus-visible:outline-hidden"
            />
            <Button type="submit" variant="primary" size="lg" disabled={!looksLikeKey || connect.isPending}>
              {connect.isPending ? "Connecting…" : "Connect"}
            </Button>
          </form>
          <p className="mt-2 text-[0.8125rem] text-ink-3">A test key (rk_test_…) works too. The key is kept encrypted on this box.</p>
          {connect.isError && <ProblemNote className="mt-3" error={connect.error} title={isProblem(connect.error, "validation") ? "Stripe didn’t take that key" : "Couldn’t connect Stripe"} />}
        </Step>
        <Step n={3} title="Past payments come in" last>
          The box imports the last 12 months of payments, refunds and subscriptions, and adds a webhook to your Stripe account for new ones.
        </Step>
      </ol>
    </section>
  );
}

// ------------------------------------------------------------------ status

/** The import while it runs, and anything that keeps revenue incomplete, in plain words. */
function Health({ project, s }: { project: string; s: StripeStatus }) {
  const qc = useQueryClient();
  const repair = useMutation({
    mutationFn: () => mod2.stripeRepair(project),
    onSuccess: (n) => {
      qc.setQueryData(["analytics-stripe", project], n);
      toast({ title: "The webhook points at the project’s address again." });
    },
  });
  const b = s.backfill;
  const warnings = s.warnings ?? [];
  if (b.state !== "running" && !warnings.length) return null;
  return (
    <div className="mt-4 flex flex-col gap-2">
      {b.state === "running" && (
        <p role="status" className="flex items-center gap-2.5 rounded-[10px] border border-rule-2 bg-paper-raised px-3.5 py-2.5 text-[0.875rem] text-ink-2">
          <span aria-hidden className="spinner size-3.5 shrink-0" />
          <span>
            <b className="font-[550] text-ink">Importing the last 12 months from Stripe.</b> {int(b.payments)} payments, {int(b.refunds)} refunds and {int(b.subscriptions)} subscriptions so far.
          </span>
        </p>
      )}
      {warnings.length > 0 && (
        <div role="status" className="flex flex-wrap items-start gap-x-4 gap-y-2 rounded-[10px] bg-warn-wash px-3.5 py-2.5">
          <ul className="min-w-0 flex-[1_1_20rem] text-[0.875rem] text-ink">
            {warnings.map((w) => (
              <li key={w} className="py-0.5">
                {sentence(w)}
              </li>
            ))}
          </ul>
          {(s.urlMismatch || b.state === "failed") && (
            <Button size="md" disabled={repair.isPending} onClick={() => repair.mutate()}>
              {repair.isPending ? "Repairing…" : s.urlMismatch ? "Repair webhook" : "Import again"}
            </Button>
          )}
          {repair.isError && <ProblemNote className="w-full" error={repair.error} />}
        </div>
      )}
    </div>
  );
}

/** The connection: account, webhook, import, events; Disconnect. */
function Connection({ project, s }: { project: string; s: StripeStatus }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [confirm, setConfirm] = useState(false);
  const b = s.backfill;
  const rows: Array<{ k: string; v: ReactNode }> = [
    {
      k: "Account",
      v: (
        <>
          {s.mode === "live" ? "Live mode" : "Test mode"}
          {s.key && <span className="ml-2 font-mono text-[0.78rem] text-ink-3">{s.key}</span>}
        </>
      ),
    },
    { k: "Webhook", v: <span className="font-mono text-[0.78rem] break-all text-ink-2">{s.webhookUrl}</span> },
    {
      k: "Events",
      v: (
        <>
          {int(s.eventsReceived)} received{s.lastEventAt ? `, the last ${relative(s.lastEventAt)}` : ""}
          {s.eventsWaiting > 0 && <span className="text-ink-3">, {int(s.eventsWaiting)} waiting</span>}
          {s.eventsFailed > 0 && <span className="text-danger">, {int(s.eventsFailed)} failed</span>}
        </>
      ),
    },
    {
      k: "Imported",
      v:
        b.state === "running"
          ? "Importing…"
          : b.state === "failed"
            ? "Failed. See above."
            : `${int(b.payments)} payments, ${int(b.refunds)} refunds, ${int(b.subscriptions)} subscriptions${b.finished ? ` (${dateFmt.format(new Date(b.finished))})` : ""}`,
    },
    ...(s.connectedAt ? [{ k: "Connected", v: `${dateFmt.format(new Date(s.connectedAt))}${s.connectedBy ? ` by ${s.connectedBy}` : ""}` }] : []),
  ];
  return (
    <section aria-labelledby="stripe-connection" className="mt-10">
      <h2 id="stripe-connection" className="label">
        Stripe
      </h2>
      <dl className="mt-2 divide-y divide-rule border-y border-rule">
        {rows.map((r) => (
          <div key={r.k} className="grid gap-x-6 gap-y-0.5 py-2.5 text-[0.875rem] sm:grid-cols-[8rem_minmax(0,1fr)]">
            <dt className="text-ink-3">{r.k}</dt>
            <dd className="min-w-0 text-ink">{r.v}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-[12px] border border-danger-rule px-4 py-4 sm:px-5">
        <div className="min-w-0 flex-[1_1_18rem]">
          <h3 className="text-[0.875rem] font-[550] text-ink">Disconnect Stripe</h3>
          <p className="mt-0.5 max-w-[34rem] text-[0.8125rem] text-ink-3">Deletes the webhook in your Stripe account, the key, and every payment imported from Stripe. Connecting again imports the last 12 months anew.</p>
        </div>
        {can("apply:irreversible") ? (
          <Button variant="danger-outline" onClick={() => setConfirm(true)}>
            Disconnect
          </Button>
        ) : (
          <p className="text-[0.8125rem] text-ink-3">Only an admin can disconnect it</p>
        )}
      </div>
      <Confirm
        open={confirm}
        onClose={() => setConfirm(false)}
        title={`Disconnect Stripe from ${project}?`}
        body={`The webhook, the key and ${b.payments ? `${int(b.payments)} imported payments` : "every imported payment"} will be gone for good. Payments in Stripe itself stay.`}
        action="Disconnect"
        cancel="Keep it"
        run={() => mod2.stripeDisconnect(project)}
        done={() => {
          setConfirm(false);
          void qc.invalidateQueries({ queryKey: ["analytics-stripe", project] });
          void qc.invalidateQueries({ queryKey: ["analytics-revenue", project] });
          void qc.invalidateQueries({ queryKey: ["analytics-privacy", project] });
          toast({ title: `Disconnected Stripe from ${project}.` });
        }}
      />
    </section>
  );
}

// ------------------------------------------------------------------ report

type Metric = "revenue" | "payments" | "newCustomers";

function Report({ project, d, per, compare }: { project: string; d: RevenueReport; per: Per; compare: boolean }) {
  const cur = d.currency;
  const t = d.totals;
  const p = d.previous;
  const base = compare ? p.visitors : NaN;
  const m = (v: number, exact?: boolean) => money(v, cur, exact ? "exact" : "auto");
  const tiles: Array<{ label: string; value: string; was: string; delta: Delta | null; note?: string }> = [
    { label: "Revenue", value: m(t.revenue), was: m(p.revenue), delta: change(t.revenue, p.revenue, base) },
    { label: "Payments", value: int(t.payments), was: int(p.payments), delta: change(t.payments, p.payments, base), note: t.renewals ? `${int(t.renewals)} renewals` : undefined },
    { label: "New customers", value: int(t.newCustomers), was: int(p.newCustomers), delta: change(t.newCustomers, p.newCustomers, base) },
    { label: "Conversion rate", value: t.visitors ? pct(t.conversionRate, 2) : "–", was: p.visitors ? pct(p.conversionRate, 2) : "–", delta: change(t.conversionRate, p.conversionRate, base) },
    { label: "Revenue per visitor", value: t.visitors ? m(t.revenuePerVisitor, true) : "–", was: p.visitors ? m(p.revenuePerVisitor, true) : "–", delta: change(t.revenuePerVisitor, p.revenuePerVisitor, base) },
    { label: "MRR", value: m(t.mrr), was: m(p.mrr), delta: p.mrr !== t.mrr ? change(t.mrr, p.mrr, base) : null, note: "Now" },
  ];
  return (
    <>
      <section aria-labelledby="revenue-trend" className="mt-4 rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
        <dl className="grid grid-cols-2 border-b border-rule sm:grid-cols-3 xl:grid-cols-6">
          {tiles.map((x, i) => (
            <div
              key={x.label}
              title={compare ? `${x.value} against ${x.was} ${per.before}` : undefined}
              className={cn(
                "min-w-0 px-4 pt-3.5 pb-4 sm:px-5",
                i % 2 === 1 && "max-sm:border-l max-sm:border-rule",
                i > 1 && "max-sm:border-t max-sm:border-rule",
                i % 3 !== 0 && "sm:max-xl:border-l sm:max-xl:border-rule",
                i > 2 && "sm:max-xl:border-t sm:max-xl:border-rule",
                i > 0 && "xl:border-l xl:border-rule",
              )}
            >
              <dt className="flex items-baseline justify-between gap-2 text-[0.8125rem] text-ink-2">
                {x.label}
                {x.note && <span className="truncate text-[0.71875rem] text-ink-3">{x.note}</span>}
              </dt>
              <dd className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="text-[1.375rem] leading-8 font-[500] tracking-[-0.02em] text-ink tnum">{x.value}</span>
                {x.delta && <DeltaBadge delta={x.delta} />}
                {compare && <span className="sr-only">{`, against ${x.was} ${per.before}`}</span>}
              </dd>
            </div>
          ))}
        </dl>
        <RevenueChart d={d} />
      </section>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Breakdown
          id="revenue-sources"
          title="Sources"
          cur={cur}
          total={t.revenue}
          tabs={[
            { label: "Sources", name: "Source", rows: d.sources ?? [] },
            { label: "UTM source", name: "utm_source", rows: d.utmSources ?? [] },
            { label: "Medium", name: "utm_medium", rows: d.utmMediums ?? [] },
            { label: "Campaign", name: "utm_campaign", rows: d.utmCampaigns ?? [] },
          ]}
          foot={
            d.unattributed.revenue > 0 ? (
              <>
                {m(d.unattributed.revenue)} from {int(d.unattributed.payments)} {d.unattributed.payments === 1 ? "payment" : "payments"} isn’t credited to a visit.
              </>
            ) : undefined
          }
        />
        <Breakdown
          id="revenue-pages"
          title="Pages and countries"
          cur={cur}
          total={t.revenue}
          tabs={[
            { label: "Landing pages", name: "First page", rows: d.landing ?? [], mono: true },
            { label: "Countries", name: "Country", rows: d.countries ?? [], label2: countryName },
          ]}
        />
      </div>

      {(d.otherCurrencies?.length || d.mrrOtherCurrencies?.length) && (
        <p className="mt-4 text-[0.8125rem] text-ink-3">
          Left out of these sums:{" "}
          {[...(d.otherCurrencies ?? []).map((o) => `${money(o.amount, o.currency)} in ${o.currency.toUpperCase()}`), ...(d.mrrOtherCurrencies ?? []).map((o) => `${money(o.amount, o.currency)} a month of subscriptions in ${o.currency.toUpperCase()}`)].join(", ")}
          .
        </p>
      )}

      <Matching project={project} d={d} />
    </>
  );
}

function RevenueChart({ d }: { d: RevenueReport }) {
  const [metric, setMetric] = useState<Metric>("revenue");
  const [table, setTable] = useState(false);
  const hourly = d.granularity === "hour";
  const fmt = metric === "revenue" ? (v: number) => money(v, d.currency) : int;
  const label = { revenue: "Revenue", payments: "Payments", newCustomers: "New customers" }[metric];
  const series = useMemo<Series[]>(() => [{ id: "now", label, points: (d.timeseries ?? []).map((p) => [Date.parse(p.t), p[metric]]), area: true, tone: "ink" }], [d, metric, label]);
  const title = `${label} per ${hourly ? "hour" : "day"}`;
  return (
    <div className="px-4 pt-3 pb-4 sm:px-5">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h2 id="revenue-trend" className="text-[0.84375rem] font-[550] text-ink">
          {title}
        </h2>
        <div className="flex items-center gap-1.5">
          <Segmented
            label="The chart shows"
            value={metric}
            onChange={setMetric}
            options={[
              { value: "revenue", label: "Revenue" },
              { value: "payments", label: "Payments" },
              { value: "newCustomers", label: "New customers", short: "Customers" },
            ]}
          />
          <ViewToggle tables={table} onToggle={() => setTable((x) => !x)} />
        </div>
      </div>
      {table ? (
        <div className="max-h-[22rem] overflow-y-auto">
          <SeriesTable series={series} step={hourly ? HOUR : DAY} utc format={fmt} caption={title} />
        </div>
      ) : (
        <TimeSeries series={series} step={hourly ? HOUR : DAY} utc format={fmt} axisFormat={metric === "revenue" ? (v) => money(v, d.currency, "whole") : undefined} label={title} height={240} />
      )}
    </div>
  );
}

type BTab = { label: string; name: string; rows: RevenueCount[]; mono?: boolean; label2?: (v: string) => string };

/** Revenue by one thing: a bar for its share of the top row, customers, revenue and its share of all. */
function Breakdown({ id, title, tabs, cur, total, foot }: { id: string; title: string; tabs: BTab[]; cur: string; total: number; foot?: ReactNode }) {
  const [i, setI] = useState(0);
  const [all, setAll] = useState(false);
  const tab = tabs[i];
  const rows = tab.rows;
  const most = Math.max(1, ...rows.map((r) => r.revenue));
  const shown = all ? rows : rows.slice(0, 8);
  return (
    <Panel id={id} title={title} tabs={tabs.map((t) => t.label)} tab={i} onTab={(n) => (setI(n), setAll(false))}>
      <div className="flex flex-1 flex-col px-2 pt-2.5 pb-2">
        <div className="flex items-center gap-3 pr-1 pb-1 text-[0.71875rem] text-ink-3">
          <span className="min-w-0 flex-1 pl-2.5">{tab.name}</span>
          <span className="w-16 shrink-0 text-right">Customers</span>
          <span className="w-20 shrink-0 text-right">Revenue</span>
          <span className="w-10 shrink-0 text-right" />
        </div>
        {rows.length === 0 ? (
          <p className="px-2.5 py-6 text-[0.84375rem] text-ink-3">No revenue credited to a {tab.name.toLowerCase().replace(/^utm_/, "utm_")} in this period.</p>
        ) : (
          <ul className="flex flex-col gap-px">
            {shown.map((r) => {
              const name = r.value === "" ? "Direct" : tab.label2 ? tab.label2(r.value) : r.value;
              return (
                <li key={r.value} className="flex items-center gap-3 pr-1" title={`${name}: ${money(r.revenue, cur, "exact")} from ${int(r.payments)} payments`}>
                  <span className="relative flex h-8 min-w-0 flex-1 items-center pl-2.5">
                    <span aria-hidden className="absolute inset-y-0.5 left-0 rounded-[5px] bg-data-wash" style={{ width: `${Math.max(1.5, (r.revenue / most) * 100)}%` }} />
                    <span className={cn("relative truncate text-[0.84375rem]", r.value === "" ? "text-ink-2" : "text-ink", tab.mono && r.value !== "" && "font-mono text-[0.78rem]")}>{name}</span>
                  </span>
                  <span className="w-16 shrink-0 text-right text-[0.84375rem] text-ink-2 tnum">{int(r.customers)}</span>
                  <span className="w-20 shrink-0 text-right text-[0.84375rem] text-ink tnum">{money(r.revenue, cur, "whole")}</span>
                  <span className="w-10 shrink-0 text-right text-[0.75rem] text-ink-3 tnum">{total > 0 ? pct(r.revenue / total) : ""}</span>
                </li>
              );
            })}
          </ul>
        )}
        {rows.length > 8 && (
          <button type="button" onClick={() => setAll((x) => !x)} className="mt-1 ml-1 inline-flex h-8 items-center self-start rounded-[6px] px-1.5 text-[0.8125rem] text-ink-3 hover:text-ink">
            {all ? "Show fewer" : `Show all ${rows.length}`}
          </button>
        )}
        {foot && <p className="mt-auto px-2.5 pt-3 pb-1 text-[0.78125rem] text-ink-3">{foot}</p>}
      </div>
    </Panel>
  );
}

const MATCHED: Record<string, string> = { reference: "by account id", email: "by email", checkout: "by checkout visit", visit: "by visit", customer: "by an earlier payment", none: "not matched" };

/** How payments were matched, the newest not matched (with why), Match again, and the hint that makes matching exact. */
function Matching({ project, d }: { project: string; d: RevenueReport }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const again = useMutation({
    mutationFn: () => mod2.revenueMatch(project),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["analytics-revenue", project] });
      toast({ title: r.checked === 0 ? "Nothing to match." : r.matched === 0 ? `None of ${int(r.checked)} payments matched yet.` : `Matched ${int(r.matched)} of ${int(r.checked)} payments.` });
    },
  });
  const unmatched = d.unmatched ?? [];
  const by = Object.entries(d.matchedBy ?? {}).filter(([k, n]) => n > 0 && k !== "none");
  const none = d.matchedBy?.none ?? 0;
  if (!unmatched.length && !by.length) return null;
  return (
    <section aria-labelledby="revenue-matching" className="mt-10">
      <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
        <div>
          <h2 id="revenue-matching" className="label">
            Matching
          </h2>
          {by.length > 0 && (
            <p className="mt-2 text-[0.875rem] text-ink-2">
              Payments matched {by.map(([k, n]) => `${MATCHED[k] ?? k} ${int(n)}`).join(", ")}
              {none ? `; ${int(none)} not matched` : ""}.
            </p>
          )}
        </div>
        {unmatched.length > 0 && can("apply:reversible") && (
          <Button size="md" disabled={again.isPending} onClick={() => again.mutate()}>
            {again.isPending ? "Matching…" : "Match again"}
          </Button>
        )}
      </div>
      {again.isError && <ProblemNote className="mt-3" error={again.error} />}
      {unmatched.length > 0 && (
        <div tabIndex={0} role="region" aria-label="Payments not matched" className="mt-3 overflow-x-auto">
          <table className="w-full min-w-[34rem] text-left text-[0.84375rem]">
            <caption className="sr-only">Payments not matched to a customer</caption>
            <thead className="text-[0.71875rem] text-ink-3">
              <tr className="border-b border-rule">
                <th className="py-1.5 pr-4 font-normal">Paid</th>
                <th className="py-1.5 pr-4 text-right font-normal">Amount</th>
                <th className="py-1.5 pr-4 font-normal">Customer</th>
                <th className="py-1.5 font-normal">Why not</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-rule">
              {unmatched.map((u) => (
                <tr key={u.id} className="align-top">
                  <td className="py-2 pr-4 whitespace-nowrap text-ink-2 tnum">
                    {dayFmt.format(new Date(u.at))}
                    {u.kind === "renewal" && <span className="ml-1.5 text-ink-3">renewal</span>}
                  </td>
                  <td className="py-2 pr-4 text-right whitespace-nowrap text-ink tnum" title={u.currency !== u.accountCurrency ? `${money(u.accountAmount, u.accountCurrency, "exact")} in the account’s currency` : undefined}>
                    {money(u.amount, u.currency, "exact")}
                  </td>
                  <td className="max-w-[12rem] truncate py-2 pr-4 font-mono text-[0.75rem] text-ink-3" title={u.customer || u.id}>
                    {u.customer || "–"}
                  </td>
                  <td className="py-2 text-ink-2">{sentence(u.reason)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-4 max-w-[46rem] text-[0.8125rem] text-ink-3">
        Matching is exact when your Checkout Session names the account: add <code className="rounded-[4px] bg-paper-sunk px-1 py-px font-mono text-[0.75rem] text-ink-2">metadata: {"{ tiffin_user: user.id }"}</code> where you create it (and to its subscription).
      </p>
    </section>
  );
}

// ------------------------------------------------------------------ the Attribution tab's card

/** On Analytics › Attribution: whether Stripe is connected, and the way to Revenue. */
export function StripeCard({ project, onRevenue }: { project: string; onRevenue: () => void }) {
  const st = useQuery(stripeQuery(project));
  const s = st.data;
  return (
    <section aria-labelledby="revenue-card" className="rounded-[12px] border border-rule-2 bg-paper-raised">
      <div className="flex min-h-11 items-center justify-between gap-4 border-b border-rule px-4 sm:px-5">
        <h2 id="revenue-card" className="text-[0.84375rem] font-[550] text-ink">
          Revenue
        </h2>
        {s && <span className="text-[0.75rem] text-ink-3">{s.connected ? `Stripe, ${s.mode === "test" ? "test mode" : "live"}` : "Stripe not connected"}</span>}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-4 py-4 sm:px-5">
        <p className="min-w-0 flex-[1_1_16rem] text-[0.875rem] text-ink-2">
          {s?.connected ? "Payments are linked to the source that brought each customer." : "Connect Stripe to see which sources bring paying customers."}
        </p>
        <Button size="md" onClick={onRevenue}>
          {s?.connected ? "See revenue" : "Connect Stripe"}
        </Button>
      </div>
    </section>
  );
}
