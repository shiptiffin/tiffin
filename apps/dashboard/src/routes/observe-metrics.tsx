import { useQueries, useQuery } from "@tanstack/react-query";
import { notOnBox, type BoxResources } from "@/api/client";
import { mod, mq, type AppMetrics } from "@/api/modules";
import { q } from "@/api/queries";
import { toPoints } from "@/components/chart";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { niceCeil, TimeChart, type Marker } from "@/components/health-chart";
import { Alarm, Group, healthCrumbs, Rows } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { Qty } from "@/components/qty";
import { SegMeter } from "@/components/seg-meter";
import { actorWords } from "@/lib/actors";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, countWords, dec, duration, int, mb, ms, pct, withUnit } from "@/lib/format";
import { useEnamels } from "@/lib/enamel";
import { relative } from "@/lib/time";

const percent = (v: number) => withUnit(dec(v, 0), "%");
const rate = (v: number) => `${bytes(v, 0)}/s`;

/** A round top for a bytes scale in the units people read: 19.1 MB/s → 20 MB/s, not 20,000,000 B/s. */
function niceBytes(points: Array<[number, number]>) {
  const peak = points.reduce((m, p) => Math.max(m, p[1]), 0) * 1.1;
  if (peak <= 0) return 1024;
  const k = Math.min(4, Math.floor(Math.log(Math.max(peak, 1)) / Math.log(1024)));
  const n = niceCeil(peak / 1024 ** k);
  return (n >= 1000 ? 1024 : n) * 1024 ** k;
}

/**
 * Metrics: the box's vital signs over the last hour, with the box's changes
 * marked on every chart, then where the memory and CPU go: each service the
 * box runs, each app, each disk.
 */
