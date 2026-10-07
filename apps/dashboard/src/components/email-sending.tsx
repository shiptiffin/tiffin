import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, ArrowUpRight, Check, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { boxMail, boxMailQ, type SendingDomain, type SendingRecord, type SendingView } from "@/api/modules";
import { Section } from "@/components/data-parts";
import { HostHints, RecordsTable, Watching } from "@/components/dns-records";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { cleanDomain, looksLikeDomain } from "@/lib/domains";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";

// A project's Email settings › Send from your domain. One click: the box
// sets the domain up with the relay's mail service (SendGrid, Resend), puts
// the records in DNS when the domain's DNS is connected (or lists them to
// copy), keeps checking until the service verifies it, then makes it the
// project's sender with a change in History.

const PURPOSE: Record<SendingRecord["purpose"], [string, string]> = {
  dkim: ["DKIM", "signs the mail"],
  spf: ["SPF", "lets the service send for the domain"],
  "return-path": ["Return path", "handles bounces and SPF"],
  dmarc: ["DMARC", "tells receivers what to do with mail that fails"],
  other: ["", ""],
};

export function EmailSending({ project }: { project: string }) {
  const qc = useQueryClient();
  const view = useQuery(boxMailQ.sending(project));
  const { can, admin } = useMe();
  const set = (v: SendingView) => qc.setQueryData(boxMailQ.sending(project).queryKey, v);
  const after = (v: SendingView) => {
    set(v);
    void qc.invalidateQueries({ queryKey: ["email-domain", project] });
    void qc.invalidateQueries({ queryKey: ["manifest", project] });
  };
  const old = view.error instanceof ApiError && [404, 405].includes(view.error.status);
  if (old) return null;
  const v = view.data;
  const s = v?.setup;
  const from = v?.from;
  const write = can("apply:outbound");

  return (
    <Section id="send-from" label="Send from your domain" aside={s && s.state !== "manual" ? `with ${s.providerName}` : v?.providerName && v.automatic ? `with ${v.providerName}` : undefined}>
      <div className="border-t border-rule pt-4">
        {view.isPending && (
          <div className="space-y-3" aria-busy>
            <Skeleton className="h-5 w-80" />
            <Skeleton className="h-10 w-full max-w-[36rem]" />
          </div>
        )}
        {view.isError && <ProblemNote error={view.error} />}
        {v && !v.relay && (
          <div className="max-w-[44rem]">
            <p className="text-base text-ink-2">
              Mail comes from {from ? <code className="ident text-ink">{from}</code> : "the box’s own address"}. To send from your own domain, connect a mail service
              first. After that it’s one click: the box sets the domain up with the service and adds the DNS records for you.
            </p>
            {admin && (
              <Button asChild size="md" variant="secondary" className="mt-4">
                <Link to="/settings" hash="email">
                  Connect a mail service <ArrowRight className="text-ink-3" />
                </Link>
              </Button>
            )}
          </div>
        )}
        {v && v.relay && !s && <StartForm project={project} view={v} from={from} write={write} onDone={after} />}
        {s && <SetupView project={project} s={s} write={write} onDone={after} />}
      </div>
    </Section>
  );
}

