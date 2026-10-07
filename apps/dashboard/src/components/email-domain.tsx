import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ApiError, request } from "@/api/client";
import { q } from "@/api/queries";
import { CopyButton } from "@/components/copy";
import { Section } from "@/components/data-parts";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { change, pendingFor, usePending } from "@/lib/staged";
import { relative } from "@/lib/time";

// The project's sending domain: its sender address, and the SPF, DKIM and
// DMARC records receivers look for, checked live in public DNS by the box
// (GET /v1/projects/{project}/email/domain).

type DomainRecord = {
  kind: "spf" | "dkim" | "dmarc";
  type: string;
  name: string;
  want?: string;
  found: string[] | null;
  state: "ok" | "missing" | "warn" | "unknown";
  detail?: string;
};
type DomainCheck = {
  from: string;
  domain: string;
  boxDomain: boolean;
  relay?: string;
  provider?: string;
  records: DomainRecord[] | null;
  checkedAt: string;
};

const KIND: Record<DomainRecord["kind"], { name: string; why: string }> = {
  spf: { name: "SPF", why: "Says which servers may send for the domain." },
  dkim: { name: "DKIM", why: "A key that proves the mail wasn’t changed on the way." },
  dmarc: { name: "DMARC", why: "Tells receivers what to do with mail that fails the other two." },
};

const STATE: Record<DomainRecord["state"], { word: string; tone: string }> = {
  ok: { word: "Found", tone: "text-ok" },
  missing: { word: "Missing", tone: "text-ink-2" },
  warn: { word: "Needs a fix", tone: "text-warn-ink" },
  unknown: { word: "No answer", tone: "text-ink-3" },
};

const FROM_PATH = ["services", "email", "from"];

export function EmailDomain({ project, relay }: { project: string; relay: boolean }) {
  const qc = useQueryClient();
  const [selector, setSelector] = useState("");
  const check = useQuery({
    queryKey: ["email-domain", project, selector],
    queryFn: () => request<DomainCheck>("GET", `/v1/projects/${encodeURIComponent(project)}/email/domain${selector ? `?selector=${encodeURIComponent(selector)}` : ""}`),
    staleTime: 60_000,
    retry: false,
    placeholderData: (prev) => prev,
  });
  // A sender change in flight: re-check once it lands.
  const pending = usePending(project);
  const staged = pendingFor(pending, `set:${FROM_PATH.join("/")}`);
  const was = useRef(staged);
  useEffect(() => {
    if (was.current && !staged) void qc.invalidateQueries({ queryKey: ["email-domain", project] });
    was.current = staged;
  }, [staged, qc, project]);

  const old = check.error instanceof ApiError && check.error.status === 404 && !/email/i.test(check.error.problem.detail ?? "");
  const c = check.data;
  const records = c?.records ?? [];
  const left = records.filter((r) => r.state !== "ok").length;

  return (
    <Section
      id="domain"
      label="Sending domain"
      aside={
        c && (
          <span className="inline-flex items-center gap-2">
            <span className="max-sm:hidden">Checked {relative(c.checkedAt)}</span>
            <button
              type="button"
              onClick={() => void check.refetch()}
              disabled={check.isFetching}
              className="inline-flex items-center gap-1 rounded-[5px] px-1.5 py-0.5 text-ink-2 hover:bg-paper-hover hover:text-ink disabled:opacity-60"
            >
              <RefreshCw className={cn("size-3.5", check.isFetching && "animate-spin")} aria-hidden />
              {check.isFetching ? "Checking…" : "Check again"}
            </button>
          </span>
        )
      }
    >
      <div className="border-t border-rule pt-4">
        {check.isPending && (
          <div className="space-y-3" aria-busy>
            <Skeleton className="h-5 w-72" />
            <Skeleton className="h-28 w-full" />
          </div>
        )}
        {old && <p className="text-base text-ink-3">This box can’t check DNS records yet. Update it to see SPF, DKIM and DMARC here.</p>}
        {check.isError && !old && !c && <ProblemNote error={check.error} />}
        {c && (
          <>
            <Sender project={project} check={c} staged={staged?.kind === "set" ? (staged.to as string | undefined) : undefined} />
            <p className="mt-3 max-w-[48rem] text-sm text-ink-3">
              {c.boxDomain
                ? "That’s the box’s own domain. Receivers trust mail more from a domain you own: set a sender address on your domain, then add these records where its DNS lives."
                : !relay
                  ? "These matter once a relay sends real mail. Until then every message stays in the dev inbox."
                  : c.provider
                    ? `Mail goes through ${c.provider}. Add ${c.domain} there as well: ${c.provider} gives you the DKIM key and checks the records too.`
                    : "Your mail service gives you the DKIM key when you add the domain there."}
            </p>
            <p className="mt-5 text-[0.875rem] text-ink" aria-live="polite">
              {left === 0 ? (
                <span className="inline-flex items-center gap-1.5">
                  <Check className="size-4 text-ok" strokeWidth={2.5} aria-hidden />
                  All {records.length} records are in place for {c.domain}.
                </span>
              ) : (
                `${left} of ${records.length} records for ${c.domain} ${left === 1 ? "needs" : "need"} attention.`
              )}
            </p>
            <ul className={cn("mt-3 divide-y divide-rule border-y border-rule-2 transition-opacity", check.isFetching && "opacity-60")}>
              {records.map((r) => (
                <RecordRow key={r.kind} r={r} />
              ))}
            </ul>
            {records.some((r) => r.kind === "dkim" && r.state !== "ok") && (
              <SelectorForm
                value={selector}
                onCheck={(s) => {
                  if (s === selector) void check.refetch();
                  else setSelector(s);
                }}
              />
            )}
          </>
        )}
      </div>
    </Section>
  );
}

