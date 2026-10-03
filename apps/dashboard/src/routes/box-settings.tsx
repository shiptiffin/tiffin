import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { Segmented } from "@/components/health-kit";
import { Nameplate } from "@/components/nameplate";
import { Page, PageHeader } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ExportBox, ImportBox } from "@/components/settings-move";
import { toast } from "@/components/toast";
import { boxName, versionLabel, whereItRuns } from "@/lib/box";
import { ENAMELS, enamelNames, useEnamels, type Enamel } from "@/lib/enamel";
import { duration } from "@/lib/format";
import { useMe } from "@/lib/me";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";
import { uptime } from "@/lib/time";

const SOUNDS = "tiffin.sounds";
function readSounds(): boolean {
  try {
    return localStorage.getItem(SOUNDS) === "on";
  } catch {
    return false;
  }
}

/** Settings: the box itself (its nameplate and facts), how it looks and sounds, project colours, moving it, updating it. */
export function SettingsPage() {
  useTitle("Settings");
  const status = useQuery(q.status());
  const res = useQuery(q.resources);
  const projects = useQuery(q.projects);
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => fetch("/v1/health").then((r) => r.json() as Promise<{ version: string; build?: string }>),
    staleTime: 300_000,
    retry: false,
  });
  const names = (projects.data ?? []).map((p) => p.name);
  const enamels = useEnamels(names);
  // The box's domain, from where its storage answers ("S3 at s3.tiffin.localhost").
  const s3 = status.data?.checks?.find((c) => c.name === "storage")?.detail?.match(/\bs3\.([\w.-]+)/)?.[1];
  const domain = s3 ?? location.hostname.replace(/^dashboard\./, "");
  const { can, admin, role } = useMe();
  const name = boxName(status.data);
  const up = res.data ? duration(res.data.uptimeSeconds) : status.data ? uptime(status.data.uptime) : undefined;
  const build = health.data?.build;

  return (
    <Page>
      <PageHeader title="Settings" lede="The box itself: what it is, how it looks, how to move it and keep it up to date." />

      <Section title="This box">
        <div className="border-y border-rule py-3">
          <Nameplate name={name} where={whereItRuns(status.data)} version={versionLabel(status.data)} uptime={up ? `up ${up}` : undefined} domain={domain} />
        </div>
        <dl className="mt-1 grid grid-cols-[8rem_minmax(0,1fr)] text-[0.875rem]">
          {(
            [
              ["Host", <span className="ident">{status.data?.host.hostname ?? "…"}</span>],
              ["System", status.data ? `${status.data.host.os === "linux" ? "Linux" : status.data.host.os}, ${status.data.host.arch}` : "…"],
              res.data && ["Machine", `${res.data.cpu.count} CPUs, ${Math.round(res.data.memory.totalBytes / 1073741824)} GB memory, ${Math.round(res.data.disks.data.totalBytes / 1073741824)} GB data disk`],
              ["Dashboard", <span className="ident">{location.host}</span>],
              status.data && ["Tiffin up for", uptime(status.data.uptime)],
            ].filter(Boolean) as Array<[string, ReactNode]>
          ).map(([k, v]) => (
            <div key={k} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5">
              <dt className="text-ink-3">{k}</dt>
              <dd className="min-w-0 text-ink">{v}</dd>
            </div>
          ))}
        </dl>
      </Section>

      <Section title="Look and sound" note="Auto follows your system. Sounds are off until you turn them on, and stay on this browser.">
        <div className="flex flex-col gap-4">
          <Row label="Theme">
            <ThemeChoice />
          </Row>
          <Row label="Sounds" note="Two at most: a soft seal when you sign an approval, a low tone if the box goes down.">
            <Sounds />
          </Row>
        </div>
      </Section>

      <Section title="Project colours" note="Each project has one enamel: its rim on the Box, its swatch in the sidebar and its share of the memory bar.">
        {projects.isError && <ProblemNote error={projects.error} />}
        <ul className="divide-y divide-rule border-y border-rule">
          {names.map((p) => (
            <ColourRow key={p} project={p} enamel={enamels[p]} canSet={can("apply:reversible")} />
          ))}
          {names.length === 0 && <li className="py-3 text-sm text-ink-3">No projects yet.</li>}
        </ul>
      </Section>

      <Section
        id="move"
        title="Move this box"
        note="One file holds every project, database, bucket, deploy, person and token. Secrets inside stay encrypted to the box key; keep the key with the file, or apart from it."
      >
        <h3 className="label mb-3">Export</h3>
        <ExportBox canExport={admin} />
        <h3 className="label mt-8 mb-3">Import</h3>
        <ImportBox boxName={name} projects={names} isOwner={role === "owner"} />
        <p className="mt-6 text-[0.8125rem] text-ink-3">From a terminal, the same thing:</p>
        <Command className="mt-2" cmd={`tiffin box export ${name}.tiffin --key-out ${name}.key`} />
        <Command cmd={`tiffin box import ${name}.tiffin --key-file ${name}.key`} className="mt-2" />
      </Section>

      <Section title="Updates" note="Brings the box to the Tiffin you have installed. Apps keep serving while it restarts.">
        <p className="text-[0.875rem] text-ink">
          {versionLabel(status.data) ?? "…"}
          {build && <span className="ident ml-2 text-ink-3">build {build.slice(0, 12)}</span>}
        </p>
        <Command className="mt-3" cmd="tiffin up" />
      </Section>
    </Page>
  );
}

