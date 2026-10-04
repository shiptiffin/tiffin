import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { type BoxResources, type ProjectState, type StatusReport } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Nameplate } from "@/components/nameplate";
import { Page, PageHeader } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { Qty } from "@/components/qty";
import { SegMeter } from "@/components/seg-meter";
import { Carrier, Lid, Rim, TierHead, TierRow, useUnlatch } from "@/components/stack";
import { checkWords } from "@/components/tier-status";
import { boxName, boxUp, domainFrom, versionLabel, whereItRuns } from "@/lib/box";
import { cn } from "@/lib/cn";
import { memoryModel, type MemoryModel } from "@/lib/memory";
import { bytesParts, countWords, dec, duration, int, words } from "@/lib/format";

const MB = 1048576;
const RESERVE_MB = 512; // kept free for spikes

type AppSpec = { framework?: string; role?: string; instances?: number; memoryMB?: number; path?: string };

/**
 * Settings › Machine: the box itself, for when you want to look inside. The
 * machine drawn as a tiffin carrier: its vitals on the lid, each project's
 * share of memory, the platform that runs every project's databases and
 * files, and the room left. Nothing here needs touching day to day; the
 * projects' own controls live on their pages.
 */
export function BoxPage() {
  useTitle("Machine");
  const status = useQuery(q.status());
  const resQ = useQuery(q.resources);
  // A laptop dev server answers 503, or zeros where it can't measure: treat both as "not measured".
  const res = { data: resQ.data && resQ.data.memory.totalBytes > 0 ? resQ.data : undefined, error: resQ.error ?? (resQ.data && resQ.data.memory.totalBytes === 0 ? new Error("not measured") : null) };
  const mem = useMemo(() => (res.data ? memoryModel(res.data) : undefined), [res.data]);
  const projects = useQuery(q.projects);
  const names = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const states = useQueries({ queries: names.map((n) => q.project(n)) });

  return (
    <Page full>
      <PageHeader
        title="Machine"
        lede="The computer your projects run on: how full it is, the parts that run every project’s databases and files, and the room left."
      />
      <div className="mt-12 max-w-[64rem]">
        <Carrier>
          <Lid>
            <Nameplate
              name={boxName(status.data)}
              where={whereItRuns(status.data)}
              version={versionLabel(status.data)}
              uptime={boxUp(res.data?.uptimeSeconds)}
              domain={<BoxDomain projects={names} />}
            />
            <Vitals res={res.data} mem={mem} unavailable={!!res.error} names={names} />
          </Lid>
          <Rim />
          <PlatformTier res={res.data} mem={mem} status={status.data} unavailable={!!res.error} />
          <RoomLeft res={res.data} mem={mem} states={states.map((s) => s.data)} empty={names.length === 0} />
        </Carrier>
      </div>
    </Page>
  );
}

function BoxDomain({ projects }: { projects: string[] }) {
  // Any project's storage endpoint names the box's domain (s3.<domain>); on the box itself the dashboard's own host does too.
  const st = useQueries({ queries: projects.map((p) => ({ ...mq.storage(p), retry: false, staleTime: 300_000 })) });
  const endpoint = st.map((x) => x.data?.endpoint).find(Boolean);
  const d = domainFrom(endpoint) ?? location.hostname.replace(/^dashboard\./, "");
  return <>{d}</>;
}

// ───────────────────────── vitals ─────────────────────────

