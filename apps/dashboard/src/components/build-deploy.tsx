import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Container, FolderTree, RotateCw } from "lucide-react";
import { useId, useRef, useState, type ReactNode } from "react";
import { request, type ManifestApp } from "@/api/client";
import { useRedeploy } from "@/components/app-github";
import { FolderPicker } from "@/components/folder-picker";
import { FrameworkSelect } from "@/components/framework-select";
import { InfoTip } from "@/components/info-tip";
import { Working } from "@/components/project-rows";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Switch, SwitchThumb } from "@/components/ui/switch";
import { COMMANDS, commandLabel, detected, draftOf, editsFor, problems, rowsFor, type BuildDraft, type Builder, type Command } from "@/lib/build-config";
import { cn } from "@/lib/cn";
import { PRESETS } from "@/lib/frameworks";
import { repoQuery } from "@/lib/github";
import { changeMany, usePending } from "@/lib/staged";

/** How each manifest framework shows in the preset picker (its mark from lib/frameworks' presets). */
const FRAMEWORKS: Array<{ id: string; name: string; logo: string }> = [
  { id: "next", name: "Next.js", logo: "nextjs" },
  { id: "hono", name: "Hono", logo: "hono" },
  { id: "bun", name: "Bun or Node server", logo: "server-other" },
  { id: "static", name: "Static site", logo: "html" },
  { id: "fastapi", name: "FastAPI", logo: "fastapi" },
  { id: "python", name: "Python server", logo: "python-other" },
];

const BUILDERS: Array<{ value: Builder; label: string; short: string }> = [
  { value: "", label: "Automatic", short: "Auto" },
  { value: "dockerfile", label: "Dockerfile", short: "Dockerfile" },
  { value: "prebuilt", label: "Prebuilt image", short: "Prebuilt" },
];

const builderNote: Record<Builder, string> = {
  "": "Railpack picks the language, package manager and commands.",
  dockerfile: "Built from your Dockerfile. The app listens on $PORT.",
  prebuilt: "Only images from tiffin deploy --prebuilt.",
};

const builderTip: Record<Builder, string> = {
  "": "A folder with a Dockerfile and no package.json or Python project builds with the Dockerfile on its own; the build log says so.",
  dockerfile:
    "BuildKit builds it with the root directory as its context, in the same build slot and limits as other builds. Plain and browser env reach it as build args; secrets and service URLs only as RUN --mount=type=secret,id=NAME,env=NAME. The port it EXPOSEs is not used.",
  prebuilt: "Nothing builds on the box: deploy an image built elsewhere (docker save) with tiffin deploy --prebuilt image.tar. Pushes and redeploys have nothing to build.",
};

const shortLabel: Record<Command, string> = { install: "Install", build: "Build", output: "Output", start: "Start" };

export const field =
  "h-9 w-full min-w-0 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-hidden transition-[border-color,box-shadow] placeholder:text-ink-4 hover:border-rule-3 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] disabled:cursor-not-allowed disabled:border-rule disabled:bg-paper-sunk disabled:text-ink-3 aria-invalid:border-danger";

/**
 * An app's Build and deploy settings, modelled on Vercel's: framework preset,
 * root directory, builder, the four commands (each the detected default
 * until its Override is on), runtime, then health check, release command and
 * watch paths. Saving is one change (History, Undo); it applies on the next
 * deploy, so the footer offers Redeploy now.
 */