function RecordRow({ r }: { r: DomainRecord }) {
  const k = KIND[r.kind];
  const s = STATE[r.state] ?? STATE.unknown;
  const found = r.found ?? [];
  const value = r.want || (r.state === "ok" ? found[0] : "");
  return (
    <li className="grid gap-x-6 gap-y-2 py-3.5 md:grid-cols-[9rem_minmax(0,1fr)]">
      <div className="flex items-baseline justify-between gap-3 md:block">
        <p className="text-[0.9375rem] font-[550] text-ink">{k.name}</p>
        <p className={cn("text-[0.8125rem] font-[550] md:mt-0.5", s.tone)}>
          {r.state === "ok" && <Check className="mr-1 inline size-3.5 align-[-2px]" strokeWidth={2.5} aria-hidden />}
          {s.word}
        </p>
      </div>
      <div className="min-w-0">
        <dl className="grid grid-cols-[3.5rem_minmax(0,1fr)] items-start gap-x-3 gap-y-1.5 text-sm">
          <dt className="pt-1 text-ink-3">Name</dt>
          <dd className="flex min-w-0 items-center gap-0.5">
            <code className="ident min-w-0 truncate rounded-[5px] bg-paper-sunk px-1.5 py-0.5 text-[0.8125rem] text-ink" title={r.name}>
              {r.name}
            </code>
            {!r.name.startsWith("<") && <CopyButton value={r.name} label={`Copy the name ${r.name}`} className="size-6" />}
            <span className="ml-2 shrink-0 font-mono text-[0.75rem] text-ink-3">{r.type}</span>
          </dd>
          {value && (
            <>
              <dt className="pt-1 text-ink-3">{r.state === "ok" && !r.want ? "Value" : "Add"}</dt>
              <dd className="flex min-w-0 items-start gap-0.5">
                <code className="ident min-w-0 rounded-[5px] bg-paper-sunk px-1.5 py-0.5 text-[0.8125rem] [overflow-wrap:anywhere] text-ink">{value}</code>
                <CopyButton value={value} label={`Copy the ${k.name} value`} className="size-6 shrink-0" />
              </dd>
            </>
          )}
          {r.state !== "ok" && found.length > 0 && (
            <>
              <dt className="pt-0.5 text-ink-3">Now</dt>
              <dd className="min-w-0 space-y-0.5">
                {found.map((f) => (
                  <code key={f} className="ident block truncate text-[0.78125rem] text-ink-3" title={f}>
                    {f}
                  </code>
                ))}
              </dd>
            </>
          )}
        </dl>
        <p className="mt-1.5 text-xs text-ink-3">{r.detail ? `${r.detail} ` : ""}{r.state === "ok" ? k.why : ""}</p>
      </div>
    </li>
  );
}

