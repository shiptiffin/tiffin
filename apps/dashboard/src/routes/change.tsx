import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Link2, Printer, Undo2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError, api, type Change, type Plan } from "@/api/client";
import { q } from "@/api/queries";
import { CopyValue } from "@/components/copy";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import {
  ActorLine,
  Facts,
  Leaf,
  LedgerCrumbs,
  RiskLine,
  Sec,
  Signature,
  Steps,
  UndoCant,
  clockSeconds,
  opTitle,
  planShort,
  reasonWords,
  splitIntent,
  tokenWho,
  stamp,
  useApprovalsByChange,
  when,
} from "@/components/ledger-parts";
import { Logo } from "@/components/logo";
import { Page } from "@/components/page";
import { Code, ProblemNote, sentence } from "@/components/problem";
import { RiskDots } from "@/components/risk-dots";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { actorWords } from "@/lib/actors";
import { asTier, intentWords, tierCopy, tierRank } from "@/lib/changes";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { useEnamel } from "@/lib/enamel";
import { duration } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock } from "@/lib/time";
import "./ledger-print.css";
import { actorShown } from "@/lib/who";

export function ChangePage({ id }: { id: string }) {
  const { data: c, isPending, error } = useQuery(q.change(id));
  useTitle(c ? intentWords(c) || "Entry" : "Entry");
  const [undoOpen, setUndoOpen] = useState(false);
  const [justUndone, setJustUndone] = useState<string | null>(null);
  const approvals = useApprovalsByChange();
  const names = useQuery({ ...q.tokenNames, retry: false });
  const { can } = useMe();
  const undoneBy = justUndone ?? c?.undoneBy ?? undefined;
  const undo = useQuery({ ...q.change(undoneBy ?? ""), enabled: !!undoneBy });
  const undid = useQuery({ ...q.change(c?.undoOf ?? ""), enabled: !!c?.undoOf });
  const enamel = useEnamel(c?.project);

  if (isPending)
    return (
      <Page>
        <div className="sm:pl-[92px]">
          <div className="h-3 w-40 rounded bg-paper-sunk" />
          <div className="mt-8 h-4 w-64 rounded bg-paper-sunk" />
          <div className="mt-3 h-10 w-2/3 animate-pulse rounded-md bg-paper-sunk" />
        </div>
      </Page>
    );
  if (error || !c)
    return (
      <Page>
        <Leaf>
          <LedgerCrumbs items={[{ label: "Ledger", to: "/ledger" }, { label: "Entry" }]} />
          <ProblemNote
            className="mt-6"
            error={error}
            title={error instanceof ApiError && error.status === 404 ? "There’s no entry with that ID, or it belongs to a project you can’t see" : "Couldn’t load this entry"}
          />
        </Leaf>
      </Page>
    );

  const tier = asTier(c.plan.risk);
  const ops = c.plan.ops ?? [];
  const agent = c.actor.kind === "agent";
  const approval = approvals.get(c.id);
  const intent = splitIntent(intentWords(c));
  // People by their current name (the owner token reads "Bilal", not "Owner"); agents by theirs.
  const who = agent ? actorWords(c.actor) : actorShown(c.actor, names.data);
  const signer = approval ? tokenWho(approval.decidedBy, names.data) : who;
  const undoneAt = undo.data?.at;

  return (
    <Page>
      <article data-receipt aria-label="Ledger entry" className="flex flex-col">
        <PrintHead />
        <div className="print:hidden">
          <LedgerCrumbs
            items={[
              { label: "Ledger", to: "/ledger" },
              {
                label: (
                  <span className="inline-flex items-center gap-1.5">
                    <EnamelSwatch enamel={enamel} size={7} />
                    {c.project}
                  </span>
                ),
                to: "/ledger",
                search: { project: c.project },
              },
              { label: `Entry · version ${c.version}` },
            ]}
          />
        </div>

        <Leaf className="mt-7" time={clock(c.at)} note="applied">
          <p className="flex flex-wrap items-baseline gap-x-2.5 text-[0.8125rem] text-ink-2">
            <span className={cn("label", approval && "!text-brass-ink")}>
              {approval ? (undoneBy ? "Signed, applied, then undone" : "Signed and applied") : undoneBy ? "Applied, then undone" : "Applied"}
            </span>
            <span>{when(c.at)}</span>
          </p>
          <ActorLine className="mt-4" kind={c.actor.kind} name={actorShown(c.actor, names.data)} session={c.actor.session} model={c.actor.model} verb="changed" project={c.project} />
          <h1
            className={cn(
              "intent mt-1.5",
              agent && !approval ? "text-graphite" : "text-ink",
              undoneBy && "text-ink-2 line-through decoration-ink-4/50 decoration-1",
            )}
          >
            {intent.head || <span className="text-ink-3">No intent given.</span>}
          </h1>
          {intent.rest && (
            <p className={cn("mt-3.5 max-w-[37.5rem] text-[0.90625rem] leading-[1.375rem]", agent ? "text-graphite" : "text-ink-2")}>
              {agent && <span className="label mb-0.5 block">In its words</span>}
              {intent.rest}
            </p>
          )}
          <RiskLine tier={tier} ops={ops} hash={c.plan.hash} />
        </Leaf>

        {undoneBy && (
          <Leaf className="mt-5" time={undoneAt ? clock(undoneAt) : undefined} note="undone">
            <p className="max-w-[38rem] border-l-2 border-rule-3 py-0.5 pl-4 text-[0.875rem] leading-[1.3125rem] text-ink">
              Undone by {undo.data ? (undo.data.actor.kind === "agent" ? actorWords(undo.data.actor) : actorShown(undo.data.actor, names.data)) : "someone"}
              {undoneAt ? ` ${when(undoneAt)}` : ""}.{" "}
              <Link to="/changes/$id" params={{ id: undoneBy }} className="text-brass-ink hover:underline hover:underline-offset-4 print:text-ink">
                Read the undo
              </Link>
            </p>
          </Leaf>
        )}
        {c.undoOf && (
          <Leaf className="mt-5">
            <p className="max-w-[38rem] border-l-2 border-rule-3 py-0.5 pl-4 text-[0.875rem] leading-[1.3125rem] text-ink">
              This entry undid{" "}
              {undid.data ? (
                <>
                  “{intentWords(undid.data).replace(/[.]$/, "")}” from {when(undid.data.at)}
                </>
              ) : (
                "an earlier entry"
              )}
              .{" "}
              <Link to="/changes/$id" params={{ id: c.undoOf }} className="text-brass-ink hover:underline hover:underline-offset-4 print:text-ink">
                Read it
              </Link>
            </p>
          </Leaf>
        )}

        <Leaf className="mt-7">
          <Sec label="What it did, in order">
            <Steps ops={ops} project={c.project} past />
          </Sec>
          <UndoCant ops={ops} project={c.project} done />
          {!undoneBy && <UndoSection change={c} canUndo={can("apply:reversible")} onUndo={() => setUndoOpen(true)} />}
          <Sec label="Times and numbers">
            <Facts
              items={[
                approval && ["Asked", <span key="a">{clockSeconds(approval.createdAt)}, by {who}</span>],
                approval?.decidedAt && [
                  "Signed",
                  <span key="s">
                    {clockSeconds(approval.decidedAt)}, {duration((new Date(approval.decidedAt).getTime() - new Date(approval.createdAt).getTime()) / 1000)} later
                  </span>,
                ],
                ["Applied", <span key="ap">{stamp(c.at)}</span>],
                undoneAt && ["Undone", <span key="u">{stamp(undoneAt)}</span>],
                ["Version", <span key="v">{c.plan.baseVersion} to {c.version} of {c.project}</span>],
                [
                  "Plan",
                  <span key="p">
                    <CopyValue value={c.plan.hash} display={`${c.plan.hash.slice(0, 16)}…`} className="-my-1 max-w-full print:hidden" />
                    <span className="ident hidden break-all print:inline">{c.plan.hash}</span>
                  </span>,
                ],
                ["Entry", <CopyValue key="e" value={c.id} className="-my-1 max-w-full" />],
                approval && ["Approval", <Link key="ap" to="/approvals/$id" params={{ id: approval.id }} className="ident text-ink-2 hover:text-ink">{approval.id}</Link>],
              ]}
            />
          </Sec>
        </Leaf>

        <Leaf className="mt-3" time={clock(approval?.decidedAt ?? c.at)} note={approval ? "signed" : "applied"}>
          <div className="border-t border-rule pt-7">
            <Signature
              name={signer}
              at={approval?.decidedAt ?? c.at}
              hash={c.plan.hash}
              seal={!!approval}
              how={
                approval ? (
                  <>
                    Signed by {signer} · passkey · {clock(approval.decidedAt ?? c.at)} · plan <span className="ident text-ink">{planShort(c.plan.hash)}</span>
                  </>
                ) : agent ? (
                  <>
                    Applied by {who}{c.actor.model ? <> (<span className="ident">{c.actor.model}</span>)</> : null} within its grant{c.actor.session ? <> · session <span className="ident">{c.actor.session}</span></> : null} · {clock(c.at)} · plan{" "}
                    <span className="ident text-ink">{planShort(c.plan.hash)}</span>
                  </>
                ) : (
                  <>
                    Applied by {who} · confirmed plan <span className="ident text-ink">{planShort(c.plan.hash)}</span> · {clock(c.at)}
                  </>
                )
              }
            />
            {approval && <p className="mt-4 text-[0.84375rem] text-ink-2">{who} asked; {signer} signed it with a passkey, so the box let it apply this exact plan, once.</p>}
          </div>
          <ReceiptActions />
        </Leaf>
      </article>

      <UndoDialog change={c} open={undoOpen} onOpenChange={setUndoOpen} onUndone={(nid) => setJustUndone(nid)} />
    </Page>
  );
}

