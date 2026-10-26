import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { api, ApiError, isProblem, type ApplyResult } from "@/api/client";
import { mod2 } from "@/api/modules";
import { q, queryClient } from "@/api/queries";
import { CodeBox } from "@/components/analytics-kit";
import { CopyText, PrivacyBox } from "@/components/analytics-privacy";
import { StripeCard } from "@/components/analytics-revenue";
import { Breaker } from "@/components/breaker";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  ATTR,
  CONSENT_SNIPPET,
  OFF,
  cookieChoices,
  cookieRank,
  copiedHash,
  fromSpec,
  privacyHash,
  privacyText,
  rememberCopied,
  specOf,
  withPending,
  type Attribution,
  type Cookie,
} from "@/lib/attribution";
import { asTier } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { change, doneWords, intentFor, pendingFor, planEdits, undoChange, usePending, type StagedEdit } from "@/lib/staged";

/**
 * Analytics › Attribution: marketing attribution for one project
 * (services.analytics.attribution; docs/guide/analytics.md).
 *
 * Off, it says what it does and offers Turn on. Turning it on, and setting
 * the first-visit cookie, open a dialog first: what it means in plain words
 * and the privacy text to add, then plan and apply (should the box ever answer
 * reauth_required, the dialog confirms it's the person in place and tries
 * again). The other switches change at
 * once, with Undo. Turning it off deletes what it saved: the plan comes back
 * irreversible and the usual confirm dialog shows the box's reason.
 */

const path = (f?: string) => (f ? [...ATTR, f] : ATTR);
const key = (f?: string) => `set:${path(f).join("/")}`;

