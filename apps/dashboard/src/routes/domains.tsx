import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { q } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { HostHints, RecordsTable, Watching, type Row } from "@/components/dns-records";
import { useTitle } from "@/components/favicon";
import { Group } from "@/components/health-kit";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Addresses } from "@/components/project-rows";
import { StatusDot } from "@/components/project-domains";
import { toast } from "@/components/toast";
import { Breaker } from "@/components/breaker";
import { Button } from "@/components/ui/button";
import { Checkbox, Select } from "@/components/ui/choice";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import {
  addDomain,
  boxDomainQuery,
  cleanDomain,
  isApex,
  looksLikeDomain,
  projectDomainsQuery,
  providerName,
  recheckDomain,
  removeDomain,
  setRecords,
  stateWords,
  withWww,
  type Domain,
} from "@/lib/domains";
import { useMe } from "@/lib/me";
import { rememberProject } from "@/lib/recent";
import { change, pendingFor, undoChange, usePending } from "@/lib/staged";

/**
 * Project › Settings › Domains: the project's own names (example.com) with
 * their state in words, which app serves each, and the www redirect. Adding
 * one shows exactly the records to add and watches DNS until it's live; with
 * a DNS provider connected, one button adds them. The box's own addresses
 * for its apps are listed too, since they always work.
 */
export function DomainsPage({ project }: { project: string }) {
  useTitle(`${project} · Domains`);
  useEffect(() => rememberProject(project), [project]);
  const list = useQuery(projectDomainsQuery(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const box = useQuery(boxDomainQuery);
  const { can } = useMe();
  const [fresh, setFresh] = useState<string | null>(null);
  const writer = can("apply:reversible");

  const webApps = useMemo(
    () =>
      Object.entries(m.data?.manifest.apps ?? {})
        .filter(([, a]) => a.role !== "worker")
        .map(([n]) => n),
    [m.data],
  );
  const rows = withWww(list.data ?? []);
  const local = box.data?.certificates === "internal";
  useRecheck(project, list.data ?? [], writer);

  return (
    <Page>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              { label: project, to: "/projects/$project", params: { project } },
              { label: "Settings", to: "/projects/$project/settings", params: { project } },
              { label: "Domains" },
            ]}
          />
        }
        title="Domains"
        lede={`Give ${project} a name of your own, like ${project}.com. HTTPS comes with it and renews itself.`}
      />

      {local && (
        <p className="mt-6 max-w-[40rem] rounded-[10px] bg-paper-sunk px-4 py-3 text-[0.875rem] text-ink-2">
          This box runs on your computer, so no domain can point at it yet. Domains you add here start working once the box runs on a server.
        </p>
      )}

      {writer && <AddDomain project={project} apps={webApps} loading={!m.data} taken={rows.map((r) => r.d.domain)} onAdded={setFresh} />}

      <Group label="Your domains" id="yours" aside={rows.length > 1 ? `${rows.length} domains` : undefined}>
        {list.isError ? (
          <ProblemNote error={list.error} />
        ) : !list.data ? (
          <Skeleton className="h-16" />
        ) : rows.length === 0 ? (
          <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">None yet. Add one above; you’ll get the exact records to set at your DNS host.</p>
        ) : (
          <ul className="divide-y divide-rule border-y border-rule">
            {rows.map(({ d, www }) => (
              <DomainRow key={d.domain} project={project} d={d} www={www} writer={writer} fresh={fresh === d.domain} local={local} />
            ))}
          </ul>
        )}
      </Group>

      {webApps.length > 0 && (
        <Group label="Addresses on your box" id="box-addresses">
          <p className="-mt-1 mb-2 text-[0.8125rem] text-ink-3">Every app answers here too, whatever domains it has. They come from the app’s name.</p>
          <Addresses project={project} apps={webApps} />
        </Group>
      )}
    </Page>
  );
}

// ───────────────────────── add ─────────────────────────

