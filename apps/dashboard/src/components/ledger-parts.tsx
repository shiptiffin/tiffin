// Pieces shared by the Ledger's three pages (the list, a receipt, an approval
// permit): the time margin, the actor line, steps with their diff, what undo
// can't restore, and the signature with its Seal.
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import type { Approval, Op, Tier, Token } from "@/api/client";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { actorName } from "@/lib/actors";
import { asTier, diffOp, formatValue, splitAddress } from "@/lib/changes";
import { MINUS, words } from "@/lib/format";
import { serviceNames } from "@/lib/staged";
import { clock, dayLabel } from "@/lib/time";
import { RiskDots } from "./risk-dots";
import { Seal } from "./seal";

/** Ticks while mounted (for countdowns and "4 minutes ago"). */
export function useNow(ms = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/** A plan hash the way people read it aloud: "d746 1a9e". */
export const planShort = (hash: string) => `${hash.slice(0, 4)} ${hash.slice(4, 8)}`;

/**
 * An intent split into its headline and the rest, so a long note from an agent
 * reads as a title and then "in its words". Only splits between sentences.
 *   "Turn off analytics. The team reads Plausible now." → head "Turn off analytics.", rest "The team reads Plausible now."
 */
export function splitIntent(raw: string): { head: string; rest?: string } {
  const s = raw.trim();
  const m = s.match(/^(.{12,}?[.!?])\s+(?=[A-Z0-9“"])(.+)$/s);
  const cap = (x: string) => x.charAt(0).toUpperCase() + x.slice(1);
  const stop = (x: string) => (/[.!?”"]$/.test(x) ? x : `${x}.`);
  if (!m) return { head: s ? stop(cap(s)) : s };
  return { head: cap(m[1]), rest: stop(m[2]) };
}

/** "Owner", "Bilal", "Claude Code": who a token ID belongs to. */
export function tokenWho(id: string | undefined, names: Map<string, Token> | undefined): string {
  if (!id) return "someone";
  const t = names?.get(id);
  if (!t) return "someone";
  if (t.name === "dashboard session" && t.sponsor) return tokenWho(t.sponsor, names);
  return actorName({ kind: t.kind, name: t.name }).name;
}

/** The approval a change spent, if any (it was signed with a passkey). Quiet when the box has no approvals. */
export function useApprovalsByChange() {
  const all = useQuery({ ...q.approvals, retry: false });
  const map = new Map<string, Approval>();
  for (const a of all.data ?? []) if (a.usedBy) map.set(a.usedBy, a);
  return map;
}

const dayMonth = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" });
const hms = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
/** "19:19 today", "09:12 yesterday", "17:40 on Friday 2 October". */
export function when(iso: string) {
  const d = dayLabel(iso);
  return d === "Today" || d === "Yesterday" ? `${clock(iso)} ${d.toLowerCase()}` : `${clock(iso)} on ${dayWords(iso)}`;
}

const longFmt = new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long" });
const stampFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
/** "Saturday 3 October". */
export const dayWords = (iso: string) => longFmt.format(new Date(iso));
/** "3 October 2026 at 11:44:58". */
export const stamp = (iso: string) => stampFmt.format(new Date(iso));

/** "3 Oct 2026". */
export const sealDate = (iso: string) => dayMonth.format(new Date(iso));
/** "19:46:03". */
export const clockSeconds = (iso: string) => hms.format(new Date(iso));

/**
 * One line of the page with the time in the margin, the Ledger's grammar:
 *   10:42    WAITING FOR YOU …
 *   asked
 * The margin folds away on a phone; the content keeps one left edge.
 */
export function Leaf({ time, note, children, className, id }: { time?: ReactNode; note?: ReactNode; children: ReactNode; className?: string; id?: string }) {
  return (
    <div id={id} className={cn("grid grid-cols-1 sm:grid-cols-[64px_minmax(0,1fr)] sm:gap-x-7", className)}>
      <div aria-hidden={!time} className="hidden pt-[3px] text-right text-[0.78125rem] leading-[1.125rem] text-ink-3 tnum sm:block print:block">
        {time && <span className="block text-ink-2">{time}</span>}
        {note}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

/** A section of a permit or receipt: a hairline above, a small-caps label, then the content. */
export function Sec({ label, tone, children, className }: { label: ReactNode; tone?: "danger"; children: ReactNode; className?: string }) {
  return (
    <section className={cn("border-t border-rule py-5 print:break-inside-avoid", className)}>
      <h2 className={cn("label mb-3", tone === "danger" && "!text-danger")}>{label}</h2>
      {children}
    </section>
  );
}

/** "Claude Code opus-5-5 · session 7f3a asks to change shop": agents in graphite, people in ink. */
export function ActorLine({
  kind,
  name,
  session,
  verb,
  project,
  className,
}: {
  kind: string;
  name: string;
  session?: string;
  verb: string;
  project?: string;
  className?: string;
}) {
  const agent = kind === "agent";
  const who = actorName({ kind, name });
  return (
    <p className={cn("text-[0.875rem] leading-5 text-ink-2", className)}>
      <b className={cn("font-[550]", agent ? "text-graphite" : "text-ink")}>{who.name}</b>
      {agent && (who.tag || session) && (
        <span className="ident ml-1.5 text-[0.75rem] text-ink-3">
          {[who.tag, session && `session ${session}`].filter(Boolean).join(" · ")}
        </span>
      )}{" "}
      {verb}
      {project && (
        <>
          {" "}
          <b className="font-[550] text-ink">{project}</b>
        </>
      )}
    </p>
  );
}

/** `+0 ~0 −1` with the deleting count in red when something is destroyed. */
export function CountsLine({ ops }: { ops: Op[] | null | undefined }) {
  let c = 0,
    u = 0,
    d = 0;
  let lost = false;
  for (const o of ops ?? []) {
    if (o.action === "create") c++;
    else if (o.action === "update") u++;
    else if (o.action === "delete") {
      d++;
      if (asTier(o.risk) === "irreversible") lost = true;
    }
  }
  const cell = (n: number, g: string, red?: boolean) => (
    <span className={cn(n > 0 ? "text-ink-2" : "text-ink-3", red && n > 0 && "text-danger")}>{`${g}${n}`}</span>
  );
  return (
    <span className="counts inline-flex gap-1.5" aria-label={`${c} to create, ${u} to update, ${d} to delete`}>
      {cell(c, "+")}
      {cell(u, "~")}
      {cell(d, MINUS, lost)}
    </span>
  );
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/** One op as a sentence: "Scale worker from 1 to 2 instances", "Remove Analytics from shop" (or the past tense on a receipt). */
export function opTitle(op: Op, project: string, past = false): string {
  const { kind, name } = splitAddress(op.address);
  const v = (now: string, then: string) => (past ? then : now);
  const before = (op.before ?? {}) as Record<string, unknown>;
  const after = (op.after ?? {}) as Record<string, unknown>;
  if (kind === "app" && op.action === "update" && op.fields?.length === 1 && op.fields[0] === "instances") {
    const to = Number(after.instances ?? 1);
    return `${v("Scale", "Scaled")} ${name} from ${before.instances ?? 1} to ${to} ${plural(to, "instance", "instances")}`;
  }
  if (kind === "service") {
    const s = serviceNames[name] ?? name;
    if (op.action === "create") return `${v("Add", "Added")} ${s} to ${project}`;
    if (op.action === "delete") return `${v("Remove", "Removed")} ${s} from ${project}`;
    const f = op.fields ?? [];
    if (f.length === 1) {
      const k = f[0];
      return `${v("Change", "Changed")} ${s}’s ${fieldWords(k)} from ${show(before[k])} to ${show(after[k])}`;
    }
    return `${v("Change", "Changed")} ${s} in ${project}`;
  }
  if (kind === "project") {
    if (op.action === "create") return `${v("Start", "Started")} the project ${project}`;
    if (op.action === "delete") return `${v("Destroy", "Destroyed")} the project ${project}`;
    return `${v("Change", "Changed")} ${project}’s settings`;
  }
  if (kind === "bucket" && op.action === "update" && op.fields?.length === 1 && op.fields[0] === "public") {
    return `${v("Make", "Made")} the ${name} bucket ${after.public ? "public" : "private"}`;
  }
  const noun: Record<string, string> = { app: "app", bucket: "bucket", cron: "cron job", queue: "queue", topic: "topic", env: "variable" };
  const thing = noun[kind] ? (kind === "env" ? `the variable ${name}` : `the ${name} ${noun[kind]}`) : name ? `${kind} ${name}` : kind;
  if (op.action === "create") return `${v("Add", "Added")} ${thing}`;
  if (op.action === "delete") return `${v(kind === "bucket" ? "Delete" : "Remove", kind === "bucket" ? "Deleted" : "Removed")} ${thing}`;
  return `${v("Change", "Changed")} ${thing}`;
}

const fieldNames: Record<string, string> = { maxMemoryMB: "memory cap", retentionDays: "retention", memoryMB: "memory", instances: "instances", public: "visibility" };
const fieldWords = (k: string) => fieldNames[k] ?? k;
function show(v: unknown): string {
  if (typeof v === "number") return String(v);
  if (typeof v === "string") return v;
  if (v === undefined || v === null) return "unset";
  return formatValue(v);
}

/** The machine's reason as a sentence, minus its "undo …" half (said separately). */
export function reasonWords(op: Op): { did?: string; undo?: string } {
  const r = (op.reason ?? "").trim();
  if (!r) return {};
  const [a, b] = r.split(/;\s*(?=undo\b)/i);
  const cap = (x: string) =>
    (x.charAt(0).toUpperCase() + x.slice(1).replace(/[.]$/, "") + ".").replace(/"([^"]*)"/g, "“$1”");
  return { did: a ? cap(a) : undefined, undo: b ? cap(b) : undefined };
}

/**
 * What a plan does, in order: a numbered step per op, its risk as dots, the
 * reason when the risk is more than reversible, and its field diff.
 */
export function Steps({ ops, project, past }: { ops: Op[]; project: string; past?: boolean }) {
  if (ops.length === 0) return <p className="text-sm text-ink-3">Nothing: the plan was empty.</p>;
  return (
    <ol className="flex flex-col">
      {ops.map((op, k) => {
        const tier = asTier(op.risk);
        const r = reasonWords(op);
        return (
          <li key={op.address + k} className="grid grid-cols-[22px_minmax(0,1fr)] gap-x-3 border-t border-rule py-3 first:border-t-0 first:pt-0 print:break-inside-avoid">
            <span className="ident pt-px text-[0.6875rem] leading-5 text-ink-3">{String(k + 1).padStart(2, "0")}</span>
            <div className="min-w-0">
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <h3 className="text-[0.9375rem] leading-5 font-[550] tracking-[-0.01em] text-ink">{opTitle(op, project, past)}</h3>
                <RiskDots tier={tier} label={tier === "reversible" ? "Low" : undefined} className="text-xs" />
              </div>
              {tier !== "reversible" && r.did && <p className="mt-1 max-w-[60ch] text-sm text-ink-2">{r.did}</p>}
              <FieldDiff op={op} />
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/** The fields an op touches, as a few lines of diff (unchanged fields fold away). */
function FieldDiff({ op }: { op: Op }) {
  const rows = diffOp(op).filter((r) => r.kind !== "same");
  if (rows.length === 0) return null;
  const lines: Array<{ k: "add" | "del"; text: string }> = [];
  for (const r of rows) {
    if (r.kind === "removed" || r.kind === "changed") lines.push({ k: "del", text: `${r.path}: ${formatValue(r.before)}` });
    if (r.kind === "added" || r.kind === "changed") lines.push({ k: "add", text: `${r.path}: ${formatValue(r.after)}` });
  }
  const shown = lines.slice(0, 12);
  return (
    <pre className="mt-2.5 overflow-x-auto rounded-[8px] border border-rule bg-paper-sunk py-1.5 font-mono text-[0.75rem] leading-[1.1875rem] tracking-[-0.015em] [font-variant-ligatures:none] print:whitespace-pre-wrap">
      {shown.map((l, i) => (
        <span
          key={i}
          className={cn(
            "grid grid-cols-[22px_minmax(0,1fr)] pr-3",
            l.k === "add" ? "bg-brass-wash text-ink" : "bg-[color-mix(in_oklch,var(--danger)_7%,transparent)] text-ink-3",
          )}
        >
          <span className={cn("text-center", l.k === "add" ? "text-brass-ink" : "text-danger")}>{l.k === "add" ? "+" : MINUS}</span>
          <span className="whitespace-pre">{l.text}</span>
        </span>
      ))}
      {lines.length > shown.length && (
        <span className="block pl-[22px] font-sans text-xs text-ink-3">
          and {words(lines.length - shown.length)} more {plural(lines.length - shown.length, "line", "lines")}
        </span>
      )}
    </pre>
  );
}

/** "What undo can't restore": the danger rule, only when a step destroys something. */
export function UndoCant({ ops, project, done }: { ops: Op[]; project: string; done?: boolean }) {
  const lost = ops.filter((o) => asTier(o.risk) === "irreversible");
  if (lost.length === 0) return null;
  return (
    <Sec label="What undo can’t restore" tone="danger">
      <div className="max-w-[38rem] border-l-2 border-danger py-0.5 pl-4">
        {lost.map((o, i) => (
          <p key={o.address + i} className="text-[0.875rem] leading-[1.3125rem] text-ink">
            {reasonWords(o).did ?? opTitle(o, project)}
          </p>
        ))}
        <p className="mt-1 text-[0.84375rem] leading-5 text-ink-2">
          Undo {done ? "can put" : "would put"} the settings of {project} back, but not the data. To get the data, restore a backup by hand from Health.
        </p>
      </div>
    </Sec>
  );
}

/** A risk line under an intent: dots, `+0 ~0 −1`, and the plan. */
export function RiskLine({ tier, ops, hash, children }: { tier: Tier; ops: Op[] | null | undefined; hash: string; children?: ReactNode }) {
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[0.8125rem] text-ink-2">
      <RiskDots tier={tier} />
      <CountsLine ops={ops} />
      <span>
        Plan <span className="ident text-ink" title={hash}>{planShort(hash)}</span>
      </span>
      {children}
    </div>
  );
}

/**
 * The signature block: the name written large, a ruled line, what signed it,
 * and the brass Seal (drawn once when `fresh`, static otherwise).
 */
export function Signature({
  name,
  at,
  hash,
  how,
  fresh,
  seal = true,
}: {
  name: string;
  at: string;
  hash: string;
  how: ReactNode;
  fresh?: boolean;
  seal?: boolean;
}) {
  return (
    <div className={cn("grid items-end gap-x-6 print:break-inside-avoid", seal ? "grid-cols-[minmax(0,1fr)_auto]" : "grid-cols-1")}>
      <div className="min-w-0">
        <p className="pb-1.5 font-serif text-[1.875rem] leading-[2.125rem] tracking-[-0.01em] text-ink [font-optical-sizing:auto]">{name}</p>
        <p className="border-t border-ink pt-2 text-[0.8125rem] leading-[1.1875rem] text-ink-2">{how}</p>
      </div>
      {seal && <Seal name={name} date={sealDate(at)} plan={planShort(hash)} animate={fresh} size={112} className="max-sm:size-[76px]" />}
    </div>
  );
}

/** Small key/value pairs on a receipt. */
export function Facts({ items }: { items: Array<[string, ReactNode] | false | "" | undefined | null> }) {
  return (
    <dl className="grid grid-cols-[minmax(5.5rem,auto)_minmax(0,1fr)] gap-x-4 sm:gap-x-6 gap-y-1.5 text-[0.84375rem] leading-5">
      {items.filter(Boolean).map((it) => {
        const [k, v] = it as [string, ReactNode];
        return (
          <div key={k} className="contents">
            <dt className="text-ink-3">{k}</dt>
            <dd className="min-w-0 overflow-hidden text-ink-2 tnum">{v}</dd>
          </div>
        );
      })}
    </dl>
  );
}

/** "Ledger › shop › Entry": like <Crumbs>, but a crumb can carry search params (a filtered Ledger). */
export function LedgerCrumbs({
  items,
}: {
  items: Array<{ label: ReactNode; to?: string; params?: Record<string, string>; search?: Record<string, string | undefined>; hash?: string }>;
}) {
  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-[0.8125rem] text-ink-3">
      {items.map((c, i) => (
        <span key={i} className="flex min-w-0 items-center gap-1.5">
          {i > 0 && (
            <span aria-hidden className="text-ink-4">
              ›
            </span>
          )}
          {c.to ? (
            <Link to={c.to as "/"} params={c.params as never} search={c.search as never} hash={c.hash} className="truncate transition-colors hover:text-ink">
              {c.label}
            </Link>
          ) : (
            <span className="truncate" aria-current="page">
              {c.label}
            </span>
          )}
        </span>
      ))}
    </nav>
  );
}
