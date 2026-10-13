import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { HostHints, RecordsTable } from "@/components/dns-records";
import { agoWords, failedAt, Steps, useNow, withTicks } from "@/components/domains-parts";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { isApex, providerName, recheckDomain, setRecords, stateWords, type DnsRecord, type Domain } from "@/lib/domains";

/**
 * What's left before a domain (and its www) is live, said precisely: where
 * it is on the three steps, the exact records to add with a tick for each
 * one DNS already answers, the registrar's parking records to delete first
 * when DNS still answers with them, and a calm line that says when the box
 * last looked and when it looks next (it keeps looking after you leave).
 */
export function DomainSetup({
  project,
  d,
  www,
  writer,
  local,
  className,
}: {
  project: string;
  d: Domain;
  www?: Domain;
  writer: boolean;
  local: boolean;
  className?: string;
}) {
  const qc = useQueryClient();
  const [byHand, setByHand] = useState(false);
  const pending = [d, www].filter((x): x is Domain => !!x && x.state !== "live");
  const lead = pending[0];
  // Each name's CNAME and its A/AAAA records, wherever the box put them (records first, alternative second).
  const ways = (x: Domain) => {
    const all = [...(x.records ?? []), ...(x.alternative ?? [])];
    return { cname: all.filter((r) => r.type === "CNAME"), a: all.filter((r) => r.type !== "CNAME") };
  };
  const hasAlt = pending.some((x) => x.state !== "issuing" && x.alternative?.length);
  // A name that already answers with addresses of its own keeps showing A records; else a CNAME where one is offered (one record, and it follows the box).
  const [mode, setMode] = useState<"cname" | "a">(() => (pending.some((x) => x.alternative?.length && !x.cname && x.found?.length) ? "a" : "cname"));
  const recordsOf = (x: Domain): DnsRecord[] => {
    const w = ways(x);
    if (!x.alternative?.length) return x.records ?? [];
    return mode === "cname" && w.cname.length ? w.cname : w.a;
  };
  const check = useMutation({
    mutationFn: () => Promise.all(pending.map((x) => recheckDomain(project, x.domain))),
    onSettled: () => qc.invalidateQueries({ queryKey: ["domains", project] }),
  });
  const needRecords = pending.filter((x) => x.state === "waiting_for_dns" || failedAt(x) === "dns");
  const managedBy = needRecords.find((x) => x.managedBy)?.managedBy;
  const auto = useMutation({
    mutationFn: () => setRecords(needRecords.flatMap((x) => x.records ?? [])),
    onSuccess: () => {
      toast({ title: `Added the records at ${providerName(managedBy)}.`, detail: "The box checks them now; HTTPS follows within a minute or two." });
      check.mutate();
    },
  });

  if (!lead) return null;
  const rows = needRecords.flatMap((x) => withTicks(x, recordsOf(x)));
  const parked = needRecords.find((x) => x.registrar?.parked);
  const host = needRecords.find((x) => x.registrar)?.registrar;
  const noAddress = local || (needRecords.length > 0 && needRecords.every((x) => !x.records?.length));
  const failed = pending.find((x) => x.state === "error");
  const issuing = pending.every((x) => x.state === "issuing");
  const looked = pending
    .map((x) => x.checkedAt)
    .filter((x): x is string => !!x)
    .sort()[0];
  const next = pending
    .map((x) => x.nextCheckAt)
    .filter((x): x is string => !!x)
    .sort()[0];
  const zone = zoneOf(d.domain);

  if (noAddress)
    return (
      <div className={cn("text-[0.875rem] text-ink-2", className)}>
        <Steps d={lead} />
        <p className="mt-3 max-w-[38rem]">
          This box runs on your computer, so the internet can’t reach it and there are no records to set yet. Once the box runs on a server, this shows the exact records for{" "}
          <span className="ident text-[0.8125rem] text-ink">{d.domain}</span>.
        </p>
      </div>
    );

  return (
    <div className={className}>
      <Steps d={failed ?? lead} />

      {failed && (
        <div className="mt-4 rounded-[8px] border border-danger-rule bg-danger-wash px-3.5 py-3">
          <p className="text-[0.875rem] text-ink">
            {pending.length > 1 && <span className="ident text-[0.8125rem]">{failed.domain}: </span>}
            {stateWords(failed).detail}
          </p>
          <p className="mt-1 text-[0.8125rem] text-ink-2">
            The box tries again on its own{failed.nextCheckAt ? `, next ${soonWords(failed.nextCheckAt)}` : ""}. Fix it and press Try again for an answer now.
          </p>
        </div>
      )}

      {needRecords.length > 0 && (
        <div className="mt-4">
          {managedBy && writer && !byHand ? (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[8px] border border-rule-2 bg-paper-raised px-3.5 py-3">
              <p className="min-w-0 flex-1 basis-64 text-[0.875rem] text-ink-2">
                <span className="text-ink">{providerName(managedBy)}</span> holds {zone}, and it’s connected to your box. The box can add the records there itself.
              </p>
              <div className="flex items-center gap-3">
                <button type="button" onClick={() => setByHand(true)} className="text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                  Show the records
                </button>
                <Button variant="primary" onClick={() => auto.mutate()} disabled={auto.isPending}>
                  {auto.isPending ? "Adding…" : "Add the records for me"}
                </Button>
              </div>
            </div>
          ) : (
            <>
              {parked?.registrar && (
                <div className="mb-3.5 rounded-[8px] border border-rule-2 bg-brass-wash px-3.5 py-3">
                  <p className="text-[0.875rem] text-ink">
                    <span className="ident text-[0.8125rem]">{parked.domain}</span> still points to {parked.registrar.parking ?? `${parked.registrar.name}’s parking page`}.
                  </p>
                  <p className="mt-1 text-[0.8125rem] text-ink-2">
                    In {parked.registrar.name} › {parked.registrar.where}, delete {parked.registrar.remove}. Then add the records below.
                  </p>
                </div>
              )}
              <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
                <p className="text-[0.875rem] text-ink">
                  {failed ? "Check these records" : `Add ${rows.length === 1 ? "this record" : `these ${rows.length} records`}`} at the DNS host for {zone}
                  {failed ? ":" : host && !parked ? ` (${host.name} › ${host.where}):` : " (where you manage its DNS, usually where you bought it):"}
                </p>
                {hasAlt && (
                  <Segmented
                    label="Record type"
                    value={mode}
                    onChange={setMode}
                    options={[
                      { value: "cname", label: "CNAME" },
                      { value: "a", label: "A records" },
                    ]}
                  />
                )}
              </div>
              {hasAlt && (
                <p className="mt-1.5 text-[0.8125rem] text-ink-3">
                  {mode === "cname"
                    ? "One CNAME per name is simplest, and it keeps working if the box’s address ever changes."
                    : "Use these if your DNS host can’t add a CNAME here. Set every one; delete other A or AAAA records for the name."}
                </p>
              )}
              <RecordsTable className="mt-2.5" records={rows} />
              <HostHints className="mt-2.5" />
            </>
          )}
          {auto.isError && <ProblemNote className="mt-3" error={auto.error} />}
        </div>
      )}

      <div className="mt-4 border-t border-rule pt-3.5">
        <Watch
          looked={looked}
          next={next}
          checking={check.isPending}
          onCheck={writer ? () => check.mutate() : undefined}
          button={failed ? "Try again" : "Check now"}
          title={
            failed
              ? null
              : issuing
                ? `${pending.length > 1 ? "Both names point" : `${lead.domain} points`} to your box. Getting the certificate from Let’s Encrypt, usually under a minute.`
                : "Waiting for DNS to point here."
          }
        >
          {!failed && !issuing && "Most DNS hosts publish new records within a few minutes; a few take some hours. The box checks every 30 seconds for half an hour after a change, then less often. You can leave this page: it finishes on its own."}
        </Watch>
      </div>
      {check.isError && !(check.error instanceof ApiError && check.error.status === 404) && <ProblemNote className="mt-3" error={check.error} />}
    </div>
  );
}

