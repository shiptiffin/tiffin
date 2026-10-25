import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Check } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { isProblem } from "@/api/client";
import { q } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { shortDate } from "@/components/domains-parts";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { addDomain, buyDomain, price, purchaseQuery, quoteOf, splitName, type Offer, type Purchase, type Quote } from "@/lib/domains";

/** The name with its ending set apart: acme.app. */
export function Name({ domain, className }: { domain: string; className?: string }) {
  const [label, ext] = splitName(domain);
  return (
    <span className={cn("min-w-0 truncate", className)}>
      {label}
      <span className="text-ink-3">{ext}</span>
    </span>
  );
}

const NONE = "-";

/** A project's web apps, "web" first: the app a domain shows. */
function useWebApps(project: string) {
  const m = useQuery({ ...q.manifest(project), enabled: !!project && project !== NONE, staleTime: 30_000 });
  return useMemo(
    () =>
      Object.entries(m.data?.manifest.apps ?? {})
        .filter(([, a]) => a.role !== "worker")
        .map(([n]) => n)
        .sort((a, b) => (a === "web" ? -1 : b === "web" ? 1 : a.localeCompare(b))),
    [m.data],
  );
}

/** Project (and app, when it has several) to put a domain on. */
function ProjectPick({ project, setProject, app, setApp, optional }: { project: string; setProject: (p: string) => void; app: string; setApp: (a: string) => void; optional?: boolean }) {
  const projects = useQuery(q.projects);
  const apps = useWebApps(project);
  useEffect(() => {
    if (project !== NONE && apps.length && !apps.includes(app)) setApp(apps[0]);
  }, [apps, app, project, setApp]);
  const options = [
    ...(optional ? [{ value: NONE, label: "Not now" }] : []),
    ...(projects.data ?? []).map((p) => ({ value: p.name, label: p.name })),
  ];
  return (
    <div className="flex flex-wrap gap-2">
      <div className="w-[13rem] max-sm:w-full">
        <Select aria-label="Project" value={project} onValueChange={setProject} options={options} placeholder="Choose a project" />
      </div>
      {project !== NONE && apps.length > 1 && (
        <div className="w-[10rem] max-sm:w-full">
          <Select aria-label="App" value={app} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} />
        </div>
      )}
    </div>
  );
}

/** One fact in the buy sheet: what on the left, the value on the right. */
function Fact({ label, children, note }: { label: ReactNode; children: ReactNode; note?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 py-3">
      <div className="min-w-0">
        <p className="text-[0.875rem] text-ink">{label}</p>
        {note && <p className="mt-0.5 text-[0.8125rem] leading-5 text-ink-3">{note}</p>}
      </div>
      <div className="shrink-0 text-right text-[0.875rem] text-ink tnum">{children}</div>
    </div>
  );
}

type Step = "review" | "sudo" | "progress";

/**
 * Buy a domain: the facts (first year, renewal, auto-renew, privacy), where
 * it goes, one primary button with the price on it. Then the same sheet
 * follows the purchase: registered, DNS ready, added to the project.
 */
