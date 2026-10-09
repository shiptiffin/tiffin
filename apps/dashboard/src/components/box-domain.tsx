import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Check } from "lucide-react";
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Confirm } from "@/components/confirm";
import { HostHints, RecordsTable, Watching } from "@/components/dns-records";
import { ProblemNote } from "@/components/problem";
import { StatusDot } from "@/components/project-domains";
import { Skeleton } from "@/components/page";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { Input } from "@/components/ui/input";
import {
  boxDomainQuery,
  checkBoxDomain,
  cleanDomain,
  looksLikeDomain,
  providerName,
  reasonWords,
  setBoxDomain,
  setRecords,
  unsetBoxDomain,
  type BoxDomain,
  type Tone,
} from "@/lib/domains";
import { clock } from "@/lib/time";

/**
 * Settings › Your box › Domain: the address the box answers on (the automatic
 * sslip.io one until you set your own), using your own domain (two records,
 * a check, then the switch: the dashboard moves and this page follows), and
 * going back to the automatic address.
 */
export function BoxDomainSection({ admin, Wrap }: { admin: boolean; Wrap: (p: { id?: string; title: string; note?: string; children: ReactNode }) => ReactNode }) {
  const bd = useQuery(boxDomainQuery);
  const [setting, setSetting] = useState(false);
  const [moving, setMoving] = useState<BoxDomain | null>(null);
  const [back, setBack] = useState(false);
  // An older box or a laptop dev server has no domain module: say nothing.
  if (bd.isError && bd.error instanceof ApiError && [404, 412, 501].includes(bd.error.status)) return null;
  const b = bd.data;
  const local = b?.certificates === "internal";

  return (
    <Wrap id="domain" title="Domain" note="The address your box, its dashboard and its apps answer on.">
      {bd.isError ? (
        <ProblemNote error={bd.error} />
      ) : !b ? (
        <Skeleton className="h-20" />
      ) : moving ? (
        <Moving to={moving} />
      ) : (
        <>
          <Current b={b} />
          {admin && !local && !setting && (
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <Button size="md" onClick={() => setSetting(true)}>
                {b.source === "set" ? "Use a different domain" : "Use your own domain"}
              </Button>
              {b.source === "set" && (
                <Button variant="ghost" size="md" onClick={() => setBack(true)}>
                  Go back to the automatic address
                </Button>
              )}
            </div>
          )}
          {setting && <UseOwn current={b} onCancel={() => setSetting(false)} onMoving={setMoving} />}
          <Confirm
            open={back}
            onClose={() => setBack(false)}
            tone="normal"
            title={`Go back to ${b.default}?`}
            body={
              <>
                The dashboard moves to dashboard.{b.default} and apps to names like web.{b.default}. The box restarts for a few seconds (apps keep running), and this page reloads on the new address. {b.domain} keeps working for a while.
              </>
            }
            action={`Go back to ${b.default}`}
            run={async () => setMoving(await unsetBoxDomain())}
            done={() => undefined}
          />
        </>
      )}
    </Wrap>
  );
}

function boxTone(b: BoxDomain): Tone {
  return b.state === "live" || b.state === "internal" ? "ok" : b.state === "error" ? "bad" : "busy";
}

/** Apps live on a domain of their own (not the dashboard's). */
const appsApart = (b: BoxDomain) => !!b.appsDomain && b.appsDomain !== b.domain;

