import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { boxSettingsQuery, memWords, setBoxSettings } from "@/lib/usage";
import { useState, type ReactNode } from "react";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Segmented } from "@/components/health-kit";
import { Nameplate } from "@/components/nameplate";
import { Page, PageHeader } from "@/components/page";
import { ExportBox, ImportBox } from "@/components/settings-move";
import { BoxDomainSection } from "@/components/box-domain";
import { BoxEmailSection } from "@/components/email-relay";
import { BoxSize } from "@/components/box-size";
import { SignInProviders } from "@/components/signin-providers";
import { boxDomainQuery } from "@/lib/domains";
import { boxName, boxUp, tiffinStarted, versionLabel, whereItRuns } from "@/lib/box";
import { useMe } from "@/lib/me";
import { Breaker } from "@/components/breaker";
import { lastUpdate, nextWindow, setUpdateSettings, updateStatusQuery } from "@/lib/updates";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";

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
  useTitle("Settings · General");
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
  const boxDomain = useQuery(boxDomainQuery);
  const domain = boxDomain.data?.domain ?? location.hostname.replace(/^dashboard\./, "");
  const { admin, role } = useMe();
  const name = boxName(status.data);
  const build = health.data?.build;

  return (
    <Page>
      <PageHeader title="General" lede="Your box: what it is, how it looks, how to move it and keep it up to date." />

      <Section title="This box">
        <div className="border-y border-rule py-3">
          <Nameplate name={name} where={whereItRuns(status.data)} version={versionLabel(status.data)} uptime={boxUp(res.data?.uptimeSeconds)} domain={domain} />
        </div>
        <dl className="mt-1 grid grid-cols-[8rem_minmax(0,1fr)] text-[0.875rem]">
          {(
            [
              ["Host", <span className="ident">{status.data?.host.hostname ?? "…"}</span>],
              ["System", status.data ? `${status.data.host.os === "linux" ? "Linux" : status.data.host.os}, ${status.data.host.arch}` : "…"],
              res.data && ["Machine", `${res.data.cpu.count} CPUs, ${Math.round(res.data.memory.totalBytes / 1073741824)} GB memory, ${Math.round(res.data.disks.data.totalBytes / 1073741824)} GB data disk`],
              ["Dashboard", <span className="ident">{location.host}</span>],
              status.data && ["Tiffin started", tiffinStarted(status.data.uptime)],
            ].filter(Boolean) as Array<[string, ReactNode]>
          ).map(([k, v]) => (
            <div key={k} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5">
              <dt className="text-ink-3">{k}</dt>
              <dd className="min-w-0 text-ink">{v}</dd>
            </div>
          ))}
        </dl>
        <BoxSize server={res.data?.server} />
      </Section>

      <BoxDomainSection admin={admin} Wrap={Section} />

      {/* Email: the box-wide mail service and its delivery events (components/email-relay.tsx). */}
      <BoxEmailSection admin={admin} Wrap={Section} />

      <Section
        id="sign-in"
        title="Sign-in providers"
        note="A shortcut for side projects: set a provider’s keys once and any project can turn it on. People see this box’s app name on the provider’s screen, so a product with its own name uses its own keys, set on its Auth page. Users and sessions always stay in each project."
      >
        <SignInProviders admin={admin} />
      </Section>

      <Section title="Look and sound" note="Auto follows your system. Sounds are off until you turn them on, and stay on this browser.">
        <div className="flex flex-col gap-4">
          <Row label="Theme">
            <ThemeChoice />
          </Row>
          <Row label="Sounds" note="One at most: a low tone if the box goes down.">
            <Sounds />
          </Row>
        </div>
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

      <Updates admin={admin} version={versionLabel(status.data)} build={build} />
    </Page>
  );
}

/**
 * Updates: what runs, the last update, the next window and the automatic
 * switch. A box without the endpoint (older, or not an admin) shows how to
 * update by hand.
 */