function AddDomain({ project, apps, loading, taken, onAdded }: { project: string; apps: string[]; loading: boolean; taken: string[]; onAdded: (d: string) => void }) {
  const qc = useQueryClient();
  const [raw, setRaw] = useState("");
  const [app, setApp] = useState("");
  const [www, setWww] = useState(true);
  const domain = cleanDomain(raw);
  const chosen = app || (apps.includes("web") ? "web" : apps[0]) || "";
  const apex = looksLikeDomain(domain) && isApex(domain) && !domain.startsWith("www.");
  const add = useMutation({
    mutationFn: () => addDomain(project, { domain, app: chosen, www: apex && www }),
    onSuccess: (r) => {
      setRaw("");
      onAdded(domain);
      void qc.invalidateQueries({ queryKey: ["domains", project] });
      for (const k of [["manifest", project], ["project", project], ["changes"]]) void qc.invalidateQueries({ queryKey: k });
      const id = r.change?.id;
      toast({
        title: `Added ${domain} to ${project}.`,
        detail: r.domain?.state === "live" ? "It’s live already." : "Next: point it at your box with the records below.",
        action: id ? { label: "Undo", run: () => undoChange(id).then(() => qc.invalidateQueries({ queryKey: ["domains", project] })) } : undefined,
      });
    },
  });
  const invalid = raw.trim() !== "" && !looksLikeDomain(domain);
  const dup = taken.includes(domain);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (looksLikeDomain(domain) && chosen && !dup) add.mutate();
  };
  const noApps = !loading && apps.length === 0;

  return (
    <form onSubmit={submit} className="mt-8 rounded-[12px] border border-rule-2 bg-paper-raised px-4 py-4 shadow-[var(--top-light)] sm:px-5" aria-label="Add a domain">
      <label htmlFor="new-domain" className="text-[0.9375rem] font-[550] text-ink">
        Add a domain
      </label>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center">
        <Input
          id="new-domain"
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
          placeholder={`${project}.com`}
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
          aria-invalid={invalid || dup || undefined}
          disabled={noApps}
          className="ident text-[0.875rem] sm:flex-1"
        />
        {apps.length > 1 && (
          <div className="flex items-center gap-2 sm:w-52">
            <span className="shrink-0 text-[0.8125rem] text-ink-3">served by</span>
            <Select value={chosen} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} id="domain-app" />
          </div>
        )}
        <Button type="submit" variant="primary" size="lg" className="h-9" disabled={!looksLikeDomain(domain) || dup || !chosen || add.isPending}>
          {add.isPending ? "Adding…" : "Add"}
        </Button>
      </div>
      <div className="mt-2.5 min-h-5 text-[0.8125rem]">
        {noApps ? (
          <span className="text-ink-3">A domain shows one of the project’s web apps. Add one first.</span>
        ) : invalid ? (
          <span className="text-danger">That doesn’t look like a domain. Use a name like {project}.com or shop.example.com.</span>
        ) : dup ? (
          <span className="text-ink-3">{domain} is already here.</span>
        ) : apex ? (
          <label className="inline-flex cursor-pointer items-center gap-2 text-ink-2">
            <Checkbox checked={www} onCheckedChange={(v) => setWww(v === true)} />
            Also send <span className="ident text-[0.8125rem] text-ink">www.{domain}</span> here
          </label>
        ) : (
          <span className="text-ink-3">{apps.length === 1 ? `It will show ${chosen}. ` : ""}A whole domain (example.com) or a name under one (shop.example.com).</span>
        )}
      </div>
      {add.isError && <ProblemNote className="mt-3" error={add.error} />}
    </form>
  );
}

// ───────────────────────── a domain ─────────────────────────

/** "Shows web", or with paths "Shows web; /help shows docs". */
function routeWords(d: Domain): string {
  const routes = d.routes ?? [];
  const root = routes.find((r) => r.path === "/" || r.path === "");
  const rest = routes.filter((r) => r !== root).map((r) => `${r.path} shows ${r.app}`);
  return [root ? `Shows ${root.app}` : "", ...rest].filter(Boolean).join("; ");
}