function StartForm({ project, view, from, write, onDone }: { project: string; view: SendingView; from?: string; write: boolean; onDone: (v: SendingView) => void }) {
  const [domain, setDomain] = useState(view.suggested ?? "");
  const [local, setLocal] = useState("hello");
  const [probe, setProbe] = useState(view.suggested ?? "");
  const d = cleanDomain(domain);
  const ok = looksLikeDomain(d);
  const localOk = /^[a-zA-Z0-9.!#$%&'*+/=?^_{|}~-]{1,64}$/.test(local.trim()) && !/^\.|\.$|\.\./.test(local.trim());
  // Whether the box's DNS provider holds the domain: asked when the field settles.
  const managed = useQuery({
    queryKey: ["email-sending-dns", project, probe],
    queryFn: () => boxMail.sending(project, probe),
    enabled: looksLikeDomain(probe),
    staleTime: 60_000,
    select: (x) => x.dnsManaged,
  });
  const start = useMutation({ mutationFn: () => boxMail.startSending(project, d, local.trim()), onSuccess: onDone });
  const name = view.providerName ?? "your mail service";
  const shown = ok && probe === d ? managed.data : undefined;

  return (
    <div className="max-w-[44rem]">
      <p className="text-base text-ink-2">
        Mail now comes from {from ? <code className="ident text-ink">{from}</code> : "the box’s own address"}. Receivers trust mail from a domain you own more.{" "}
        {view.automatic
          ? `The box sets the domain up with ${name}, adds the DNS records when it can, and switches the sender once ${name} has verified it.`
          : `${name} has no way for the box to do this for you, so you’ll get the steps.`}
      </p>
      {write ? (
        <form
          className="mt-5"
          onSubmit={(e) => {
            e.preventDefault();
            if (ok && localOk && !start.isPending) start.mutate();
          }}
        >
          <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
            <div className="flex min-w-0 flex-1 items-center rounded-md border border-rule bg-paper transition-[border-color,box-shadow] focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)] hover:border-rule-2">
              <label htmlFor="send-local" className="sr-only">
                The part before the @
              </label>
              <input
                id="send-local"
                value={local}
                onChange={(e) => setLocal(e.target.value)}
                spellCheck={false}
                autoComplete="off"
                aria-invalid={!localOk || undefined}
                style={{ width: `calc(${Math.min(Math.max(local.length, 3), 16)}ch + 1rem)` }}
                className="ident h-9 min-w-0 shrink-0 bg-transparent pr-1 pl-3 text-base text-ink outline-hidden placeholder:text-ink-4"
              />
              <span aria-hidden className="ident text-base text-ink-3">
                @
              </span>
              <label htmlFor="send-domain" className="sr-only">
                Domain
              </label>
              <input
                id="send-domain"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
                onBlur={() => setProbe(d)}
                placeholder="example.com"
                spellCheck={false}
                autoComplete="off"
                inputMode="url"
                className="ident h-9 min-w-0 flex-1 bg-transparent pr-3 pl-1 text-base text-ink outline-hidden placeholder:text-ink-4"
              />
            </div>
            <Button type="submit" variant="primary" size="lg" className="h-9 shrink-0" disabled={!ok || !localOk || start.isPending}>
              {start.isPending ? `Setting up with ${name}…` : view.automatic ? "Set up domain" : "Show the steps"}
            </Button>
          </div>
          <p className="mt-2 min-h-5 text-[0.8125rem] text-ink-3" aria-live="polite">
            {!localOk
              ? "The part before the @ is letters, digits and . _ - +"
              : shown === true
                ? `${d}’s DNS is on the DNS provider connected to this box: the box adds the records itself.`
                : shown === false
                  ? `You’ll get a few records to add where ${d}’s DNS lives.`
                  : view.automatic
                    ? "A domain you own, or a subdomain of it such as mail.example.com."
                    : null}
          </p>
          {start.isError && <ProblemNote className="mt-3" error={start.error} />}
        </form>
      ) : (
        <p className="mt-3 text-[0.8125rem] text-ink-3">Someone who can send mail for this project can set this up.</p>
      )}
    </div>
  );
}

function SetupView({ project, s, write, onDone }: { project: string; s: SendingDomain; write: boolean; onDone: (v: SendingView) => void }) {
  const check = useMutation({ mutationFn: () => boxMail.checkSending(project), onSuccess: onDone });
  const stop = useMutation({ mutationFn: () => boxMail.stopSending(project), onSuccess: onDone });
  const [showRecords, setShowRecords] = useState(false);
  const records = s.records ?? [];
  const rows = records.map((r) => ({ type: r.type, name: r.name, host: r.host, value: r.value, ok: r.status === "ok" }));
  const failing = records.filter((r) => r.status === "failed" && r.detail);
  const verified = s.state === "verified";

  return (
    <div className="max-w-[52rem]">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2">
        <div className="min-w-0">
          <p className="flex items-center gap-2 text-[0.9375rem] text-ink">
            <StateMark state={s.state} />
            <span className="ident font-[550] [overflow-wrap:anywhere]">{s.domain}</span>
            <span className="text-ink-3">·</span>
            <span className={cn("text-[0.875rem]", verified ? "text-ok" : s.state === "failed" ? "text-warn-ink" : "text-ink-2")}>
              {verified ? "Verified" : s.state === "failed" ? "Stopped" : s.state === "manual" ? "Steps to follow" : `Waiting for ${s.providerName}`}
            </span>
          </p>
          <p className="mt-1 text-[0.875rem] text-ink-2">{s.detail}</p>
        </div>
        {write && (
          <span className="flex shrink-0 gap-1">
            {s.state === "failed" && (
              <Button size="sm" onClick={() => check.mutate()} disabled={check.isPending}>
                {check.isPending ? "Checking…" : "Check again"}
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => stop.mutate()} disabled={stop.isPending}>
              {verified ? "Use another domain" : s.state === "failed" ? "Start over" : "Stop"}
            </Button>
          </span>
        )}
      </div>

      {s.hint && (
        <p className="mt-3 flex max-w-[44rem] items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-[0.8125rem] text-ink">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" aria-hidden />
          <span>{s.hint}</span>
        </p>
      )}
      {check.isError && <ProblemNote className="mt-3" error={check.error} />}
      {stop.isError && <ProblemNote className="mt-3" error={stop.error} />}

      {s.state === "manual" && (
        <ol className="mt-4 max-w-[44rem] list-decimal space-y-1.5 pl-5 text-[0.875rem] text-ink-2 marker:text-ink-3">
          <li>
            Add <span className="ident text-ink">{s.domain}</span> in {s.providerName}
            {s.domainUrl && (
              <>
                {" "}
                <a href={s.domainUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                  sending domains
                  <ArrowUpRight className="size-3.5" aria-hidden />
                </a>
              </>
            )}
            .
          </li>
          <li>Add the records {s.providerName} gives you where the domain’s DNS lives. Receivers check the ones below.</li>
          <li>
            When {s.providerName} says it’s verified, change the sender under Sending domain to <span className="ident text-ink">{s.from}</span>.
          </li>
        </ol>
      )}

      {!verified && s.state !== "manual" && (
        <p className="mt-4 text-[0.875rem] text-ink-2">
          {s.dns === "auto" ? "" : "Copy each value exactly. "}Once {s.providerName} verifies the domain, mail goes out as{" "}
          <span className="ident text-ink">{s.from}</span>.
        </p>
      )}
      {s.dnsError && (
        <p className="mt-2 max-w-[44rem] text-[0.8125rem] text-warn-ink">
          {s.dnsError} Add them by hand instead.
        </p>
      )}

      {(!verified || showRecords) && rows.length > 0 && (
        <>
          <RecordsTable records={rows} className="mt-3" wideNames />
          <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
            {[...new Set(records.map((r) => r.purpose))].map((p) =>
              PURPOSE[p][0] ? (
                <li key={p}>
                  <span className="text-ink-2">{PURPOSE[p][0]}</span> {PURPOSE[p][1]}
                </li>
              ) : null,
            )}
          </ul>
          {failing.length > 0 && (
            <ul className="mt-3 space-y-1 text-[0.8125rem] text-warn-ink">
              {failing.map((r) => (
                <li key={r.name}>
                  <span className="ident">{r.host}</span>: {r.detail}
                </li>
              ))}
            </ul>
          )}
          {s.dns === "manual" && <HostHints className="mt-3" />}
        </>
      )}

      {s.state === "verifying" && (
        <div className="mt-4 border-t border-rule pt-3">
          <Watching checkedAt={s.checkedAt} checking={check.isPending} onCheck={write ? () => check.mutate() : undefined}>
            {s.nextCheckAt ? `The box asks ${s.providerName} again ${relative(s.nextCheckAt)}, for up to two days.` : `The box keeps asking ${s.providerName}.`}
          </Watching>
        </div>
      )}

      {verified && (
        <p className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.8125rem] text-ink-3">
          {s.verifiedAt && <span>Verified {relative(s.verifiedAt)}.</span>}
          {s.senderSet && s.senderChange && <span>The sender change is in History, where you can undo it.</span>}
          {rows.length > 0 && (
            <button type="button" onClick={() => setShowRecords((x) => !x)} className="font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
              {showRecords ? "Hide the records" : "Show the records"}
            </button>
          )}
        </p>
      )}
    </div>
  );
}

function StateMark({ state }: { state: SendingDomain["state"] }) {
  if (state === "verified") return <Check className="size-4 shrink-0 text-ok" strokeWidth={2.5} aria-hidden />;
  if (state === "failed") return <TriangleAlert className="size-4 shrink-0 text-warn-ink" aria-hidden />;
  if (state === "verifying") return <span className="spinner shrink-0 text-brass" aria-hidden />;
  return <span aria-hidden className="size-[7px] shrink-0 rounded-full bg-ink-3" />;
}