export function MetricsPage() {
  useTitle("Metrics");
  const o = useQuery(mq.overview);
  const res = useQuery(q.resources);
  const changes = useQuery(q.changes());
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings, staleTime: 300_000 });
  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Metrics" />;
  if (o.isPending)
    return (
      <Page wide>
        <Skeleton className="h-4 w-20" />
        <Skeleton className="mt-3 h-8 w-40" />
        <div className="mt-12 grid gap-10 md:grid-cols-2">
          <Skeleton className="h-44" />
          <Skeleton className="h-44" />
        </div>
      </Page>
    );
  if (o.isError)
    return (
      <Page wide>
        <PageHeader eyebrow={healthCrumbs} title="Metrics" />
        <ProblemNote className="mt-8" error={o.error} title="Couldn't read the box's metrics" />
      </Page>
    );
  const d = o.data;
  const n = d.now;
  const s = d.series ?? {};
  const cpu = toPoints(s.cpu);
  const from = cpu[0]?.[0];
  const to = Math.floor(new Date(n.at).getTime() / 1000);
  const markers: Marker[] = (changes.data ?? [])
    .map((c) => ({ t: Math.floor(new Date(c.at).getTime() / 1000), label: sentence(c.intent), who: actorWords(c.actor) }))
    .filter((m) => from !== undefined && m.t >= from && m.t <= to);
  const data = (n.disks ?? []).find((x) => x.mount === "/var/lib/tiffin") ?? (n.disks ?? [])[0];
  const rx = toPoints(s.rx);
  const tx = toPoints(s.tx);
  const firing = d.firing ?? [];
  const used = n.memoryTotalBytes - n.memoryAvailableBytes;

  return (
    <Page wide>
      <PageHeader
        eyebrow={healthCrumbs}
        title="Metrics"
        lede={
          <>
            A point a minute, kept for {settings.data?.metricsRetention?.replace(/d$/, " days") ?? "30 days"}. The last hour is drawn here;{" "}
            <span className="whitespace-nowrap">
              <span aria-hidden className="mr-1 inline-block size-[7px] rotate-45 rounded-[1px] bg-brass align-[1px]" />
              marks
            </span>{" "}
            a change to the box.
          </>
        }
        actions={
          <span className="text-[0.8125rem] text-ink-3">
            Box up {duration(n.uptimeSeconds)} · {countWords(n.cpus, "CPU")}
          </span>
        }
      />

      {firing.length > 0 && (
        <Group label="Firing" flush className="mt-8">
          <Rows>
            {firing.map((a) => (
              <Alarm key={a.rule + a.subject} title={sentence(a.summary)} detail={`Since ${relative(a.since)}.`} to="/alerts" />
            ))}
          </Rows>
        </Group>
      )}

      <section aria-label="The last hour" className="mt-10 grid gap-x-12 gap-y-10 md:grid-cols-2">
        <Vital label="CPU" reading={<Qty className="reading" value={dec(n.cpuUsedRatio * 100, 0)} unit="%" />} note={`load ${dec(n.load1, 2)} · ${dec(n.load5, 2)} · ${dec(n.load15, 2)}`}>
          <TimeChart label="CPU in use" points={cpu} max={100} format={percent} markers={markers} />
        </Vital>
        <Vital
          label="Memory"
          reading={<Qty className="reading" value={dec(n.memoryUsedRatio * 100, 0)} unit="%" />}
          note={`${bytes(used)} of ${bytes(n.memoryTotalBytes)}${n.swapTotalBytes ? ` · swap ${bytes(n.swapTotalBytes - n.swapFreeBytes)}` : ""}`}
        >
          <TimeChart label="Memory in use" points={toPoints(s.memory)} max={100} format={percent} markers={markers} />
        </Vital>
        <Vital
          label="Data disk"
          reading={data ? <Qty className="reading" value={dec(data.usedRatio * 100, 0)} unit="%" /> : "–"}
          note={data ? `${bytes(data.freeBytes)} free of ${bytes(data.totalBytes)}` : undefined}
        >
          <TimeChart label="Data disk in use" points={toPoints(s.disk)} max={100} format={percent} markers={markers} />
        </Vital>
        <Vital label="Network in" reading={<Rate v={rx.at(-1)?.[1] ?? 0} />} note={`${rate(tx.at(-1)?.[1] ?? 0)} going out`}>
          <TimeChart label="Network in" points={rx} max={niceBytes(rx)} format={(v) => (v === 0 ? "0" : rate(v))} markers={markers} />
        </Vital>
      </section>

      <Services res={res.data} unavailable={res.isError} />
      <Apps res={res.data} />
      <Disks res={res.data} stores={d.stores} />
    </Page>
  );
}

function Rate({ v }: { v: number }) {
  const p = bytesParts(v);
  return <Qty className="reading" value={p.value} unit={`${p.unit}/s`} />;
}

function Vital({ label, reading, note, children }: { label: string; reading: React.ReactNode; note?: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 border-t border-rule pt-4">
      <div className="mb-4 flex items-end justify-between gap-4">
        <div>
          <h2 className="label">{label}</h2>
          <p className="mt-1">{reading}</p>
        </div>
        {note && <p className="pb-0.5 text-right text-[0.8125rem] text-ink-3 tnum">{note}</p>}
      </div>
      {children}
    </div>
  );
}

const MB = 1048576;
const unitName = (u: string) => u.replace(/\.service$/, "").replace(/^tiffin-/, "");

