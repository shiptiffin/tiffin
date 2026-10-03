import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Code, ProblemNote } from "@/components/problem";
import { cn } from "@/lib/cn";
import { uptime } from "@/lib/time";
import { Page } from "@/components/page";

export function StatusPage() {
  useTitle("Status");
  const s = useQuery(q.status(5_000));
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  if (s.isPending)
    return (
      <Page>
        <div className="h-12 w-72 animate-pulse rounded-md bg-hover" />
      </Page>
    );
  if (s.isError && !s.data)
    return (
      <Page>
        <ProblemNote error={s.error} title="Can't reach the box" />
      </Page>
    );

  const d = s.data!;
  const checks = d.checks ?? [];
  const failing = checks.filter((c) => !c.ok);
  const ago = Math.max(0, Math.round((now - s.dataUpdatedAt) / 1000));

  return (
    <Page>
      <header className="animate-rise">
        <p className="flex items-center gap-2 text-sm text-ink-3">
          <span className={cn("relative grid size-2 place-items-center")}>
            <span className={cn("size-2 rounded-full", d.ok ? "bg-rev" : "bg-irr")} />
          </span>
          {s.isError ? "Lost contact; showing the last report" : `Checked ${ago < 2 ? "just now" : `${ago} s ago`} · refreshes every 5 s`}
        </p>
        <h1 className="display mt-3 text-3xl text-ink">
          {d.ok ? (
            <>
              All good<span className="text-ink-3">. Your box is </span>
              <span className="display-italic text-rev">healthy</span>
              <span className="text-ink-3">.</span>
            </>
          ) : (
            <>
              <span className="text-irr">{failing.length === 1 ? "One check is failing" : `${failing.length} checks are failing`}</span>
              <span className="text-ink-3">. Here's what we know.</span>
            </>
          )}
        </h1>
      </header>

      <dl className="mt-10 grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-4">
        <Stat label="Version" value={d.version} mono />
        <Stat label="Up for" value={uptime(d.uptime)} />
        <Stat label="Host" value={d.host.hostname} mono />
        <Stat label="Platform" value={`${d.host.os}/${d.host.arch}`} mono />
      </dl>

      <section className="mt-12" aria-labelledby="checks">
        <div className="mb-3 flex items-baseline justify-between">
          <h2 id="checks" className="display-italic text-xl text-ink">
            Checks
          </h2>
          <span className="text-sm text-ink-3">
            {checks.length - failing.length} of {checks.length} passing
          </span>
        </div>
        <ul className="divide-y divide-rule border-y border-rule">
          {checks.map((c) => (
            <li key={c.name} className="grid grid-cols-[1.5rem_1fr] items-start gap-x-3 py-3.5 sm:grid-cols-[1.5rem_10rem_1fr]">
              <CheckMark ok={c.ok} />
              <span className="font-mono text-base text-ink">{c.name}</span>
              <span className={cn("col-start-2 text-base sm:col-start-auto", c.ok ? "text-ink-2" : "text-irr")}>
                {c.detail ? <Code text={c.detail} /> : c.ok ? "OK" : "Failing"}
              </span>
            </li>
          ))}
        </ul>
        <p className="mt-4 text-sm text-ink-3">
          This page keeps working when apps and databases don't. From a terminal,{" "}
          <code className="rounded-xs bg-hover px-1 font-mono text-ink-2">tiffin doctor</code> runs the same checks.
        </p>
      </section>
    </Page>
  );
}

function Stat({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="bg-raised px-4 py-4">
      <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</dt>
      <dd className={cn("mt-1.5 truncate text-lg text-ink", mono && "font-mono text-md")} title={value}>
        {value}
      </dd>
    </div>
  );
}

function CheckMark({ ok }: { ok: boolean }) {
  return ok ? (
    <svg
      viewBox="0 0 16 16"
      className="mt-0.5 size-4 text-rev"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-label="passing"
    >
      <circle cx="8" cy="8" r="6.25" />
      <path d="m5.4 8.2 1.8 1.8 3.5-3.7" />
    </svg>
  ) : (
    <svg viewBox="0 0 16 16" className="mt-0.5 size-4 text-irr" fill="currentColor" aria-label="failing">
      <circle cx="8" cy="8" r="7" />
      <path d="M8 4.6v4" stroke="var(--raised)" strokeWidth={1.7} strokeLinecap="round" />
      <circle cx="8" cy="11.2" r="1" fill="var(--raised)" />
    </svg>
  );
}