function Current({ b }: { b: BoxDomain }) {
  const local = b.certificates === "internal";
  const missing = (b.records ?? []).filter((r) => !r.ok);
  const apart = appsApart(b);
  return (
    <div>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <p className="ident text-[1rem] text-ink">{b.domain}</p>
        <span className="text-[0.8125rem] text-ink-3">{local ? "On this computer" : b.source === "set" ? "Your own domain" : "Automatic"}</span>
      </div>
      {apart && (
        <div className="mt-0.5 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <p className="ident text-[1rem] text-ink">{b.appsDomain}</p>
          <span className="text-[0.8125rem] text-ink-3">Apps</span>
        </div>
      )}
      <p className="mt-1 max-w-[36rem] text-[0.875rem] text-ink-2">
        {local
          ? `A box on your computer answers on names under ${b.domain}, with its own certificates your browser trusts. Your own domain needs a box on a server.`
          : apart
            ? `The dashboard is at ${b.dashboard}. Apps answer at names under ${b.appsDomain} made from their project, like shop.${b.appsDomain}: a domain of their own, so app code can’t set cookies on the dashboard’s.`
            : b.source === "set"
              ? `Apps answer at names under it made from their project, like shop.${b.domain}; the dashboard is at ${b.dashboard}.`
              : "The address your box uses until you set your own. It works with HTTPS already, with nothing to set up."}
      </p>
      {!local && (
        <ul className="mt-3 divide-y divide-rule border-y border-rule text-[0.8125rem]">
          <li className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 py-2">
            <StatusDot tone={boxTone(b)} />
            <a href={b.dashboardUrl} className="ident text-[0.75rem] text-ink hover:text-brass-ink">
              {b.dashboard}
            </a>
            <span className="text-ink-3">
              {b.state === "live" ? "· Live with HTTPS" : b.state === "error" ? `· Problem: ${reasonWords(b.dashboardCertificate.error ?? "the certificate failed").replace(/^./, (c) => c.toLowerCase())}` : "· Getting a certificate…"}
            </span>
          </li>
          {(b.bare ?? []).map((d) => (
            <li key={d.host} className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 py-2">
              <StatusDot tone={d.project ? "ok" : "wait"} />
              <span className="ident text-[0.75rem] text-ink">{d.host}</span>
              {d.project ? (
                <span className="text-ink-3">
                  · Served by{" "}
                  <Link to="/projects/$project" params={{ project: d.project }} className="text-ink-2 hover:text-brass-ink">
                    {d.project}
                  </Link>
                </span>
              ) : (
                <span className="text-ink-3">· Sends visitors to {d.redirectsTo === `${b.dashboardUrl}/` ? "your dashboard" : d.redirectsTo?.replace(/^https:\/\/|\/$/g, "")} until an app uses it</span>
              )}
            </li>
          ))}
          {b.wildcard && (
            <li className="flex items-center gap-2.5 py-2">
              <StatusDot tone={b.wildcard.certificate.state === "live" ? "ok" : b.wildcard.certificate.state === "error" ? "bad" : "busy"} />
              <span className="text-ink-2">One certificate covers every app and preview, through {providerName(b.wildcard.provider)}.</span>
            </li>
          )}
          {b.previous && (
            <li className="py-2 text-ink-3">
              {b.previous.appsDomain ? (
                <>
                  The old addresses, <span className="ident text-[0.75rem]">{b.previous.domain}</span> and <span className="ident text-[0.75rem]">{b.previous.appsDomain}</span>, keep working
                </>
              ) : (
                <>
                  The old address, <span className="ident text-[0.75rem]">{b.previous.domain}</span>, keeps working
                </>
              )}{" "}
              {b.previous.until ? `until ${clock(b.previous.until)}` : "until the new one has its certificates"}.
            </li>
          )}
        </ul>
      )}
      {missing.length > 0 && (
        <div className="mt-3 rounded-[10px] bg-warn-wash px-4 py-3">
          <p className="text-[0.875rem] text-ink">DNS for {apart ? `${b.domain} and ${b.appsDomain}` : b.domain} doesn’t point here any more. Put these records back at your DNS host:</p>
          <RecordsTable className="mt-2.5" records={b.records ?? []} />
        </div>
      )}
    </div>
  );
}

