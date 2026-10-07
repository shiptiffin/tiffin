import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError } from "@/api/client";
import { HostHints, RecordsTable } from "@/components/dns-records";
import { agoWords, failedAt, Steps, useNow, withTicks } from "@/components/domains-parts";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { isApex, providerName, recheckDomain, setRecords, stateWords, type Domain } from "@/lib/domains";

/**
 * What's left before a domain (and its www) is live, said precisely: where
 * it is on the three steps, the exact records to add with a tick for each
 * one DNS already answers, and a calm line that says when the box last
 * looked and that it keeps looking after you leave.
 */
export function DomainSetup({
  project,
  d,
  www,
  writer,
  local,
  every,
  className,
}: {
  project: string;
  d: Domain;
  www?: Domain;
  writer: boolean;
  local: boolean;
  /** Seconds between the page's own checks, when it is checking. */
  every?: number;
  className?: string;
}) {
  const qc = useQueryClient();
  const [byHand, setByHand] = useState(false);
  const pending = [d, www].filter((x): x is Domain => !!x && x.state !== "live");
  const lead = pending[0];
  const hasAlt = pending.some((x) => x.state !== "issuing" && x.alternative?.length);
  // A name that already answers through a CNAME keeps showing the CNAME; else a subdomain starts with it (one record, and it follows the box).
  const [mode, setMode] = useState<"cname" | "a">(() => (pending.some((x) => x.alternative?.length && !x.cname && x.found?.length) ? "a" : "cname"));

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
  const rows = needRecords.flatMap((x) => withTicks(x, x.alternative?.length && mode === "cname" ? x.alternative : (x.records ?? [])));
  const noAddress = local || (needRecords.length > 0 && needRecords.every((x) => !x.records?.length));
  const failed = pending.find((x) => x.state === "error");
  const issuing = pending.every((x) => x.state === "issuing");
  const looked = pending
    .map((x) => x.checkedAt)
    .filter((x): x is string => !!x)
    .sort()[0];
  const next = failed?.nextCheckAt;
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
            The box tries again on its own{next ? `, next ${soonWords(next)}` : ""}. Fix it and press Try again for an answer now.
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
              <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
                <p className="text-[0.875rem] text-ink">
                  {failed ? "Check these records" : `Add ${rows.length === 1 ? "this record" : `these ${rows.length} records`}`} at the DNS host for {zone}
                  {failed ? ":" : " (where you manage its DNS, usually where you bought it):"}
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
                  {needRecords.some((x) => !x.alternative?.length)
                    ? mode === "cname"
                      ? `${needRecords.filter((x) => !x.alternative?.length).map((x) => x.domain).join(" and ")} needs A records. For ${needRecords.filter((x) => x.alternative?.length).map((x) => x.domain).join(" and ")}, one CNAME is simplest, and it follows the box if its address changes.`
                      : "Use A records for both names if your DNS host can’t add a CNAME. Delete any other A or AAAA records for them."
                    : mode === "cname"
                      ? "One CNAME is simplest, and it keeps working if the box’s address ever changes."
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
          every={failed ? undefined : every}
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
          {!failed && !issuing && "Most DNS hosts publish new records within a few minutes; a few take some hours. You can leave this page: the box keeps checking and finishes on its own."}
        </Watch>
      </div>
      {check.isError && !(check.error instanceof ApiError && check.error.status === 404) && <ProblemNote className="mt-3" error={check.error} />}
    </div>
  );
}

/** The calm line: a spinner, what it waits for, when it last looked and how often, and Check now. */
function Watch({
  title,
  looked,
  every,
  checking,
  onCheck,
  button,
  children,
}: {
  title: string | null;
  looked?: string;
  every?: number;
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
            <span className="text-ink-3">
              {looked ? `Checked ${agoWords(looked, now)}` : "Not checked yet"}
              {every ? ` · checking every ${every} seconds while this is open` : ""}.
            </span>
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

/**
 * While a domain waits for DNS and the page is open, ask the box to look
 * again: every 10 seconds for the first two minutes, then every 30. The box
 * also checks on its own (with backoff), so leaving the page is fine.
 * Returns the current interval in seconds (0 when not checking).
 */
export function useRecheck(project: string, list: Domain[], writer: boolean): number {
  const qc = useQueryClient();
  const waiting = list.filter((d) => d.state === "waiting_for_dns" && d.records?.length).map((d) => d.domain);
  const key = writer ? waiting.join(",") : "";
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    if (!key) return;
    const started = Date.now();
    let t: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (document.visibilityState === "visible") {
        await Promise.allSettled(key.split(",").map((d) => recheckDomain(project, d)));
        void qc.invalidateQueries({ queryKey: ["domains", project] });
      }
      const late = Date.now() - started >= 120_000;
      setSlow(late);
      t = setTimeout(tick, late ? 30_000 : 10_000);
    };
    t = setTimeout(tick, 10_000);
    return () => {
      clearTimeout(t);
      setSlow(false);
    };
  }, [project, key, qc]);
  return key ? (slow ? 30 : 10) : 0;
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