export function BuildAndDeploy({ project, app, spec, writer }: { project: string; app: string; spec: ManifestApp; writer: boolean }) {
  const uid = useId();
  const base = draftOf(spec);
  const [draft, setDraftState] = useState<BuildDraft | null>(null);
  // The settings last saved from here: "Saved" shows while the app still has them (an Undo clears it).
  const [sent, setSent] = useState<string | null>(null);
  const [picker, setPicker] = useState(false);
  // A new spec (saved, or changed elsewhere) drops a draft it already matches.
  const [seen, setSeen] = useState(spec);
  if (seen !== spec) {
    setSeen(spec);
    if (draft && editsFor(app, spec, draft).length === 0) setDraftState(null);
  }
  const d = draft ?? base;
  const set = (patch: Partial<BuildDraft>) => {
    setSent(null);
    setDraftState({ ...d, ...patch });
  };
  const pending = usePending(project).filter((e) => e.kind === "set" && e.path[0] === "apps" && e.path[1] === app);
  const edits = draft ? editsFor(app, spec, draft) : [];
  const errs = problems(d);
  const bad = Object.keys(errs).length > 0;
  const rows = rowsFor(d, spec.role);
  const def = detected(d);
  const saving = pending.length > 0;
  const saved = sent !== null && !saving && !draft && JSON.stringify(base) === sent;

  const gh = spec.git;
  const repo = useQuery({ ...repoQuery(gh?.repo ?? "", gh?.branch), enabled: !!gh && picker });

  const save = () => {
    if (!edits.length || bad) return;
    setSent(JSON.stringify(d));
    changeMany(project, edits);
  };
  const frameworks = FRAMEWORKS.filter((f) => f.id === d.framework || PRESETS.some((p) => p.build === f.id));

  return (
    <section aria-labelledby={`${uid}-h`} className="min-w-0">
      <h2 id={`${uid}-h`} className="label">
        Build and deploy
      </h2>

      <fieldset disabled={!writer} className="mt-2 divide-y divide-rule border-y border-rule">
        <Row label="Framework preset" hint="How the box builds and runs it.">
          <FrameworkSelect
            aria-label="Framework preset"
            value={d.framework}
            onChange={(framework) => set({ framework, builder: framework === "static" ? "" : d.builder })}
            options={frameworks.map((f) => ({ id: f.id, name: f.name, logo: f.logo, disabled: f.id === "static" && d.builder !== "" }))}
            className="sm:max-w-[20rem]"
          />
        </Row>

        <Row label="Root directory" htmlFor={`${uid}-root`} hint={gh ? "The folder with the app’s code." : "Relative to tiffin.config.ts."} error={errs.root}>
          <div className="flex min-w-0 gap-2">
            <div className="relative min-w-0 flex-1">
              <span className="ident pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-[0.8125rem] text-ink-4">/</span>
              <input
                id={`${uid}-root`}
                value={d.root}
                onChange={(e) => set({ root: e.target.value.replace(/^\/+/, "") })}
                placeholder={gh ? "the top of the repository" : "the folder of tiffin.config.ts"}
                spellCheck={false}
                aria-invalid={!!errs.root}
                className={cn(field, "ident pl-5")}
              />
            </div>
            {gh && (
              <Button type="button" size="md" variant="secondary" className="h-9" onClick={() => setPicker(true)}>
                <FolderTree className="size-3.5" aria-hidden /> Browse
              </Button>
            )}
          </div>
        </Row>

        {rows.builder ? (
          <Row label="Builder" hint={builderNote[d.builder]} tip={builderTip[d.builder]}>
            <Segmented
              label="Builder"
              value={d.builder}
              onChange={(builder) => set({ builder })}
              options={BUILDERS.map((b) => ({ value: b.value, label: b.label, short: b.short }))}
            />
            {d.builder === "dockerfile" && (
              <div className="mt-3 grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,11rem)]">
                <label className="min-w-0">
                  <span className="mb-1 flex items-center gap-1.5 text-xs text-ink-3">
                    <Container className="size-3" aria-hidden /> Dockerfile, in the root directory
                  </span>
                  <input value={d.dockerfile} onChange={(e) => set({ dockerfile: e.target.value })} placeholder="Dockerfile" spellCheck={false} aria-invalid={!!errs.dockerfile} className={cn(field, "ident")} />
                </label>
                <label className="min-w-0">
                  <span className="mb-1 block text-xs text-ink-3">Build stage</span>
                  <input value={d.target} onChange={(e) => set({ target: e.target.value })} placeholder="the last one" spellCheck={false} aria-invalid={!!errs.target} className={cn(field, "ident")} />
                </label>
                {(errs.dockerfile || errs.target) && <p className="text-xs text-danger sm:col-span-2">{errs.dockerfile ?? errs.target}</p>}
              </div>
            )}
            {d.builder === "prebuilt" && gh && <p className="mt-2 text-xs text-warn-ink">Disconnect {gh.repo} first: pushes would have nothing to build.</p>}
          </Row>
        ) : (
          <Row label="Builder" hint="Built with Bun (or Railpack for npm, pnpm and yarn); the edge serves the files.">
            <p className="flex h-9 items-center text-[0.8125rem] text-ink-2">Static files</p>
          </Row>
        )}

        <Row label="Commands" hint={rows.commandsInImage ? "The image installs and builds itself." : "Detected unless you override one."}>
          <CommandFields
            show={COMMANDS.filter((c) => (c === "output" ? rows.output : c === "start" ? rows.start : !rows.commandsInImage))}
            values={d}
            placeholders={def}
            errors={errs}
            onChange={(c, v) => set({ [c]: v } as Partial<BuildDraft>)}
          />
        </Row>

        {rows.runtime && (
          <Row label="Runtime" hint="Bun starts faster and uses less memory; Node.js if a library needs it.">
            <Segmented
              label="Runtime"
              value={d.runtime || "bun"}
              onChange={(v) => set({ runtime: v === "node" ? "node" : "" })}
              options={[
                { value: "bun", label: "Bun" },
                { value: "node", label: "Node.js" },
              ]}
            />
          </Row>
        )}

        {rows.healthcheck && (
          <Row label="Health check" htmlFor={`${uid}-hc`} error={errs.healthcheck} hint="New instances answer it before traffic switches." tip="With / any status below 500 passes. A path you set must answer 2xx or 3xx.">
            <input id={`${uid}-hc`} value={d.healthcheck} onChange={(e) => set({ healthcheck: e.target.value })} placeholder="/" spellCheck={false} aria-invalid={!!errs.healthcheck} className={cn(field, "ident sm:max-w-[20rem]")} />
          </Row>
        )}
        {rows.release && (
          <Row label="Release command" htmlFor={`${uid}-rel`} hint="Runs once before each release: migrations." tip="It runs after the build, in a one-off container of the new image with the app’s env. If it fails, the running version keeps serving. Rollbacks don’t run it.">
            <input id={`${uid}-rel`} value={d.release} onChange={(e) => set({ release: e.target.value })} placeholder="none, e.g. bunx drizzle-kit migrate" spellCheck={false} className={cn(field, "ident")} />
          </Row>
        )}
        <Row
          label="Watch paths"
          htmlFor={gh || d.watch ? `${uid}-watch` : undefined}
          error={errs.watch}
          hint={gh ? "Only pushes that change these deploy it." : undefined}
          tip="One pattern per line, from the top of the repository. * matches within a folder, ** across folders, a folder name covers everything in it, and ! excludes; the last pattern that matches a file decides. Pull requests follow them too; redeploys always build."
        >
          {gh || d.watch ? (
            <textarea
              id={`${uid}-watch`}
              value={d.watch}
              onChange={(e) => set({ watch: e.target.value })}
              placeholder={"every push deploys, e.g.\napps/web/**"}
              rows={Math.min(6, Math.max(2, d.watch.split("\n").length))}
              spellCheck={false}
              aria-invalid={!!errs.watch}
              className={cn(field, "ident h-auto resize-y py-2 leading-5")}
            />
          ) : (
            <p className="flex min-h-9 items-center text-[0.8125rem] text-ink-3">For apps that deploy from GitHub: connect a repository first.</p>
          )}
        </Row>
      </fieldset>

      <Footer project={project} app={app} spec={spec} writer={writer} dirty={edits.length > 0} bad={bad} saving={saving} saved={saved} count={edits.length} onSave={save} onDiscard={() => (setDraftState(null), setSent(null))} />

      {gh && (
        <FolderPicker
          open={picker}
          onOpenChange={setPicker}
          repo={gh.repo}
          folders={repo.data?.folders ?? []}
          roots={repo.data?.roots ?? []}
          loading={repo.isPending}
          value={d.root}
          onPick={(root) => set({ root })}
        />
      )}
    </section>
  );
}

