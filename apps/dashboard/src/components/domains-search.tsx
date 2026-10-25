import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Check, RefreshCw, X } from "lucide-react";
import type { ReactNode } from "react";
import { Breaker } from "@/components/breaker";
import { agoWords, useNow } from "@/components/domains-parts";
import { InfoTip } from "@/components/info-tip";
import { ProblemNote } from "@/components/problem";
import { StatusDot } from "@/components/project-domains";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { pingSearch, purposeWords, reasonWords, searchQuery, setSearchSettings, type Crawler, type SearchHost, type SearchStatus } from "@/lib/search";
import { full } from "@/lib/time";

/**
 * "Search engines" under an open, live domain: whether search engines may
 * find it, when the box last told them about its pages (IndexNow), which
 * search and AI bots actually came (the crawler log), and what the box saw
 * in its robots.txt, home page and sitemap. Google takes no pings, so it
 * links out to Search Console.
 */
export function DomainSearch({ project, host, writer }: { project: string; host: string; writer: boolean }) {
  const qc = useQueryClient();
  const s = useQuery(searchQuery(project));
  const now = useNow(30_000);
  const ping = useMutation({
    mutationFn: () => pingSearch(project, host),
    onSuccess: (st) => {
      qc.setQueryData(["search", project], st);
      const h = st.hosts?.find((x) => x.host === host);
      const sent = h?.last && Date.now() - new Date(h.last.at).getTime() < 60_000;
      const google = h?.google?.state === "verified";
      toast({
        title: sent ? `Told search engines about ${h.last!.pages} ${h.last!.pages === 1 ? "page" : "pages"}.` : "Nothing new for IndexNow.",
        detail: google ? "The sitemap went to Google again too." : undefined,
      });
    },
  });
  const h = s.data?.hosts?.find((x) => x.host === host);

  return (
    <section className="mt-6" aria-label={`Search engines for ${host}`}>
      <div className="mb-2.5 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <h3 className="label">Search engines</h3>
        {writer && h?.findable && (
          <Button size="sm" variant="ghost" onClick={() => ping.mutate()} disabled={ping.isPending} title="Read the sitemap now, send IndexNow what changed, and submit the sitemap to Google again">
            <RefreshCw className={cn("size-3.5!", ping.isPending && "animate-spin")} />
            Tell search engines
          </Button>
        )}
      </div>
      {s.isError ? (
        <ProblemNote error={s.error} title="The search status can’t be read right now." />
      ) : !h ? (
        <p className="text-[0.8125rem] text-ink-3">{s.data ? "The box looks at this domain once an app is deployed on it." : "Reading…"}</p>
      ) : !h.findable ? (
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
          <p className="flex items-start gap-2.5 text-[0.875rem] text-ink-2">
            <StatusDot tone="wait" className="mt-1.5" />
            <span>
              {h.hidden ? "Kept out of search results. " : "Not findable yet. "}
              <span className="text-ink-3">
                {h.hidden ? "Anyone with the link can still open it; search engines are asked not to list it." : h.why} {h.fix}
              </span>
            </span>
          </p>
          {writer && h.hidden && s.data && <ShowButton project={project} host={host} st={s.data} />}
        </div>
      ) : (
        <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
          <div className="flex items-start gap-2.5 border-b border-rule px-4 py-3">
            <StatusDot tone="ok" className="mt-1.5" />
            <p className="text-[0.875rem] text-ink">
              Findable
              <span className="text-ink-3">
                {" "}
                · search engines may index {host}
                {h.since ? `, since ${agoWords(h.since, now)}` : ""}.
              </span>
            </p>
          </div>
          <dl className="divide-y divide-rule px-4">
            <Row label="Told">
              <Told h={h} now={now} />
            </Row>
            <Row label="Crawled">
              <Crawled list={h.crawled ?? []} fakes={h.fakes7d ?? 0} now={now} />
            </Row>
            <Row label="Checks">
              <Checks h={h} />
            </Row>
            <Row label="Google">
              <GoogleRow h={h} now={now} />
            </Row>
          </dl>
        </div>
      )}
      {ping.isError && <ProblemNote className="mt-2" error={ping.error} title="The check didn’t run." />}
    </section>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-3 gap-y-1 py-3 text-[0.875rem] sm:grid-cols-[6.5rem_minmax(0,1fr)]">
      <dt className="text-[0.8125rem] text-ink-3">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

function Told({ h, now }: { h: SearchHost; now: number }) {
  const last = h.last;
  const engines = (
    <InfoTip label="Which search engines">
      One IndexNow message reaches Bing, Yandex, Seznam, Naver, Yep, Amazon and the Internet Archive. DuckDuckGo, Yahoo and Copilot use Bing’s index. Google takes no pings; it reads your sitemap.
    </InfoTip>
  );
  return (
    <div className="space-y-1">
      {!last ? (
        <p className="text-ink-2">Not yet: the box tells them within a minute of the domain going live.</p>
      ) : last.ok ? (
        <p className="text-ink">
          {last.pages} {last.pages === 1 ? "page" : "pages"}{" "}
          <span className="text-ink-2" title={full(last.at)}>
            · {agoWords(last.at, now)}, {reasonWords[last.reason] ?? last.reason}
          </span>{" "}
          {engines}
        </p>
      ) : (
        <p className="text-warn-ink">
          {last.detail} <span className="text-ink-3">· {agoWords(last.at, now)}</span>
        </p>
      )}
      {last?.ok && last.sample && last.sample.length > 0 && (
        <p className="truncate text-[0.8125rem] text-ink-3" title={last.sample.join("\n")}>
          {last.sample.map((u) => u.replace(/^https:\/\/[^/]+/, "") || "/").join(" · ")}
          {last.pages > last.sample.length ? ` and ${last.pages - last.sample.length} more` : ""}
        </p>
      )}
      <p className="text-[0.8125rem] text-ink-3">
        {h.pending
          ? `Checking again ${h.pending.reason === "deploy" ? "a couple of minutes after the deploy" : "in a moment"}.`
          : "New, changed and removed pages are sent after each deploy and every 6 hours."}
      </p>
    </div>
  );
}

function Crawled({ list, fakes, now }: { list: Crawler[]; fakes: number; now: number }) {
  if (list.length === 0)
    return (
      <p className="text-ink-2">
        No search bot has come yet. <span className="text-ink-3">A new domain usually sees Googlebot and Bingbot within a few days.</span>
      </p>
    );
  return (
    <div>
      <ul className="space-y-1">
        {list.slice(0, 6).map((c) => (
          <li key={c.bot} className="flex flex-wrap items-baseline gap-x-2">
            <span className="ident text-[0.8125rem] text-ink">{c.bot}</span>
            <span className="text-[0.8125rem] text-ink-3">{purposeWords[c.purpose] ?? c.purpose}</span>
            <span className="text-[0.8125rem] text-ink-2" title={`${full(c.lastAt)} · ${c.lastPath} (${c.lastStatus})`}>
              · {agoWords(c.lastAt, now)}
              {c.pages7d > 0 ? `, ${c.pages7d} ${c.pages7d === 1 ? "page" : "pages"} this week` : ""}
            </span>
            {c.errors7d > 0 && <span className="text-[0.8125rem] text-warn-ink">· {c.errors7d} answered with an error</span>}
          </li>
        ))}
      </ul>
      <p className="mt-1.5 text-[0.8125rem] text-ink-3">
        Counted only from the addresses each company publishes.
        {fakes > 0 ? ` ${fakes} ${fakes === 1 ? "request" : "requests"} this week claimed to be a search bot from elsewhere and were ignored.` : ""}
      </p>
    </div>
  );
}

function Checks({ h }: { h: SearchHost }) {
  const c = h.checks;
  if (!c) return <p className="text-ink-3">Not checked yet.</p>;
  const items: Array<[string, boolean, string]> = [
    ["robots.txt", c.robots.ok, c.robots.detail],
    ["Sitemap", c.sitemap.ok && !c.sitemap.sameDates, c.sitemap.detail],
    ["Home page", c.home.ok, c.home.detail],
    ["IndexNow key", c.key.ok, c.key.detail],
  ];
  return (
    <ul className="space-y-1">
      {items.map(([name, ok, detail]) => (
        <li key={name} className="flex gap-2">
          <span className={cn("mt-[3px] grid size-4 shrink-0 place-items-center rounded-full", ok ? "bg-ok-wash text-ok" : "bg-warn-wash text-warn-ink")} aria-label={ok ? "Fine" : "Needs a look"}>
            {ok ? <Check className="size-2.5" strokeWidth={3} /> : <X className="size-2.5" strokeWidth={3} />}
          </span>
          <span className="min-w-0 text-[0.8125rem]">
            <span className="text-ink">{name}</span> <span className={ok ? "text-ink-3" : "text-ink-2"}>{detail}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

/** Project Settings › General: keep the whole project out of search results. */
export function SearchVisibility({ project, writer }: { project: string; writer: boolean }) {
  const qc = useQueryClient();
  const s = useQuery(searchQuery(project));
  const flip = useMutation({
    mutationFn: (hidden: boolean) => setSearchSettings(project, { ...(s.data?.settings ?? {}), hidden }),
    onSuccess: (st) => qc.setQueryData(["search", project], st),
  });
  if (s.isError) return <ProblemNote error={s.error} title="The search setting can’t be read right now." />;
  const hidden = flip.isPending ? !!flip.variables : !!s.data?.settings.hidden;
  const own = (s.data?.hosts ?? []).filter((h) => h.findable || h.hidden).map((h) => h.host);
  return (
    <div>
      <label className="inline-flex items-center gap-2.5 text-[0.875rem] text-ink-2">
        <Breaker label="Keep out of search results" state={hidden ? "on" : "off"} staged={flip.isPending ? (flip.variables ? "on" : "off") : undefined} onFlip={(n) => flip.mutate(n === "on")} disabled={!writer || !s.data} />
        <span>
          {hidden ? "Kept out" : "Findable"}
          <span className="text-ink-3">
            {" "}
            ·{" "}
            {hidden
              ? "its apps still open for anyone with the link, but search engines are asked not to list them"
              : own.length > 0
                ? `${own.join(", ")} ${own.length === 1 ? "is" : "are"} findable`
                : "its apps become findable once they have a domain of their own"}
          </span>
        </span>
      </label>
      {flip.isError && <ProblemNote className="mt-2" error={flip.error} />}
    </div>
  );
}

/** Keep one address out of search results, or let it back in (the domain's ⋯ menu, and the panel). */
export function useHideHost(project: string) {
  const qc = useQueryClient();
  const s = useQuery(searchQuery(project));
  const m = useMutation({
    mutationFn: ({ host, hide }: { host: string; hide: boolean }) => {
      const cur = s.data?.settings ?? { hidden: false };
      const hosts = new Set(cur.hiddenHosts ?? []);
      if (hide) hosts.add(host);
      else hosts.delete(host);
      return setSearchSettings(project, { ...cur, hiddenHosts: [...hosts] });
    },
    onSuccess: (st, { host, hide }) => {
      qc.setQueryData(["search", project], st);
      toast({ title: hide ? `${host} is kept out of search results.` : `${host} is findable again.`, detail: hide ? "It still opens for anyone with the link. Search engines drop it over the next few weeks." : undefined });
    },
  });
  const status = s.data;
  return {
    hidden: (host: string) => !!status?.settings.hiddenHosts?.includes(host),
    projectHidden: !!status?.settings.hidden,
    set: (host: string, hide: boolean) => m.mutate({ host, hide }),
  };
}

function ShowButton({ project, host, st }: { project: string; host: string; st: SearchStatus }) {
  const hide = useHideHost(project);
  if (st.settings.hidden)
    return (
      <Link to="/projects/$project/settings" params={{ project }} className="text-[0.8125rem] text-ink underline decoration-rule-3 underline-offset-4">
        Project settings
      </Link>
    );
  return (
    <Button size="sm" variant="ghost" onClick={() => hide.set(host, false)}>
      Let search engines list it
    </Button>
  );
}

function GoogleRow({ h, now }: { h: SearchHost; now: number }) {
  const g = h.google;
  const { admin } = useMe();
  const verifiedByTxt = !!h.checks?.google.ok;
  if (!g || g.state === "not_connected")
    return (
      <div className="space-y-1">
        <p className={verifiedByTxt ? "text-ink" : "text-ink-2"}>
          {verifiedByTxt ? "Verified in Search Console. " : "Google reads your sitemap itself. "}
          <span className="text-ink-3">
            {admin ? "Connect Google once and the box shows what people search for and which pages are indexed, right here, for every domain." : "To see searches and indexing here, the box’s owner connects Google in Settings › Search engines."}
          </span>
        </p>
        <div className="flex flex-wrap gap-x-4">
          {admin && (
            <Link to="/settings/search" className="text-[0.8125rem] text-ink underline decoration-rule-3 underline-offset-4 hover:text-brass-ink">
              Connect Google
            </Link>
          )}
          <a href="https://search.google.com/search-console" target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-brass-ink">
            Open Search Console
            <ArrowUpRight className="size-3.5" />
          </a>
        </div>
      </div>
    );
  if (g.state !== "verified")
    return (
      <p className="flex items-start gap-2 text-ink-2">
        <StatusDot tone={g.state === "failed" ? "bad" : "busy"} className="mt-1.5" />
        <span>
          {g.state === "failed" ? "Not verified. " : "Verifying in Search Console. "}
          <span className="text-ink-3">{g.detail}</span>
        </span>
      </p>
    );
  const scUrl = `https://search.google.com/search-console?resource_id=${encodeURIComponent(g.property ?? "")}`;
  const err = g.error && <p className="text-[0.8125rem] text-warn-ink">{g.error}</p>;
  const ix = g.index;
  return (
    <div className="space-y-2">
      {err}
      {g.pulledAt ? (
        <p className="text-ink">
          {g.clicks.toLocaleString()} {g.clicks === 1 ? "click" : "clicks"}
          <span className="text-ink-2">
            {" "}
            · {g.impressions.toLocaleString()} {g.impressions === 1 ? "impression" : "impressions"}
            {g.impressions > 0 ? ` · average position ${g.position}` : ""}
          </span>
          <span className="text-ink-3"> · last 28 days</span>
        </p>
      ) : (
        !g.error && <p className="text-ink-2">Verified. Reading the search data now.</p>
      )}
      {(g.queries ?? []).length > 0 && (
        <ul className="space-y-0.5">
          {(g.queries ?? []).slice(0, 5).map((q) => (
            <li key={q.key} className="flex items-baseline gap-2 text-[0.8125rem]">
              <span className="min-w-0 truncate text-ink">“{q.key}”</span>
              <span className="shrink-0 text-ink-3">
                {q.clicks} · {q.impressions} shown · #{q.position}
              </span>
            </li>
          ))}
        </ul>
      )}
      {g.sitemap && (
        <p className="text-[0.8125rem] text-ink-2">
          {g.sitemap.lastRead
            ? `Google read your sitemap ${agoWords(g.sitemap.lastRead, now)}: ${g.sitemap.pages} ${g.sitemap.pages === 1 ? "page" : "pages"}`
            : "Sitemap submitted; Google hasn’t read it yet"}
          {g.sitemap.errors > 0 ? (
            <span className="text-warn-ink">
              {" "}
              · {g.sitemap.errors} {g.sitemap.errors === 1 ? "error" : "errors"}
            </span>
          ) : g.sitemap.lastRead ? (
            ", no errors"
          ) : (
            ""
          )}
          {g.sitemap.warnings > 0 ? ` · ${g.sitemap.warnings} ${g.sitemap.warnings === 1 ? "warning" : "warnings"}` : ""}.
        </p>
      )}
      {ix && (
        <div className="text-[0.8125rem]">
          <p className="text-ink-2">
            Google has indexed {ix.indexed} of the {ix.checked} {ix.checked === 1 ? "page" : "pages"} checked so far.
          </p>
          {(ix.notIndexed ?? []).slice(0, 3).map((n) => (
            <p key={n.url} className="truncate text-ink-3" title={n.url}>
              <span className="ident text-ink-2">{n.url.replace(/^https:\/\/[^/]+/, "") || "/"}</span> · {n.coverage}
            </p>
          ))}
        </div>
      )}
      <p className="text-[0.8125rem] text-ink-3">
        {g.pulledAt ? `Read ${agoWords(g.pulledAt, now)}; again every 12 hours. ` : ""}
        <a href={scUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-brass-ink">
          Open in Search Console
          <ArrowUpRight className="size-3.5" />
        </a>
      </p>
    </div>
  );
}

