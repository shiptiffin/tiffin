import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { Page, PageHeader } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { boxName, versionLabel, whereItRuns } from "@/lib/box";
import { cn } from "@/lib/cn";
import { ENAMELS, enamelNames, useEnamels, type Enamel } from "@/lib/enamel";
import { duration } from "@/lib/format";
import { useMe } from "@/lib/me";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";

/** Settings: the box itself (name, where it runs, version), how it looks, moving it, updating it. */
export function SettingsPage() {
  useTitle("Settings");
  const status = useQuery(q.status());
  const res = useQuery(q.resources);
  const projects = useQuery(q.projects);
  const names = (projects.data ?? []).map((p) => p.name);
  const enamels = useEnamels(names);
  const { can } = useMe();
  return (
    <Page>
      <PageHeader title="Settings" lede="The box itself: what it is, how it looks, how to move it and keep it up to date." />

      <Section title="This box">
        <dl className="grid grid-cols-[9rem_minmax(0,1fr)] gap-y-2.5 text-[0.875rem]">
          <dt className="text-ink-3">Name</dt>
          <dd className="text-ink">{boxName(status.data)}</dd>
          <dt className="text-ink-3">Runs on</dt>
          <dd className="text-ink">
            {whereItRuns(status.data) ?? "…"}
            {status.data && <span className="ident ml-2 text-ink-3">{status.data.host.hostname}</span>}
          </dd>
          <dt className="text-ink-3">Version</dt>
          <dd className="text-ink">{versionLabel(status.data) ?? "…"}</dd>
          <dt className="text-ink-3">Up for</dt>
          <dd className="text-ink">{res.data ? duration(res.data.uptimeSeconds) : (status.data?.uptime ?? "…")}</dd>
        </dl>
      </Section>

      <Section title="Theme" note="Auto follows your system.">
        <ThemeChoice />
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

      <Section title="Move this box" note="One archive holds every project, database, bucket and deploy. Secrets stay encrypted to the box key, so keep the key with it.">
        <Command cmd="tiffin box export box.tiffin --key-out box.key" />
        <Command cmd="tiffin box import box.tiffin --key-file box.key" className="mt-2" />
      </Section>

      <Section title="Update" note="Brings the box to the version of Tiffin you have installed. Apps keep serving while it restarts.">
        <Command cmd="tiffin up" />
      </Section>
    </Page>
  );
}

function Section({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <section className="mt-10 grid gap-x-10 gap-y-3 border-t border-rule pt-6 md:grid-cols-[13rem_minmax(0,1fr)]">
      <div>
        <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
        {note && <p className="mt-1 text-sm text-ink-3">{note}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </section>
  );
}

function ThemeChoice() {
  const { pref } = useTheme();
  const opts: Array<{ v: ThemePref; label: string }> = [
    { v: "system", label: "Auto" },
    { v: "light", label: "Light" },
    { v: "dark", label: "Dark" },
  ];
  return (
    <div role="radiogroup" aria-label="Theme" className="flex w-max gap-0.5 rounded-[8px] border border-rule-2 p-0.5">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={pref === o.v}
          onClick={() => setTheme(o.v)}
          className={cn("rounded-[6px] px-3 py-1 text-sm text-ink-2 transition-colors hover:text-ink", pref === o.v && "bg-paper-sunk font-[550] text-ink")}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function ColourRow({ project, enamel, canSet }: { project: string; enamel: Enamel; canSet: boolean }) {
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (e: Enamel) => api.setAppearance(project, e),
    onSuccess: (a) => qc.setQueryData(["appearance", project], a),
  });
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
      <span className="flex w-32 items-center gap-2 text-[0.875rem] text-ink">
        <EnamelSwatch enamel={enamel} size={9} />
        {project}
      </span>
      <div role="radiogroup" aria-label={`${project} colour`} className="flex gap-1.5">
        {ENAMELS.map((e) => (
          <button
            key={e}
            role="radio"
            aria-checked={e === enamel}
            aria-label={enamelNames[e]}
            title={enamelNames[e]}
            disabled={!canSet || set.isPending}
            onClick={() => set.mutate(e)}
            className={cn(
              "grid size-7 place-items-center rounded-[6px] border transition-colors disabled:cursor-not-allowed",
              e === enamel ? "border-ink-3" : "border-transparent hover:border-rule-2",
            )}
          >
            <EnamelSwatch enamel={e} size={14} />
          </button>
        ))}
      </div>
      {set.isError && <ProblemNote error={set.error} className="w-full" />}
    </li>
  );
}
