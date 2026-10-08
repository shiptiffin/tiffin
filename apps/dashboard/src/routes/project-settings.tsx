import { useQuery } from "@tanstack/react-query";
import { Link, Navigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { ToggleGroup } from "radix-ui";
import { useEffect, useState, type ReactNode } from "react";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { SERVICES, ServiceRow, Working, type SetEdit } from "@/components/project-rows";
import { Button } from "@/components/ui/button";
import { useMe } from "@/lib/me";
import { DangerZone } from "@/components/danger-zone";
import { rememberProject } from "@/lib/recent";
import { change, pendingFor, usePending } from "@/lib/staged";
import { CreateKeyDialog, KeyList, keyProjects, onlyKeys } from "./keys";
import { ProjectIconSettings } from "@/components/project-icon-settings";
import { CopyAndMove, StoppedNote } from "@/components/project-copy";
import { AppsSettings } from "@/components/settings-apps";
import { SettingsNav, useSettingsSection, type SettingsSection } from "@/components/settings-nav";

/**
 * A project's settings, one section at a time (the sub-navigation keeps the
 * section in the URL's #hash): General (icon, sleep), Apps (how each runs and
 * builds), Services (sign-in on or off; the other parts are always there), API keys, Copy & move,
 * and the Danger zone (Delete all data in a part, its restore, Delete project). Environment variables and domains have their own pages.
 */
export function ProjectSettingsPage({ project }: { project: string }) {
  useTitle(`${project} · Settings`);
  useEffect(() => rememberProject(project), [project]);
  const p = useQuery(q.project(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const pending = usePending(project);
  const man = m.data?.manifest;
  const apps = Object.entries(man?.apps ?? {});
  const svc = (man?.services ?? {}) as Record<string, unknown>;
  const crons = Object.entries(man?.crons ?? {});
  const queues = Object.entries(man?.queues ?? {});
  const status = p.data?.status ?? {};
  const setEdits = pending.filter((e): e is SetEdit => e.kind === "set");
  const pendingSet = (path: string[]) => setEdits.find((e) => e.path.join("/") === path.join("/"));
  const sections: SettingsSection[] = [
    { id: "general", label: "General" },
    ...(apps.length > 0 ? [{ id: "apps", label: apps.length === 1 ? "App" : "Apps" }] : []),
    { id: "services", label: "Services" },
    { id: "keys", label: "API keys" },
    { id: "copy", label: "Copy & move" },
    { id: "danger", label: "Danger zone" },
  ];
  const active = useSettingsSection(sections);

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Settings" }]} />} title="Settings" />
      <StoppedNote project={project} state={p.data} />
      {m.isError && <ProblemNote className="mt-6" error={m.error} title="The project’s config can’t be read." />}

      <div className="mt-8 grid gap-6 lg:grid-cols-[11rem_minmax(0,1fr)] lg:gap-12">
        <SettingsNav project={project} sections={sections} active={active} />
        <div className="max-w-[52rem] min-w-0">
          {active === "general" && (
            <>
              <Section title="Icon" first note="Shown beside the project’s name everywhere: the sidebar, ⌘K, lists, and its emails.">
                <ProjectIconSettings project={project} />
              </Section>

              {apps.length > 0 && (
                <Section title="When nobody visits" note="Its apps are always awake unless you let them sleep, which frees their memory for your other projects. The next visit, job or schedule wakes them in a few seconds.">
                  <SleepAfter project={project} live={man?.sleepAfter} staged={pendingSet(["sleepAfter"])} />
                </Section>
              )}
            </>
          )}

          {active === "apps" && (
            <Section title={apps.length === 1 ? "App" : "Apps"} note="How each app runs and where its code comes from. The same settings are on each app’s own page." first>
              {m.data ? <AppsSettings project={project} manifest={man} /> : <Skeleton className="h-40" />}
            </Section>
          )}

          {active === "services" && (
            <>
              <Section
                title="Built-in parts"
                note="Database, KV, Files, Email, Analytics and Jobs are always there; to start one over, use Delete all data in the Danger zone. Sign-in is the one you add."
                first
              >
                <div className="divide-y divide-rule border-y border-rule">
                  {SERVICES.map((s) => (
                    <ServiceRow
                      key={s.key}
                      project={project}
                      s={s}
                      live={!(s.key in svc) ? "off" : status[`service/${s.key}`]?.state === "failed" ? "tripped" : "on"}
                      message={status[`service/${s.key}`]?.message}
                      staged={pendingFor(pending, `service:${s.key}`)}
                    />
                  ))}
                </div>
              </Section>
              {(crons.length > 0 || queues.length > 0) && (
                <p className="mt-10 border-t border-rule pt-4 text-[0.8125rem] text-ink-3">
                  Its {[crons.length > 0 && "schedules", queues.length > 0 && "queues"].filter(Boolean).join(" and ")} are on the{" "}
                  <Link to={crons.length > 0 ? "/projects/$project/jobs/schedules" : "/projects/$project/jobs/queues"} params={{ project }} className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                    Jobs
                  </Link>{" "}
                  page.
                </p>
              )}
              {!m.data && !m.isError && <Skeleton className="mt-8 h-40" />}
            </>
          )}

          {active === "keys" && <ProjectKeys project={project} />}

          {active === "copy" && (
            <Section title="Copy & move" note="Make a copy of it here, take it with you as a file, or move it to another box." first>
              <CopyAndMove project={project} />
            </Section>
          )}

          {active === "danger" && (
            <Section title="Danger zone" first>
              <DangerZone project={project} />
            </Section>
          )}
        </div>
      </div>
    </Page>
  );
}

/** The API keys that reach this project (its own and the all-projects ones), and Create key for it. */
function ProjectKeys({ project }: { project: string }) {
  const tokens = useQuery({ ...q.tokens, retry: false });
  const [open, setOpen] = useState(false);
  const { admin } = useMe();
  if (!admin || tokens.isError)
    return (
      <Section title="API keys" note="For Claude Code, other agents and scripts." first>
        <Link to="/settings/keys" className="inline-flex items-center gap-1 text-[0.875rem] text-ink-2 hover:text-ink">
          API keys
          <ChevronRight className="size-4 text-ink-4" />
        </Link>
      </Section>
    );
  const keys = onlyKeys(tokens.data ?? []).filter((t) => {
    const p = keyProjects(t as never);
    return p === "all" || p.includes(project);
  });
  return (
    <Section title="API keys" note="Keys that can reach this project, for Claude Code, other agents and scripts. Whatever a key does shows up in History under its name." first>
      {tokens.isPending ? <Skeleton className="h-16" /> : <KeyList keys={keys} empty={`No keys reach ${project} yet.`} />}
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button size="md" onClick={() => setOpen(true)}>
          Create key for {project}
        </Button>
        <Link to="/settings/keys" className="inline-flex items-center gap-1 text-[0.875rem] text-ink-2 hover:text-ink">
          All API keys
          <ChevronRight className="size-4 text-ink-4" />
        </Link>
      </div>
      <CreateKeyDialog open={open} onOpenChange={setOpen} project={project} />
    </Section>
  );
}

const SLEEP: Array<{ value?: string; title: string }> = [
  { title: "Never" },
  { value: "24h", title: "After 24 hours" },
  { value: "7d", title: "After 7 days" },
  { value: "14d", title: "After 14 days" },
];

/** "24h", "1d" → 24; absent → 0. */
const sleepHours = (v?: string) => {
  const m = v?.match(/^(\d+)([hd])$/);
  return m ? Number(m[1]) * (m[2] === "d" ? 24 : 1) : 0;
};

/** How long its apps may go unused before they sleep: sleepAfter in its config, never by default. */
function SleepAfter({ project, live, staged }: { project: string; live?: string; staged?: SetEdit }) {
  const value = staged ? (staged.to as string | undefined) : live;
  const choices = [...SLEEP];
  // A time set in tiffin.config.ts that the dashboard doesn't offer stays shown as it is.
  if (live && !SLEEP.some((c) => sleepHours(c.value) === sleepHours(live))) choices.push({ value: live, title: `After ${live}` });
  const label = (v?: string) => choices.find((c) => sleepHours(c.value) === sleepHours(v))?.title.toLowerCase() ?? v ?? "never";
  const pick = (to?: string) =>
    change(
      project,
      {
        kind: "set",
        path: ["sleepAfter"],
        from: live,
        to,
        what: to ? `Let ${project}’s apps sleep ${label(to)} without visitors` : `Keep ${project}’s apps always awake`,
        undo: live ? `${project}’s apps sleep ${label(live)} without visitors again` : `${project}’s apps stay awake again`,
      },
      { immediate: true },
    );
  return (
    // A single-choice toggle group (role radiogroup): arrow keys only move focus,
    // since every pick is a saved change; Space or Enter picks.
    <ToggleGroup.Root
      type="single"
      aria-label={`When ${project}’s apps sleep`}
      value={choices.find((c) => sleepHours(c.value) === sleepHours(value))?.title ?? ""}
      onValueChange={(t) => {
        const c = choices.find((x) => x.title === t);
        if (c) pick(c.value);
      }}
      className="flex flex-wrap gap-1.5"
    >
      {choices.map((c) => (
        <ToggleGroup.Item
          key={c.title}
          value={c.title}
          className="group flex items-center gap-2.5 rounded-[10px] border border-rule-2 px-3 py-2 text-left transition-colors duration-[var(--dur-state)] hover:border-rule-3 data-[state=on]:border-brass data-[state=on]:bg-brass-wash"
        >
          <span className="grid size-4 shrink-0 place-items-center rounded-full border border-rule-3 group-data-[state=on]:border-brass" aria-hidden>
            <span className="hidden size-2 rounded-full bg-brass group-data-[state=on]:block" />
          </span>
          <span className="text-[0.875rem] font-[550] text-ink">{c.title}</span>
        </ToggleGroup.Item>
      ))}
      {staged && <Working> Saving…</Working>}
    </ToggleGroup.Root>
  );
}

function Section({ title, note, first, children }: { title: string; note?: string; first?: boolean; children: ReactNode }) {
  return (
    <section className={first ? "" : "mt-10"} aria-label={title}>
      <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
      {note && <p className="mt-0.5 mb-3 max-w-[40rem] text-[0.8125rem] text-ink-3">{note}</p>}
      {!note && <div className="mb-3" />}
      {children}
    </section>
  );
}

// ───────────────────────── secrets ─────────────────────────

/** Secrets moved to Environment Variables: the old address opens that page. */
export function SecretsPage({ project }: { project: string }) {
  return <Navigate to="/projects/$project/env" params={{ project }} replace />;
}