/** On paper only: whose ledger this is and when it was printed. */
function PrintHead() {
  const [now] = useState(() => new Date().toISOString());
  return (
    <div className="mb-8 hidden items-center gap-2 border-b border-ink pb-3 text-[0.8125rem] text-ink-2 print:flex">
      <Logo className="size-5 text-ink" />
      <b className="font-[550] text-ink">tiffin</b>
      <span>Ledger receipt</span>
      <span className="ml-auto">Printed {stamp(now)}</span>
    </div>
  );
}

function ReceiptActions() {
  const [copied, setCopied] = useState(false);
  return (
    <div data-print-hide className="mt-6 flex flex-wrap items-center gap-2">
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

/** What undo does, step by step (the inverse plan), and the way to do it. */
function UndoSection({ change, canUndo, onUndo }: { change: Change; canUndo: boolean; onUndo: () => void }) {
  const inverse = change.inverse ?? [];
  const lost = (change.plan.ops ?? []).some((o) => asTier(o.risk) === "irreversible");
  return (
    <Sec label="What undo does">
      {
        <>
          {inverse.length > 0 ? (
            <ul className="flex flex-col gap-1.5">
              {inverse.map((op, k) => {
                const t = asTier(op.risk);
                return (
                  <li key={op.address + k} className="grid grid-cols-[22px_minmax(0,1fr)_auto] items-baseline gap-x-3 text-[0.875rem] leading-5">
                    <span aria-hidden className="text-center text-ink-3">
                      ↩
                    </span>
                    <span className="text-ink">
                      {opTitle(op, change.project)}
                      {t === "irreversible" && reasonWords(op).did && <span className="block text-[0.8125rem] text-danger">{reasonWords(op).did}</span>}
                    </span>
                    <RiskDots tier={t} label={t === "reversible" ? "Low" : undefined} className="text-xs" />
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="text-[0.875rem] text-ink-2">Undo has nothing to put back for this entry.</p>
          )}
          <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2" data-print-hide>
            {canUndo ? (
              <Button variant="secondary" onClick={onUndo}>
                <Undo2 />
                Undo this change
              </Button>
            ) : (
              <p className="text-[0.8125rem] text-ink-3">Your session can’t apply changes, so it can’t undo this one.</p>
            )}
            <span className="text-[0.8125rem] text-ink-3">
              {lost ? "You’ll review the undo plan first. The deleted data stays deleted." : "You’ll review the undo plan first; nothing changes until you confirm."}
            </span>
          </div>
        </>
      }
    </Sec>
  );
}

// ───────────────────────── the undo flow ─────────────────────────

type Phase = { k: "loading" } | { k: "review"; plan: Plan; changed?: boolean } | { k: "applying"; plan: Plan } | { k: "error"; error: unknown; plan?: Plan };

function UndoDialog({
  change,
  open,
  onOpenChange,
  onUndone,
}: {
  change: Change;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onUndone: (id: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <UndoBody change={change} onOpenChange={onOpenChange} onUndone={onUndone} />}
    </Dialog>
  );
}

function UndoBody({ change, onOpenChange, onUndone }: { change: Change; onOpenChange: (o: boolean) => void; onUndone: (id: string) => void }) {
  const qc = useQueryClient();
  const [phase, setPhase] = useState<Phase>({ k: "loading" });
  const [typed, setTyped] = useState("");
  const [attempt, setAttempt] = useState(0);

  const done = (newId?: string) => {
    qc.invalidateQueries({ queryKey: ["changes"] });
    qc.invalidateQueries({ queryKey: ["change"] });
    qc.invalidateQueries({ queryKey: ["projects"] });
    onOpenChange(false);
    if (newId) onUndone(newId);
  };

  // Ask for the undo plan: without a confirm hash the API answers 428 with it.
  useEffect(() => {
    let live = true;
    api
      .undo(change.id)
      .then(() => live && done())
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 428 && e.problem.plan) setPhase({ k: "review", plan: e.problem.plan });
        else setPhase({ k: "error", error: e });
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [change.id, attempt]);

  const fetchPlan = () => {
    setPhase({ k: "loading" });
    setTyped("");
    setAttempt((n) => n + 1);
  };

  const confirm = async (plan: Plan) => {
    setPhase({ k: "applying", plan });
    try {
      const r = await api.undo(change.id, plan.hash);
      done(r.change?.id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 428 && e.problem.plan) setPhase({ k: "review", plan: e.problem.plan, changed: true });
      else setPhase({ k: "error", error: e, plan });
    }
  };

  const plan = phase.k === "review" || phase.k === "applying" ? phase.plan : phase.k === "error" ? phase.plan : undefined;
  const tier = plan ? asTier(plan.risk) : "reversible";
  const serious = tier === "irreversible";
  const typedOk = !serious || typed.trim() === change.project;

  return (
    <DialogContent tone={serious ? "danger" : "default"} className="max-w-2xl" onOpenAutoFocus={(e) => e.preventDefault()}>
      <DialogHeader>
        <DialogTitle className={cn(serious && "text-danger")}>{serious ? "This undo destroys data" : "Review the undo"}</DialogTitle>
        <DialogDescription>
          {phase.k === "loading" ? (
            "Working out what undo would do. Nothing changes until you confirm."
          ) : plan ? (
            <>
              Undoing <span className="text-ink">“{intentWords(change).replace(/[.]$/, "")}”</span> takes {plan.ops?.length ?? 0}{" "}
              {plan.ops?.length === 1 ? "step" : "steps"} in {plan.project}. Nothing changes until you confirm.
            </>
          ) : (
            "Undo couldn’t be planned."
          )}
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        {phase.k === "loading" && (
          <div className="space-y-2">
            <div className="h-12 animate-pulse rounded-[8px] bg-paper-sunk" />
            <div className="h-12 animate-pulse rounded-[8px] bg-paper-sunk/60" />
          </div>
        )}
        {phase.k === "review" && phase.changed && (
          <p className="mb-3 rounded-[8px] border border-warn/40 bg-warn-wash px-3 py-2 text-[0.875rem] text-ink">
            The project moved while you were reviewing, so this is a fresh plan. Check it again.
          </p>
        )}
        {phase.k === "error" && <UndoProblem error={phase.error} />}
        {plan && (
          <>
            <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.8125rem]">
              <RiskDots tier={tier} />
              <span className="text-ink-3">{tierCopy[tier].blurb}</span>
            </div>
            <Steps
              ops={(plan.ops ?? []).slice().sort((a, b) => (tierRank[b.risk] ?? 4) - (tierRank[a.risk] ?? 4))}
              project={plan.project}
            />
            {asTier(change.plan.risk) === "irreversible" && !serious && (
              <p className="mt-3 text-[0.8125rem] text-ink-3">The original change deleted data. Undo restores the settings; the data itself is gone.</p>
            )}
            <p className="mt-4 flex items-center gap-2 text-[0.8125rem] text-ink-3">
              Undo plan
              <CopyValue value={plan.hash} display={planShort(plan.hash)} />
            </p>
            {serious && phase.k !== "error" && (
              <label className="mt-4 block">
                <span className="text-[0.875rem] text-ink">
                  Type <code className="ident rounded-xs bg-danger-wash px-1 text-danger">{change.project}</code> to confirm.
                </span>
                <Input
                  className="mt-2 font-mono"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={`Type ${change.project} to confirm`}
                />
              </label>
            )}
          </>
        )}
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" onClick={() => onOpenChange(false)}>
          {phase.k === "error" ? "Close" : "Keep it"}
        </Button>
        {phase.k === "error" && !plan && (
          <Button variant="secondary" onClick={fetchPlan}>
            Plan again
          </Button>
        )}
        {(phase.k === "review" || phase.k === "applying") && (
          <Button
            variant={serious ? "danger" : "primary"}
            disabled={!typedOk || phase.k === "applying"}
            onClick={() => confirm(phase.plan)}
            autoFocus={!serious}
          >
            <Undo2 />
            {phase.k === "applying" ? "Undoing…" : serious ? "Destroy and undo" : "Confirm undo"}
          </Button>
        )}
      </DialogFooter>
    </DialogContent>
  );
}

function UndoProblem({ error }: { error: unknown }) {
  if (error instanceof ApiError) {
    const p = error.problem;
    if (p.code === "precondition" || (error.status === 409 && p.code !== "conflict")) {
      return (
        <Callout title="Something changed since, so undo would overwrite newer work">
          <p>{sentence(p.detail ?? "")}</p>
          <p className="mt-1 text-ink-3">Undo the newer entries in the Ledger first, or change the project by hand and apply a new plan.</p>
        </Callout>
      );
    }
    if (error.status === 409) {
      return (
        <Callout title="The project moved on while you were deciding">
          <p className="text-ink-3">Close this and press Undo again to get a fresh plan.</p>
        </Callout>
      );
    }
    if (error.status === 403) {
      return (
        <Callout title="Your session isn’t allowed to apply this undo">
          <p>{sentence(p.detail ?? "")}</p>
          {p.hint && (
            <p className="mt-1 text-ink-3">
              <Code text={sentence(p.hint)} />
            </p>
          )}
        </Callout>
      );
    }
  }
  return <ProblemNote error={error} />;
}

function Callout({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div role="alert" className="mb-4 border-l-2 border-warn py-0.5 pl-4 text-[0.875rem] text-ink-2">
      <p className="font-[550] text-ink">{title}</p>
      <div className="mt-1">{children}</div>
    </div>
  );
}

