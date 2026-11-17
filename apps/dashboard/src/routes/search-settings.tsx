import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link as RouterLink } from "@tanstack/react-router";
import { ArrowUpRight, Check, RadioTower, Search, TriangleAlert } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { accessCrumbs } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { StatusDot } from "@/components/project-domains";
import { ProviderMark } from "@/components/signin-oauth";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { disconnectGoogle, googleApis, googleQuery, indexNowQuery, startGoogle, type GoogleConnection } from "@/lib/search";
import { useWho } from "@/lib/me";
import { relative } from "@/lib/time";

/**
 * Settings › Search engines: every engine the box works with. IndexNow is
 * on with nothing to set up (it shows which addresses it covers and when
 * it last told the engines); Google Search Console takes one Connect with
 * Google (through ShipTiffin's Google app on a managed box, else with the
 * box's own Google sign-in keys). Then the box verifies
 * every app on a domain of its own as that person, submits sitemaps and reads
 * searches and index status back. What it finds shows in each project's
 * Analytics › Search.
 */
export function SearchSettingsPage() {
  useTitle("Search engines");
  const g = useQuery(googleQuery);
  if (g.isError && g.error instanceof ApiError && [404, 412, 501].includes(g.error.status)) return <NotOnBox what="Search engine settings" />;
  const forbidden = g.isError && g.error instanceof ApiError && g.error.status === 403;
  return (
    <Page>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Search engines"
        lede="Apps on a domain of their own are findable. What people search for and which bots read the pages shows in each project’s Analytics › Search."
      />
      {!forbidden && <IndexNowCard />}
      {forbidden ? (
        <p className="mt-8 max-w-[40rem] border-y border-rule py-4 text-[0.875rem] text-ink-2">IndexNow is on for every app on a domain of its own. Only the box’s owner or an admin can connect Google. Ask them to open Settings › Search engines.</p>
      ) : g.isError ? (
        <ProblemNote className="mt-8" error={g.error} />
      ) : !g.data ? (
        <Skeleton className="mt-5 h-72 max-w-[44rem]" />
      ) : (
        <GoogleCard conn={g.data} />
      )}
    </Page>
  );
}

