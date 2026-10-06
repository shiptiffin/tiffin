import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, notOnBox, type SecretInfo } from "@/api/client";
import { q } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Addresses, ConfigRow, SERVICES, ServiceRow, StagedRow, type SetEdit } from "@/components/project-rows";
import { AddMenu } from "@/components/start-add-menu";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { count, cronWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import { DeleteProject } from "@/components/delete-project";
import { rememberProject } from "@/lib/recent";
import { pendingFor, undoChange, usePending } from "@/lib/staged";
import { relative } from "@/lib/time";
import { CreateKeyDialog, KeyList, keyProjects, onlyKeys } from "./keys";
import { ProjectIcon } from "@/components/project-icon";
import { DomainsLink } from "@/components/project-domains";
import { CopyAndMove, StoppedNote } from "@/components/project-copy";

/**
 * A project's settings: its name and colour, its addresses, the settings
 * and secrets its apps read, which built-in parts it has (turn one off to
 * remove it; anything that deletes data asks first), and its jobs' config.
 */
export function ProjectSettingsPage({ project }: { project: string }) {
  useTitle(`${project} · Settings`);
  useEffect(() => rememberProject(project), [project]);
  const p = useQuery(q.project(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const pending = usePending(project);
  const secrets = useQuery({ ...q.secrets(project), retry: false });
  const man = m.data?.manifest;
  const apps = Object.entries(man?.apps ?? {});
  const svc = (man?.services ?? {}) as Record<string, unknown>;
  const env = Object.entries(man?.env ?? {});
  const crons = Object.entries(man?.crons ?? {});
  const queues = Object.entries(man?.queues ?? {});
  const status = p.data?.status ?? {};
  const setEdits = pending.filter((e): e is SetEdit => e.kind === "set");
  const adding = (section: string, existing: string[]) => setEdits.filter((e) => e.path[0] === section && e.path.length === 2 && e.to !== undefined && !existing.includes(e.path[1]));
  const pendingSet = (path: string[]) => setEdits.find((e) => e.path.join("/") === path.join("/"));
  const routes = useMemo(() => [] as string[], []);
  const { admin } = useMe();

  return (
    <Page>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Settings" }]} />} title="Settings" />
      <StoppedNote project={project} state={p.data} />
      {m.isError && <ProblemNote className="mt-6" error={m.error} title="The project’s config can’t be read." />}

      <Section title="Name">
        <p className="flex items-center gap-2.5 text-[0.9375rem] font-[550] text-ink">
          <ProjectIcon project={project} size={20} />
          {project}
        </p>
        <p className="mt-1 text-[0.8125rem] text-ink-3">Its icon is its app’s own once it’s live; until then, a tiffin with a tier for each part.</p>
      </Section>

      {apps.length > 0 && (
        <Section title="Addresses" note="Where each app answers. They come from the app’s name; add your own domain too.">
          <Addresses project={project} apps={apps.filter(([, a]) => a.role !== "worker").map(([n]) => n)} />
          {apps.some(([, a]) => a.role !== "worker") && <DomainsLink project={project} />}
        </Section>
      )}

      <Section title="Settings for its apps" note="Plain values every app reads as environment variables. Changing one restarts the apps.">
        <div className="divide-y divide-rule border-y border-rule">
          {env.length === 0 && adding("env", []).length === 0 && <p className="py-3.5 text-sm text-ink-3">None yet.</p>}
          {env.map(([k, v]) => (
            <ConfigRow key={k} project={project} path={["env", k]} name={<span className="ident text-[0.8125rem]">{k}</span>} status={<span className="ident text-[0.75rem] break-all text-ink-2">{v}</span>} value={v} staged={pendingSet(["env", k])} />
          ))}
          {adding(
            "env",
            env.map(([k]) => k),
          ).map((e) => (
            <StagedRow key={e.path.join("/")} e={e} name={<span className="ident text-[0.8125rem]">{e.path[1]}</span>} sub={String(e.to)} />
          ))}
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <AddMenu project={project} manifest={man} routes={routes} only="env" trigger={<Button size="md">Add a setting…</Button>} />
          {!notOnBox(secrets.error) && (
            <Link to="/projects/$project/secrets" params={{ project }} className="group inline-flex items-center gap-1 text-[0.875rem] text-ink-2 hover:text-ink">
              Secrets{secrets.data ? ` (${secrets.data.length})` : ""}: keys and passwords, never shown back
              <ChevronRight className="size-4 text-ink-4" />
            </Link>
          )}
        </div>
      </Section>

      <Section title="Built-in parts" note="Turn one on and it’s ready in seconds. Turning off something that holds data asks first and says what would be lost.">
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

      <ProjectKeys project={project} />

      {(crons.length > 0 || queues.length > 0) && (
        <Section title="Jobs" note="Schedules and queues, as the config declares them.">
          <div className="divide-y divide-rule border-y border-rule">
            {crons.map(([name, c]) => (
              <ConfigRow key={name} project={project} path={["crons", name]} name={name} sub="Schedule" status={<>Calls {c.url ? <span className="ident text-[0.75rem]">{c.url}</span> : <>{c.app} at <span className="ident text-[0.75rem]">{c.path}</span></>} {cronWords(c.schedule)}.</>} value={c} staged={pendingSet(["crons", name])} to="/projects/$project/jobs/schedules" />
            ))}
            {queues.map(([name, qq]) => (
              <ConfigRow key={name} project={project} path={["queues", name]} name={name} sub="Queue" status={<>Delivers to {qq.url ? <span className="ident text-[0.75rem]">{qq.url}</span> : <>{qq.app} at <span className="ident text-[0.75rem]">{qq.path}</span></>}, up to {count(qq.maxAttempts || 8, "attempt")}.</>} value={qq} staged={pendingSet(["queues", name])} to="/projects/$project/jobs/queues" />
            ))}
          </div>
        </Section>
      )}
      {!m.data && !m.isError && <Skeleton className="mt-8 h-40" />}

      <Section title="Copy & move" note="Make a copy of it here, take it with you as a file, or move it to another box.">
        <CopyAndMove project={project} />
      </Section>

      {admin && (
        <Section title="Delete this project" note="Everything in it goes: apps, database, files, users. History keeps the record.">
          <DeleteProject project={project} />
        </Section>
      )}
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
      <Section title="API keys" note="For Claude Code, other agents and scripts.">
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
    <Section title="Keys that can reach this project" note="For Claude Code, other agents and scripts. Whatever a key does shows up in History under its name.">
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

function Section({ title, note, children }: { title: string; note?: string; children: ReactNode }) {
  return (
    <section className="mt-10" aria-label={title}>
      <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
      {note && <p className="mt-0.5 mb-3 max-w-[40rem] text-[0.8125rem] text-ink-3">{note}</p>}
      {!note && <div className="mb-3" />}
      {children}
    </section>
  );
}

// ───────────────────────── secrets ─────────────────────────

export function SecretsPage({ project }: { project: string }) {
  useTitle(`${project} · Secrets`);
  const qc = useQueryClient();
  const secrets = useQuery(q.secrets(project));
  const names = useQuery({ ...q.tokenNames, retry: false });
  const { can } = useMe();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [deleting, setDeleting] = useState<SecretInfo | null>(null);
  const deleted = useRef<string | undefined>(undefined);
  const valid = /^[A-Z_][A-Z0-9_]{0,127}$/.test(name);
  const writer = can("apply:reversible");

  const save = useMutation({
    mutationFn: () => api.setSecret(project, name, value),
    onSuccess: (r) => {
      const n = name;
      const replaced = (secrets.data ?? []).some((s) => s.name === n);
      setName("");
      setValue("");
      void qc.invalidateQueries({ queryKey: ["secrets", project] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      const id = r?.change;
      toast({
        title: <>{id ? (replaced ? "Replaced" : "Saved") : "Already set"} {n}.</>,
        detail: id ? `Apps in ${project} restart with it, one instance at a time.` : "It already had that value, so nothing changed.",
        action: id ? { label: "Undo", run: () => undoChange(id) } : undefined,
      });
    },
  });

  if (secrets.isError && notOnBox(secrets.error)) return <NotOnBox what="Secrets" />;
  const list = secrets.data ?? [];
  const replacing = list.some((s) => s.name === name);

  return (
    <Page>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              {
                label: (
                  <span className="inline-flex items-center gap-1.5">
                    <ProjectIcon project={project} size={14} />
                    {project}
                  </span>
                ),
                to: "/projects/$project",
                params: { project },
              },
              { label: "Settings", to: "/projects/$project/settings", params: { project } },
              { label: "Secrets" },
            ]}
          />
        }
        title="Secrets"
        lede={
          <>
            Apps in {project} read these as environment variables. Values are write-only: once saved nobody can read them here, you included. Saving or removing one restarts the apps.
          </>
        }
      />

      {secrets.isError && <ProblemNote className="mt-8" error={secrets.error} />}

      <div className="mt-9 mb-2 flex items-baseline gap-2">
        <h2 className="label">Saved</h2>
        {list.length > 0 && <span className="text-xs text-ink-4 tnum">{list.length}</span>}
      </div>
      <ul className="divide-y divide-rule border-y border-rule">
        {secrets.isPending && <Skeleton className="my-3 h-10" />}
        {list.length === 0 && secrets.isSuccess && <li className="py-5 text-sm text-ink-3">No secrets yet. Add the first one below.</li>}
        {list.map((s) => (
          <li key={s.name} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 py-2.5 sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_auto]">
            <code className="ident truncate text-[0.8125rem] text-ink">{s.name}</code>
            <span className="text-[0.8125rem] text-ink-3 max-sm:col-start-1 max-sm:row-start-2">
              set {relative(s.updatedAt)} by {names.data?.get(s.updatedBy)?.name ?? "someone"}
            </span>
            {writer && (
              <span className="flex gap-1 max-sm:row-span-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setName(s.name);
                    setValue("");
                    document.getElementById("s-value")?.focus();
                  }}
                >
                  Replace
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`Delete ${s.name}`} onClick={() => setDeleting(s)} className="text-ink-3 hover:text-danger">
                  <Trash2 />
                </Button>
              </span>
            )}
          </li>
        ))}
      </ul>

      {writer && (
        <form
          className="mt-10"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid && value) save.mutate();
          }}
        >
          <h2 className="label mb-3">{replacing ? `Replace ${name}` : "Add a secret"}</h2>
          <div className="grid gap-3 sm:grid-cols-[15rem_minmax(0,1fr)_auto] sm:items-end">
            <label className="block">
              <span className="mb-1 block text-xs font-[550] text-ink-2">Name</span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
                placeholder="STRIPE_SECRET_KEY"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={name !== "" && !valid}
                className="ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
              />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs font-[550] text-ink-2">Value</span>
              <input
                id="s-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="Paste it here"
                autoComplete="new-password"
                spellCheck={false}
                className="ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
              />
            </label>
            <Button type="submit" variant="primary" size="lg" disabled={!valid || !value || save.isPending}>
              {save.isPending ? "Saving…" : replacing ? `Replace ${name}` : name && valid ? `Save ${name}` : "Save secret"}
            </Button>
          </div>
          {save.isError && <ProblemNote className="mt-4" error={save.error} />}
          <p className="mt-2.5 text-xs text-ink-3">
            {replacing
              ? "The old value is replaced. History keeps it encrypted, so you can undo."
              : "Stored encrypted with the box’s own key. It never leaves the box, and this page never shows it again."}
          </p>
        </form>
      )}

      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? "this secret"}?`}
        body={`Apps in ${project} restart without it. You can undo this from History.`}
        action={`Delete ${deleting?.name ?? "secret"}`}
        run={async () => {
          deleted.current = (await api.deleteSecret(project, deleting!.name))?.change;
        }}
        done={() => {
          const id = deleted.current;
          void qc.invalidateQueries({ queryKey: ["secrets", project] });
          void qc.invalidateQueries({ queryKey: ["changes"] });
          toast({
            title: <>Deleted {deleting?.name}.</>,
            detail: `Apps in ${project} restart without it.`,
            action: id ? { label: "Undo", run: () => undoChange(id) } : undefined,
          });
        }}
      />
    </Page>
  );
}


