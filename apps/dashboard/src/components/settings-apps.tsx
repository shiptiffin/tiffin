import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import type { Manifest, ManifestApp } from "@/api/client";
import { q } from "@/api/queries";
import { AppRepo } from "@/components/app-github";
import { BuildAndDeploy } from "@/components/build-deploy";
import { buildAs } from "@/components/build-settings";
import { RESERVE_MB } from "@/components/project-rows";
import { useMe } from "@/lib/me";
import { pendingFor, usePending, type StagedEdit } from "@/lib/staged";
import { Scale } from "@/routes/apps";

const MB = 1048576;

/**
 * Settings › Apps: a card per app with how it runs (Scale), where its code
 * comes from (GitHub), and its Build and deploy settings (framework, root
 * directory, builder, commands, runtime, health check, release, watch
 * paths). Scale and GitHub are the same controls as the app's own page, so
 * a change here is the same change.
 */
export function AppsSettings({ project, manifest }: { project: string; manifest?: Manifest }) {
  const { can } = useMe();
  const writer = can("apply:reversible");
  const edits = usePending(project);
  const res = useQuery(q.resources);
  const free = res.data && res.data.memory.totalBytes > 0 ? res.data.memory.availableBytes / MB - RESERVE_MB : undefined;
  const apps = Object.entries((manifest?.apps ?? {}) as Record<string, ManifestApp>).sort(([a], [b]) => a.localeCompare(b));
  if (apps.length === 0) return <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">No apps in {project} yet.</p>;
  return (
    <div className="grid gap-4">
      {apps.map(([name, spec]) => (
        <AppCard key={name} project={project} name={name} spec={spec} writer={writer} free={free} edits={edits} />
      ))}
    </div>
  );
}

function AppCard({ project, name, spec, writer, free, edits }: { project: string; name: string; spec: ManifestApp; writer: boolean; free?: number; edits: StagedEdit[] }) {
  const isStatic = spec.framework === "static";
  return (
    <article id={`app-${name}`} aria-label={name} className="scroll-mt-6 rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
      <header className="flex items-baseline justify-between gap-3 border-b border-rule px-4 py-3 sm:px-5">
        <div className="min-w-0">
          <Link to="/projects/$project/apps/$app" params={{ project, app: name }} className="text-[0.9375rem] font-[550] text-ink hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {name}
          </Link>
          <span className="ml-2 text-[0.8125rem] text-ink-3">
            {buildAs(spec.framework).replace(/^an? /, "")}
            {spec.role === "worker" ? ", works in the background" : ""}
          </span>
        </div>
        <Link to="/projects/$project/apps/$app" params={{ project, app: name }} className="inline-flex shrink-0 items-center gap-0.5 text-[0.8125rem] text-ink-3 hover:text-ink">
          Open
          <ChevronRight className="size-3.5" />
        </Link>
      </header>
      {(!isStatic || spec.git) && (
        <div className={!isStatic && spec.git ? "grid gap-6 border-b border-rule px-4 py-4 sm:px-5 md:grid-cols-2" : "grid gap-6 border-b border-rule px-4 py-4 sm:px-5"}>
          {!isStatic && (
            <div>
              <Scale
                project={project}
                app={name}
                spec={spec}
                free={free}
                instances={pendingFor(edits, `instances:${name}`)}
                memory={edits.find((e) => e.kind === "set" && e.path.join("/") === `apps/${name}/memoryMB`)}
                writer={writer}
              />
            </div>
          )}
          {spec.git && <AppRepo project={project} app={name} git={spec.git} writer={writer} />}
        </div>
      )}
      <div className="px-4 py-4 sm:px-5">
        <BuildAndDeploy project={project} app={name} spec={spec} writer={writer} />
      </div>
    </article>
  );
}