/** IndexNow: on by itself. Which addresses it covers and when each was last told. */
function IndexNowCard() {
  const q = useQuery(indexNowQuery);
  const v = q.data;
  const list = v?.addresses ?? [];
  return (
    <section className="mt-9 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised" aria-label="IndexNow">
      <div className="flex items-start gap-4 px-5 py-5 sm:px-6">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <RadioTower className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {v?.on !== false && <Check className="size-4 text-ok" strokeWidth={2.5} />}
            {v?.on === false ? "IndexNow is off on a local box." : "IndexNow is on. Nothing to set up."}
          </h2>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {v?.on === false
              ? "Nothing outside this box can reach its apps, so there is nothing to tell search engines."
              : "Bing, Yandex, Seznam, Naver and Yep hear about new, changed and removed pages after each deploy and every 6 hours. Bing Webmaster Tools isn’t needed for that."}
          </p>
          {q.isError && <ProblemNote className="mt-3" error={q.error} />}
          {v?.on !== false && q.isSuccess && (
            <ul className="mt-3 divide-y divide-rule border-y border-rule">
              {list.length === 0 && <li className="py-2.5 text-[0.8125rem] text-ink-3">No app has a domain of its own yet. Each one is covered as soon as it goes live.</li>}
              {list.map((a) => (
                <li key={a.host} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2.5">
                  <span className="ident min-w-0 truncate text-[0.8125rem] text-ink">{a.host}</span>
                  <span className={cn("text-xs", a.last && !a.last.ok ? "text-warn-ink" : "text-ink-3")}>
                    {!a.last
                      ? "Not told yet"
                      : a.last.ok
                        ? `Told ${relative(a.last.at)}${a.last.pages ? ` · ${a.last.pages === 1 ? "1 page" : `${a.last.pages} pages`}` : " · nothing new"}`
                        : `Refused ${relative(a.last.at)}`}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </section>
  );
}

/** What the callback put in ?google=…, in words (a failed try's own words come from the connection). */
const backWords: Record<string, { title: string; detail?: string; tone?: "danger" }> = {
  connected: { title: "Google Search Console is connected.", detail: "The box looks at your domains now; it takes a minute or two." },
  denied: { title: "Nothing was connected.", detail: "You chose Cancel on Google’s page." },
  scopes: { title: "Google needs both permissions.", detail: "Tick both boxes on Google’s page: Search Console, and verifying your sites.", tone: "danger" },
  expired: { title: "That took too long.", detail: "Or it started in another browser. Choose Connect with Google again.", tone: "danger" },
  failed: { title: "Google didn’t connect.", detail: "The reason is on this page.", tone: "danger" },
};

function GoogleCard({ conn }: { conn: GoogleConnection }) {
  const qc = useQueryClient();
  const who = useWho();
  const [leaving, setLeaving] = useState(false);
  const start = useMutation({ mutationFn: startGoogle, onSuccess: (r) => window.location.assign(r.url) });
  const props = conn.properties ?? [];
  const apis = googleApis(conn.project);

  // Back from Google: say how it went, once, and take the answer out of the address.
  useEffect(() => {
    const q = new URLSearchParams(window.location.search);
    const back = backWords[q.get("google") ?? ""];
    if (!back) return;
    q.delete("google");
    window.history.replaceState(null, "", window.location.pathname + (q.size ? `?${q}` : ""));
    toast(back);
    void qc.invalidateQueries({ queryKey: ["search"] });
  }, [qc]);

  const connectButton = (label: string, primary = true) => (
    <Button variant={primary ? "primary" : "ghost"} size={primary ? "lg" : "sm"} className={primary ? "h-10" : undefined} disabled={start.isPending || !conn.ready} onClick={() => start.mutate()}>
      {primary && <ProviderMark id="google" className="size-4 rounded-full bg-white p-px" />}
      {start.isPending ? "Opening Google…" : label}
    </Button>
  );

  return (
    <section className="mt-5 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised" aria-label="Google Search Console">
      <div className="flex items-start gap-4 px-5 pt-5 sm:px-6">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <Search className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {conn.connected && <Check className="size-4 text-ok" strokeWidth={2.5} />}
            {conn.connected ? "Connected to Google Search Console." : "Google Search Console"}
          </h2>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {conn.connected ? (
              <>
                The box works as <span className="text-ink">{conn.email || "your Google account"}</span>: every domain is in that account’s Search Console.
              </>
            ) : (
              "Optional. Google reads your sitemaps without it. With it, you see what people search for and which pages Google indexed, beside your analytics:"
            )}
          </p>
        </div>
      </div>

      {!conn.connected && (
        <ul className="mt-4 grid gap-2 px-5 text-[0.875rem] text-ink sm:grid-cols-3 sm:px-6">
          {[
            ["Verified for you", "Every app on a domain of its own, in your own Search Console. No TXT records to copy."],
            ["Searches beside analytics", "Clicks, impressions and searches for each domain, 16 months of them, in Analytics › Search."],
            ["Which pages are indexed", "Google’s answer for each page, and why one isn’t."],
          ].map(([t, d]) => (
            <li key={t} className="rounded-[10px] bg-paper-sunk px-3.5 py-3">
              <p className="font-[550]">{t}</p>
              <p className="mt-0.5 text-[0.8125rem] text-ink-2">{d}</p>
            </li>
          ))}
        </ul>
      )}

      {conn.connected && props.length > 0 && (
        <ul className="mx-5 mt-4 divide-y divide-rule border-y border-rule sm:mx-6">
          {props.map((p) => (
            <li key={p.property} className="flex items-start gap-2.5 py-2.5">
              <StatusDot tone={p.state === "verified" ? "ok" : p.state === "failed" ? "bad" : "busy"} className="mt-1.5" />
              <span className="min-w-0">
                <span className="ident block text-[0.8125rem] text-ink">{p.property}</span>
                <span className="block text-xs text-ink-3">
                  {p.state === "verified"
                    ? `Verified ${p.verifiedAt ? relative(p.verifiedAt) : ""}${p.detail?.includes("owns it") ? ": yours in Search Console already" : p.method === "DNS_TXT" ? ", through DNS" : ", through Google’s file on the host"}`
                    : p.detail}
                </span>
                {p.state === "verified" && p.detail?.startsWith("Verified, but ") && <span className="block text-xs text-warn-ink">{p.detail.replace(/^Verified, but /, "")}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {conn.connected && conn.error && (
        <div className="mx-5 mt-4 flex items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-sm text-ink sm:mx-6">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
          <div className="min-w-0">
            <p>Google refuses the box{conn.errorAt ? ` since ${relative(conn.errorAt)}` : ""}, so search data isn’t updating.</p>
            <p className="mt-0.5 text-[0.8125rem] text-ink-2">{conn.error}</p>
            <p className="mt-1 text-[0.8125rem] text-ink-2">Connect with Google again: every domain and its history stay. If an API was turned off, turn it back on first.</p>
            <div className="mt-2">{connectButton("Connect again", false)}</div>
          </div>
        </div>
      )}

      {conn.connected && props.length === 0 && (
        <p className="mx-5 mt-4 border-y border-rule py-2.5 text-[0.8125rem] text-ink-3 sm:mx-6">No app has a domain of its own yet. Each one is verified as soon as it goes live.</p>
      )}

      {!conn.connected && (
        <div className="mt-5 border-t border-rule px-5 py-5 sm:px-6">
          {conn.legacy && (
            <p className="mb-4 flex items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-[0.8125rem] text-ink">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
              <span>This box used a service account key before, which it no longer does. Connect with Google to carry on: your domains and their history stay.</span>
            </p>
          )}
          {conn.ready ? (
            <>
              {connectButton("Connect with Google")}
              <p className="mt-3 text-[0.8125rem] text-ink-2">
                Google asks you to let this box see and manage your Search Console and verify your sites. Tick both boxes. The box keeps that access encrypted, and you can end it here or in your Google account.
              </p>
              {conn.managed ? (
                <p className="mt-3 text-xs text-ink-3">Nothing to set up: Google shows ShipTiffin’s name, and ShipTiffin passes the access on to this box without keeping it.</p>
              ) : (
                <>
                  <p className="mt-3 text-xs text-ink-3">It uses the box’s Google sign-in keys. In their Google Cloud project, these two APIs must be on (once):</p>
                  <span className="flex flex-wrap gap-2">
                    <Link href={apis.search}>Search Console API</Link>
                    <Link href={apis.verify}>Site Verification API</Link>
                  </span>
                </>
              )}
            </>
          ) : (
            <>
              <p className="text-[0.875rem] text-ink">Connect with Google uses the box’s Google sign-in keys, and this box has none yet.</p>
              <p className="mt-1 text-[0.8125rem] text-ink-2">Add them once under Sign-in providers. The same keys then sign people in to your apps and to this dashboard.</p>
              <Button asChild size="sm" className="mt-3">
                <RouterLink to="/settings/box">Open Sign-in providers</RouterLink>
              </Button>
            </>
          )}
          {start.isError && <ProblemNote className="mt-4" error={start.error} />}
          {conn.lastTry && !start.isError && (
            <div className="mt-4 flex items-start gap-2 rounded-[8px] bg-danger-wash px-3 py-2.5 text-[0.8125rem] text-ink">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-danger" />
              <span className="min-w-0 [overflow-wrap:anywhere]">
                <span className="block font-[550]">The last try didn’t connect.</span>
                {conn.lastTry}
              </span>
            </div>
          )}
        </div>
      )}

      {conn.connected && (
        <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-rule px-5 py-3.5 sm:px-6">
          <span className="min-w-0 flex-1 basis-56 text-xs text-ink-3">
            Connected {conn.connectedAt ? relative(conn.connectedAt) : ""} by {who(conn.by ?? "")}
          </span>
          {!conn.error && connectButton("Use another account", false)}
          <Button variant="danger-quiet" size="sm" onClick={() => setLeaving(true)}>
            Disconnect…
          </Button>
        </div>
      )}
      {conn.connected && start.isError && <ProblemNote className="mx-5 mb-4 sm:mx-6" error={start.error} />}

      <Confirm
        open={leaving}
        onClose={() => setLeaving(false)}
        title="Disconnect Google?"
        body="The box deletes its Google access and the search data it read. Your properties stay in Search Console, and the verification records stay in DNS. Your Google account still lists the app until you remove it there."
        action="Disconnect"
        run={() => disconnectGoogle()}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["search-google"] });
          void qc.invalidateQueries({ queryKey: ["search"] });
          toast({ title: "Google is disconnected.", detail: "To remove the app from your Google account too, open myaccount.google.com › Security › Third-party apps." });
        }}
      />
    </section>
  );
}

function Link({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Button asChild size="sm" className="mt-2">
      <a href={href} target="_blank" rel="noopener noreferrer">
        {children}
        <ArrowUpRight className="text-ink-3" />
      </a>
    </Button>
  );
}