export function AttributionView({ project, onRevenue }: { project: string; onRevenue: () => void }) {
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const pending = usePending(project);
  const { can } = useMe();
  const writer = can("apply:reversible");
  const [dialog, setDialog] = useState<null | { kind: "on" } | { kind: "cookie"; to: Cookie }>(null);

  if (m.isError) return <ProblemNote className="mt-6 max-w-[46rem]" error={m.error} title="Couldn’t read the project’s settings" />;
  if (!m.data)
    return (
      <div className="mt-8 max-w-[44rem]" aria-busy aria-label="Loading attribution">
        <Skeleton className="h-8 w-3/4" />
        <Skeleton className="mt-6 h-40" />
      </div>
    );

  const live = fromSpec(specOf(m.data.manifest));
  const a = withPending(m.data.manifest, pending);
  const turningOff = live.on && !!pendingFor(pending, key());

  const close = () => setDialog(null);
  const dialogs = (
    <>
      <TurnOnDialog project={project} open={dialog?.kind === "on"} onClose={close} />
      {dialog?.kind === "cookie" && <CookieDialog project={project} from={live} to={dialog.to} onClose={close} />}
    </>
  );

  if (!live.on) return <Off project={project} writer={writer} onTurnOn={() => setDialog({ kind: "on" })}>{dialogs}</Off>;

  const flip = (f: "campaigns" | "signups", next: "on" | "off", what: [string, string]) =>
    change(project, { kind: "set", path: path(f), from: live[f], to: next === "on", what: next === "on" ? what[0] : what[1], undo: next === "on" ? `${what[1]}, again` : `${what[0]}, again` }, { immediate: true });
  const staged = (f: string) => {
    const e = pendingFor(pending, key(f));
    return e?.kind === "set" ? (e.to ? "on" : "off") : undefined;
  };
  const setCookie = (to: Cookie) => {
    if (to === live.cookie) return;
    // Setting it for more visitors needs a person's yes; for fewer, it changes at once.
    if (cookieRank(to) > cookieRank(live.cookie)) return setDialog({ kind: "cookie", to });
    change(
      project,
      {
        kind: "set",
        path: path("firstVisitCookie"),
        from: live.cookie,
        to,
        what: to === "off" ? `Stop setting the first-visit cookie in ${project}` : `Set the first-visit cookie in ${project} outside the EU only`,
        undo: `The first-visit cookie is set as before`,
      },
      { immediate: true },
    );
  };
  const turnOff = () =>
    change(
      project,
      { kind: "set", path: path(), from: specOf(m.data.manifest), to: undefined, what: `Turn off marketing attribution for ${project}`, undo: "Marketing attribution is on again, with nothing saved" },
      { immediate: true },
    );

  return (
    <div className="mt-8">
      <p className="state-sentence max-w-[40rem] text-ink">Marketing attribution is on.</p>
      <p className="mt-1.5 max-w-[40rem] text-[0.875rem] text-ink-3">Sources and campaigns are kept with each sign-up. Visitors whose browser sends Global Privacy Control are left out.</p>

      <div className="mt-8 grid items-start gap-x-10 gap-y-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-8">
          <section aria-labelledby="attr-settings">
            <h2 id="attr-settings" className="label">
              What it keeps
            </h2>
            <div className="mt-2 divide-y divide-rule border-y border-rule">
              <Setting id="attr-campaigns" title="Campaign tags" line="Keep utm_source, utm_medium, utm_campaign and ref, so every chart can filter by them.">
                <Breaker
                  id="attr-campaigns"
                  label="Campaign tags"
                  state={live.campaigns ? "on" : "off"}
                  staged={staged("campaigns")}
                  disabled={!writer}
                  onFlip={(n) => flip("campaigns", n, [`Keep campaign tags in ${project}’s analytics`, `Stop keeping campaign tags in ${project}’s analytics`])}
                />
              </Setting>
              <Setting id="attr-signups" title="Sign-up sources" line="Each new account keeps where the visit it signed up in came from. Your app reads it with attribution().">
                <Breaker
                  id="attr-signups"
                  label="Sign-up sources"
                  state={live.signups ? "on" : "off"}
                  staged={staged("signups")}
                  disabled={!writer}
                  onFlip={(n) => flip("signups", n, [`Keep each new account’s source in ${project}`, `Stop keeping new accounts’ sources in ${project}`])}
                />
              </Setting>
              <div className="py-4">
                <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
                  <div className="min-w-0 flex-[1_1_16rem]">
                    <h3 className="text-[0.875rem] font-[550] text-ink">First-visit cookie</h3>
                    <p className="mt-0.5 max-w-[30rem] text-[0.8125rem] text-ink-3">Credits a sign-up to the visit that first brought them, up to 90 days earlier. The only setting that stores something in visitors’ browsers.</p>
                  </div>
                </div>
                <fieldset disabled={!writer} className="mt-3">
                  <Segmented label="First-visit cookie" value={a.cookie} onChange={setCookie} options={cookieChoices.map((c) => ({ value: c.value, label: c.label, name: c.label, short: c.value === "outside-eu" ? "Outside EU" : c.value === "ask-eu" ? "Ask EU" : undefined }))} />
                </fieldset>
                <p className="mt-2 text-[0.8125rem] text-ink-2">{cookieChoices.find((c) => c.value === a.cookie)!.line}</p>
                {a.cookie === "ask-eu" && live.cookie === "ask-eu" && (
                  <div className="mt-3">
                    <p className="mb-1.5 text-[0.8125rem] text-ink-2">Add the prompt to your pages. It shows only to visitors who need asking.</p>
                    <CodeBox code={CONSENT_SNIPPET} name="consent prompt" />
                  </div>
                )}
              </div>
            </div>
          </section>

          <StripeCard project={project} onRevenue={onRevenue} />

          <section aria-labelledby="attr-off" className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-[12px] border border-danger-rule px-4 py-4 sm:px-5">
            <div className="min-w-0 flex-[1_1_18rem]">
              <h2 id="attr-off" className="text-[0.875rem] font-[550] text-ink">
                Turn off marketing attribution
              </h2>
              <p className="mt-0.5 max-w-[34rem] text-[0.8125rem] text-ink-3">Deletes the sign-up sources, checkout returns, first visits and payments it saved. Visit counts stay.</p>
            </div>
            {can("apply:irreversible") ? (
              <Button variant="danger-outline" disabled={turningOff} onClick={turnOff}>
                {turningOff ? "Turning off…" : "Turn off"}
              </Button>
            ) : (
              <p className="text-[0.8125rem] text-ink-3">Only an admin can turn it off</p>
            )}
          </section>
        </div>

        <PrivacyPanel project={project} version={m.data.version} />
      </div>
      {dialogs}
    </div>
  );
}