/** Use your own domain: which one (and, optionally, one for the apps), its two records (checked every few seconds), then Switch. */
function UseOwn({ current, onCancel, onMoving }: { current: BoxDomain; onCancel: () => void; onMoving: (b: BoxDomain) => void }) {
  const qc = useQueryClient();
  const currentApps = appsApart(current) ? current.appsDomain : "";
  const [raw, setRaw] = useState("");
  const [rawApps, setRawApps] = useState(currentApps);
  const [apart, setApart] = useState(!!currentApps);
  const [want, setWant] = useState({ domain: "", apps: "" });
  const { domain, apps } = want;
  const check = useQuery({
    queryKey: ["box-domain-check", domain, apps],
    queryFn: () => checkBoxDomain(domain, apps),
    enabled: !!domain,
    retry: false,
    refetchInterval: (qq) => (qq.state.data?.ok || qq.state.error ? false : 10_000),
  });
  const c = check.data;
  const mine = (c?.records ?? []).filter((r) => r.managedBy);
  const provider = mine[0]?.managedBy;
  const auto = useMutation({
    mutationFn: () => setRecords(mine),
    onSuccess: () => {
      toast({ title: `Added the records ${providerName(provider)} looks after.`, detail: "The check below turns green within a minute or so." });
      setTimeout(() => void check.refetch(), 3000);
    },
  });
  const sw = useMutation({
    mutationFn: () => setBoxDomain(domain, apps),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["box-domain"] });
      onMoving(r);
    },
  });
  const typed = cleanDomain(raw);
  const typedApps = apart ? cleanDomain(rawApps) : "";
  const valid = looksLikeDomain(typed) && (!apart || (looksLikeDomain(typedApps) && typedApps !== typed));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (valid) setWant({ domain: typed, apps: typedApps });
  };
  const dash = `dashboard.${domain}`;
  const appsAt = apps || domain;

  return (
    <div className="mt-5 rounded-[12px] border border-rule-2 bg-paper-raised px-4 py-4 shadow-[var(--top-light)] sm:px-5">
      <form onSubmit={submit}>
        <label htmlFor="box-domain" className="text-[0.9375rem] font-[550] text-ink">
          Your own domain
        </label>
        <p className="mt-0.5 text-[0.8125rem] text-ink-3">The box takes the domain and everything under it. To keep example.com for something else, use a name like apps.example.com.</p>
        <div className="mt-3 flex flex-col gap-2 sm:flex-row">
          <Input
            id="box-domain"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder="example.com"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            className="ident text-[0.875rem] sm:flex-1"
          />
          <Button
            type="submit"
            size="lg"
            className="h-9"
            variant={domain ? "secondary" : "primary"}
            disabled={!valid || (typed === current.domain && typedApps === currentApps) || (typed === domain && typedApps === apps && check.isFetching)}
          >
            {typed === domain && typedApps === apps && check.isFetching ? "Checking…" : "Check"}
          </Button>
          <Button type="button" variant="ghost" size="lg" className="h-9" onClick={onCancel}>
            Cancel
          </Button>
        </div>
        <label className="mt-3 flex cursor-pointer items-start gap-3">
          <Checkbox className="mt-0.5" checked={apart} onCheckedChange={(v) => setApart(v === true)} />
          <span className="text-[0.875rem] text-ink">
            Put apps on a domain of their own
            <span className="block text-[0.8125rem] text-ink-3">Like example.app beside example.com: the dashboard stays on your domain, and app code can’t set cookies on it.</span>
          </span>
        </label>
        {apart && (
          <Input
            aria-label="Apps domain"
            value={rawApps}
            onChange={(e) => setRawApps(e.target.value)}
            placeholder="example.app"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            className="ident mt-2 text-[0.875rem] sm:max-w-[20rem]"
          />
        )}
      </form>

      {check.isError && <ProblemNote className="mt-4" error={check.error} />}
      {c && c.domain === domain && (c.appsDomain ?? "") === apps && (
        <div className="mt-5 border-t border-rule pt-4">
          {c.ok ? (
            <>
              <p className="flex items-center gap-2 text-[0.9375rem] font-[550] text-ink">
                <Check className="size-4 text-ok" strokeWidth={2.5} />
                {apps ? `${domain} and ${apps} point at your box.` : `${domain} points at your box.`}
              </p>
              <ul className="mt-2 max-w-[38rem] list-disc space-y-1 pl-5 text-[0.875rem] text-ink-2 marker:text-ink-4">
                <li>
                  The dashboard moves to <span className="ident text-[0.8125rem] text-ink">{dash}</span>, and apps to names like <span className="ident text-[0.8125rem] text-ink">web.{appsAt}</span>.
                </li>
                <li>The box restarts for a few seconds; apps keep running. This page reloads on the new address by itself.</li>
                <li>Passkey sign-ins (Touch ID, Windows Hello…) belong to this address. Set them up again there; until then, sign in with a link from <code className="ident text-[0.8125rem] text-ink">tiffin login</code>.</li>
                <li>{current.domain} keeps working until the new names have their certificates, then for another hour.</li>
              </ul>
              <Button variant="primary" size="lg" className="mt-4" onClick={() => sw.mutate()} disabled={sw.isPending}>
                {sw.isPending ? "Switching…" : `Switch to ${domain}`}
              </Button>
            </>
          ) : (
            <>
              <p className="text-[0.875rem] text-ink">
                {c.managedBy
                  ? `${providerName(c.managedBy)} looks after ${apps ? `${domain} and ${apps}` : domain}, so the box can add the records itself.`
                  : provider
                    ? `${providerName(provider)} looks after some of these, so the box can add those; add the others at your DNS host:`
                    : `Add ${(c.records ?? []).length === 2 ? "these two records" : "these records"} at your DNS host:`}
              </p>
              {provider && (
                <Button variant="primary" size="md" className="mt-3" onClick={() => auto.mutate()} disabled={auto.isPending}>
                  {auto.isPending ? "Adding…" : c.managedBy ? "Add the records for me" : `Add the ${providerName(provider)} ones for me`}
                </Button>
              )}
              <RecordsTable className="mt-3" records={c.records ?? []} />
              {!c.managedBy && <HostHints className="mt-2.5" />}
              {c.caa && <p className="mt-3 text-[0.8125rem] text-danger">{reasonWords(c.caa)}</p>}
              {auto.isError && <ProblemNote className="mt-3" error={auto.error} />}
              <div className="mt-3 border-t border-rule pt-3">
                <Watching checking={check.isFetching} onCheck={() => void check.refetch()}>
                  Checking every few seconds. New records usually show up within minutes.
                </Watching>
              </div>
            </>
          )}
          {sw.isError && <ProblemNote className="mt-4" error={sw.error} />}
        </div>
      )}
    </div>
  );
}

