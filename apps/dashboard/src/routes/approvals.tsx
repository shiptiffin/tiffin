import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Fingerprint, Link2, Printer } from "lucide-react";
import { useState, type ReactNode } from "react";
import { ApiError, api, notOnBox, type Approval } from "@/api/client";
import type { Names } from "@/lib/who";
import { q } from "@/api/queries";
import emptyApprovals from "@/assets/illustrations/empty-approvals.webp";
import { useTitle } from "@/components/favicon";
import {
  ActorLine,
  Leaf,
  LedgerCrumbs,
  RiskLine,
  Sec,
  Signature,
  Steps,
  UndoCant,
  clockSeconds,
  planShort,
  reasonWords,
  splitIntent,
  tokenWho,
  stamp,
  useNow,
  when,
} from "@/components/ledger-parts";
import { WorkflowRow } from "@/components/ledger-workflow";
import { Logo } from "@/components/logo";
import { NotOnBox, Page } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { SignedEntry } from "@/components/signed-entry";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { actorWords } from "@/lib/actors";
import { splitAddress, asTier, intentWords, opCounts, splitRequester as split, tierRank } from "@/lib/changes";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { countWords, duration, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, relative } from "@/lib/time";
import { getAssertion, passkeyError, webauthnSupported } from "@/lib/webauthn";
import { useWaitingWorkflowApprovals } from "@/lib/wf";
import "./ledger-print.css";