function Row({ label, htmlFor, hint, tip, error, aside, children }: { label: string; htmlFor?: string; hint?: ReactNode; tip?: string; error?: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid gap-x-6 gap-y-1.5 py-3 sm:grid-cols-[minmax(0,11rem)_minmax(0,1fr)]">
      <div className="min-w-0">
        <div className="flex items-center justify-between gap-2">
          <span className="flex items-center gap-1 text-[0.8125rem] font-[550] text-ink">
            {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : label}
            {tip && <InfoTip label={`About ${label.toLowerCase()}`}>{tip}</InfoTip>}
          </span>
          {aside && <span className="sm:hidden">{aside}</span>}
        </div>
        {hint && <p className="mt-0.5 text-xs leading-[1.125rem] text-ink-3 max-sm:hidden">{hint}</p>}
      </div>
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-3">
          <div className="min-w-0 flex-1">{children}</div>
          {aside && <span className="max-sm:hidden">{aside}</span>}
        </div>
        {hint && <p className="mt-1 text-xs text-ink-3 sm:hidden">{hint}</p>}
        {error && <p className="mt-1 text-xs text-danger">{error}</p>}
      </div>
    </div>
  );
}

/**
 * The four commands, each the detected default (its placeholder) until its
 * Override is on. Compact: a short label, the field, the switch on one line.
 * A field turned on and left empty says so once it loses focus.
 */