export function BuyDialog({ offer, project: wanted, onClose }: { offer: Offer | null; project?: string; onClose: () => void }) {
  return (
    <Dialog open={!!offer} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[34rem]">{offer && <BuyFlow key={offer.domain} offer={offer} wanted={wanted} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function BuyFlow({ offer, wanted, onClose }: { offer: Offer; wanted?: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>("review");
  const [autoRenew, setAutoRenew] = useState(true);
  const [project, setProject] = useState(wanted || NONE);
  const [app, setApp] = useState("web");
  const [quote, setQuote] = useState<Quote>({ domain: offer.domain, years: 1, firstYear: offer.firstYear ?? "", renewal: offer.renewal ?? "", total: offer.firstYear ?? "", currency: offer.currency ?? "USD" });
  const [changed, setChanged] = useState(false);
  const cur = quote.currency;
  const buy = useMutation({
    mutationFn: () => buyDomain({ domain: offer.domain, autoRenew, price: quote.total, ...(project !== NONE ? { project, app } : {}) }),
    onSuccess: (p) => {
      qc.setQueryData(purchaseQuery(offer.domain).queryKey, p);
      void qc.invalidateQueries({ queryKey: ["domains-owned"] });
      void qc.invalidateQueries({ queryKey: ["domain-search"] });
      setStep("progress");
    },
    onError: (e) => {
      const nq = quoteOf(e);
      if (nq) {
        setChanged(nq.total !== quote.total);
        setQuote(nq);
      } else if (isProblem(e, "reauth_required")) setStep("sudo");
    },
  });

  if (step === "sudo")
    return (
      <ConfirmItsYou
        why={`Buying ${offer.domain} charges the card on your Cloudflare account, so the box asks it’s really you.`}
        back="/domains"
        working={buy.isPending}
        workingLabel="Buying…"
        onBack={() => (setStep("review"), buy.reset())}
        onConfirmed={() => buy.mutate()}
      />
    );
  if (step === "progress") return <Progress domain={offer.domain} project={project !== NONE ? project : ""} app={app} onClose={onClose} />;

  const quoteError = buy.isError && !quoteOf(buy.error) && !isProblem(buy.error, "reauth_required");
  return (
    <>
      <DialogHeader>
        <DialogTitle className="ident text-[1.375rem] leading-8 tracking-[-0.01em]">
          <Name domain={offer.domain} />
        </DialogTitle>
        <DialogDescription>Registered with Cloudflare, in your own account, at cost.</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <div className="divide-y divide-rule border-t border-rule">
          <Fact label="First year">{price(quote.firstYear, cur)}</Fact>
          <Fact label="Then">{price(quote.renewal, cur)} a year</Fact>
          <Fact
            label={<label htmlFor="buy-renew">Renew automatically</label>}
            note={autoRenew ? "Cloudflare charges the renewal 30 days before it expires." : "You renew it yourself in Cloudflare before it expires, or lose it."}
          >
            <Breaker id="buy-renew" label="Renew automatically" state={autoRenew ? "on" : "off"} onFlip={(n) => setAutoRenew(n === "on")} />
          </Fact>
          <Fact label="Your contact details" note="The default contact on your Cloudflare account.">
            <span className="text-ink-2">Hidden from WHOIS</span>
          </Fact>
          <div className="py-3">
            <p className="text-[0.875rem] text-ink">Use it on</p>
            <p className="mt-0.5 mb-2.5 text-[0.8125rem] text-ink-3">The box sets its DNS records and HTTPS once it’s registered.</p>
            <ProjectPick project={project} setProject={setProject} app={app} setApp={setApp} optional />
          </div>
        </div>
        {changed && (
          <p className="mt-3 rounded-[8px] bg-warn-wash px-3 py-2 text-[0.8125rem] text-warn-ink">
            The price just changed to {price(quote.total, cur)}. Nothing was charged; buy again at the new price if it’s still fine.
          </p>
        )}
        {quoteError && <ProblemNote className="mt-3" error={buy.error} />}
      </DialogBody>
      <DialogFooter>
        <span className="mr-auto text-[0.8125rem] leading-5 text-ink-3 max-sm:text-center">Charged to the card on your Cloudflare account. Domains can’t be refunded.</span>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="primary" disabled={buy.isPending || !quote.total} onClick={() => buy.mutate()} autoFocus>
          {buy.isPending ? "Buying…" : `Buy for ${price(quote.total, cur)}`}
        </Button>
      </DialogFooter>
    </>
  );
}

/** A step in the purchase: done, on its way, or still to come. */
function Tick({ state, children, detail }: { state: "done" | "now" | "todo" | "bad"; children: ReactNode; detail?: ReactNode }) {
  return (
    <li className="flex gap-3 py-2.5">
      <span
        className={cn(
          "mt-0.5 grid size-5 shrink-0 place-items-center rounded-full",
          state === "done" && "bg-ink text-paper",
          state === "now" && "border border-rule-3",
          state === "todo" && "border border-dashed border-rule-3",
          state === "bad" && "bg-danger text-paper",
        )}
      >
        {state === "done" && <Check className="size-3" strokeWidth={3} />}
        {state === "now" && <span className="spinner size-3" />}
        {state === "bad" && <span className="text-[0.6875rem] leading-none font-[700]">!</span>}
      </span>
      <div className="min-w-0">
        <p className={cn("text-[0.875rem]", state === "todo" ? "text-ink-3" : "text-ink")}>{children}</p>
        {detail && <p className="mt-0.5 text-[0.8125rem] leading-5 text-ink-2">{detail}</p>}
      </div>
    </li>
  );
}

/** Follows a purchase to the end, then puts the domain on the project (records and HTTPS by the box). */
function Progress({ domain, project, app, onClose }: { domain: string; project: string; app: string; onClose: () => void }) {
  const qc = useQueryClient();
  const p = useQuery(purchaseQuery(domain));
  const st = p.data?.state ?? "registering";
  const attach = useMutation({
    mutationFn: () => addDomain(project, { domain, app, www: true, createRecords: true }),
    onSuccess: () => {
      for (const k of [["domains", project], ["manifest", project], ["project", project], ["changes"], ["domains-owned"]]) void qc.invalidateQueries({ queryKey: k });
    },
  });
  const started = useRef(false);
  useEffect(() => {
    if (st === "ready" && project && !p.data?.sandbox && !started.current) {
      started.current = true;
      attach.mutate();
    }
  }, [st, project, p.data?.sandbox, attach]);

  const registered = ["setting_up_dns", "ready", "action_required"].includes(st);
  const failed = st === "failed";
  const done = st === "ready" && (!project || p.data?.sandbox || attach.isSuccess);
  const regState = failed ? "bad" : registered ? "done" : "now";
  const dnsState = st === "ready" ? "done" : st === "setting_up_dns" ? "now" : "todo";
  const projState = attach.isSuccess ? "done" : attach.isError ? "bad" : attach.isPending ? "now" : "todo";

  return (
    <>
      <DialogHeader>
        <DialogTitle>{failed ? `${domain} wasn’t registered` : done ? `${domain} is yours` : `Buying ${domain}`}</DialogTitle>
        <DialogDescription>
          {done && p.data?.expiresAt && !p.data.sandbox
            ? `Registered in your Cloudflare account until ${shortDate(p.data.expiresAt)}${p.data.autoRenew ? ", and it renews itself" : ""}.`
            : (p.data?.detail ?? "Cloudflare is registering it. This usually takes under a minute.")}
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        <ol className="divide-y divide-rule border-y border-rule">
          <Tick state={regState} detail={st === "action_required" && p.data?.sentTo ? `Confirm the email sent to ${p.data.sentTo}.` : undefined}>
            {failed ? "Not registered" : registered ? "Registered with Cloudflare" : "Registering with Cloudflare"}
          </Tick>
          {!p.data?.sandbox && <Tick state={failed ? "todo" : dnsState}>{st === "ready" ? "DNS is ready" : "Setting up its DNS"}</Tick>}
          {project && !p.data?.sandbox && (
            <Tick state={failed ? "todo" : projState} detail={attach.isError ? <ProblemNote error={attach.error} /> : attach.isSuccess ? "The box set its records; HTTPS follows in a minute." : undefined}>
              {attach.isSuccess ? `Added to ${project}` : `Add to ${project}`}
            </Tick>
          )}
        </ol>
        {p.isError && <ProblemNote className="mt-3" error={p.error} />}
      </DialogBody>
      <DialogFooter>
        {!done && !failed && <span className="mr-auto text-[0.8125rem] text-ink-3 max-sm:text-center">You can close this. The box keeps going.</span>}
        {done && project && attach.isSuccess ? (
          <>
            <Button variant="ghost" onClick={onClose}>
              Done
            </Button>
            <Button variant="primary" asChild>
              <Link to="/projects/$project/domains" params={{ project }} search={{ domain }} onClick={onClose}>
                Open {project}’s domains
              </Link>
            </Button>
          </>
        ) : (
          <Button variant={done ? "primary" : "secondary"} onClick={onClose} autoFocus>
            {done ? "Done" : "Close"}
          </Button>
        )}
      </DialogFooter>
    </>
  );
}

/** Put a domain you own on a project: one change, records set by the box. */
export function UseDialog({ domain, onClose }: { domain: string | null; onClose: () => void }) {
  return (
    <Dialog open={!!domain} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[30rem]">{domain && <UseFlow key={domain} domain={domain} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function UseFlow({ domain, onClose }: { domain: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [project, setProject] = useState(NONE);
  const [app, setApp] = useState("web");
  const add = useMutation({
    mutationFn: () => addDomain(project, { domain, app, www: true, createRecords: true }),
    onSuccess: () => {
      for (const k of [["domains", project], ["manifest", project], ["project", project], ["changes"], ["domains-owned"]]) void qc.invalidateQueries({ queryKey: k });
    },
  });
  if (add.isSuccess)
    return (
      <>
        <DialogHeader>
          <DialogTitle>Added {domain} to {project}</DialogTitle>
          <DialogDescription>The box set its DNS records. HTTPS follows by itself, usually within a minute.</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Done
          </Button>
          <Button variant="primary" asChild>
            <Link to="/projects/$project/domains" params={{ project }} search={{ domain }} onClick={onClose}>
              Open {project}’s domains
            </Link>
          </Button>
        </DialogFooter>
      </>
    );
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          Use <Name domain={domain} className="ident" /> on a project
        </DialogTitle>
        <DialogDescription>It shows the project’s web app, with www redirecting to it. The box sets the records.</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <ProjectPick project={project} setProject={setProject} app={app} setApp={setApp} />
        {add.isError && <ProblemNote className="mt-3" error={add.error} />}
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="primary" disabled={project === NONE || add.isPending} onClick={() => add.mutate()}>
          {add.isPending ? "Adding…" : project === NONE ? "Add to project" : `Add to ${project}`}
        </Button>
      </DialogFooter>
    </>
  );
}

/** Cloudflare's dashboard, for names its API doesn't sell yet. */
export function OutLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className="group inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 hover:text-ink">
      {children}
      <ArrowUpRight className="size-3.5 text-ink-3 group-hover:text-ink" />
    </a>
  );
}

export type { Purchase };
