import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Lock, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { ApiError, api, notOnBox, type Change, type ResourceStatus, type SecretInfo } from "@/api/client";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { ProblemNote } from "@/components/problem";
import { RiskMark } from "@/components/risk";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { asTier, splitAddress } from "@/lib/changes";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";
import { Page, NotOnBox } from "@/components/page";
import { Confirm } from "./settings";

const kindOrder = ["project", "app", "service", "bucket", "env", "cron"];
const kindTitle: Record<string, string> = {
  project: "Project",
  app: "Apps",
  service: "Services",
  bucket: "Buckets",
  env: "Environment",
  cron: "Schedules",
};

/** One line of plain facts from a spec: "next · 1024 MB · 2 instances". */
function facts(kind: string, spec: unknown): string {
  if (spec === null || spec === undefined) return "";
  if (typeof spec !== "object") return JSON.stringify(spec);
  const s = spec as Record<string, unknown>;
  const out: string[] = [];
  const take = (k: string, f: (v: unknown) => string) => s[k] !== undefined && s[k] !== null && out.push(f(s[k]));
  take("framework", String);
  take("role", (v) => `${v}`);
  take("instances", (v) => `${v} ${v === 1 ? "instance" : "instances"}`);
  take("memoryMB", (v) => `${v} MB`);
  take("maxMemoryMB", (v) => `${v} MB max`);
  take("routes", (v) => (Array.isArray(v) ? v.join(", ") : String(v)));
  take("extensions", (v) => (Array.isArray(v) && v.length ? `extensions: ${v.join(", ")}` : "no extensions"));
  take("public", (v) => (v ? "public" : "private"));
  take("schedule", (v) => `runs ${String(v)}`);
  take("methods", (v) => `sign in with ${(v as string[]).join(", ")}`);
  take("organizations", (v) => (v ? "teams on" : "no teams"));
  take("from", (v) => `sends as ${String(v)}`);
  take("retentionDays", (v) => `raw events kept ${String(v)} days`);
  if (kind === "app" && s.env && typeof s.env === "object") out.push(`${Object.keys(s.env as object).length} env`);
  if (out.length === 0) {
    const keys = Object.keys(s);
    return keys.length ? keys.slice(0, 4).join(", ") : "defaults";
  }
  return out.join(" · ");
}

/** Where each resource is managed, when it has a page. */
function pageFor(kind: string, name: string): { to: "/"; params: Record<string, string>; search?: Record<string, string> } | null {
  const to = (path: string, params: Record<string, string> = {}) => ({ to: path as "/", params });
  if (kind === "app") return to("/projects/$project/apps/$app", { app: name });
  if (kind === "bucket") return to("/projects/$project/storage/$bucket", { bucket: name });
  if (kind === "cron") return to("/projects/$project/queues");
  if (kind !== "service") return null;
  return (
    {
      postgres: to("/projects/$project/data"),
      valkey: to("/projects/$project/data/kv"),
      storage: to("/projects/$project/storage"),
      email: to("/projects/$project/email"),
      auth: to("/projects/$project/users"),
      analytics: to("/projects/$project/analytics"),
    }[name] ?? null
  );
}

function StatePill({ st }: { st?: ResourceStatus }) {
  if (!st) return <span className="text-sm text-ink-4">not tracked</span>;
  const state = st.state;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-sm",
        state === "ready" ? "text-ink-2" : state === "failed" ? "font-medium text-irr" : "text-brass-ink",
      )}
      title={`${state} · updated ${relative(st.updatedAt)}`}
    >
      <span
        className={cn(
          "size-1.5 rounded-full",
          state === "ready" ? "bg-rev" : state === "failed" ? "bg-irr" : "animate-pulse bg-brass", // pending is live, so it moves
        )}
      />
      {state === "ready" ? "Ready" : state === "failed" ? "Failed" : state === "pending" ? "Starting" : state}
    </span>
  );
}