function Vitals({
  res,
  mem,
  unavailable,
  names,
}: {
  res?: BoxResources;
  mem?: MemoryModel;
  unavailable: boolean;
  names: string[];
}) {
  if (unavailable) return <p className="mt-4 text-sm text-ink-3">Memory, CPU and disk are measured on a running box. This server runs without one.</p>;
  if (!res || !mem) return <div className="mt-4 h-[88px]" />;
  const used = bytesParts(mem.usedMB * MB, 2);
  const total = bytesParts(mem.totalMB * MB, 1);
  const disk = res.disks.data;
  const dUsed = bytesParts(disk.usedBytes, 1);
  const dTotal = bytesParts(disk.totalBytes, 0);
  const partsMB = mem.platformMB - mem.systemMB;
  const seg = (v: number) => `${Math.max(0, (v / mem.totalMB) * 100)}%`;
  const withApps = names.filter((n) => (mem.projects[n] ?? 0) > 0);
  return (
    <div className="mt-4 grid grid-cols-2 gap-x-7 gap-y-5 sm:grid-cols-[1.9fr_1fr_1fr] max-sm:gap-y-4">
      <div className="min-w-0 max-sm:col-span-full">
        <p className="label">Memory</p>
        <p className="reading mt-0.5">
          {used.value}
          <span className="u text-[0.8125rem]">&#8239;{used.unit} in use of {total.value}&#8239;{total.unit}</span>
        </p>
        <div
          className="mt-2.5 flex h-2.5 gap-0.5"
          role="img"
          aria-label={`Memory: ${int(mem.usedMB)} MB in use of ${int(mem.totalMB)} MB; ${int(mem.freeMB)} MB room left`}
        >
          {withApps.map((n) => (
            <span key={n} title={n} className={cn("h-full min-w-[3px] rounded-[2px]", withApps.indexOf(n) % 2 ? "bg-ink-4" : "bg-ink-3")} style={{ width: seg(mem.projects[n]) }} />
          ))}
          {partsMB > 0 && <span className="h-full min-w-[3px] rounded-[2px] bg-[var(--part-3)]" style={{ width: seg(partsMB) }} />}
          {mem.systemMB > 0 && <span className="h-full min-w-[3px] rounded-[2px] bg-[var(--part-4)]" style={{ width: seg(mem.systemMB) }} />}
          <span className="h-full flex-1 rounded-[2px] border border-dashed border-rule-3" />
        </div>
        <p className="mt-2 flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-ink-3">
          {withApps.map((n) => (
            <span key={n} className="inline-flex items-center gap-1.5">
              <i className={cn("inline-block size-[7px] rounded-[1.5px]", withApps.indexOf(n) % 2 ? "bg-ink-4" : "bg-ink-3")} />
              {n}
            </span>
          ))}
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] bg-[var(--part-3)]" />
            platform
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] bg-[var(--part-4)]" />
            Linux and builds
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] border border-dashed border-rule-3" />
            room left
          </span>
        </p>
      </div>
      <div className="min-w-0">
        <p className="label">CPU</p>
        <p className="reading mt-0.5">
          {dec(res.cpu.usedPercent, 0)}
          <span className="u text-[0.8125rem]">&#8239;% of {countWords(res.cpu.count, "CPU")}</span>
        </p>
        <SegMeter className="mt-2.5" label="CPU in use" value={res.cpu.usedPercent} scale warnAt={0.8} fullAt={0.95} valueText={`${dec(res.cpu.usedPercent, 0)} percent`} />
      </div>
      <div className="min-w-0">
        <p className="label">Disk</p>
        <p className="reading mt-0.5">
          {dUsed.value}
          <span className="u text-[0.8125rem]">
            &#8239;{dUsed.unit} of {dTotal.value}&#8239;{dTotal.unit}
          </span>
        </p>
        <SegMeter className="mt-2.5" label="Data disk in use" value={disk.usedPercent} scale warnAt={0.8} fullAt={0.95} valueText={`${dec(disk.usedPercent, 0)} percent`} />
      </div>
    </div>
  );
}

// ───────────────────────── platform ─────────────────────────

