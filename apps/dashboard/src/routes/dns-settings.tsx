import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, Cloud, TriangleAlert } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { accessCrumbs } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { boxDomainQuery, CLOUDFLARE_TOKENS, connectDns, disconnectDns, dnsProvidersQuery, type ConnectedProvider, type Providers } from "@/lib/domains";
import { useWho } from "@/lib/me";
import { relative } from "@/lib/time";
import { countWords } from "@/lib/format";

/**
 * Settings › DNS: connect Cloudflare so the box adds DNS records itself
 * (one-click domains) and holds one certificate for every app (instant HTTPS
 * for new apps and previews). One card: what it unlocks, the token page,
 * paste, done. Disconnecting asks first.
 */
export function DnsSettingsPage() {
  useTitle("DNS");
  const pv = useQuery(dnsProvidersQuery);
  if (pv.isError && pv.error instanceof ApiError && [404, 412, 501].includes(pv.error.status)) return <NotOnBox what="DNS settings" />;
  const forbidden = pv.isError && pv.error instanceof ApiError && pv.error.status === 403;
  const cf = pv.data?.connected?.find((c) => c.name === "cloudflare");

  return (
    <Page>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Connect DNS"
        lede="Optional. Without it you copy two records by hand when you add a domain; with it, the box does that for you."
      />
      {forbidden ? (
        <p className="mt-8 max-w-[40rem] border-y border-rule py-4 text-[0.875rem] text-ink-2">Only the box’s owner or an admin can connect DNS. Ask them to open Settings › DNS.</p>
      ) : pv.isError ? (
        <ProblemNote className="mt-8" error={pv.error} />
      ) : !pv.data ? (
        <Skeleton className="mt-9 h-64 max-w-[44rem]" />
      ) : (
        <CloudflareCard connected={cf} />
      )}
    </Page>
  );
}