/** "23 h 14 min", "4:09": how long a request has left. */
function left(iso: string, now: number) {
  const s = Math.max(0, Math.floor((new Date(iso).getTime() - now) / 1000));
  if (s >= 3600) return duration(s);
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, "0")}`;
}

const agentOf = (a: Approval) => actorWords({ kind: "agent", name: split(a.requester).name });

// ───────────────────────── the list ─────────────────────────

export function ApprovalsPage() {
  useTitle("Approvals");
  const all = useQuery(q.approvals);
  const passkeys = useQuery({ ...q.passkeys, retry: false });
  const names = useQuery({ ...q.tokenNames, retry: false });
  const now = useNow(30_000);
  const wf = useWaitingWorkflowApprovals();
  const [showAll, setShowAll] = useState(false);

  if (all.isError && notOnBox(all.error)) return <NotOnBox what="Approvals" />;
  if (all.isPending)
    return (
      <Page>
        <div className="h-3 w-32 rounded bg-paper-sunk" />
        <div className="mt-4 h-8 w-96 max-w-full animate-pulse rounded-md bg-paper-sunk" />
      </Page>
    );
  if (all.isError)
    return (
      <Page>
        <ProblemNote error={all.error} title="Couldn’t load approvals" />
      </Page>
    );

  const pending = all.data.filter((a) => a.status === "pending" && new Date(a.expiresAt).getTime() > now);
  const decided = all.data.filter((a) => !pending.includes(a));
  const agents = [...new Set(pending.map(agentOf))];
  let headline: string;
  if (pending.length === 0 && wf.length > 0) headline = wf.length === 1 ? "A workflow is waiting for a person." : `${words(wf.length, true)} workflows are waiting for a person.`;
  else if (pending.length === 0) headline = "Nobody is waiting on you.";
  else if (agents.length === 1) headline = pending.length === 1 ? `${agents[0]} is waiting on you.` : `${agents[0]} is waiting on you for ${words(pending.length)} changes.`;
  else headline = `${words(agents.length, true)} agents are waiting on you.`;
  const shownDecided = showAll ? decided : decided.slice(0, 30);

  return (
    <Page>
      <LedgerCrumbs items={[{ label: "Ledger", to: "/ledger" }, { label: "Approvals" }]} />
      <h1 className="sentence mt-3 text-ink">{headline}</h1>
      <p className="mt-2 max-w-[40rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
        When a plan goes beyond an agent’s token (anything irreversible, or anything that reaches outside the box), the agent asks here. You sign with your
        passkey; it can then apply exactly that plan, once.
      </p>

      {passkeys.data && passkeys.data.length === 0 && (
        <div className="mt-7 flex flex-col gap-3 border-y border-rule py-3 sm:flex-row sm:items-center">
          <Fingerprint className="size-[18px] shrink-0 text-brass-ink" aria-hidden />
          <p className="flex-1 text-[0.875rem] text-ink">
            <span className="font-[550]">Add a passkey first.</span> <span className="text-ink-2">Signing needs one, so a stolen session alone can’t.</span>
          </p>
          <Button asChild variant="secondary" size="sm" className="self-start sm:self-auto">
            <Link to="/settings/passkeys">Add a passkey</Link>
          </Button>
        </div>
      )}

      {pending.length > 0 && (
        <section aria-labelledby="waiting" className="mt-9 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-raised">
          <h2 id="waiting" className="label px-4 pt-3 !text-brass-ink sm:px-5">
            Waiting for you
          </h2>
          <div className="divide-y divide-rule px-4 sm:px-5">
            {pending.map((a) => {
              const r = split(a.requester);
              return (
                <div key={a.id} className="flex flex-col gap-x-6 sm:flex-row sm:items-center">
                  <SignedEntry
                    className="min-w-0 flex-1"
                    time={clock(a.createdAt)}
                    timeNote="asked"
                    actor={{ kind: "agent", name: r.name, session: r.session }}
                    intent={splitIntent(intentWords({ intent: a.intent, plan: a.plan })).head}
                    to="/approvals/$id"
                    params={{ id: a.id }}
                    counts={opCounts(a.plan.ops)}
                    tier={asTier(a.plan.risk)}
                    extra={
                      <span className="tnum">
                        {a.project} · expires in {left(a.expiresAt, now)}
                      </span>
                    }
                  />
                  <Button asChild variant="primary" size="md" className="mb-3 self-start max-sm:ml-[56px] sm:mb-0 sm:self-center">
                    <Link to="/approvals/$id" params={{ id: a.id }}>
                      Review
                    </Link>
                  </Button>
                </div>
              );
            })}
          </div>
        </section>
      )}

      {wf.length > 0 && (
        <section id="workflows" aria-labelledby="wf" className="mt-10 scroll-mt-6">
          <h2 id="wf" className="label">
            Waiting for a person
          </h2>
          <p className="mt-1 max-w-[40rem] text-[0.8125rem] text-ink-3">
            Steps in your apps’ workflows that asked for a human decision. They don’t change the box, so no passkey is needed.
          </p>
          <div className="mt-3 divide-y divide-rule border-y border-rule">
            {wf.map((w) => (
              <WorkflowRow key={w.project + w.id} w={w} />
            ))}
          </div>
        </section>
      )}

      {pending.length === 0 && wf.length === 0 && (
        <div className="mt-10 flex flex-col items-center gap-1 border-y border-rule py-10 text-center">
          <img src={emptyApprovals} alt="" width={200} height={160} className="mb-2 h-auto w-[160px] sm:w-[200px]" />
          <p className="text-[0.9375rem] text-ink">Nothing to sign.</p>
          <p className="max-w-[28rem] text-[0.875rem] text-ink-3">
            When an agent needs your signature, it shows up here and in the Ledger, and the agent sends you the link.
          </p>
        </div>
      )}

      {decided.length > 0 && (
        <section aria-labelledby="decided" className="mt-12">
          <h2 id="decided" className="label">
            Decided
          </h2>
          <div className="mt-2 divide-y divide-rule border-y border-rule">
            {shownDecided.map((a) => (
              <DecidedRow key={a.id} a={a} names={names.data} />
            ))}
          </div>
          {decided.length > shownDecided.length && (
            <Button variant="ghost" size="sm" className="mt-3" onClick={() => setShowAll(true)}>
              Show {countWords(decided.length - shownDecided.length, "older one", "older ones")}
            </Button>
          )}
        </section>
      )}
    </Page>
  );
}

const statusNote: Partial<Record<Approval["status"], string>> = { approved: "signed", used: "signed" };

function DecidedRow({ a, names }: { a: Approval; names?: Names }) {
  const r = split(a.requester);
  const by = tokenWho(a.decidedBy, names);
  const at = a.decidedAt ?? a.expiresAt;
  let signature: ReactNode;
  if (a.status === "used") signature = `signed by ${by} · passkey · applied`;
  else if (a.status === "approved") signature = `signed by ${by} · passkey · not applied yet`;
  else if (a.status === "rejected") signature = `declined by ${by}`;
  else signature = "expired, unsigned";
  return (
    <SignedEntry
      time={clock(at)}
      timeNote={statusNote[a.status]}
      actor={{ kind: "agent", name: r.name, session: r.session }}
      intent={splitIntent(intentWords({ intent: a.intent, plan: a.plan })).head}
      to="/approvals/$id"
      params={{ id: a.id }}
      counts={opCounts(a.plan.ops)}
      tier={asTier(a.plan.risk)}
      signature={signature}
      extra={
        <>
          {a.status === "rejected" && a.reason && <span className="text-ink-2">“{a.reason}”</span>}
          <span>{a.project}</span>
        </>
      }
    />
  );
}

// ───────────────────────── the permit ─────────────────────────

type Step = { k: "idle" } | { k: "signing" } | { k: "error"; message: ReactNode } | { k: "rejecting" };

export function ApprovalPage({ id }: { id: string }) {
  const qc = useQueryClient();
  const a = useQuery(q.approval(id));
  const names = useQuery({ ...q.tokenNames, retry: false });
  const used = useQuery({ ...q.change(a.data?.usedBy ?? ""), enabled: !!a.data?.usedBy });
  const { name: myName, role } = useMe();
  const now = useNow(1000);
  const [step, setStep] = useState<Step>({ k: "idle" });
  const [typed, setTyped] = useState("");
  const [declining, setDeclining] = useState(false);
  const [reason, setReason] = useState("");
  const [fresh, setFresh] = useState(false);
  const [armedAt, setArmedAt] = useState<string | null>(null);
  useTitle(a.data ? `Sign: ${splitIntent(intentWords({ intent: a.data.intent, plan: a.data.plan })).head}` : "Approval");

  if (a.isError && notOnBox(a.error)) return <NotOnBox what="Approvals" />;
  if (a.isPending)
    return (
      <Page>
        <div className="sm:pl-[92px]">
          <div className="h-3 w-40 rounded bg-paper-sunk" />
          <div className="mt-8 h-4 w-64 rounded bg-paper-sunk" />
          <div className="mt-3 h-10 w-2/3 animate-pulse rounded-md bg-paper-sunk" />
        </div>
      </Page>
    );
  if (a.isError || !a.data)
    return (
      <Page>
        <Leaf>
          <LedgerCrumbs items={[{ label: "Ledger", to: "/ledger" }, { label: "Approvals", to: "/approvals" }]} />
          <ProblemNote
            className="mt-6"
            error={a.error}
            title={a.error instanceof ApiError && a.error.status === 404 ? "There’s no approval request with that ID" : "Couldn’t load this approval"}
          />
        </Leaf>
      </Page>
    );

  const ap = a.data;
  const tier = asTier(ap.plan.risk);
  const r = split(ap.requester);
  const agent = actorWords({ kind: "agent", name: r.name });
  const expired = ap.status === "expired" || (ap.status === "pending" && new Date(ap.expiresAt).getTime() <= now);
  const pending = ap.status === "pending" && !expired;
  const signed = ap.status === "approved" || ap.status === "used";
  const serious = tier === "irreversible";
  const ops = (ap.plan.ops ?? []).slice().sort((x, y) => (tierRank[y.risk] ?? 4) - (tierRank[x.risk] ?? 4));
  // The guard word is the thing that can't come back ("analytics", "imports"), not the project around it.
  const guard = splitAddress(ops.find((o) => asTier(o.risk) === "irreversible")?.address ?? "").name || ap.project;
  const armed = !serious || typed.trim() === guard;
  const outbound = ops.filter((o) => asTier(o.risk) === "outbound");
  const intent = splitIntent(intentWords({ intent: ap.intent, plan: ap.plan }));
  const signer = tokenWho(ap.decidedBy, names.data);

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
      if (e instanceof ApiError && /no passkey/i.test(e.problem.detail ?? "")) {
        setStep({
          k: "error",
          message: (
            <>
              You haven’t added a passkey yet.{" "}
              <Link to="/settings/passkeys" className="font-[550] text-ink underline underline-offset-4">
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
      setDeclining(false);
      setStep({ k: "idle" });
      refresh();
    } catch (e) {
      setStep({ k: "error", message: e instanceof ApiError ? sentence(e.problem.detail ?? e.message) : String(e) });
    }
  };

  const status = pending
    ? { label: "Waiting for you", tone: "brass", tail: <span className="tnum">expires in {left(ap.expiresAt, now)}</span> }
    : ap.status === "used"
      ? { label: "Signed and applied", tone: "brass", tail: ap.decidedAt && when(ap.decidedAt) }
      : ap.status === "approved"
        ? { label: "Signed, not applied yet", tone: "brass", tail: ap.decidedAt && when(ap.decidedAt) }
        : ap.status === "rejected"
          ? { label: "Declined", tone: "", tail: ap.decidedAt && when(ap.decidedAt) }
          : { label: "Expired", tone: "", tail: when(ap.expiresAt) };

  const meLine = myName ? (role && myName.toLowerCase() === role ? `${myName} of this box` : `${myName}${role ? `, ${role} of this box` : ""}`) : "you";

  return (
    <Page>
      <article data-receipt aria-label="Approval request" className="flex flex-col">
        <div className="mb-8 hidden items-center gap-2 border-b border-ink pb-3 text-[0.8125rem] text-ink-2 print:flex">
          <Logo className="size-5 text-ink" />
          <b className="font-[550] text-ink">tiffin</b>
          <span>Approval</span>
          <span className="ml-auto">Printed {stamp(new Date(now).toISOString())}</span>
        </div>
        <div className="print:hidden">
          <LedgerCrumbs items={[{ label: "Ledger", to: "/ledger" }, { label: "Approvals", to: "/approvals" }, { label: pending ? "Waiting for you" : "Request" }]} />
        </div>

        <Leaf className="mt-7" time={clock(ap.createdAt)} note="asked">
          <p className="flex flex-wrap items-baseline gap-x-2.5 text-[0.8125rem] text-ink-2">
            <span className={cn("label", status.tone === "brass" && "!text-brass-ink")}>{status.label}</span>
            <span>{status.tail}</span>
          </p>
          <ActorLine
            className="mt-4"
            kind="agent"
            name={r.name}
            session={r.session}
            model={used.data?.actor.model}
            verb={pending ? "asks to change" : "asked to change"}
            project={ap.project}
          />
          <h1 className="intent mt-1.5 text-ink">{intent.head || <span className="text-ink-3">No intent given.</span>}</h1>
          {intent.rest && (
            <p className="mt-3.5 max-w-[37.5rem] text-[0.90625rem] leading-[1.375rem] text-graphite">
              <span className="label mb-0.5 block">In its words</span>
              {intent.rest}
            </p>
          )}
          <RiskLine tier={tier} ops={ops} hash={ap.planHash} />
        </Leaf>

        <Leaf className="mt-6">
          <Sec label={pending ? "What it will do" : "What it asked to do"}>
            <Steps ops={ops} project={ap.project} />
          </Sec>
          {outbound.length > 0 && (
            <Sec label="What reaches outside the box">
              <div className="max-w-[38rem] border-l-2 border-warn py-0.5 pl-4 text-[0.875rem] leading-[1.3125rem] text-ink">
                {outbound.map((o, i) => (
                  <p key={o.address + i}>{reasonWords(o).did}</p>
                ))}
                <p className="mt-1 text-[0.84375rem] text-ink-2">
                  Signing lets {agent} make something visible outside the box, once. People or the internet will be able to see it.
                </p>
              </div>
            </Sec>
          )}
          <UndoCant ops={ops} project={ap.project} />
        </Leaf>

        {pending && (
          <Leaf time={serious && armed && armedAt && !declining ? clock(armedAt) : undefined} note={declining ? "declining" : serious && armed ? "armed" : undefined} className="print:hidden">
            <section aria-label="Your decision" className="border-t border-rule pt-5">
              {step.k === "error" && (
                <p role="alert" className="mb-3 border-l-2 border-danger py-0.5 pl-3 text-[0.875rem] text-ink">
                  {step.message}
                </p>
              )}
              <div
                className={cn(
                  "rounded-[12px] border bg-paper-raised px-4 py-4 shadow-[var(--top-light)] transition-[border-color,box-shadow] duration-[var(--dur-state)] ease-[var(--ease-out)] sm:px-[18px]",
                  serious && armed && !declining ? "border-danger shadow-[0_0_0_3px_var(--danger-wash)]" : "border-rule-2",
                )}
              >
                {declining ? (
                  <div className="flex flex-col gap-3">
                    <label className="text-[0.84375rem] text-ink-2" htmlFor="reason">
                      Tell {agent} why <span className="text-ink-3">(it sees this)</span>
                    </label>
                    <textarea
                      id="reason"
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                      maxLength={500}
                      rows={2}
                      autoFocus
                      className="w-full resize-none rounded-[8px] border border-rule-2 bg-paper px-3 py-2 text-[0.875rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
                      placeholder="Not yet: the reports page still reads these events."
                    />
                    <div className="flex flex-wrap justify-end gap-2">
                      <Button variant="ghost" onClick={() => setDeclining(false)}>
                        Back
                      </Button>
                      <Button variant="secondary" onClick={reject} disabled={step.k === "rejecting"}>
                        {step.k === "rejecting" ? "Declining…" : "Decline"}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <>
                    {serious ? (
                      <div className="flex items-center gap-3 text-[0.84375rem] text-ink-2">
                        <span>
                          Type <span className="ident text-ink">{guard}</span> to lift the guard.
                        </span>
                        <span className={cn("label ml-auto flex items-center gap-1.5", armed && "!text-danger")} aria-live="polite">
                          {armed && <span aria-hidden className="size-1.5 rounded-full bg-danger" />}
                          {armed ? "Armed" : "Locked"}
                        </span>
                      </div>
                    ) : (
                      <p className="text-[0.84375rem] text-ink-2">
                        {agent} can apply this exact plan once you sign. It expires in <span className="tnum">{left(ap.expiresAt, now)}</span>.
                      </p>
                    )}
                    <div className="mt-3 flex flex-col gap-2.5 sm:flex-row sm:items-center">
                      {serious && (
                        <Input
                          className="h-[38px] min-h-[38px] w-full font-mono sm:w-auto sm:flex-1"
                          value={typed}
                          onChange={(e) => {
                            setTyped(e.target.value);
                            setArmedAt(e.target.value.trim() === guard ? new Date().toISOString() : null);
                          }}
                          onKeyDown={(e) => {
                            if (e.key === "Escape") {
                              setTyped("");
                              setArmedAt(null);
                            }
                            if (e.key === "Enter" && armed) void approve();
                          }}
                          data-guard={guard}
                          autoComplete="off"
                          spellCheck={false}
                          aria-label={`Type ${guard} to arm`}
                        />
                      )}
                      <Button
                        variant={serious ? "danger" : "primary"}
                        size="lg"
                        className={cn("max-sm:w-full", serious && !armed && "!bg-paper-sunk !text-ink-2 border border-rule-2 !shadow-none")}
                        onClick={approve}
                        disabled={!armed || step.k === "signing" || !webauthnSupported()}
                        title={webauthnSupported() ? undefined : "Passkeys need HTTPS or localhost"}
                      >
                        <Fingerprint />
                        {step.k === "signing" ? "Waiting for your passkey…" : "Sign with passkey"}
                      </Button>
                    </div>
                    <div className="mt-3.5 flex flex-wrap items-center gap-x-3 gap-y-2 text-[0.78125rem] text-ink-3">
                      <span className="min-w-0 flex-1">Signing approves this plan once. Nothing else is approved.</span>
                      <Button variant="secondary" onClick={() => setDeclining(true)}>
                        Decline…
                      </Button>
                    </div>
                  </>
                )}
              </div>
              <div className="mt-7 max-w-[calc(100%-8rem)] border-b border-rule-3 pt-7 max-sm:max-w-full" aria-hidden />
              <p className="mt-1.5 text-xs text-ink-3">Signature · {meLine}</p>
            </section>
          </Leaf>
        )}

        {signed && (
          <>
            <Leaf time={ap.decidedAt ? clock(ap.decidedAt) : undefined} note="signed" className="mt-2">
              <div className="border-t border-rule pt-7">
                <Signature
                  name={signer}
                  at={ap.decidedAt ?? ap.createdAt}
                  hash={ap.planHash}
                  fresh={fresh}
                  how={
                    <>
                      Signed by {signer} · passkey · {ap.decidedAt ? clock(ap.decidedAt) : ""} · plan <span className="ident text-ink">{planShort(ap.planHash)}</span>
                    </>
                  }
                />
              </div>
            </Leaf>
            <Leaf time={used.data ? clock(used.data.at) : undefined} note={used.data ? "applied" : undefined} className="mt-5">
              {ap.status === "used" && ap.usedBy ? (
                <p className="text-[0.875rem] leading-[1.3125rem] text-ink">
                  {used.data ? (
                    <>
                      Applied at {clockSeconds(used.data.at)}
                      {ap.decidedAt && `, ${duration((new Date(used.data.at).getTime() - new Date(ap.decidedAt).getTime()) / 1000)} after you signed`}, as version{" "}
                      {used.data.version} of {ap.project}.
                    </>
                  ) : (
                    <>Applied.</>
                  )}
                  <span className="block text-[0.84375rem] text-ink-2">
                    {agent} applied it with this approval, so the approval is spent.{" "}
                    <Link to="/changes/$id" params={{ id: ap.usedBy }} className="text-brass-ink hover:underline hover:underline-offset-4">
                      Open the Ledger entry
                    </Link>
                  </span>
                </p>
              ) : (
                <p className="text-[0.875rem] leading-[1.3125rem] text-ink">
                  {agent} can now apply this exact plan once.
                  <span className="block text-[0.84375rem] text-ink-2">It hasn’t yet; this page updates when it does. If the project moves first, the plan won’t match and nothing happens.</span>
                </p>
              )}
              <PermitActions />
            </Leaf>
          </>
        )}

        {ap.status === "rejected" && (
          <Leaf time={ap.decidedAt ? clock(ap.decidedAt) : undefined} note="declined" className="mt-2">
            <div className="border-t border-rule pt-5 text-[0.875rem] leading-[1.3125rem] text-ink">
              <p>
                Declined by {signer}
                {ap.decidedAt ? ` ${when(ap.decidedAt)}` : ""}.
              </p>
              {ap.reason && <p className="mt-2 max-w-[38rem] border-l-2 border-rule-3 py-0.5 pl-4 text-ink-2">“{ap.reason}”</p>}
              <p className="mt-2 text-[0.8125rem] text-ink-3">{agent} sees this reason. Nothing was changed.</p>
            </div>
          </Leaf>
        )}

        {expired && (
          <Leaf time={clock(ap.expiresAt)} note="expired" className="mt-2">
            <p className="border-t border-rule pt-5 text-[0.875rem] leading-[1.3125rem] text-ink">
              Nobody signed this before it expired {relative(ap.expiresAt, now)}. Nothing was changed; {agent} has to ask again.
            </p>
          </Leaf>
        )}
      </article>
    </Page>
  );
}

function PermitActions() {
  const [copied, setCopied] = useState(false);
  return (
    <div data-print-hide className="mt-5 flex flex-wrap items-center gap-2">
      <Button variant="secondary" onClick={() => window.print()}>
        <Printer />
        Print receipt
      </Button>
      <Button
        variant="ghost"
        onClick={async () => {
          if (await copyText(location.href)) {
            setCopied(true);
            setTimeout(() => setCopied(false), 1600);
          }
        }}
      >
        {copied ? <Check className="text-ok" /> : <Link2 />}
        {copied ? "Link copied" : "Copy link"}
      </Button>
    </div>
  );
}