/**
 * After a switch: the box restarts and gets its certificate. Try the new
 * address every few seconds (a no-cors fetch only succeeds once its HTTPS
 * works), then go there.
 */
function Moving({ to }: { to: BoxDomain }) {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    let stop = false;
    const started = Date.now();
    const go = async () => {
      while (!stop) {
        await new Promise((r) => setTimeout(r, 3000));
        try {
          await fetch(`${to.dashboardUrl}/v1/health`, { mode: "no-cors", cache: "no-store" });
          if (!stop) location.assign(`${to.dashboardUrl}/login`);
          return;
        } catch {
          if (Date.now() - started > 120_000) setSlow(true);
        }
      }
    };
    void go();
    return () => {
      stop = true;
    };
  }, [to.dashboardUrl]);
  return (
    <div className="rounded-[12px] border border-rule-2 bg-paper-raised px-5 py-4 shadow-[var(--top-light)]" role="status">
      <p className="flex items-center gap-2.5 text-[0.9375rem] font-[550] text-ink">
        <span className="spinner text-brass" aria-hidden />
        Moving your box to {to.domain}…
      </p>
      <p className="mt-1.5 max-w-[36rem] text-[0.875rem] text-ink-2">
        It restarts for a few seconds, then gets a certificate for <span className="ident text-[0.8125rem] text-ink">{to.dashboard}</span>, usually within a minute. This page goes there by itself; sign in again there.
      </p>
      {slow && (
        <p className="mt-3 text-[0.875rem] text-ink-2">
          Taking longer than usual.{" "}
          <a href={to.dashboardUrl} className="inline-flex items-center gap-0.5 font-[550] text-brass-ink hover:text-ink">
            Open {to.dashboard}
            <ArrowUpRight className="size-3.5" />
          </a>{" "}
          or check <Link to="/status" className="font-[550] text-ink underline decoration-rule-3 underline-offset-4">Health</Link>.
        </p>
      )}
    </div>
  );
}
