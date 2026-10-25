import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Globe, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "@/api/client";
import { BuyDialog, Name, OutLink, UseDialog } from "@/components/domain-buy";
import { shortDate } from "@/components/domains-parts";
import { useTitle } from "@/components/favicon";
import { Empty, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useDebounced } from "@/lib/debounced";
import { domainCheckQuery, domainSearchQuery, ownedDomainsQuery, price, type BuyReadiness, type Offer, type OwnedDomain } from "@/lib/domains";

/**
 * Domains: find a name and buy it, or put one you own on a project. Domains
 * belong to the box (one can serve several projects), so this sits beside
 * Projects. Bought with Cloudflare Registrar in the owner's own Cloudflare
 * account, at cost; the box sets the records and HTTPS itself.
 * The search is in the URL (?q=acme), and ?project=shop preselects where it goes.
 */
export function DomainsHomePage({ q: initial = "", project }: { q?: string; project?: string }) {
  useTitle("Domains");
  const navigate = useNavigate();
  const [raw, setRaw] = useState(initial);
  const query = useDebounced(raw.trim().toLowerCase(), 300);
  const input = useRef<HTMLInputElement>(null);
  const [buying, setBuying] = useState<Offer | null>(null);
  const [using, setUsing] = useState<string | null>(null);
  const owned = useQuery(ownedDomainsQuery);
  const search = useQuery(domainSearchQuery(query));
  // Then the names shown from Cloudflare's cache, checked live (a second or so later).
  const unchecked = (search.data?.offers ?? []).filter((o) => !o.checked).map((o) => o.domain).slice(0, 20);
  const live = useQuery(domainCheckQuery(unchecked));
  const offers = useMemo(() => {
    if (!search.data) return undefined;
    const got = new Map((live.data?.offers ?? []).map((o) => [o.domain, o]));
    return (search.data.offers ?? []).map((o) => (got.has(o.domain) ? { ...got.get(o.domain)!, exact: o.exact } : o));
  }, [search.data, live.data]);

  useEffect(() => {
    void navigate({ to: "/domains", search: (s: Record<string, unknown>) => ({ ...s, q: query || undefined }), replace: true });
  }, [query, navigate]);

  if (owned.isError && owned.error instanceof ApiError && [404, 412, 501].includes(owned.error.status)) return <NotOnBox what="Domains" />;
  if (owned.isError && owned.error instanceof ApiError && owned.error.status === 403)
    return (
      <Page>
        <PageHeader title="Domains" />
        <p className="mt-8 max-w-[40rem] border-y border-rule py-4 text-[0.875rem] text-ink-2">Domains belong to the whole box, so only its owner or an admin can buy and manage them.</p>
      </Page>
    );

  const ready = search.data?.buy ?? owned.data?.buy;
  const typing = raw.trim() !== "" && raw.trim().toLowerCase() !== query;
  return (
    <Page>
      <PageHeader title="Domains" lede="Find a name and buy it, or put one you already own on a project. The box sets up DNS and HTTPS." />

      {!ready && owned.isPending ? (
        <Skeleton className="mt-7 h-12 rounded-[10px]" />
      ) : ready && !ready.canBuy ? (
        <Setup r={ready} checking={owned.isFetching} onCheck={() => void owned.refetch()} />
      ) : (
        <div className="relative mt-7">
          <Search aria-hidden className="pointer-events-none absolute top-1/2 left-4 size-[18px] -translate-y-1/2 text-ink-3" />
          <input
            ref={input}
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && raw && (e.preventDefault(), setRaw(""))}
            placeholder="Search for a domain, like acme.app"
            aria-label="Search for a domain"
            autoFocus
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            className="h-12 w-full rounded-[10px] border border-rule-2 bg-paper-raised pr-12 pl-11 text-[1rem] text-ink shadow-[var(--top-light)] transition-[border-color,box-shadow] outline-hidden placeholder:text-ink-4 hover:border-rule-3 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
          />
          {raw && (
            <button
              type="button"
              onClick={() => (setRaw(""), input.current?.focus())}
              className="absolute top-1/2 right-3 grid size-7 -translate-y-1/2 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink"
              aria-label="Clear the search"
            >
              <X className="size-4" />
            </button>
          )}
        </div>

      )}

      {ready && !ready.canBuy ? (
        (owned.data?.domains?.length ?? 0) > 0 && <Mine owned={owned.data?.domains ?? undefined} loading={false} error={null} onUse={setUsing} onSearch={() => input.current?.focus()} />
      ) : query ? (
        <Results query={query} loading={typing || search.isFetching} offers={offers} error={search.isError ? search.error : null} canBuy={!!ready?.canBuy} onBuy={setBuying} />
      ) : (
        <Mine owned={owned.data?.domains ?? undefined} loading={owned.isPending} error={owned.isError ? owned.error : null} onUse={setUsing} onSearch={() => input.current?.focus()} />
      )}

      <BuyDialog offer={buying} project={project} onClose={() => setBuying(null)} />
      <UseDialog domain={using} onClose={() => setUsing(null)} />
    </Page>
  );
}