function CloudflareCard({ connected }: { connected?: ConnectedProvider }) {
  const qc = useQueryClient();
  const who = useWho();
  const [replacing, setReplacing] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [token, setToken] = useState("");
  const connect = useMutation({
    mutationFn: () => connectDns("cloudflare", token.trim()),
    onSuccess: (r: Providers) => {
      qc.setQueryData(dnsProvidersQuery.queryKey, r);
      void qc.invalidateQueries({ queryKey: ["box-domain"] });
      void qc.invalidateQueries({ queryKey: ["domains"] });
      setToken("");
      setReplacing(false);
      const zones = r.connected?.find((c) => c.name === "cloudflare")?.zones ?? [];
      toast({ title: "Cloudflare is connected.", detail: zones.length ? `The box can manage ${listWords(zones)}.` : "Its token can’t reach any domains yet." });
    },
  });
  const zones = connected?.zones ?? [];
  const showForm = !connected || replacing || !!connected.error;
  // Apps live under the box domain, or a domain of their own: the wildcard is for that one.
  const bd = useQuery(boxDomainQuery);
  const appsHost = bd.data?.appsDomain ?? location.hostname;
  const apart = !!bd.data && bd.data.appsDomain !== bd.data.domain;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (token.trim()) connect.mutate();
  };

  return (
    <section className="mt-9 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised" aria-label="Cloudflare">
      <div className="flex items-start gap-4 px-5 pt-5 sm:px-6">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <Cloud className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {connected && !connected.error && <Check className="size-4 text-ok" strokeWidth={2.5} />}
            {connected ? (connected.error ? "Cloudflare needs a new token." : "Connected to Cloudflare.") : "Cloudflare"}
          </h2>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {connected
              ? connected.error
                ? connected.error.replace(/^./, (c) => c.toUpperCase())
                : zones.length
                  ? `It can manage ${countWords(zones.length, "domain", "domains", true).toLowerCase()} for your box.`
                  : "Its token can’t reach any domains. Make a new one for All zones."
              : "If your domains’ DNS is at Cloudflare, connect it and two things get easier:"}
          </p>
        </div>
      </div>

      {!connected && (
        <ul className="mt-4 grid gap-2 px-5 text-[0.875rem] text-ink sm:grid-cols-2 sm:px-6">
          {[
            ["One-click domains", "Add a domain and the box adds its records."],
            ["Instant HTTPS for new apps", "One certificate covers every app and preview, so each is secure the moment it exists."],
          ].map(([t, d]) => (
            <li key={t} className="rounded-[10px] bg-paper-sunk px-3.5 py-3">
              <p className="font-[550]">{t}</p>
              <p className="mt-0.5 text-[0.8125rem] text-ink-2">{d}</p>
            </li>
          ))}
        </ul>
      )}

      {connected && zones.length > 0 && (
        <ul className="mx-5 mt-4 divide-y divide-rule border-y border-rule sm:mx-6">
          {zones.map((z) => (
            <li key={z} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2.5">
              <span className="ident text-[0.8125rem] text-ink">{z}</span>
              {connected.boxDomain && inZone(appsHost, z) ? <span className="text-xs text-ink-3">{apart ? "Your apps’ domain" : "Your box’s domain"}: one certificate for every app</span> : null}
            </li>
          ))}
        </ul>
      )}

      {showForm && (
        <form onSubmit={submit} className="mt-5 border-t border-rule px-5 py-5 sm:px-6">
          <ol className="flex flex-col gap-4 text-[0.875rem] text-ink">
            <Step n={1}>
              <span>Open Cloudflare’s API tokens page.</span>
              <Button asChild size="md" className="mt-2">
                <a href={CLOUDFLARE_TOKENS} target="_blank" rel="noopener noreferrer">
                  Open Cloudflare
                  <ArrowUpRight className="text-ink-3" />
                </a>
              </Button>
            </Step>
            <Step n={2}>
              <span>
                Choose <b className="font-[550]">Create Token</b> → <b className="font-[550]">Edit zone DNS</b> template → under Zone Resources, <b className="font-[550]">All zones</b>. Then Continue
                and Create Token.
              </span>
            </Step>
            <Step n={3}>
              <label htmlFor="cf-token">Paste the token here.</label>
              <div className="mt-2 flex w-full flex-col gap-2 sm:flex-row">
                <Input
                  id="cf-token"
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="Cloudflare API token"
                  autoComplete="off"
                  spellCheck={false}
                  className="ident text-[0.875rem] sm:flex-1"
                />
                <Button type="submit" variant="primary" size="lg" className="h-9" disabled={!token.trim() || connect.isPending}>
                  {connect.isPending ? "Checking the token…" : "Connect"}
                </Button>
              </div>
              <p className="mt-1.5 text-xs text-ink-3">The box checks it by listing your domains, then keeps it encrypted. It never leaves the box.</p>
            </Step>
          </ol>
          {connect.isError && <ProblemNote className="mt-4" error={connect.error} />}
          {replacing && (
            <button type="button" onClick={() => setReplacing(false)} className="mt-3 text-[0.8125rem] text-ink-3 hover:text-ink">
              Keep the current token
            </button>
          )}
        </form>
      )}

      {connected && (
        <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-rule px-5 py-3.5 sm:px-6">
          <span className="min-w-0 flex-1 basis-56 text-xs text-ink-3">
            Connected {relative(connected.connectedAt)} by {who(connected.connectedBy)}.
          </span>
          {!showForm && (
            <Button variant="ghost" size="sm" onClick={() => setReplacing(true)}>
              Replace token
            </Button>
          )}
          <Button variant="danger-quiet" size="sm" onClick={() => setLeaving(true)}>
            Disconnect…
          </Button>
        </div>
      )}
      {connected?.error && (
        <p className="mx-5 mb-4 flex items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-sm text-ink sm:mx-6">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
          Until then the box can’t add records, and new apps get their certificates one at a time.
        </p>
      )}

      <Confirm
        open={leaving}
        onClose={() => setLeaving(false)}
        title="Disconnect Cloudflare?"
        body="The box forgets the token. Records it added stay where they are. New domains go back to records by hand, and each app gets its own certificate on its first visit."
        action="Disconnect"
        run={() => disconnectDns("cloudflare")}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["dns-providers"] });
          void qc.invalidateQueries({ queryKey: ["box-domain"] });
          toast({ title: "Cloudflare is disconnected.", detail: "You can delete the token in Cloudflare, under My Profile › API Tokens." });
        }}
      />
    </section>
  );
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3">
      <span className="ident mt-px grid size-5 place-items-center rounded-full border border-rule-2 text-[0.6875rem] text-ink-3">{n}</span>
      <div className="flex min-w-0 flex-col items-start">{children}</div>
    </li>
  );
}

/** Host h is under zone z. */
function inZone(h: string, z: string) {
  return h === z || h.endsWith(`.${z}`);
}

function listWords(xs: string[]) {
  if (xs.length <= 2) return xs.join(" and ");
  return `${xs.slice(0, 2).join(", ")} and ${xs.length - 2} more`;
}