/** The sender address, and a way to change it (services.email.from in tiffin.config.ts). */
function Sender({ project, check, staged }: { project: string; check: DomainCheck; staged?: string }) {
  const { can } = useMe();
  const manifest = useQuery(q.manifest(project));
  const set = (manifest.data?.manifest as { services?: { email?: { from?: string } } } | undefined)?.services?.email?.from;
  const [editing, setEditing] = useState(false);
  const [v, setV] = useState("");
  const t = v.trim();
  const bad = t !== "" && !/^([^<>]*<)?[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+>?$/.test(t);
  const save = () => {
    if (bad) return;
    change(
      project,
      {
        kind: "set",
        path: FROM_PATH,
        from: set,
        to: t || undefined,
        what: t ? `Send ${project}’s mail from ${t}` : `Send ${project}’s mail from the box’s own address`,
        undo: set ? `mail comes from ${set} again` : "mail comes from the box’s own address again",
      },
      { immediate: true },
    );
    setEditing(false);
  };
  if (editing)
    return (
      <form
        className="flex max-w-[36rem] flex-col gap-2 sm:flex-row sm:items-start"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <div className="min-w-0 flex-1">
          <Input
            autoFocus
            value={v}
            onChange={(e) => setV(e.target.value)}
            placeholder={`Shop <hello@example.com>`}
            aria-label="Sender address"
            aria-invalid={bad || undefined}
            spellCheck={false}
          />
          <p className={bad ? "mt-1 text-sm text-danger" : "mt-1 text-xs text-ink-3"}>
            {bad ? "That isn’t an email address." : "An address, optionally with a name. Leave it empty for the box’s own address."}
          </p>
        </div>
        <span className="flex gap-2">
          <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={bad || t === (set ?? "")}>
            Save
          </Button>
        </span>
      </form>
    );
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-base text-ink-2">
      <span>
        Mail is sent from <code className="ident text-ink">{staged ?? check.from}</code>
        {staged !== undefined && <span className="ml-2 text-sm text-brass-ink">changing…</span>}
      </span>
      {can("apply:reversible") && staged === undefined && (
        <Button
          size="sm"
          variant="ghost"
          className="-ml-1"
          onClick={() => {
            setV(set ?? "");
            setEditing(true);
          }}
        >
          Change
        </Button>
      )}
    </p>
  );
}

function SelectorForm({ value, onCheck }: { value: string; onCheck: (s: string) => void }) {
  const [v, setV] = useState(value);
  const t = v.trim().replace(/\._domainkey.*$/, "");
  const ok = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$/.test(t);
  return (
    <form
      className="mt-4 flex max-w-[34rem] flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) onCheck(t);
      }}
    >
      <label htmlFor="dkim-sel" className="text-sm text-ink-2">
        DKIM selector from your mail service
      </label>
      <div className="flex gap-2">
        <Input id="dkim-sel" value={v} onChange={(e) => setV(e.target.value)} placeholder="resend" spellCheck={false} className="h-8 font-mono text-[0.8125rem]" />
        <Button type="submit" disabled={!ok}>
          Check
        </Button>
      </div>
      <p className="text-xs text-ink-3">The part before ._domainkey in the DKIM record it gave you.</p>
    </form>
  );
}