/** Each service the box runs: what it is, what it costs, only the trouble in colour. */
function Services({ res, unavailable }: { res?: BoxResources; unavailable: boolean }) {
  if (unavailable) return null;
  const list = res?.services ?? [];
  return (
    <Group label="Services" id="services" aside={res ? `${countWords(list.length, "service")}, sampled over ${duration(res.windowSeconds)}` : undefined}>
      <div className="hidden grid-cols-[minmax(0,1fr)_5rem_9rem_4.5rem] gap-x-5 pb-1.5 sm:grid" aria-hidden>
        <span />
        <span className="label text-right">CPU</span>
        <span className="label flex justify-between">
          <span>Memory</span>
          <span className="tracking-normal normal-case">0–512</span>
        </span>
        <span className="label text-right">MB</span>
      </div>
      <Rows>
        {!res && <li className="h-40 animate-pulse" />}
        {list.map((sv) => {
          const down = sv.state !== "active";
          const failed = sv.state === "failed";
          return (
            <li key={sv.unit} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-5 py-2.5 sm:grid-cols-[minmax(0,1fr)_5rem_9rem_4.5rem]">
              <div className="min-w-0">
                <p className="truncate text-[0.875rem] text-ink">
                  {pretty(unitName(sv.unit))}
                  {sv.restarts > 0 && <span className="ml-2 text-[0.8125rem] text-warn-ink">{count(sv.restarts, "restart")}</span>}
                </p>
                <p className={cn("truncate text-xs", failed || down ? "text-danger" : "text-ink-3")}>
                  {failed ? "Stopped after a failure." : down ? `Not running (${sv.state}).` : sv.description}
                </p>
              </div>
              <span className="hidden text-right text-[0.84375rem] text-ink-2 tnum sm:block">{sv.cpuPercent >= 0.5 ? percent(sv.cpuPercent) : "idle"}</span>
              <SegMeter
                className="hidden sm:block"
                size="row"
                segments={16}
                max={512}
                value={Math.min(512, sv.memoryBytes / MB)}
                label={`${sv.name} memory`}
                valueText={`${mb(sv.memoryBytes)} MB`}
              />
              <span className="text-right text-[0.875rem] text-ink tnum">{mb(sv.memoryBytes)}</span>
            </li>
          );
        })}
      </Rows>
    </Group>
  );
}

const names: Record<string, string> = {
  postgres: "Postgres",
  valkey: "Valkey",
  buildkit: "BuildKit",
  containerd: "containerd",
  caddy: "Edge",
  edge: "Edge",
  auth: "Sign-in engine",
  storage: "Storage",
  tiffin: "Tiffin",
  crowdsec: "CrowdSec",
  "victoria-metrics": "VictoriaMetrics",
  "victoria-logs": "VictoriaLogs",
  "lima-guestagent": "Lima guest agent",
  logs: "Log store",
  metrics: "Metrics store",
  "runtime-firewall": "App firewall",
  firewall: "Firewall",
};
const pretty = (n: string) => names[n] ?? n;

