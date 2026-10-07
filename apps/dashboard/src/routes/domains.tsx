import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Plus, Search } from "lucide-react";
import { Accordion } from "radix-ui";
import { useEffect, useMemo, useState } from "react";
import { q } from "@/api/queries";
import { AddDomainDialog } from "@/components/domains-add";
import { FreeAddresses } from "@/components/domains-free";
import { DomainsGuide } from "@/components/domains-guide";
import { useOpenDomain } from "@/components/domains-parts";
import { DOMAIN_COLS, DomainRow } from "@/components/domains-row";
import { useRecheck } from "@/components/domains-setup";
import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { boxDomainQuery, projectDomainsQuery, withWww } from "@/lib/domains";
import { useMe } from "@/lib/me";
import { rememberProject } from "@/lib/recent";

/**
 * Project › Domains: every address the project answers at. Its own domains
 * first (state in words, the app each shows, the certificate), each opening
 * to the exact records and a live check while it waits; then the free
 * addresses on the box, which always work; then how it all fits together.
 * The open domain is in the URL (?domain=example.com).
 */
export function DomainsPage({ project }: { project: string }) {
  useTitle(`${project} · Domains`);
  useEffect(() => rememberProject(project), [project]);
  const qc = useQueryClient();
  const list = useQuery(projectDomainsQuery(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const box = useQuery(boxDomainQuery);
  const { can, admin } = useMe();
  const writer = can("apply:reversible");
  const [adding, setAdding] = useState(false);
  const [filter, setFilter] = useState("");
  const [openDomain, setOpenDomain] = useOpenDomain(project);

  const webApps = useMemo(
    () =>
      Object.entries(m.data?.manifest.apps ?? {})
        .filter(([, a]) => a.role !== "worker")
        .map(([n]) => n)
        .sort((a, b) => (a === "web" ? -1 : b === "web" ? 1 : a.localeCompare(b))),
    [m.data],
  );
  const domains = list.data ?? [];
  const rows = withWww(domains);
  const shown = filter ? rows.filter((r) => r.d.domain.includes(filter.trim().toLowerCase())) : rows;
  const local = box.data?.certificates === "internal";
  const every = useRecheck(project, domains, writer);

  // A route change (another app, a path, www) lands in the manifest first; refresh the list when it does.
  const version = m.data?.version;
  useEffect(() => {
    if (version !== undefined) void qc.invalidateQueries({ queryKey: ["domains", project] });
  }, [version, project, qc]);

  const live = rows.filter((r) => r.d.state === "live" && (!r.www || r.www.state === "live")).length;
  const problems = rows.filter((r) => r.d.state === "error" || r.www?.state === "error").length;
  const waiting = rows.length - live - problems;
  const tally = [live && `${live} live`, waiting && `${waiting} setting up`, problems && `${problems} with a problem`].filter(Boolean).join(" · ");
  const noApps = m.data && webApps.length === 0;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Domains" }]} />}
        title="Domains"
        lede={`Where people reach ${project}. Use a domain you own; HTTPS comes with it and renews itself.`}
        actions={
          writer && (
            <Button variant="primary" size="lg" onClick={() => setAdding(true)} disabled={!m.data || !!noApps}>
              <Plus />
              Add domain
            </Button>
          )
        }
      />

      {local && (
        <p className="mt-6 max-w-[46rem] rounded-[10px] bg-paper-sunk px-4 py-3 text-[0.875rem] text-ink-2">
          This box runs on your computer, so the internet can’t reach it yet. You can add domains now; they start working once the box runs on a server with a public address.
        </p>
      )}
      {!writer && list.data && (
        <p className="mt-6 max-w-[46rem] text-[0.8125rem] text-ink-3">You can see these domains. Adding or changing them needs a key that can apply changes to {project}.</p>
      )}

      <section className="mt-10" aria-labelledby="own-domains">
        <div className="mb-2.5 flex min-h-8 flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <h2 id="own-domains" className="label">
            Your domains
          </h2>
          <div className="flex items-center gap-3">
            {tally && <span className="text-[0.8125rem] text-ink-3">{tally}</span>}
            {rows.length > 6 && (
              <label className="flex h-8 w-52 items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
                <Search className="size-3.5 shrink-0 text-ink-3" aria-hidden />
                <input
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  placeholder="Find a domain"
                  aria-label="Find a domain"
                  spellCheck={false}
                  className="min-w-0 flex-1 bg-transparent text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4"
                />
              </label>
            )}
          </div>
        </div>

        {list.isError ? (
          <ProblemNote error={list.error} title="The domains can’t be listed right now." />
        ) : !list.data ? (
          <div className="grid gap-2 border-t border-rule py-3">
            <Skeleton className="h-10" />
            <Skeleton className="h-10" />
          </div>
        ) : rows.length === 0 ? (
          <NoDomains project={project} writer={writer} noApps={!!noApps} onAdd={() => setAdding(true)} />
        ) : (
          <div role="table" aria-label="Your domains">
            <div role="row" className={cn("hidden gap-x-4 border-b border-rule pb-2 sm:grid", DOMAIN_COLS)}>
              {["Domain", "Shows", "Status", "HTTPS"].map((h, i) => (
                <span key={h} role="columnheader" className={cn("label text-ink-3", i === 0 ? "pl-[22px]" : "")}>
                  {h}
                </span>
              ))}
              <span role="columnheader" className="sr-only">
                Actions
              </span>
            </div>
            <Accordion.Root type="single" collapsible value={openDomain ?? ""} onValueChange={(v) => setOpenDomain(v || undefined)} asChild>
            <ul className="divide-y divide-rule border-b border-rule max-sm:border-t">
              {shown.map(({ d, www }) => (
                <DomainRow
                  key={d.domain}
                  project={project}
                  d={d}
                  www={www}
                  writer={writer}
                  local={local}
                  every={every}
                  open={openDomain === d.domain}
                  onToggle={() => setOpenDomain(openDomain === d.domain ? undefined : d.domain)}
                  apps={webApps}
                  manifest={m.data?.manifest}
                  admin={admin}
                />
              ))}
            </ul>
            </Accordion.Root>
            {shown.length === 0 && <p className="py-6 text-[0.875rem] text-ink-3">No domain matches “{filter}”.</p>}
          </div>
        )}
      </section>

      {webApps.length > 0 && box.data && (
        <section className="mt-12" aria-labelledby="free-addresses">
          <div className="mb-2.5 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h2 id="free-addresses" className="label">
              Free addresses
            </h2>
            <span className="text-[0.8125rem] text-ink-3">Included with your box. They work whatever domains you add.</span>
          </div>
          <FreeAddresses project={project} manifest={m.data?.manifest} box={box.data} />
        </section>
      )}

      <DomainsGuide box={box.data} admin={admin} />

      <AddDomainDialog
        open={adding}
        onOpenChange={setAdding}
        project={project}
        apps={webApps}
        taken={domains.map((d) => d.domain)}
        box={box.data}
        list={domains}
        local={local}
        every={every}
        onDone={(d) => setOpenDomain(d)}
      />
    </Page>
  );
}

function NoDomains({ project, writer, noApps, onAdd }: { project: string; writer: boolean; noApps: boolean; onAdd: () => void }) {
  return (
    <div className="rounded-[10px] border border-dashed border-rule-3 px-6 py-10 text-center">
      <div className="mx-auto mb-3 grid size-9 place-items-center rounded-full bg-paper-sunk text-ink-3">
        <Globe className="size-[18px]" />
      </div>
      <p className="text-[0.9375rem] font-[550] text-ink">No domains of your own yet</p>
      <p className="mx-auto mt-1 max-w-[30rem] text-[0.875rem] text-ink-2">
        {noApps
          ? `A domain shows one of ${project}’s web apps, and it has none yet. Deploy a web app first.`
          : `Point a domain you own, like ${project}.com, at this project. You’ll get the exact DNS records to set, and HTTPS follows by itself.`}
      </p>
      {writer && !noApps && (
        <Button variant="primary" className="mt-5" onClick={onAdd}>
          <Plus />
          Add domain
        </Button>
      )}
    </div>
  );
}
