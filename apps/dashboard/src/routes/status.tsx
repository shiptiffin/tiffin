import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import type { BoxResources, Check } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Alarm, Facts, Group, Rows } from "@/components/health-kit";
import { Page, Skeleton } from "@/components/page";
import { Code, ProblemNote, sentence } from "@/components/problem";
import { boxName, versionLabel, whereItRuns } from "@/lib/box";
import { cn } from "@/lib/cn";
import { countWords, dec, duration, int, plainWords, withUnit, words } from "@/lib/format";
import { relative, uptime } from "@/lib/time";

/** What each check is, in the words the rest of the dashboard uses. */
const checkNames: Record<string, string> = {
  state: "Platform state",
  provision: "Modules",
  protection: "Protection",
  crowdsec: "CrowdSec",
  firewall: "Firewall",
  postgres: "Postgres",
  valkey: "Valkey",
  "observe.metrics": "Metrics store",
  "observe.logs": "Log store",
  "observe.ingest": "Error intake",
  email: "Email",
  storage: "Storage",
  auth: "Sign-in",
  queue: "Queues",
  "analytics.collector": "Analytics",
  "analytics.geoip": "Visitor locations",
  runtime: "Apps",
  backups: "Backups",
};
const checkName = (c: Check) => checkNames[c.name] ?? c.name.charAt(0).toUpperCase() + c.name.slice(1);

/** Status lines are written for terminals; this tidies the common ones for people. */
function checkWords(detail: string) {
  return sentence(
    plainWords(detail)
      .replace(/\bdatabase\(s\)/g, "databases")
      .replace(/(\d+)m(\d+)s ago/g, (_, m) => `${m} min ago`)
      .replace(/ \((full|incremental), bk_[A-Z0-9]+\)/, " ($1)")
      .replace(/; local repository only \(off-box copies come later\)/, "; kept on this box only")
      .replace(/\b([KMG])iB\b/g, "$1B")
      .replace(/\b(\d+)s\b/g, "$1\u202fs")
      .replace(/ (\d+(?:\.\d+)?)\.0( |\u202f)(MB|GB)/g, " $1$2$3"),
  );
}

/**
 * Health, the landing: is anything wrong? One sentence answers; then only
 * what isn't fine speaks up; then the six health pages as rows with one
 * line each; then every check, quiet unless failing.
 */