/**
 * Before the box can buy: connect Cloudflare (Settings › DNS, then back here),
 * or give the connected token the Registrar permission (in Cloudflare; its
 * value stays, so Check again is all that's left).
 */
function Setup({ r, checking, onCheck }: { r: BuyReadiness; checking: boolean; onCheck: () => void }) {
  const connect = r.fixUrl === "/settings/dns";
  return (
    <section className="mt-7 rounded-[14px] border border-rule-2 bg-paper-raised px-5 py-6 shadow-[var(--top-light)] sm:px-7 sm:py-7" aria-label="Set up buying domains">
      <span className="grid size-10 place-items-center rounded-[10px] bg-paper-sunk text-ink">
        <Globe className="size-5" />
      </span>
      <h2 className="mt-4 text-[1.0625rem] font-[600] text-ink">{connect ? "Connect Cloudflare to search and buy domains" : "One more permission to buy domains"}</h2>
      <p className="mt-1.5 max-w-[34rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
        {connect
          ? "Domains are bought in your own Cloudflare account, at cost, and the box sets up their DNS and HTTPS. Connect it once in Settings › DNS; it takes a minute, and you come straight back here."
          : `${r.why} ${r.fix}`}
      </p>
      <div className="mt-5 flex flex-wrap gap-2">
        {connect ? (
          <Button asChild variant="primary" size="lg">
            <Link to="/settings/dns" search={{ next: "/domains" }}>
              Connect Cloudflare
            </Link>
          </Button>
        ) : (
          <>
            <Button asChild variant="primary" size="lg">
              <a href={r.fixUrl} target="_blank" rel="noopener noreferrer">
                Open Cloudflare tokens
              </a>
            </Button>
            <Button size="lg" onClick={onCheck} disabled={checking}>
              {checking ? "Checking…" : "Check again"}
            </Button>
          </>
        )}
      </div>
    </section>
  );
}

function Results({
  query,
  loading,
  offers,
  error,
  canBuy,
  onBuy,
}: {
  query: string;
  loading: boolean;
  offers?: Offer[];
  error: unknown;
  canBuy: boolean;
  onBuy: (o: Offer) => void;
}) {
  if (error) return <ProblemNote className="mt-6" error={error} />;
  if (!offers) return <ResultsSkeleton />;
  const exact = offers.find((o) => o.exact);
  const rest = offers.filter((o) => o !== exact);
  if (!offers.length)
    return (
      <p className="mt-8 text-[0.875rem] text-ink-3">
        Nothing for “{query}”. Try a shorter word, or a name with its ending, like {query.split(/[\s.]/)[0] || "acme"}.app.
      </p>
    );
  return (
    <div className={cn("mt-6 transition-opacity", loading && "opacity-60")} aria-busy={loading || undefined}>
      {exact && <Hero o={exact} canBuy={canBuy} onBuy={onBuy} />}
      {rest.length > 0 && (
        <>
          <h2 className="mt-8 mb-1 text-[0.8125rem] font-[550] text-ink-3">{exact ? "Other names" : "Names you can get"}</h2>
          <ul className="divide-y divide-rule border-y border-rule">
            {rest.map((o) => (
              <Row key={o.domain} o={o} canBuy={canBuy} onBuy={onBuy} />
            ))}
          </ul>
        </>
      )}
      <p className="mt-4 text-[0.8125rem] text-ink-3">Cloudflare’s prices, at cost. Bought in your own Cloudflare account.</p>
    </div>
  );
}

/** The name you typed: big, with its answer. */
function Hero({ o, canBuy, onBuy }: { o: Offer; canBuy: boolean; onBuy: (o: Offer) => void }) {
  return (
    <section aria-label={`${o.domain}`} className="rounded-[12px] border border-rule-2 bg-paper-raised px-5 py-5 shadow-[var(--top-light)] sm:px-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <p className={cn("ident text-[1.5rem] leading-8 tracking-[-0.01em]", o.available ? "text-ink" : "text-ink-3")}>
            <Name domain={o.domain} />
          </p>
          <p className="mt-1 text-[0.875rem] text-ink-2">{o.available ? "Available" : o.why}</p>
        </div>
        {o.available ? (
          <div className="flex items-center gap-4 max-sm:justify-between">
            <Price o={o} big />
            <Button variant="primary" size="lg" disabled={!canBuy} onClick={() => onBuy(o)}>
              Buy
            </Button>
          </div>
        ) : (
          o.link && <OutLink href={o.link}>Look in Cloudflare</OutLink>
        )}
      </div>
    </section>
  );
}

function Price({ o, big }: { o: Offer; big?: boolean }) {
  return (
    <div className="text-right tnum">
      <p className={cn("text-ink", big ? "text-[1.0625rem] font-[550]" : "text-[0.875rem]")}>{price(o.firstYear, o.currency)}</p>
      <p className="text-[0.75rem] text-ink-3">
        {o.renewal === o.firstYear ? "a year" : `then ${price(o.renewal, o.currency)}/yr`}
      </p>
    </div>
  );
}