/** Each app: memory against its limit, CPU, and its traffic in the last hour. */
function Apps({ res }: { res?: BoxResources }) {
  const projects = useQuery(q.projects);
  const names = (projects.data ?? []).map((p) => p.name);
  const enamels = useEnamels(names);
  const traffic = useQueries({
    queries: names.map((p) => ({ queryKey: ["observe-apps", p], queryFn: () => mod.apps(p, "1h"), refetchInterval: 30_000, retry: false })),
  });
  const byKey = new Map<string, AppMetrics>();
  traffic.forEach((t) => (t.data ?? []).forEach((a) => byKey.set(`${a.project}/${a.app}`, a)));
  const groups = new Map<string, { project: string; app: string; containers: number; mem: number; limit: number; cpu: number }>();
  for (const a of res?.apps ?? []) {
    const k = `${a.project}/${a.app}`;
    const g = groups.get(k) ?? { project: a.project, app: a.app, containers: 0, mem: 0, limit: 0, cpu: 0 };
    g.containers++;
    g.mem += a.memoryBytes;
    g.limit += a.memoryLimitBytes ?? 0;
    g.cpu += a.cpuPercent;
    groups.set(k, g);
  }
  const rows = [...groups.values()].sort((a, b) => a.project.localeCompare(b.project) || b.mem - a.mem);
  if (!res || rows.length === 0) return null;
  return (
    <Group label="Apps" id="apps" aside="Memory against each app's limit; traffic in the last hour">
      <Rows>
        {rows.map((g) => {
          const t = byKey.get(`${g.project}/${g.app}`);
          const share = g.limit ? (g.mem / g.limit) * 100 : 0;
          return (
            <li key={g.project + g.app} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-5 gap-y-1 py-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_9rem_4.5rem]">
              <div className="min-w-0">
                <p className="flex items-center gap-2 text-[0.875rem] text-ink">
                  <EnamelSwatch enamel={enamels[g.project]} size={7} />
                  <span className="truncate">
                    <span className="text-ink-3">{g.project} ›</span> {g.app}
                  </span>
                </p>
                <p className="truncate pl-[15px] text-xs text-ink-3">
                  {countWords(g.containers, "instance")} · {g.cpu >= 0.5 ? `${percent(g.cpu)} CPU` : "CPU idle"}
                </p>
              </div>
              <p className="col-span-2 row-start-2 pl-[15px] text-[0.8125rem] text-ink-2 tnum sm:col-span-1 sm:row-start-auto sm:pl-0">
                {t ? (
                  <>
                    {int(t.requests)} requests
                    {t.requests > 0 && (
                      <>
                        {" · "}
                        <span className={t.errorRate > 0.01 ? "text-danger" : undefined}>{t.errors ? `${pct(t.errorRate, 1)} errors` : "no errors"}</span>
                        {" · "}p95 {ms(t.p95ms)}
                      </>
                    )}
                  </>
                ) : (
                  <span className="text-ink-3">No traffic in the last hour.</span>
                )}
              </p>
              <SegMeter
                className="hidden sm:block"
                size="row"
                segments={16}
                value={share}
                warnAt={0.8}
                fullAt={0.95}
                label={`${g.app} memory of its limit`}
                valueText={`${mb(g.mem)} of ${mb(g.limit)} MB`}
              />
              <span className="col-start-2 row-start-1 text-right text-[0.875rem] text-ink tnum sm:col-start-auto sm:row-start-auto">
                {mb(g.mem)}
                <span className="text-xs text-ink-3"> / {mb(g.limit)}</span>
              </span>
            </li>
          );
        })}
      </Rows>
    </Group>
  );
}

function Disks({ res, stores }: { res?: BoxResources; stores?: Record<string, string> }) {
  if (!res) return null;
  const disks = [
    { name: "Data", what: "Databases, files, backups, apps", d: res.disks.data },
    { name: "System", what: "The operating system", d: res.disks.system },
  ];
  const broken = Object.entries(stores ?? {}).filter(([, v]) => v !== "ok");
  return (
    <Group label="Disks" id="disks" aside={broken.length ? <span className="text-danger">{broken.map(([k, v]) => `${k}: ${v}`).join("; ")}</span> : undefined}>
      <Rows>
        {disks.map(({ name, what, d }) => (
          <li key={d.mount} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-5 gap-y-2 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_9rem]">
            <div className="min-w-0">
              <p className="text-[0.875rem] text-ink">
                {name} <span className="ident ml-1 text-ink-3">{d.mount}</span>
              </p>
              <p className="text-xs text-ink-3">{what}</p>
            </div>
            <SegMeter
              className="col-span-2 row-start-2 max-w-[26rem] sm:col-span-1 sm:row-start-auto"
              value={d.usedPercent}
              warnAt={0.8}
              fullAt={0.95}
              scale
              label={`${name} disk in use`}
              valueText={`${dec(d.usedPercent, 0)} percent`}
            />
            <p className={cn("col-start-2 row-start-1 text-right text-[0.875rem] tnum sm:col-start-auto sm:row-start-auto", d.usedPercent > 85 ? "text-danger" : "text-ink")}>
              {bytes(d.usedBytes)}
              <span className="block text-xs text-ink-3">of {bytes(d.totalBytes)}</span>
            </p>
          </li>
        ))}
        <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-5 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_9rem]">
          <p className="text-[0.875rem] text-ink">Swap</p>
          <p className="hidden text-[0.8125rem] text-ink-3 sm:block">Spill-over when memory is short; little use is normal.</p>
          <p className="text-right text-[0.875rem] text-ink tnum">{res.memory.swapTotalBytes ? `${bytes(res.memory.swapUsedBytes)}` : "off"}</p>
        </li>
      </Rows>
    </Group>
  );
}