/** The calm line: a spinner, what it waits for, when the box last looked and when it looks next, and Check now. */
function Watch({
  title,
  looked,
  next,
  checking,
  onCheck,
  button,
  children,
}: {
  title: string | null;
  looked?: string;
  next?: string;
  checking: boolean;
  onCheck?: () => void;
  button: string;
  children?: React.ReactNode;
}) {
  const now = useNow(1000);
  return (
    <div role="status" aria-live="polite">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 items-start gap-2.5">
          {title && <span className="spinner mt-[3px] shrink-0 text-brass" aria-hidden />}
          <p className="min-w-0 text-[0.875rem] text-ink-2">
            {title && <span className="text-ink">{title} </span>}
            <span className="text-ink-3">{checkedLine(looked, next, now)}.</span>
          </p>
        </div>
        {onCheck && (
          <Button size="sm" onClick={onCheck} disabled={checking}>
            <RefreshCw className={cn("size-3.5!", checking ? "animate-spin" : "")} />
            {checking ? "Checking…" : button}
          </Button>
        )}
      </div>
      {children && <p className={cn("mt-1.5 max-w-[42rem] text-[0.8125rem] text-ink-3", title ? "pl-[26px]" : "")}>{children}</p>}
    </div>
  );
}

/** "Checked 2 minutes ago · next check in 30 s", as the box says it (domains list's checked). */
export function checkedLine(looked: string | undefined, next: string | undefined, now: number): string {
  let s = looked ? `Checked ${agoWords(looked, now)}` : "Not checked yet";
  if (next) {
    const secs = Math.round((new Date(next).getTime() - now) / 1000);
    s += secs <= 0 ? " · checking again now" : ` · next check in ${inWords(secs)}`;
  }
  return s;
}

/** 30 s, 2 min, 1 h 30 min. */
function inWords(secs: number): string {
  if (secs < 60) return `${secs} s`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} min`;
  return mins % 60 ? `${Math.floor(mins / 60)} h ${mins % 60} min` : `${mins / 60} h`;
}

/** example.com for shop.example.com: where its records live. */
export function zoneOf(d: string): string {
  const labels = d.split(".");
  const three = labels.slice(-3).join(".");
  return labels.length >= 3 && isApex(three) ? three : labels.slice(-2).join(".");
}

function soonWords(iso: string) {
  const s = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  if (s < 60) return "in under a minute";
  if (s < 3600) return `in ${Math.round(s / 60)} minutes`;
  return `in about ${Math.round(s / 3600)} hours`;
}