function Updates({ admin, version, build }: { admin: boolean; version?: string; build?: string }) {
  const qc = useQueryClient();
  const st = useQuery({ ...updateStatusQuery, enabled: admin });
  const save = useMutation({
    mutationFn: (auto: boolean) => setUpdateSettings({ auto }),
    onSuccess: (r) => {
      qc.setQueryData(updateStatusQuery.queryKey, r);
      toast({ title: r.auto ? "New releases install by themselves in the maintenance window." : "Automatic updates are off. Releases wait for you." });
    },
  });
  const s = st.data;
  const last = s && lastUpdate(s);
  const auto = s && (save.isPending ? save.variables : s.auto);
  const rows: Array<[string, ReactNode] | false | undefined> = [
    ["Version", <>{version ?? "…"}{build && <span className="ident ml-2 text-ink-3">build {build.slice(0, 12)}</span>}</>],
    s?.running && ["Now", <span className="text-ink">Updating to Tiffin {s.running.to}…</span>],
    s && s.release && ["Last update", last ? <span className={last.bad ? "text-warn-ink" : undefined}>{last.words}</span> : "None yet"],
    s?.available && ["Available", <>
      Tiffin {s.available.version}
      {s.available.notes && <a className="ml-2 text-ink-3 underline underline-offset-2" href={s.available.notes} target="_blank" rel="noreferrer">What’s new</a>}
      {s.available.blocked && <span className="block text-[0.8125rem] text-ink-3">Needs an update by hand first: run tiffin up.</span>}
    </>],
    s && s.release && ["Next window", s.nextRun ? nextWindow(s.nextRun) : "No maintenance window yet"],
  ];
  return (
    <Section title="Updates" note="New releases install by themselves in the maintenance window, after a backup. Apps keep serving while Tiffin restarts.">
      <dl className="grid grid-cols-[8rem_minmax(0,1fr)] text-[0.875rem]">
        {(rows.filter(Boolean) as Array<[string, ReactNode]>).map(([k, v]) => (
          <div key={k} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5 first:border-t">
            <dt className="text-ink-3">{k}</dt>
            <dd className="min-w-0 text-ink">{v}</dd>
          </div>
        ))}
      </dl>
      {s && s.release ? (
        <div className="mt-4">
          <Row label="Install updates by themselves" note={s.nextRun ? undefined : "They wait for a maintenance window: tiffin update settings --window 04:00"}>
            <Breaker label="Install updates by themselves" state={auto ? "on" : "off"} disabled={save.isPending} onFlip={(v) => save.mutate(v === "on")} />
          </Row>
          {save.isError && <ProblemNote className="mt-3" error={save.error} />}
          <p className="mt-5 text-[0.8125rem] text-ink-3">To install the newest release now:</p>
          <Command className="mt-2" cmd="tiffin update apply" />
        </div>
      ) : (
        <>
          <p className="mt-4 text-[0.8125rem] text-ink-3">{s ? "This is a development build. Update it from your computer:" : "Update it from your computer:"}</p>
          <Command className="mt-2" cmd="tiffin up" />
        </>
      )}
    </Section>
  );
}

/**
 * The box-wide default limit: "no project may use more than N% unless it says
 * otherwise". Shown once the box has the setting (GET /v1/box/settings).
 */
export function ShareLimit({ admin, Wrap = Section }: { admin: boolean; Wrap?: typeof Section }) {
  const qc = useQueryClient();
  const settings = useQuery(boxSettingsQuery);
  const [drag, setDrag] = useState<number | null>(null);
  const save = useMutation({
    mutationFn: (p: number) => setBoxSettings({ defaultMaxSharePercent: p }),
    onSuccess: (r) => {
      qc.setQueryData(boxSettingsQuery.queryKey, r);
      setDrag(null);
      toast({ title: r.defaultMaxSharePercent && r.defaultMaxSharePercent < 100 ? `No project may use more than ${r.defaultMaxSharePercent}% of the box now.` : "Projects may grow to the whole box now." });
    },
  });
  if (!settings.data || settings.data.defaultMaxSharePercent === undefined) return null;
  const v = drag ?? settings.data.defaultMaxSharePercent;
  const means = v < 100 && settings.data.appMemoryMB ? `That’s about ${memWords((v / 100) * settings.data.appMemoryMB)} each.` : "Projects grow into whatever the box has free.";
  return (
    <Wrap title="Sharing the box" note="A project with its own limit keeps it. The rest grow as they need, up to this.">
      <p className="text-[0.875rem] text-ink">
        No project may use more than <b className="font-[550] tnum">{v}%</b> of the box unless it says otherwise.
      </p>
      <input
        type="range"
        min={10}
        max={100}
        step={5}
        value={v}
        disabled={!admin || save.isPending}
        onChange={(e) => setDrag(Number(e.target.value))}
        onPointerUp={() => drag !== null && save.mutate(drag)}
        onKeyUp={() => drag !== null && save.mutate(drag)}
        aria-label="Most of the box any one project may use"
        className="range mt-3 w-full max-w-[24rem]"
      />
      <p className="mt-1.5 text-sm text-ink-2">{means}</p>
      {settings.data.explanation && <p className="mt-1 text-xs text-ink-3">{settings.data.explanation}</p>}
      {save.isError && <ProblemNote className="mt-3" error={save.error} />}
    </Wrap>
  );
}

export function Section({ id, title, note, children }: { id?: string; title: string; note?: string; children: ReactNode }) {
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

/** Wires only the setting: the sound itself comes with outages. */
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