function DomainRow({ project, d, www, writer, fresh, local }: { project: string; d: Domain; www?: Domain; writer: boolean; fresh: boolean; local: boolean }) {
  const qc = useQueryClient();
  const [removing, setRemoving] = useState(false);
  const pending = usePending(project);
  const ref = useRef<HTMLLIElement>(null);
  const s = stateWords(d);
  const apex = isApex(d.domain) && !d.redirectTo;
  const wwwOn = !!www || d.wwwRedirect;
  const staged = pendingFor(pending, `set:domains/${d.domain}`);
  const shownWww = staged && staged.kind === "set" ? staged.to !== undefined : wwwOn;
  // On a box without a public address the note at the top says it all.
  const needs = !local && (d.state !== "live" || (www && www.state !== "live"));

  useEffect(() => {
    if (fresh) ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [fresh]);

  const flipWww = (to: "on" | "off") =>
    change(
      project,
      {
        kind: "set",
        path: ["domains", d.domain],
        from: wwwOn ? { www: "redirect" } : undefined,
        to: to === "on" ? { www: "redirect" } : undefined,
        what: to === "on" ? `Add the www.${d.domain} redirect` : `Remove the www.${d.domain} redirect`,
        undo: to === "on" ? `www.${d.domain} stops redirecting` : `www.${d.domain} redirects again`,
      },
      { immediate: true },
    );

  return (
    <li ref={ref} className={cn("py-3.5", fresh && "animate-[fade-in_400ms_both]")}>
      <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-x-3">
        <span className="mt-[7px]">
          <StatusDot tone={s.tone} />
        </span>
        <div className="min-w-0">
          {d.state === "live" ? (
            <a href={d.url} target="_blank" rel="noopener noreferrer" className="group ident inline-flex max-w-full items-center gap-1 text-[0.875rem] text-ink hover:text-brass-ink">
              <span className="truncate">{d.domain}</span>
              <ArrowUpRight className="size-3.5 shrink-0 text-ink-3 group-hover:text-brass-ink" />
            </a>
          ) : (
            <p className="ident truncate text-[0.875rem] text-ink">{d.domain}</p>
          )}
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            <span className={cn(s.tone === "bad" ? "text-danger" : s.tone === "busy" ? "text-brass-ink" : "text-ink-2")}>{s.word}</span>
            {d.redirectTo ? <> · redirects to {d.redirectTo}</> : routeWords(d) && <> · {routeWords(d)}</>}
          </p>
          {s.detail && s.tone === "ok" && <p className="mt-1 text-[0.8125rem] text-warn-ink">{s.detail}</p>}
        </div>
        {writer && (
          <Button variant="ghost" size="sm" onClick={() => setRemoving(true)} className="-mr-2 hover:text-danger">
            Remove…
          </Button>
        )}
      </div>

      {apex && (
        <div className="mt-2.5 ml-5 flex flex-wrap items-center gap-x-3 gap-y-1">
          <label className="inline-flex items-center gap-2.5 text-[0.8125rem] text-ink-2">
            <Breaker label={`Redirect www.${d.domain} here`} state={wwwOn ? "on" : "off"} staged={staged && staged.kind === "set" ? (staged.to === undefined ? "off" : "on") : undefined} onFlip={flipWww} disabled={!writer} />
            <span>
              Send <span className="ident text-[0.75rem] text-ink">www.{d.domain}</span> here
            </span>
          </label>
          {www && shownWww && www.state !== "live" && d.state === "live" && <span className="text-[0.8125rem] text-ink-3">· www: {stateWords(www).word.toLowerCase()}</span>}
        </div>
      )}

      {needs && <Setup project={project} d={d} www={www} writer={writer} />}

      <Confirm
        open={removing}
        onClose={() => setRemoving(false)}
        title={`Stop serving ${d.domain}?`}
        body={
          <>
            Visitors to {d.domain}
            {wwwOn ? ` and www.${d.domain}` : ""} get an error from now on. Your DNS records stay as they are. You can add it again any time.
          </>
        }
        action="Remove domain"
        run={() => removeDomain(project, d.domain)}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["domains", project] });
          for (const k of [["manifest", project], ["project", project], ["changes"]]) void qc.invalidateQueries({ queryKey: k });
          toast({ title: `${d.domain} is no longer served.` });
        }}
      />
    </li>
  );
}