function Row({ o, canBuy, onBuy }: { o: Offer; canBuy: boolean; onBuy: (o: Offer) => void }) {
  return (
    <li className="flex min-h-14 items-center gap-4 py-2.5">
      <div className="min-w-0 flex-1">
        <p className={cn("ident truncate text-[0.9375rem]", o.available ? "text-ink" : "text-ink-3")}>
          <Name domain={o.domain} />
        </p>
        {!o.available && <p className="truncate text-[0.8125rem] text-ink-3">{o.premium ? "Premium name" : o.reason === "domain_unavailable" ? "Taken" : o.why}</p>}
      </div>
      {o.available ? (
        <>
          <Price o={o} />
          <Button size="sm" className="w-14 justify-center" disabled={!canBuy} onClick={() => onBuy(o)} aria-label={`Buy ${o.domain} for ${price(o.firstYear, o.currency)}`}>
            Buy
          </Button>
        </>
      ) : o.link ? (
        <OutLink href={o.link}>In Cloudflare</OutLink>
      ) : null}
    </li>
  );
}

function ResultsSkeleton() {
  return (
    <div className="mt-6">
      <Skeleton className="h-[5.5rem] rounded-[12px]" />
      <div className="mt-8 divide-y divide-rule border-y border-rule">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="flex h-14 items-center gap-4">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="ml-auto h-4 w-16" />
            <Skeleton className="h-7 w-14" />
          </div>
        ))}
      </div>
    </div>
  );
}

/** Your domains: where each one is used, and when it renews. */
function Mine({ owned, loading, error, onUse, onSearch }: { owned?: OwnedDomain[]; loading: boolean; error: unknown; onUse: (d: string) => void; onSearch: () => void }) {
  if (error) return <ProblemNote className="mt-8" error={error} />;
  if (loading)
    return (
      <div className="mt-10 divide-y divide-rule border-y border-rule">
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex h-16 items-center gap-4">
            <Skeleton className="h-4 w-44" />
            <Skeleton className="ml-auto h-7 w-28" />
          </div>
        ))}
      </div>
    );
  const list = owned ?? [];
  if (!list.length)
    return (
      <Empty className="mt-10" icon={<Globe />} title="No domains yet">
        Search above to find one. Once it’s yours, the box can put it on any project in a click.
        <div className="mt-4">
          <Button onClick={onSearch}>Search for a domain</Button>
        </div>
      </Empty>
    );
  return (
    <section className="mt-10">
      <h2 className="mb-1 text-[0.9375rem] font-[600] text-ink">Your domains</h2>
      <ul className="divide-y divide-rule border-y border-rule">
        {list.map((d) => (
          <Mined key={d.domain} d={d} onUse={onUse} />
        ))}
      </ul>
    </section>
  );
}

function Mined({ d, onUse }: { d: OwnedDomain; onUse: (d: string) => void }) {
  const pu = d.purchase;
  const busy = pu && !["ready", "failed"].includes(pu.state);
  const uses = d.usedBy ?? [];
  const projects = [...new Map(uses.map((u) => [u.project, u])).values()];
  return (
    <li className="flex min-h-16 flex-col gap-2 py-3 sm:flex-row sm:items-center sm:gap-4">
      <div className="min-w-0 flex-1">
        <p className="ident truncate text-[0.9375rem] text-ink">
          <Name domain={d.domain} />
        </p>
        <p className="mt-0.5 truncate text-[0.8125rem] text-ink-3">
          {busy ? (
            <span className="inline-flex items-center gap-1.5 text-ink-2">
              <span className="spinner size-3" aria-hidden />
              {pu.detail}
            </span>
          ) : pu?.state === "failed" ? (
            <span className="text-danger">{pu.detail}</span>
          ) : projects.length ? (
            <>
              Used by{" "}
              {projects.map((u, i) => (
                <span key={u.project}>
                  {i > 0 && ", "}
                  <Link to="/projects/$project/domains" params={{ project: u.project }} search={{ domain: u.host }} className="text-ink-2 hover:text-ink">
                    {u.project}
                  </Link>
                </span>
              ))}
            </>
          ) : d.box ? (
            "Used by this box’s own address"
          ) : (
            "Not used yet"
          )}
        </p>
      </div>
      {d.registered && d.expiresAt && (
        <p className="shrink-0 text-[0.8125rem] text-ink-3 sm:text-right">
          {d.autoRenew ? "Renews" : "Expires"} {shortDate(d.expiresAt)}
          {d.autoRenew === false && <span className="text-warn-ink"> · auto-renew off</span>}
        </p>
      )}
      {!busy && d.manageable && !d.box && (
        <Button size="sm" className="shrink-0 self-start sm:self-auto" onClick={() => onUse(d.domain)}>
          {projects.length ? "Use on another project" : "Use on a project"}
        </Button>
      )}
    </li>
  );
}