const platformRows: Array<{ key: string; name: string; sub: string; units: string[]; check?: string[]; to?: string; say?: (s: StatusReport | undefined) => string }> = [
  {
    key: "tiffin",
    name: "Tiffin",
    sub: "API, dashboard, edge",
    units: ["tiffin"],
    check: ["edge"],
    to: "/status",
    say: (s) => `Serves the dashboard, the API and every app’s HTTPS. Running for ${uptimeWords(s?.uptime)}.`,
  },
  {
    key: "postgres",
    name: "Database",
    sub: "Postgres 18, every project’s",
    units: ["postgres"],
    check: ["postgres"],
    to: "/backups",
    say: (s) => checkWords(detail(s, "postgres").replace(/^Postgres [\d.]+(?: \([^)]*\))? up, /, "")),
  },
  { key: "valkey", name: "Cache", sub: "Valkey, Redis-compatible", units: ["valkey"], check: ["valkey"], say: (s) => checkWords(detail(s, "valkey").replace(/^Valkey [\d.]+ up, /, "").replace(/\.0 MB/g, " MB")) },
  { key: "auth", name: "Auth", sub: "Users, passkeys, sign-in", units: ["auth"], check: ["auth"] },
  { key: "storage", name: "Files", sub: "S3-compatible", units: ["storage"], check: ["storage"], say: (s) => checkWords(detail(s, "storage").replace(/^versitygw v[\d.]+ on [\d.:]+,\s*/i, "")) },
  { key: "observe", name: "Health", sub: "Metrics, logs, errors", units: ["victoria-metrics", "victoria-logs"], check: ["observe.metrics"], to: "/metrics", say: () => "Every app’s metrics and logs, stored here. Nothing leaves the box." },
  {
    key: "protect",
    name: "Shield",
    sub: "CrowdSec, firewall",
    units: ["crowdsec", "firewall", "app-firewall"],
    check: ["protection"],
    to: "/protect",
    say: (s) => {
      const d = detail(s, "protection");
      const mode = d.match(/^(\w+) mode/)?.[1] ?? "normal";
      const bans = Number(d.match(/CrowdSec (\d+) ban/)?.[1] ?? 0);
      return `${mode.charAt(0).toUpperCase()}${mode.slice(1)} mode. ${bans === 0 ? "Nobody banned right now." : `${countWords(bans, "address", "addresses", true)} banned right now.`}`;
    },
  },
];

