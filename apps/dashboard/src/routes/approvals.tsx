import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, ArrowRight, Check, Fingerprint, KeyRound, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError, api, notOnBox, type Approval, type Token } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { CopyValue } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { NotOnBox } from "@/components/page";
import { OpCounts, OpView } from "@/components/op";
import { ProblemNote, sentence } from "@/components/problem";
import { RiskBadge, RiskMark } from "@/components/risk";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { asTier, tierRank } from "@/lib/changes";
import { full, relative } from "@/lib/time";
import { getAssertion, passkeyError, webauthnSupported } from "@/lib/webauthn";
import { useWaitingWorkflowApprovals } from "@/lib/wf";
import { ApprovalCard } from "./queues";
import { Page } from "@/components/page";

const words = ["No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine"];

/** "claude-code (claude-code-4)" → name and session. */
export function splitRequester(r: string) {
  const m = r.match(/^(.*?)\s*\((.*)\)$/);
  return m ? { name: m[1], session: m[2] } : { name: r, session: undefined };
}

/** Ticks once a second while mounted. */
function useNow(ms = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

function left(iso: string, now: number) {
  const s = Math.max(0, Math.floor((new Date(iso).getTime() - now) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h > 0 ? `${h} h ${String(m).padStart(2, "0")} min` : `${m}:${String(sec).padStart(2, "0")}`;
}

function who(id: string | undefined, names: Map<string, Token> | undefined) {
  if (!id) return "someone";
  const t = names?.get(id);
  if (!t) return "someone";
  if (t.name === "dashboard session") return `${t.sponsor ? (names?.get(t.sponsor)?.name ?? "the owner") : "the owner"}, in the dashboard`;
  return t.name;
}

const statusCopy: Record<Approval["status"], { label: string; cls: string }> = {
  pending: { label: "Waiting for you", cls: "text-brass-ink bg-brass-wash" },
  approved: { label: "Approved, not applied yet", cls: "text-rev bg-rev-wash" },
  used: { label: "Approved and applied", cls: "text-rev bg-rev-wash" },
  rejected: { label: "Rejected", cls: "text-ink-2 bg-hover" },
  expired: { label: "Expired", cls: "text-ink-3 bg-hover" },
};

export function ApprovalsPage() {
  useTitle("Approvals");
  const all = useQuery(q.approvals);
  const passkeys = useQuery(q.passkeys);
  const names = useQuery(q.tokenNames);
  const now = useNow(30_000);
  const wf = useWaitingWorkflowApprovals();

  if (all.isError && notOnBox(all.error)) return <NotOnBox what="Approvals" />;
  if (all.isPending)
    return (
      <Page>
        <div className="h-10 w-96 max-w-full animate-pulse rounded-md bg-hover" />
      </Page>
    );
  if (all.isError)
    return (
      <Page>
        <ProblemNote error={all.error} title="Couldn't load approvals" />
      </Page>
    );

  const pending = all.data.filter((a) => a.status === "pending");
  const decided = all.data.filter((a) => a.status !== "pending");
  const askers = new Set(pending.map((a) => splitRequester(a.requester).name)).size;

  return (
    <Page>
      <header className="animate-rise">
        <h1 className="display text-2xl text-ink sm:text-3xl">
          {pending.length === 0 && wf.length > 0 ? (
            `${words[wf.length] ?? wf.length} ${wf.length === 1 ? "workflow is" : "workflows are"} waiting on you.`
          ) : pending.length === 0 ? (
            "Nobody is waiting on you."
          ) : (
            <>
              {words[askers] ?? askers} {askers === 1 ? "agent is" : "agents are"} waiting on you.
            </>
          )}
        </h1>
        <p className="mt-3 max-w-[38rem] text-md text-ink-2">
          When a plan goes beyond an agent's token (anything irreversible, or anything that reaches outside the box), the agent asks here. You approve
          with your passkey; it can then apply exactly that plan, once.
        </p>
      </header>

      {passkeys.data && passkeys.data.length === 0 && (
        <div className="mt-8 flex flex-col gap-3 rounded-xl border border-brass/40 bg-brass-wash px-5 py-4 sm:flex-row sm:items-center">
          <Fingerprint className="size-5 shrink-0 text-brass-ink" />
          <p className="flex-1 text-base text-ink">
            <span className="font-medium">Add a passkey first.</span>{" "}
            <span className="text-ink-2">Approving needs one, so a stolen session alone can't.</span>
          </p>
          <Button asChild variant="secondary" size="sm">
            <Link to="/settings/passkeys">Add a passkey</Link>
          </Button>
        </div>
      )}

      {pending.length > 0 && (
        <ul className="mt-10 flex flex-col gap-3">
          {pending.map((a, k) => {
            const tier = asTier(a.plan.risk);
            const r = splitRequester(a.requester);
            return (
              <li key={a.id} className="animate-rise" style={{ animationDelay: `${k * 50}ms` }}>
                <Link
                  to="/approvals/$id"
                  params={{ id: a.id }}
                  className={cn(
                    "group relative flex gap-4 overflow-hidden rounded-xl border bg-raised px-5 py-4 transition-[border-color,transform,box-shadow] hover:-translate-y-px hover:shadow-pop",
                    tier === "irreversible" ? "border-irr-rule" : tier === "outbound" ? "border-out/40" : "border-rule",
                  )}
                >
                  <span
                    aria-hidden
                    className={cn("absolute inset-y-0 left-0 w-1", tier === "irreversible" ? "bg-irr" : tier === "outbound" ? "bg-out" : "bg-rev")}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-ink-3">
                      <ActorMark actor={{ kind: "agent", name: r.name, id: a.requestedBy }} className="size-4 text-[0.55rem]" />
                      <span className="font-medium text-ink-2">{r.name}</span>
                      {r.session && <code className="font-mono text-xs text-ink-4">{r.session}</code>}
                      <span>asks to change</span>
                      <code className="font-mono text-xs text-ink-2">{a.project}</code>
                    </p>
                    <p className="mt-1.5 text-lg font-medium text-ink">{a.intent || <em className="text-ink-3">No intent given</em>}</p>
                    <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-ink-3">
                      <RiskBadge tier={tier} size="sm" />
                      <OpCounts ops={a.plan.ops} />
                      <span>asked {relative(a.createdAt, now)}</span>
                      <span className="tnum">expires in {left(a.expiresAt, now)}</span>
                    </p>
                  </div>
                  <ArrowRight className="mt-1 size-4 shrink-0 self-center text-ink-4 transition-transform group-hover:translate-x-0.5 group-hover:text-ink" />
                </Link>
              </li>
            );
          })}
        </ul>
      )}

      {wf.length > 0 && (
        <section className="mt-12" aria-labelledby="wf">
          <h2 id="wf" className="display-italic mb-1 text-xl text-ink">
            Workflows waiting for a person
          </h2>
          <p className="mb-3 text-sm text-ink-3">
            Steps in your apps' durable workflows that asked for a human decision. No passkey needed: they don't change the box itself.
          </p>
          <ul className="flex flex-col gap-2">
            {wf.map((a) => (
              <li key={a.project + a.id}>
                <ApprovalCard project={a.project} a={a} />
              </li>
            ))}
          </ul>
        </section>
      )}

      {pending.length === 0 && wf.length === 0 && (
        <div className="mt-10 rounded-xl border border-dashed border-rule-strong px-6 py-8 text-center">
          <Check className="mx-auto size-6 text-rev" />
          <p className="mt-3 text-md text-ink">All clear.</p>
          <p className="mt-1 text-base text-ink-3">When an agent needs your OK, it'll show up here, and the agent will send you a link.</p>
        </div>
      )}

      {decided.length > 0 && (
        <section className="mt-14" aria-labelledby="decided">
          <h2 id="decided" className="display-italic mb-3 text-xl text-ink">
            Decided
          </h2>
          <ul className="divide-y divide-rule border-y border-rule">
            {decided.map((a) => {
              const r = splitRequester(a.requester);
              return (
                <li key={a.id}>
                  <Link to="/approvals/$id" params={{ id: a.id }} className="flex items-start gap-3 py-3 transition-colors hover:bg-hover/50 sm:px-2">
                    <RiskMark tier={asTier(a.plan.risk)} className="mt-1" />
                    <span className="min-w-0 flex-1">
                      <span className="block text-base text-ink">{a.intent}</span>
                      <span className="mt-0.5 block text-sm text-ink-3">
                        {r.name} · <code className="font-mono text-xs">{a.project}</code> ·{" "}
                        {a.decidedAt ? `decided ${relative(a.decidedAt, now)} by ${who(a.decidedBy, names.data)}` : relative(a.createdAt, now)}
                      </span>
                      {a.status === "rejected" && a.reason && <span className="mt-1 block text-sm text-ink-2 italic">“{a.reason}”</span>}
                    </span>
                    <span className={cn("shrink-0 rounded-full px-2 py-0.5 text-xs font-medium", statusCopy[a.status].cls)}>
                      {statusCopy[a.status].label}
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        </section>
      )}
    </Page>
  );
}

type Step = { k: "idle" } | { k: "signing" } | { k: "error"; message: ReactNode } | { k: "rejecting" };

export function ApprovalPage({ id }: { id: string }) {
  const qc = useQueryClient();
  const a = useQuery(q.approval(id));
  const names = useQuery(q.tokenNames);
  const now = useNow(1000);
  const [step, setStep] = useState<Step>({ k: "idle" });
  const [typed, setTyped] = useState("");
  const [rejectOpen, setRejectOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [fresh, setFresh] = useState(false);
  useTitle(a.data ? `Approve: ${a.data.intent}` : "Approval");

  if (a.isError && notOnBox(a.error)) return <NotOnBox what="Approvals" />;
  if (a.isPending)
    return (
      <Page>
        <div className="h-10 w-2/3 animate-pulse rounded-md bg-hover" />
      </Page>
    );
  if (a.isError || !a.data)
    return (
      <Page>
        <BackToApprovals />
        <ProblemNote
          className="mt-6"
          error={a.error}
          title={a.error instanceof ApiError && a.error.status === 404 ? "There's no approval request with that ID" : "Couldn't load this approval"}
        />
      </Page>
    );

  const ap = a.data;
  const tier = asTier(ap.plan.risk);
  const r = splitRequester(ap.requester);
  const pending = ap.status === "pending" && new Date(ap.expiresAt).getTime() > now;
  const serious = tier === "irreversible";
  const typedOk = !serious || typed.trim() === ap.project;
  const ops = (ap.plan.ops ?? []).slice().sort((x, y) => (tierRank[y.risk] ?? 4) - (tierRank[x.risk] ?? 4));

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["approval", id] });
    qc.invalidateQueries({ queryKey: ["approvals"] });
  };

  const approve = async () => {
    setStep({ k: "signing" });
    try {
      const options = await api.beginApproval(id);
      const credential = await getAssertion(options);
      const res = await api.approve(id, credential);
      qc.setQueryData(q.approval(id).queryKey, res);
      setFresh(true);
      setStep({ k: "idle" });
      refresh();
    } catch (e) {
      if (e instanceof ApiError && /passkey/i.test(e.problem.detail ?? "") && /no passkey/i.test(e.problem.detail ?? "")) {
        setStep({
          k: "error",
          message: (
            <>
              You haven't added a passkey yet.{" "}
              <Link to="/settings/passkeys" className="font-medium text-ink underline underline-offset-4">
                Add one
              </Link>
              , then come back to this page.
            </>
          ),
        });
      } else if (e instanceof ApiError) {
        setStep({ k: "error", message: `${sentence(e.problem.detail ?? e.message)} ${e.problem.hint ? sentence(e.problem.hint) : ""}` });
        refresh();
      } else setStep({ k: "error", message: passkeyError(e) });
    }
  };

  const reject = async () => {
    setStep({ k: "rejecting" });
    try {
      const res = await api.reject(id, reason.trim());
      qc.setQueryData(q.approval(id).queryKey, res);
      setRejectOpen(false);
      setStep({ k: "idle" });
      refresh();
    } catch (e) {
      setStep({ k: "error", message: e instanceof ApiError ? sentence(e.problem.detail ?? e.message) : String(e) });
    }
  };

  return (
    <Page>
      <BackToApprovals />
      <header className="relative mt-6 animate-rise">
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base text-ink-2">
          <ActorMark actor={{ kind: "agent", name: r.name, id: ap.requestedBy }} />
          <span className="font-medium text-ink">{r.name}</span>
          {r.session && (
            <>
              <span className="text-ink-3">in session</span>
              <code className="font-mono text-sm">{r.session}</code>
            </>
          )}
          <span className="text-ink-3">asks to change</span>
          <code className="font-mono text-sm text-ink">{ap.project}</code>
          <span className="text-ink-4">·</span>
          <time dateTime={ap.createdAt} title={full(ap.createdAt)} className="text-ink-3">
            {relative(ap.createdAt, now)}
          </time>
        </p>
        <h1 className="display mt-3 text-2xl text-ink sm:text-[2.125rem] sm:leading-[2.6rem]">
          {ap.intent || <em className="text-ink-3">No intent given</em>}
        </h1>
        <div className="mt-4 flex flex-wrap items-center gap-3">
          <RiskBadge tier={tier} />
          {pending ? (
            <span className="inline-flex h-6 items-center gap-1.5 rounded-full bg-hover px-2.5 font-mono text-xs text-ink-2 tnum">
              expires in {left(ap.expiresAt, now)}
            </span>
          ) : (
            <span
              className={cn(
                "inline-flex h-6 items-center rounded-full px-2.5 text-sm font-medium",
                statusCopy[ap.status === "pending" ? "expired" : ap.status].cls,
              )}
            >
              {statusCopy[ap.status === "pending" ? "expired" : ap.status].label}
            </span>
          )}
        </div>
        {(ap.status === "approved" || ap.status === "used") && (
          <div
            aria-hidden
            className={cn(
              "pointer-events-none absolute -top-1 right-0 hidden rotate-[-6deg] rounded-md border-2 border-rev/70 px-3 py-1 font-mono text-sm font-medium tracking-[0.2em] text-rev uppercase sm:block",
              fresh && "animate-stamp",
            )}
          >
            Approved
          </div>
        )}
      </header>

      {pending && tier !== "reversible" && (
        <div className={cn("mt-6 overflow-hidden rounded-xl border", serious ? "border-irr-rule bg-irr-wash" : "border-out/40 bg-out-wash")}>
          {serious && <div aria-hidden className="h-1 bg-[repeating-linear-gradient(135deg,var(--irr)_0_8px,transparent_8px_14px)] opacity-70" />}
          <div className="flex gap-3 px-4 py-3.5">
            <RiskMark tier={tier} className="mt-0.5 size-4" />
            <p className="text-base text-ink">
              {serious ? (
                <>
                  <span className="font-medium">Approving lets {r.name} destroy data, once.</span>{" "}
                  <span className="text-ink-2">Undo can bring back settings, not what's deleted. Read every step below.</span>
                </>
              ) : (
                <>
                  <span className="font-medium">Approving lets {r.name} make something visible outside the box, once.</span>{" "}
                  <span className="text-ink-2">People or the internet will be able to see it.</span>
                </>
              )}
            </p>
          </div>
        </div>
      )}

      {ap.status === "rejected" && (
        <div className="mt-6 rounded-xl border border-rule bg-raised px-5 py-4">
          <p className="text-base text-ink">
            Rejected {ap.decidedAt && relative(ap.decidedAt, now)} by {who(ap.decidedBy, names.data)}.
          </p>
          {ap.reason && <p className="display-italic mt-1.5 text-lg text-ink-2">“{ap.reason}”</p>}
          <p className="mt-2 text-sm text-ink-3">{r.name} sees this reason.</p>
        </div>
      )}
      {ap.status === "approved" && (
        <p className="mt-6 text-base text-ink-2">
          Approved {ap.decidedAt && relative(ap.decidedAt, now)} by {who(ap.decidedBy, names.data)}. {r.name} can now apply this exact plan once, with
          approval <code className="font-mono text-sm text-ink">{ap.id}</code>.
        </p>
      )}
      {ap.status === "used" && ap.usedBy && (
        <p className="mt-6 text-base text-ink-2">
          Approved by {who(ap.decidedBy, names.data)} and applied as{" "}
          <Link to="/changes/$id" params={{ id: ap.usedBy }} className="font-mono text-sm text-brass-ink underline underline-offset-4 hover:text-ink">
            {ap.usedBy}
          </Link>
          .
        </p>
      )}

      <section className="mt-10" aria-labelledby="plan">
        <div className="mb-4 flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="plan" className="display-italic text-xl text-ink">
            What it will do
          </h2>
          <span className="flex items-center gap-2 text-sm text-ink-3">
            plan <CopyValue value={ap.planHash} display={ap.planHash.slice(0, 12)} />
          </span>
        </div>
        <div className="flex flex-col gap-3">
          {ops.map((op, k) => (
            <div key={op.address + k} className="animate-rise" style={{ animationDelay: `${60 + k * 40}ms` }}>
              <OpView op={op} />
            </div>
          ))}
        </div>
      </section>

      {pending && (
        <section
          className="sticky bottom-0 z-10 -mx-4 mt-10 border-t border-rule bg-paper/95 px-4 pt-4 pb-5 backdrop-blur-md sm:mx-0 sm:rounded-xl sm:border sm:px-5"
          aria-label="Decision"
        >
          {step.k === "error" && (
            <p role="alert" className="mb-3 rounded-lg border border-irr-rule bg-irr-wash px-3 py-2 text-base text-ink">
              {step.message}
            </p>
          )}
          {rejectOpen ? (
            <div className="flex flex-col gap-3">
              <label className="text-sm font-medium text-ink" htmlFor="reason">
                Tell {r.name} why <span className="font-normal text-ink-3">(it sees this)</span>
              </label>
              <textarea
                id="reason"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                maxLength={500}
                rows={2}
                autoFocus
                className="w-full resize-none rounded-md border border-rule bg-paper px-3 py-2 text-base text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
                placeholder="Not yet: the site still reads drafts from Postgres."
              />
              <div className="flex justify-end gap-2">
                <Button variant="ghost" onClick={() => setRejectOpen(false)}>
                  Back
                </Button>
                <Button variant="primary" onClick={reject} disabled={step.k === "rejecting"}>
                  <X />
                  {step.k === "rejecting" ? "Rejecting…" : "Reject"}
                </Button>
              </div>
            </div>
          ) : (
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              {serious && (
                <label className="flex-1">
                  <span className="text-sm text-ink-2">
                    Type <code className="rounded-xs bg-irr-wash px-1 font-mono text-irr">{ap.project}</code> to unlock approval
                  </span>
                  <Input
                    className="mt-1.5 font-mono"
                    value={typed}
                    onChange={(e) => setTyped(e.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    aria-label={`Type ${ap.project} to unlock approval`}
                  />
                </label>
              )}
              <div className={cn("flex gap-2", !serious && "sm:ml-auto")}>
                <Button variant="ghost" size="lg" onClick={() => setRejectOpen(true)}>
                  Reject…
                </Button>
                <Button
                  variant={serious ? "danger" : "primary"}
                  size="lg"
                  onClick={approve}
                  disabled={!typedOk || step.k === "signing" || !webauthnSupported()}
                  title={webauthnSupported() ? undefined : "Passkeys need HTTPS or localhost"}
                >
                  <KeyRound />
                  {step.k === "signing" ? "Waiting for your passkey…" : "Approve with passkey"}
                </Button>
              </div>
            </div>
          )}
        </section>
      )}
    </Page>
  );
}

function BackToApprovals() {
  return (
    <Link to="/approvals" className="inline-flex items-center gap-1.5 rounded-md text-sm text-ink-3 transition-colors hover:text-ink">
      <ArrowLeft className="size-3.5" />
      Approvals
    </Link>
  );
}