export function CommandFields({
  show,
  values,
  placeholders,
  errors = {},
  onChange,
}: {
  show: Command[];
  values: Partial<Record<Command, string | undefined>>;
  placeholders: Record<Command, string>;
  errors?: Partial<Record<Command, string>>;
  onChange: (c: Command, v: string | undefined) => void;
}) {
  const uid = useId();
  const inputs = useRef<Partial<Record<Command, HTMLInputElement | null>>>({});
  const [touched, setTouched] = useState<Partial<Record<Command, boolean>>>({});
  return (
    <div className="grid gap-2.5">
      {show.map((c) => {
        const on = values[c] !== undefined;
        const id = `${uid}-${c}`;
        const err = on && touched[c] ? errors[c] : undefined;
        return (
          <div key={c} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 sm:grid-cols-[4.5rem_minmax(0,1fr)_auto]">
            <label htmlFor={id} className="text-xs text-ink-2">
              {shortLabel[c]}
            </label>
            <span className="sm:order-3">
              <OverrideSwitch
                label={`Override the ${commandLabel[c].toLowerCase()}`}
                checked={on}
                onChange={(v) => {
                  setTouched((t) => ({ ...t, [c]: false }));
                  onChange(c, v ? (values[c] ?? "") : undefined);
                  if (v) requestAnimationFrame(() => inputs.current[c]?.focus());
                }}
              />
            </span>
            <input
              id={id}
              ref={(el) => {
                inputs.current[c] = el;
              }}
              value={on ? values[c] : ""}
              onChange={(e) => onChange(c, e.target.value)}
              onBlur={() => setTouched((t) => ({ ...t, [c]: true }))}
              placeholder={placeholders[c]}
              disabled={!on}
              spellCheck={false}
              aria-invalid={!!err}
              className={cn(field, "ident col-span-2 h-8 sm:order-2 sm:col-span-1")}
            />
            {err && <p className="col-span-2 text-xs text-danger sm:order-4 sm:col-start-2 sm:col-end-4">{err}</p>}
          </div>
        );
      })}
    </div>
  );
}

export function OverrideSwitch({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="inline-flex shrink-0 cursor-pointer items-center gap-2 text-xs text-ink-3 select-none">
      <span>Override</span>
      <Switch
        checked={checked}
        onCheckedChange={onChange}
        aria-label={label}
        className="relative h-[18px] w-8 rounded-full border border-rule-3 bg-paper-press transition-colors duration-[var(--dur-state)] disabled:opacity-50 data-[state=checked]:border-brass data-[state=checked]:bg-brass"
      >
        <SwitchThumb className="absolute top-[1px] left-[1px] size-3.5 rounded-full bg-paper-raised shadow-[0_1px_1px_oklch(0.2_0.01_60/0.25)] transition-transform duration-[var(--dur-state)] ease-[var(--ease-out)] data-[state=checked]:translate-x-3.5" />
      </Switch>
    </label>
  );
}

/** Save and Discard while there are changes; afterwards, Redeploy now (settings apply on the next deploy). */
function Footer({
  project,
  app,
  spec,
  writer,
  dirty,
  bad,
  saving,
  saved,
  count,
  onSave,
  onDiscard,
}: {
  project: string;
  app: string;
  spec: ManifestApp;
  writer: boolean;
  dirty: boolean;
  bad: boolean;
  saving: boolean;
  saved: boolean;
  count: number;
  onSave: () => void;
  onDiscard: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const gh = useRedeploy(project, app, spec.git);
  const live = useMutation({
    mutationFn: () => request<{ id: string }>("POST", `/v1/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/deploys/redeploy`),
    onSuccess: (dep) => {
      void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
      toast({ title: <>Redeploying {app} with its new settings.</>, action: { label: "Watch the build", run: () => void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id: dep.id } }) } });
    },
    onError: (e) => toast({ title: <>{app} didn’t start redeploying.</>, detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });
  const redeploy = spec.git ? gh : live;
  if (!writer) return <p className="mt-3 text-xs text-ink-3">Changing these needs write access to {project}.</p>;
  return (
    <div className="mt-3 flex min-h-8 flex-wrap items-center justify-between gap-x-4 gap-y-2" aria-live="polite">
      <p className="text-xs text-ink-3">
        {saving ? (
          <Working> Saving…</Working>
        ) : dirty ? (
          <>
            {count === 1 ? "1 unsaved change" : `${count} unsaved changes`}. They apply on the next deploy.
          </>
        ) : saved ? (
          <span className="text-ink-2">Saved. It applies on the next deploy; History can undo it.</span>
        ) : (
          "Changes apply on the next deploy."
        )}
      </p>
      <div className="flex items-center gap-2">
        {dirty && !saving ? (
          <>
            <Button type="button" size="md" variant="ghost" onClick={onDiscard}>
              Discard
            </Button>
            <Button type="button" size="md" variant="primary" onClick={onSave} disabled={bad}>
              Save
            </Button>
          </>
        ) : (
          spec.builder !== "prebuilt" && (
            <Button type="button" size="md" variant={saved ? "primary" : "secondary"} onClick={() => redeploy.mutate()} disabled={saving || redeploy.isPending}>
              <RotateCw className={cn("size-3.5", redeploy.isPending && "animate-spin")} aria-hidden />
              {redeploy.isPending ? "Starting…" : "Redeploy now"}
            </Button>
          )
        )}
      </div>
    </div>
  );
}