export function StatusPage() {
  useTitle("Health");
  const s = useQuery(q.status(5_000));
  const res = useQuery(q.resources);
  const alerts = useQuery({ ...mq.alerts, retry: false });
  const issues = useQuery({ ...mq.issues(undefined, "unresolved"), retry: false });
  const backups = useQuery({ ...mq.backups, retry: false });
  const protect = useQuery({ ...mq.protect, retry: false });
  const rules = useQuery({ ...mq.rules, retry: false });
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  if (s.isPending)
    return (
      <Page>
        <Skeleton className="h-4 w-48" />
        <Skeleton className="mt-3 h-9 w-[36rem] max-w-full" />
        <Skeleton className="mt-12 h-64" />
      </Page>
    );
  if (s.isError && !s.data)
    return (
      <Page>
        <p className="label mb-2">Health</p>
        <h1 className="sentence text-ink">The box isn’t answering.</h1>
        <ProblemNote className="mt-6" error={s.error} title="Can't reach the box" />
        <p className="mt-4 text-sm text-ink-3">
          From a terminal, <code className="ident text-ink-2">tiffin doctor</code> checks the box directly.
        </p>
      </Page>
    );

  const d = s.data!;
  const checks = d.checks ?? [];
  const failing = checks.filter((c) => !c.ok);
  const firing = alerts.data?.firing ?? [];
  const open = issues.data ?? [];
  const attack = protect.data?.underAttack;
  const last = backups.data?.lastOkAt;
  const staleBackup = backups.data && (!last || now - new Date(last).getTime() > 26 * 3600_000);
  const ago = Math.max(0, Math.round((now - s.dataUpdatedAt) / 1000));

  // The one sentence: the worst thing first, then the next worth knowing.
  let head: string;
  if (failing.length === 1) head = `${checkName(failing[0])} is failing.`;
  else if (failing.length > 1) head = `${countWords(failing.length, "check", "checks", true)} are failing: ${failing.map(checkName).join(", ")}.`;
  else if (firing.length > 0) head = firing.length === 1 ? `An alert is firing: ${firing[0].summary.replace(/\.$/, "")}.` : `${words(firing.length, true)} alerts are firing.`;
  else head = "Nothing is wrong.";
  const tail: string[] = [];
  if (attack?.on) tail.push(`Under-attack mode is on for ${duration(attack.minutesLeft * 60)} more.`);
  if (staleBackup) tail.push(last ? `The last backup was ${relative(last, now)}.` : "Nothing has been backed up yet.");
  if (open.length > 0) {
    const worst = [...open].sort((a, b) => b.count - a.count)[0];
    const where = new Set(open.map((i) => `${i.project}’s ${i.app}`));
    tail.push(
      where.size === 1
        ? `${[...where][0]} has ${countWords(open.length, "open error", "open errors")}${open.length > 1 ? `, the worst seen ${countWords(worst.count, "time")}` : ""}.`
        : `${countWords(open.length, "open error", "open errors", true)} across ${words(where.size)} apps.`,
    );
  }
  const rulesOn = (rules.data ?? []).filter((r) => r.enabled).length;

  return (
    <Page>
      <header>
        <p className="label mb-2 flex items-center gap-2">
          <span>Health</span>
          <span aria-hidden>·</span>
          <span className={cn("tracking-normal normal-case", s.isError && "text-danger")}>
            {s.isError ? "lost contact, showing the last report" : ago < 3 ? "checked just now" : `checked ${ago} s ago`}
          </span>
        </p>
        <h1 className={cn("sentence max-w-[44rem] max-sm:text-[1.5rem] max-sm:leading-[1.875rem]", failing.length || firing.length ? "text-danger" : "text-ink")}>
          {head}
          {tail.length > 0 && <span className="text-ink-2"> {tail.join(" ")}</span>}
        </h1>
      </header>

      {(failing.length > 0 || firing.length > 0 || attack?.on || staleBackup) && (
        <Group label="Needs a look" id="look" flush className="mt-9">
          <Rows>
            {failing.map((c) => (
              <Alarm key={c.name} title={`${checkName(c)} is failing`} detail={c.detail ? <Code text={checkWords(c.detail)} /> : undefined} />
            ))}
            {firing.map((a) => (
              <Alarm key={a.rule + a.subject} title={sentence(a.summary)} detail={`Firing since ${relative(a.since, now)}.`} to="/alerts" />
            ))}
            {attack?.on && (
              <Alarm
                tone="warn"
                title="Under-attack mode is on"
                detail={`Every visitor solves a challenge first. It switches itself off in ${duration(attack.minutesLeft * 60)}.`}
                to="/protect"
              />
            )}
            {staleBackup && (
              <Alarm
                tone="warn"
                title={last ? `No backup since ${relative(last, now)}` : "Nothing has been backed up yet"}
                detail="A bad day is only an inconvenience if there's something to go back to."
                to="/backups"
              />
            )}
          </Rows>
        </Group>
      )}

      <Group label="At a glance" id="areas" aside={versionLabel(d)}>
        <Rows>
          <Area to="/metrics" name="Metrics" status={res.data ? vitals(res.data) : res.isError ? "Measured on a running box." : "…"} />
          <Area to="/logs" name="Logs" status="Everything the box and its apps write, searchable and live." />
          <Area
            to="/errors"
            name="Errors"
            status={
              issues.isError ? (
                "Error tracking runs on a box."
              ) : open.length === 0 ? (
                "No open errors."
              ) : (
                <span className="text-danger">{countWords(open.length, "open error", "open errors", true)}.</span>
              )
            }
            amount={open.length > 0 ? int(open.length) : undefined}
          />
          <Area
            to="/alerts"
            name="Alerts"
            status={
              firing.length > 0 ? (
                <span className="text-danger">{countWords(firing.length, "alert", "alerts", true)} firing.</span>
              ) : rules.data ? (
                `Nothing firing. ${countWords(rulesOn, "rule", "rules", true)} watching.`
              ) : (
                "Nothing firing."
              )
            }
          />
          <Area
            to="/backups"
            name="Backups"
            status={
              backups.isError ? (
                "Backups run on a box."
              ) : !backups.data ? (
                "…"
              ) : staleBackup ? (
                <span className="text-warn-ink">{last ? `Last backed up ${relative(last, now)}.` : "Never backed up."}</span>
              ) : (
                `Last backed up ${relative(last!, now)}. Kept on this box only.`
              )
            }
          />
          <Area
            to="/protect"
            name="Protection"
            status={
              !protect.data ? (
                protect.isError ? "Protection runs on a box." : "…"
              ) : attack?.on ? (
                <span className="text-warn-ink">Under-attack mode, {duration(attack.minutesLeft * 60)} left.</span>
              ) : (
                `Normal. ${protect.data.crowdsec.decisions ? `${countWords(protect.data.crowdsec.decisions, "address is", "addresses are", true)} banned.` : "Nobody is banned."}`
              )
            }
          />
        </Rows>
      </Group>

      <Group
        label="Checks"
        id="checks"
        aside={failing.length ? <span className="text-danger">{failing.length} failing</span> : `${words(checks.length, true)} checks, all passing`}
      >
        <Rows>
          {[...failing, ...checks.filter((c) => c.ok)].map((c) => (
            <li key={c.name} className="grid gap-x-6 py-2.5 sm:grid-cols-[11rem_minmax(0,1fr)]">
              <span className={cn("text-[0.875rem]", c.ok ? "text-ink" : "font-[550] text-danger")}>{checkName(c)}</span>
              <span className={cn("text-[0.84375rem]", c.ok ? "text-ink-3" : "text-ink-2")}>
                {c.detail ? <Code text={checkWords(c.detail)} /> : c.ok ? "" : "Failing, with no detail."}
              </span>
            </li>
          ))}
        </Rows>
        <p className="mt-3 text-[0.8125rem] text-ink-3">
          This page keeps answering when apps and databases don’t. From a terminal, <code className="ident text-ink-2">tiffin doctor</code> runs the
          same checks.
        </p>
      </Group>

      <Group label="This box" id="box">
        <Facts
          items={[
            ["Name", boxName(d)],
            ["Runs on", <>{whereItRuns(d)} <span className="ident ml-1.5 text-ink-3">{d.host.hostname}</span></>],
            ["Version", versionLabel(d)],
            ["Tiffin up for", uptime(d.uptime)],
            res.data && ["Machine up for", duration(res.data.uptimeSeconds)],
          ]}
        />
      </Group>
    </Page>
  );
}

function vitals(r: BoxResources) {
  const p = (v: number) => withUnit(dec(v, 0), "%");
  return `CPU ${p(r.cpu.usedPercent)}, memory ${p(r.memory.usedPercent)}, data disk ${p(r.disks.data.usedPercent)}.`;
}

function Area({ to, name, status, amount }: { to: string; name: string; status: ReactNode; amount?: string }) {
  return (
    <li>
      <Link
        to={to as "/"}
        search={{} as never}
        className="group -mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 rounded-[6px] px-2 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk sm:grid-cols-[11rem_minmax(0,1fr)_7rem_1.5rem]"
      >
        <span className="text-[0.875rem] font-[550] text-ink">{name}</span>
        <span className="col-span-2 row-start-2 text-[0.84375rem] text-ink-2 sm:col-span-1 sm:row-start-auto">{status}</span>
        <span className="hidden items-center justify-end text-[0.875rem] text-danger tnum sm:flex">{amount}</span>
        <span aria-hidden className="col-start-2 row-start-1 text-right text-ink-4 transition-colors group-hover:text-ink sm:col-start-auto sm:row-start-auto">
          →
        </span>
      </Link>
    </li>
  );
}