function Setting({ id, title, line, children }: { id: string; title: string; line: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-6 py-4">
      <div className="min-w-0">
        <label htmlFor={id} className="text-[0.875rem] font-[550] text-ink">
          {title}
        </label>
        <p className="mt-0.5 max-w-[30rem] text-[0.8125rem] text-ink-3">{line}</p>
      </div>
      <div className="shrink-0 pt-0.5">{children}</div>
    </div>
  );
}

/** Attribution off: what it does, in a sentence and three facts, and Turn on. */
function Off({ project, writer, onTurnOn, children }: { project: string; writer: boolean; onTurnOn: () => void; children: ReactNode }) {
  const facts = [
    { t: "Campaign tags", d: "utm_source, utm_campaign and ref are kept, so every chart filters by them." },
    { t: "Sign-up sources", d: "Each new account keeps where the visit it signed up in came from." },
    { t: "Paying customers", d: "With Stripe connected, each payment is linked to the source that brought it." },
  ];
  return (
    <div className="mt-8 max-w-[52rem]">
      <p className="state-sentence max-w-[34rem] text-ink">See which sources and campaigns bring sign-ups and paying customers.</p>
      <dl className="mt-7 grid border-y border-rule sm:grid-cols-3">
        {facts.map((f, i) => (
          <div key={f.t} className={cn("py-4 sm:pr-6", i > 0 && "max-sm:border-t max-sm:border-rule sm:border-l sm:border-rule sm:pl-6")}>
            <dt className="text-[0.875rem] font-[550] text-ink">{f.t}</dt>
            <dd className="mt-1 text-[0.8125rem] text-ink-3">{f.d}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-6 flex flex-wrap items-center gap-x-4 gap-y-2">
        {writer ? (
          <Button variant="primary" size="lg" onClick={onTurnOn}>
            Turn on attribution
          </Button>
        ) : (
          <p className="text-[0.875rem] text-ink-2">Your role can’t turn it on. Ask the owner or an admin.</p>
        )}
        <p className="text-[0.8125rem] text-ink-3">Off for {project}. Basic analytics needs no cookies and keeps nothing about people.</p>
      </div>
      {children}
    </div>
  );
}

/** The project's privacy text from the box, with Copy, its version, and a notice when it changed since the last copy. */
function PrivacyPanel({ project, version }: { project: string; version: number }) {
  const p = useQuery({ queryKey: ["analytics-privacy", project, version], queryFn: () => mod2.analyticsPrivacy(project), retry: false, staleTime: 60_000 });
  const [copied, setCopied] = useState(() => copiedHash(project));
  const d = p.data;
  const changed = d && copied !== null && copied !== d.hash;
  return (
    <section aria-labelledby="attr-privacy" className="min-w-0 lg:sticky lg:top-6">
      <div className="flex items-baseline justify-between gap-4">
        <h2 id="attr-privacy" className="label">
          Privacy text
        </h2>
        {d && <span className="text-[0.75rem] leading-[0.875rem] text-ink-3 tnum">Version {d.version}</span>}
      </div>
      {changed ? (
        <div role="status" className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[10px] bg-warn-wash px-3.5 py-2.5">
          <p className="min-w-0 flex-[1_1_16rem] text-[0.875rem] text-ink">
            <b className="font-[550]">Your privacy text changed.</b> <span className="text-ink-2">Copy it again and update your privacy policy.</span>
          </p>
          <CopyText value={d.markdown} label="Copy new text" onCopied={() => (rememberCopied(project, d.hash), setCopied(d.hash))} />
        </div>
      ) : (
        <p className="mt-2 text-[0.875rem] text-ink-2">{copied === null ? "Add this to your privacy policy. It changes when these settings do." : "You copied this version. It changes when these settings do."}</p>
      )}
      {p.isError ? (
        <ProblemNote className="mt-3" error={p.error} title="Couldn’t load the privacy text" />
      ) : !d ? (
        <Skeleton className="mt-3 h-72 rounded-[10px]" />
      ) : (
        <PrivacyBox className="mt-3" markdown={d.markdown} onCopied={() => (rememberCopied(project, d.hash), setCopied(d.hash))} />
      )}
    </section>
  );
}

/**
 * Plan and apply edits that need a person: a dashboard session the box hasn't
 * seen prove itself lately gets reauth_required, so the dialog asks "Confirm
 * it's you" and tries again. A plan that moved on in between is planned again once.
 */
function useApplyAsPerson(project: string, edits: StagedEdit[], onDone: () => void) {
  const [sudo, setSudo] = useState(false);
  const run = useMutation({
    mutationFn: async (): Promise<ApplyResult> => {
      for (let i = 0; ; i++) {
        try {
          const { desired, plan } = await planEdits(project, edits);
          return await api.apply(desired, plan.hash, intentFor(edits, project));
        } catch (e) {
          if (i === 0 && e instanceof ApiError && e.status === 428) continue;
          throw e;
        }
      }
    },
    onSuccess: async (r) => {
      await Promise.all([["manifest", project], ["project", project], ["changes"], ["analytics-privacy", project]].map((k) => queryClient.invalidateQueries({ queryKey: k })));
      onDone();
      const id = r.change?.id;
      toast({
        title: r.applied ? doneWords(edits, project) : "Nothing to change: it already looks like that.",
        action: id && r.change && asTier(r.change.plan.risk) !== "irreversible" ? { label: "Undo", run: () => undoChange(id) } : undefined,
      });
    },
    onError: (e) => isProblem(e, "reauth_required") && setSudo(true),
  });
  const failed = run.isError && !isProblem(run.error, "reauth_required") ? run.error : null;
  return { run, sudo, back: () => (setSudo(false), run.reset()), failed };
}

/** A point in a confirm dialog: a short bold lead and its plain sentence. */
function Point({ lead, children }: { lead: string; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[0.75rem_minmax(0,1fr)] gap-x-2 py-1.5 text-[0.9375rem] leading-[1.375rem] text-ink-2">
      <span aria-hidden className="mt-[0.5625rem] size-[5px] rounded-full bg-ink-4" />
      <span>
        <b className="font-[550] text-ink">{lead}</b> {children}
      </span>
    </li>
  );
}

const back = (project: string) => `/projects/${encodeURIComponent(project)}/analytics?tab=attribution`;

function TurnOnDialog({ project, open, onClose }: { project: string; open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[40rem]">{open && <TurnOnFlow project={project} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

const ON: Attribution = { on: true, campaigns: true, signups: true, cookie: "off" };

function TurnOnFlow({ project, onClose }: { project: string; onClose: () => void }) {
  const [edits] = useState<StagedEdit[]>(() => [
    { kind: "set", path: ATTR, from: undefined, to: {}, what: `Turn on marketing attribution for ${project}`, undo: "Marketing attribution is off again, and what it saved is deleted" },
  ]);
  const { run, sudo, back: stepBack, failed } = useApplyAsPerson(project, edits, onClose);
  const text = privacyText(ON);
  if (sudo)
    return (
      <ConfirmItsYou
        why="Marketing attribution links visits to people, so the box asks that it’s really you before turning it on."
        back={back(project)}
        working={run.isPending}
        workingLabel="Turning on…"
        onBack={stepBack}
        onConfirmed={() => run.mutate()}
      />
    );
  return (
    <>
      <DialogHeader>
        <DialogTitle>Turn on marketing attribution?</DialogTitle>
        <DialogDescription>See which sources and campaigns bring sign-ups and paying customers to {project}.</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <ul>
          <Point lead="Sign-ups are linked to their source.">Where each new account came from is saved with it. That is personal data about your customers.</Point>
          <Point lead="Your privacy policy has to say so.">Add the text below to it before you turn this on.</Point>
          <Point lead="Global Privacy Control is respected.">Visitors whose browser sends it are left out.</Point>
        </ul>
        <PrivacyBox className="mt-4" markdown={text} before={privacyText(OFF)} maxHeight="15rem" onCopied={() => void privacyHash(text).then((h) => rememberCopied(project, h))} />
        {failed && <ProblemNote className="mt-4" error={failed} title="Couldn’t turn it on" />}
      </DialogBody>
      <DialogFooter>
        <span className="mr-auto text-[0.8125rem] leading-5 text-ink-3 max-sm:text-center">Turning it off later deletes what it saved.</span>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="primary" disabled={run.isPending} onClick={() => run.mutate()}>
          {run.isPending ? "Turning on…" : "Turn on"}
        </Button>
      </DialogFooter>
    </>
  );
}

/** Setting the first-visit cookie for more visitors: what it stores, the prompt for ask-eu, the new privacy text. */
function CookieDialog({ project, from, to, onClose }: { project: string; from: Attribution; to: Cookie; onClose: () => void }) {
  const ask = to === "ask-eu";
  const [edits] = useState<StagedEdit[]>(() => [
    {
      kind: "set",
      path: path("firstVisitCookie"),
      from: from.cookie,
      to,
      what: ask ? `Set the first-visit cookie in ${project}, asking EU visitors first` : `Set the first-visit cookie in ${project} outside the EU`,
      undo: from.cookie === "off" ? "The first-visit cookie is no longer set" : "The first-visit cookie is set as before",
    },
  ]);
  const { run, sudo, back: stepBack, failed } = useApplyAsPerson(project, edits, onClose);
  const text = privacyText({ ...from, cookie: to });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[40rem]">
        {sudo ? (
          <ConfirmItsYou
            why="The cookie is stored in visitors’ browsers, so the box asks that it’s really you before setting it."
            back={back(project)}
            working={run.isPending}
            workingLabel="Setting it…"
            onBack={stepBack}
            onConfirmed={() => run.mutate()}
          />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{ask ? "Ask EU visitors for the first-visit cookie?" : "Set the first-visit cookie?"}</DialogTitle>
              <DialogDescription>A sign-up is then credited to the visit that first brought them, up to 90 days earlier.</DialogDescription>
            </DialogHeader>
            <DialogBody>
              <ul>
                <Point lead="It stores something in visitors’ browsers.">
                  One first-party cookie, <code className="font-mono text-[0.8125rem] text-ink">tiffin_first_visit</code>, holding a random code for 90 days.
                </Point>
                {ask ? (
                  <Point lead="Visitors in the EU, EEA or UK are asked first.">They get it only after they agree. Add the prompt to your pages:</Point>
                ) : (
                  <Point lead="Never for visitors in the EU, EEA or UK.">Nor where their country isn’t known. No banner is needed.</Point>
                )}
              </ul>
              {ask && <CodeBox className="mt-2 mb-1 ml-5" code={CONSENT_SNIPPET} name="consent prompt" />}
              <ul>
                <Point lead="Your privacy text changes.">It now lists the cookie and an opt-out link. Copy the new text:</Point>
              </ul>
              <PrivacyBox className="mt-2" markdown={text} before={privacyText(from)} maxHeight="13rem" onCopied={() => void privacyHash(text).then((h) => rememberCopied(project, h))} />
              {failed && <ProblemNote className="mt-4" error={failed} title="Couldn’t set the cookie" />}
            </DialogBody>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button variant="primary" disabled={run.isPending} onClick={() => run.mutate()}>
                {run.isPending ? "Setting it…" : ask ? "Ask EU visitors" : "Set the cookie"}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