function Section({ id, title, note, children }: { id?: string; title: string; note?: string; children: ReactNode }) {
  return (
    <section id={id} className="mt-10 grid scroll-mt-8 gap-x-10 gap-y-3 border-t border-rule-2 pt-6 md:grid-cols-[13rem_minmax(0,1fr)]">
      <div>
        <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
        {note && <p className="mt-1 text-[0.8125rem] text-ink-3">{note}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </section>
  );
}

function Row({ label, note, children }: { label: string; note?: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
      <div className="min-w-0">
        <p className="text-[0.875rem] text-ink">{label}</p>
        {note && <p className="max-w-[24rem] text-[0.8125rem] text-ink-3">{note}</p>}
      </div>
      {children}
    </div>
  );
}

function ThemeChoice() {
  const { pref } = useTheme();
  return (
    <Segmented<ThemePref>
      label="Theme"
      value={pref}
      onChange={setTheme}
      options={[
        { v: "system", label: "Auto" },
        { v: "light", label: "Light" },
        { v: "dark", label: "Dark" },
      ]}
    />
  );
}

/** Wires only the setting: the sounds themselves come with approvals and outages. */
function Sounds() {
  const [on, setOn] = useState(readSounds);
  return (
    <Segmented
      label="Sounds"
      value={on ? "on" : "off"}
      onChange={(v) => {
        setOn(v === "on");
        try {
          localStorage.setItem(SOUNDS, v);
        } catch {
          /* private window: it lasts for this page only */
        }
      }}
      options={[
        { v: "off", label: "Off" },
        { v: "on", label: "On" },
      ]}
    />
  );
}

function ColourRow({ project, enamel, canSet }: { project: string; enamel: Enamel; canSet: boolean }) {
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (e: Enamel) => api.setAppearance(project, e),
    onSuccess: (a, e) => {
      qc.setQueryData(["appearance", project], a);
      toast({
        title: `${project} is ${enamelNames[e].toLowerCase()} now.`,
        action: { label: "Undo", run: () => api.setAppearance(project, enamel).then((b) => qc.setQueryData(["appearance", project], b)) },
      });
    },
  });
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 py-2.5">
      <span className="flex w-32 items-center gap-2 text-[0.875rem] text-ink">
        <EnamelSwatch enamel={enamel} size={9} />
        {project}
      </span>
      <div role="radiogroup" aria-label={`${project} colour`} className="flex gap-1">
        {ENAMELS.map((e) => (
          <button
            key={e}
            role="radio"
            aria-checked={e === enamel}
            aria-label={enamelNames[e]}
            title={enamelNames[e]}
            disabled={!canSet || set.isPending}
            onClick={() => e !== enamel && set.mutate(e)}
            className={
              "grid size-7 place-items-center rounded-[6px] border transition-colors duration-[var(--dur-state)] disabled:cursor-not-allowed " +
              (e === enamel ? "border-ink-3" : "border-transparent hover:border-rule-2")
            }
          >
            <EnamelSwatch enamel={e} size={14} />
          </button>
        ))}
      </div>
      <span className="ml-auto text-[0.8125rem] text-ink-3 max-sm:hidden">{enamelNames[enamel]}</span>
      {set.isError && <ProblemNote error={set.error} className="w-full" />}
    </li>
  );
}