function PlatformTier({ res, mem, status, unavailable }: { res?: BoxResources; mem?: MemoryModel; status?: StatusReport; unavailable: boolean }) {
  const { lifting, open } = useUnlatch();
  const [shown, setShown] = useState(false); // phones: collapsed until asked
  const svc = (names: string[]) => (res?.services ?? []).filter((s) => names.includes(s.name));
  // A sample can come back without the service list (systemd busy); then show the parts without numbers.
  const measured = !!mem && (res?.services ?? []).length > 0;
  const rows = platformRows.filter((r) => !measured || svc(r.units).length > 0);
  const runtime = detail(status, "runtime");
  const hide = shown ? undefined : "max-sm:hidden";
  return (
    <section aria-label="Platform">
      <TierHead
        name="Platform"
        about="Runs the box, and holds every project’s database and files."
        total={measured ? int(mem!.platformMB) : undefined}
      />
      {unavailable && <p className="px-5 pb-4 text-sm text-ink-3 max-sm:px-3.5">Measured on a running box.</p>}
      {!unavailable && (
        <button
          type="button"
          onClick={() => setShown((x) => !x)}
          aria-expanded={shown}
          className="mx-3.5 mb-2 text-[0.8125rem] font-[550] text-brass-ink sm:hidden"
        >
          {shown ? "Hide its parts" : `Show its ${words(rows.length + 1)} parts`}
        </button>
      )}
      {!unavailable &&
        rows.map((r) => {
          const units = svc(r.units);
          const mbv = measured ? (mem!.parts[r.key] ?? 0) : undefined;
          const failed = units.find((s) => s.state === "failed");
          const check = status?.checks?.find((c) => r.check?.includes(c.name));
          const sentence = failed ? (
            <span className="text-danger">{failed.name} has stopped. Restarts so far: {failed.restarts}.</span>
          ) : r.say ? (
            r.say(status)
          ) : check ? (
            <span className={cn(!check.ok && "text-danger")}>{checkWords(check.detail)}</span>
          ) : (
            "Running."
          );
          return (
            <div key={r.name} className={hide}>
              <TierRow
                lever={failed ? <PilotLight state="fault" label="Stopped" /> : undefined}
                name={r.name}
                sub={r.sub}
                status={sentence}
                share={mbv !== undefined && mbv >= 1 && <SegMeter size="row" segments={16} max={512} value={mbv} label={`${r.name} memory`} valueText={`${int(mbv)} MB`} />}
                amount={mbv !== undefined ? int(mbv) : undefined}
                fault={!!failed}
                unlatching={lifting === r.name}
                onOpen={r.to ? () => open(r.name, r.to!) : undefined}
              />
            </div>
          );
        })}
      {!unavailable && (
        <div className={hide}>
          <TierRow
            name="Linux and builds"
            sub="Kernel, containers, BuildKit"
            status={
              <>
                {runtime ? `${checkWords(runtime).replace(/\.$/, "")}. ` : ""}
                {measured && mem!.cacheMB > 0 && <span className="text-ink-3">Not counted: about {int(mem!.cacheMB)}&#8239;MB of cache Linux hands back when apps need it.</span>}
              </>
            }
            share={measured && mem!.systemMB >= 1 && <SegMeter size="row" segments={16} max={512} value={mem!.systemMB} label="Linux and builds memory" valueText={`${int(mem!.systemMB)} MB`} />}
            amount={measured ? int(mem!.systemMB) : undefined}
          />
        </div>
      )}
    </section>
  );
}

// ───────────────────────── room left ─────────────────────────

function RoomLeft({ res, mem, states, empty }: { res?: BoxResources; mem?: MemoryModel; states: Array<ProjectState | undefined>; empty: boolean }) {
  if (!res || !mem) return <div className="m-3 h-16" />;
  const free = mem.freeMB;
  // "The size of web": web's memory cap, or the first app's, or 512 MB.
  const caps = states.flatMap((s) => (s?.resources ?? []).filter((r) => r.address.startsWith("app/")).map((r) => ({ app: r.address.slice(4), mb: ((r.spec ?? {}) as AppSpec).memoryMB ?? 512 })));
  const like = caps.find((c) => c.app === "web") ?? caps[0];
  const per = like?.mb ?? 512;
  const fits = Math.max(0, Math.floor((free - RESERVE_MB) / per));
  const say = empty
    ? `Room for about ${words(fits)} apps of ${int(per)}\u202FMB each, keeping ${int(RESERVE_MB)}\u202FMB spare.`
    : `Enough for about ${words(fits)} more ${fits === 1 ? "app" : "apps"} the size of ${like?.app ?? "an app"} (up to ${int(per)}\u202FMB each), keeping ${int(RESERVE_MB)}\u202FMB spare.`;
  const disk = bytesParts(res.disks.data.freeBytes, 0);
  return (
    <>
      <div className="rim mt-1" data-thin aria-hidden />
      <div className="tier-grid min-h-[44px] max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:px-3.5">
        <div className="col-start-2 text-[0.875rem] max-sm:col-start-1">In use</div>
        <div className="col-span-2 col-start-3 text-xs text-ink-3 max-sm:hidden">
          The projects’ apps and the platform, as Linux counts it, of {int(mem.totalMB)}&#8239;MB.
        </div>
        <div className="col-start-5 text-right text-[0.875rem] font-[550] whitespace-nowrap tnum max-sm:col-start-2">
          {int(mem.usedMB)}
          <span className="u">&#8239;MB</span>
        </div>
      </div>
      <div className="room tier-grid m-3 mt-1 min-h-16 py-3.5 max-sm:mx-2 max-sm:flex max-sm:flex-col max-sm:items-start max-sm:gap-1.5 max-sm:px-3.5">
        <div className="col-start-2 flex w-full items-baseline justify-between text-[0.875rem] font-[550]">
          Room left
          <span className="tnum sm:hidden">{int(free)}&#8239;MB</span>
        </div>
        <p className="col-span-2 col-start-3 text-[0.84375rem] leading-5 text-ink-2">
          {say} {disk.value}&#8239;{disk.unit} of disk free.{" "}
          <Link to="/new" className="font-[550] whitespace-nowrap text-brass-ink hover:underline hover:underline-offset-4">
            Start a project
          </Link>
        </p>
        <div className="col-start-5 text-right max-sm:hidden">
          <Qty value={int(free)} unit="MB" className="text-[0.9375rem] font-[550]" />
        </div>
      </div>
    </>
  );
}

const detail = (s: StatusReport | undefined, name: string) => s?.checks?.find((c) => c.name === name)?.detail ?? "";
const uptimeWords = (go: string | undefined) => {
  if (!go) return "a while";
  const m = go.match(/(?:(\d+)h)?(?:(\d+)m(?!s))?(?:([\d.]+)s)?/);
  const secs = m ? Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0) : 0;
  return duration(secs);
};