/** What's left before a domain is live: the records to add (or one button), then a calm watch. */
function Setup({ project, d, www, writer }: { project: string; d: Domain; www?: Domain; writer: boolean }) {
  const qc = useQueryClient();
  const [byHand, setByHand] = useState(false);
  const [plain, setPlain] = useState(false);
  const pending = [d, www].filter((x): x is Domain => !!x && x.state !== "live");
  const waiting = pending.filter((x) => x.state === "waiting_for_dns");
  const failed = pending.find((x) => x.state === "error");
  const issuing = pending.length > 0 && pending.every((x) => x.state === "issuing");
  const cname = waiting.some((x) => x.alternative?.length);
  const rows: Row[] = waiting.flatMap((x) => (x.alternative?.length && !plain ? x.alternative : (x.records ?? [])));
  const managedBy = waiting.find((x) => x.managedBy)?.managedBy;
  const noRecords = waiting.length > 0 && rows.length === 0; // a local box has no address to point at
  const checked = pending.map((x) => x.checkedAt).filter(Boolean).sort()[0];
  const why = waiting.length === 1 && waiting[0].reason && !/not checked/i.test(waiting[0].reason) ? `${stateWords(waiting[0]).detail} ` : "";

  const check = useMutation({
    mutationFn: () => Promise.all(pending.map((x) => recheckDomain(project, x.domain))),
    onSettled: () => qc.invalidateQueries({ queryKey: ["domains", project] }),
  });
  const auto = useMutation({
    mutationFn: () => setRecords(waiting.flatMap((x) => x.records ?? [])),
    onSuccess: () => {
      toast({ title: `Added the records at ${providerName(managedBy)}.`, detail: "HTTPS follows in a minute or two." });
      check.mutate();
    },
  });

  return (
    <div className="mt-3 ml-5 rounded-[10px] bg-paper-sunk px-4 py-3.5">
      {failed ? (
        <>
          <p className="text-[0.875rem] text-ink">{stateWords(failed).detail}</p>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">The box tries again on its own{failed.nextCheckAt ? `, next ${relativeSoon(failed.nextCheckAt)}` : ""}.</p>
          {writer && (
            <Button size="sm" className="mt-3" onClick={() => check.mutate()} disabled={check.isPending}>
              {check.isPending ? "Checking…" : "Try again now"}
            </Button>
          )}
        </>
      ) : issuing ? (
        <Watching checkedAt={checked}>It points at your box. Getting its certificate, usually under a minute.</Watching>
      ) : noRecords ? (
        <p className="text-[0.875rem] text-ink-2">{stateWords(waiting[0]).detail}</p>
      ) : (
        <>
          {managedBy && writer && !byHand ? (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              <p className="min-w-0 flex-1 basis-64 text-[0.875rem] text-ink">
                {why}
                {providerName(managedBy)} looks after {zoneOf(d.domain)}, so the box can add the records itself.
              </p>
              <Button variant="primary" size="md" onClick={() => auto.mutate()} disabled={auto.isPending}>
                {auto.isPending ? "Adding…" : "Add the records for me"}
              </Button>
              <button type="button" onClick={() => setByHand(true)} className="text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                I’ll add them myself
              </button>
            </div>
          ) : (
            <>
              <p className="text-[0.875rem] text-ink">
                {why}
                Add {rows.length === 1 ? "this record" : "these records"} at your DNS host:
              </p>
              <RecordsTable className="mt-2.5" records={rows} />
              <div className="mt-2.5 flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
                <HostHints className="min-w-0 flex-1" />
                {cname && (
                  <button type="button" onClick={() => setPlain(!plain)} className="text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                    {plain ? "Use one record instead" : "Can’t add that one? Use addresses"}
                  </button>
                )}
              </div>
            </>
          )}
          {auto.isError && <ProblemNote className="mt-3" error={auto.error} />}
          <div className="mt-3 border-t border-rule pt-3">
            <Watching checkedAt={checked} checking={check.isPending} onCheck={writer ? () => check.mutate() : undefined}>
              Watching for them. New records usually show up within minutes; you can leave this page.
            </Watching>
          </div>
        </>
      )}
      {check.isError && !(check.error instanceof ApiError && check.error.status === 404) && <ProblemNote className="mt-3" error={check.error} />}
    </div>
  );
}

/**
 * While a domain isn't live and the page is open, ask the box to look again:
 * every 10 seconds for the first two minutes, then every 30. The box also
 * checks on its own (with backoff), so leaving the page is fine.
 */
function useRecheck(project: string, list: Domain[], writer: boolean) {
  const qc = useQueryClient();
  const waiting = list.filter((d) => d.state === "waiting_for_dns" && d.records?.length).map((d) => d.domain);
  const key = waiting.join(",");
  useEffect(() => {
    if (!writer || !key) return;
    const started = Date.now();
    let t: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (document.visibilityState === "visible") {
        await Promise.allSettled(key.split(",").map((d) => recheckDomain(project, d)));
        void qc.invalidateQueries({ queryKey: ["domains", project] });
      }
      t = setTimeout(tick, Date.now() - started < 120_000 ? 10_000 : 30_000);
    };
    t = setTimeout(tick, 10_000);
    return () => clearTimeout(t);
  }, [project, key, writer, qc]);
}

const zoneOf = (d: string) => d.split(".").slice(isApex(d) ? 0 : 1).join(".");

function relativeSoon(iso: string) {
  const s = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  if (s < 60) return "in under a minute";
  if (s < 3600) return `in ${Math.round(s / 60)} minutes`;
  return `in about ${Math.round(s / 3600)} hours`;
}