export function ProjectPage({ project }: { project: string }) {
  useTitle(project);
  const p = useQuery(q.project(project));
  const changes = useQuery(q.changes(project));

  if (p.isPending)
    return (
      <Page>
        <div className="h-10 w-64 animate-pulse rounded-md bg-hover" />
      </Page>
    );
  if (p.isError)
    return (
      <Page>
        <ProblemNote
          error={p.error}
          title={p.error instanceof ApiError && p.error.status === 404 ? `There's no project called ${project}` : "Couldn't load this project"}
        />
      </Page>
    );

  const res = p.data.resources ?? [];
  const status = p.data.status ?? {};
  const states = res.map((r) => status[r.address]?.state);
  const failed = states.filter((s) => s === "failed").length;
  const pending = states.filter((s) => s === "pending").length;
  const groups = new Map<string, typeof res>();
  for (const r of res) {
    const k = splitAddress(r.address).kind;
    groups.set(k, [...(groups.get(k) ?? []), r]);
  }
  const rank = (k: string) => (kindOrder.includes(k) ? kindOrder.indexOf(k) : 99);
  const kinds = [...groups.keys()].sort((a, b) => rank(a) - rank(b));

  return (
    <Page wide>
      <header className="animate-rise">
        <p className="text-sm text-ink-3">
          Project · version {p.data.version} · {res.length} resources
        </p>
        <h1 className="display mt-1 text-3xl text-ink">{project}</h1>
        <p className="display mt-1 text-xl text-ink-3">
          {failed > 0 ? (
            <span className="text-irr">{failed === 1 ? "One thing failed" : `${failed} things failed`}. Details below.</span>
          ) : pending > 0 ? (
            `${pending === 1 ? "One thing is" : `${pending} things are`} starting.`
          ) : Object.keys(status).length > 0 ? (
            "Everything is running."
          ) : (
            "Nothing reports its state yet."
          )}
        </p>
      </header>

      <div className="mt-10 grid gap-10 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div className="flex min-w-0 flex-col gap-8">
          {kinds.map((k) => (
            <section key={k} aria-labelledby={`k-${k}`}>
              <h2 id={`k-${k}`} className="display-italic mb-2 text-lg text-ink">
                {kindTitle[k] ?? k}
              </h2>
              <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
                {groups.get(k)!.map((r) => {
                  const st = status[r.address];
                  const { name } = splitAddress(r.address);
                  return (
                    <li key={r.address} className="relative px-4 py-3 has-[a]:hover:bg-hover/40">
                      <div className="flex items-center gap-3">
                        {pageFor(k, name) ? (
                          <Link
                            {...pageFor(k, name)!}
                            params={{ ...pageFor(k, name)!.params, project } as never}
                            className="min-w-0 shrink-0 font-mono text-[0.8125rem] text-ink after:absolute after:inset-0 hover:underline"
                          >
                            {name || k}
                          </Link>
                        ) : (
                          <code className="min-w-0 shrink-0 font-mono text-[0.8125rem] text-ink">{name || k}</code>
                        )}
                        <span className="min-w-0 flex-1 truncate text-sm text-ink-3">
                          {k === "env" ? <code className="font-mono text-xs text-ink-2">{JSON.stringify(r.spec)}</code> : facts(k, r.spec)}
                        </span>
                        <StatePill st={st} />
                      </div>
                      {st?.message && <p className={cn("mt-1.5 text-sm", st.state === "failed" ? "text-irr" : "text-ink-3")}>{st.message}</p>}
                    </li>
                  );
                })}
              </ul>
            </section>
          ))}
        </div>

        <aside className="flex min-w-0 flex-col gap-8">
          <section>
            <h2 className="display-italic mb-2 text-lg text-ink">Lately</h2>
            <ul className="flex flex-col gap-3">
              {(changes.data ?? []).slice(0, 5).map((c: Change) => (
                <li key={c.id}>
                  <Link to="/changes/$id" params={{ id: c.id }} className="group flex gap-2.5">
                    <RiskMark tier={asTier(c.plan.risk)} className="mt-1" />
                    <span className="min-w-0">
                      <span
                        className={cn(
                          "block text-base text-ink group-hover:underline group-hover:underline-offset-4",
                          c.undoneBy && "text-ink-3 line-through",
                        )}
                      >
                        {c.intent}
                      </span>
                      <span className="text-sm text-ink-3">
                        {c.actor.name} · {relative(c.at)}
                      </span>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
            <Link to="/" search={{ project }} className="mt-4 inline-flex items-center gap-1 text-sm text-brass-ink hover:text-ink">
              All activity in {project} <ArrowRight className="size-3.5" />
            </Link>
          </section>
          <Link
            to="/projects/$project/secrets"
            params={{ project }}
            className="group rounded-xl border border-rule bg-raised/60 p-4 transition-colors hover:border-rule-strong"
          >
            <span className="flex items-center gap-2 text-base font-medium text-ink">
              <Lock className="size-4 text-ink-3" /> Secrets
            </span>
            <span className="mt-1 block text-sm text-ink-3">API keys and passwords your apps read as environment variables.</span>
          </Link>
        </aside>
      </div>
    </Page>
  );
}

// ---------------------------------------------------------------- secrets

export function SecretsPage({ project }: { project: string }) {
  useTitle(`${project} · Secrets`);
  const qc = useQueryClient();
  const secrets = useQuery(q.secrets(project));
  const names = useQuery(q.tokenNames);
  const { can } = useMe();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [deleting, setDeleting] = useState<SecretInfo | null>(null);
  const valid = /^[A-Z_][A-Z0-9_]{0,127}$/.test(name);
  const writer = can("apply:reversible");

  const save = useMutation({
    mutationFn: () => api.setSecret(project, name, value),
    onSuccess: () => {
      setName("");
      setValue("");
      qc.invalidateQueries({ queryKey: ["secrets", project] });
    },
  });

  if (secrets.isError && notOnBox(secrets.error)) return <NotOnBox what="Secrets" />;
  const list = secrets.data ?? [];
  const replacing = list.some((s) => s.name === name);

  return (
    <Page>
      <header className="animate-rise">
        <p className="text-sm text-ink-3">
          <Link to="/projects/$project" params={{ project }} className="hover:text-ink">
            {project}
          </Link>{" "}
          / secrets
        </p>
        <h1 className="display mt-1 text-3xl text-ink">Secrets</h1>
        <p className="mt-2 max-w-[36rem] text-md text-ink-2">
          Apps in <code className="font-mono text-ink">{project}</code> get these as environment variables. Values are write-only: once saved, nobody
          can read them here, you included. Saving or removing one restarts the apps.
        </p>
      </header>

      {secrets.isError && <ProblemNote className="mt-8" error={secrets.error} />}

      <ul className="mt-10 divide-y divide-rule border-y border-rule">
        {list.length === 0 && secrets.isSuccess && <li className="py-6 text-base text-ink-3">No secrets yet. Add your first one below.</li>}
        {list.map((s) => (
          <li key={s.name} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-3">
            <code className="font-mono text-[0.8125rem] text-ink">{s.name}</code>
            <span title="Values are never shown" aria-hidden className="font-mono text-sm tracking-widest text-ink-4">
              ••••••••
            </span>
            <span className="ml-auto text-sm text-ink-3">
              set {relative(s.updatedAt)} by {names.data?.get(s.updatedBy)?.name ?? "someone"}
            </span>
            {writer && (
              <span className="flex gap-1">
                <Button variant="ghost" size="sm" onClick={() => setName(s.name)}>
                  Replace
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`Delete ${s.name}`} onClick={() => setDeleting(s)} className="hover:text-irr">
                  <Trash2 />
                </Button>
              </span>
            )}
          </li>
        ))}
      </ul>

      {writer && (
        <form
          className="mt-10 rounded-xl border border-rule bg-raised/60 p-5"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid && value) save.mutate();
          }}
        >
          <h2 className="text-md font-medium text-ink">{replacing ? `Replace ${name}` : "Add a secret"}</h2>
          <div className="mt-4 grid gap-4 sm:grid-cols-[14rem_1fr]">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="s-name">Name</Label>
              <Input
                id="s-name"
                value={name}
                onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
                placeholder="STRIPE_SECRET_KEY"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={name !== "" && !valid}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="s-value">Value</Label>
              <Input
                id="s-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="Paste it here"
                className="font-mono"
                autoComplete="new-password"
                spellCheck={false}
              />
            </div>
          </div>
          {save.isError && <ProblemNote className="mt-4" error={save.error} />}
          <div className="mt-4 flex items-center justify-between gap-3">
            <p className="text-sm text-ink-3">
              {replacing ? "The old value is replaced and can't be recovered." : "Stored encrypted with the box's own key."}
            </p>
            <Button type="submit" variant="primary" disabled={!valid || !value || save.isPending}>
              <Plus />
              {save.isPending ? "Saving…" : replacing ? "Replace" : "Save secret"}
            </Button>
          </div>
        </form>
      )}

      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? "this secret"}?`}
        body={`Apps in ${project} restart without it. The value can't be recovered.`}
        action="Delete secret"
        run={() => api.deleteSecret(project, deleting!.name)}
        done={() => qc.invalidateQueries({ queryKey: ["secrets", project] })}
      />
    </Page>
  );
}
