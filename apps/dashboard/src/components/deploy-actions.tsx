import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown, Plus } from "lucide-react";
import { useState } from "react";
import type { Manifest, ManifestApp } from "@/api/client";
import { q as core } from "@/api/queries";
import { GitHubMark } from "@/components/github-mark";
import { AddMenu } from "@/components/start-add-menu";
import { DeployTray } from "@/components/start-deploy-tray";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuTrigger } from "@/components/ui/dropdown";
import { addressesOf } from "@/lib/addresses";
import { deployGitHub, shortSha } from "@/lib/github";
import { frameworkName, nextDeployFor } from "@/lib/starters";
import { refreshApp } from "./deploy-parts";

/** Addresses other projects already use, so a new app's name can't clash with them. */
export function useOtherRoutes(project: string) {
  const projects = useQuery(core.projects);
  const others = (projects.data ?? []).map((x) => x.name).filter((n) => n !== project);
  const om = useQueries({ queries: others.map((n) => ({ ...core.manifest(n), staleTime: 60_000 })) });
  return om.flatMap((x) => (x.data ? addressesOf(x.data.project, x.data.manifest.apps) : []));
}

/** "Add app": the Add menu's app form, opened straight away. */
export function AddAppButton({ project, manifest, variant = "secondary" }: { project: string; manifest?: Manifest; variant?: "primary" | "secondary" }) {
  const routes = useOtherRoutes(project);
  return (
    <AddMenu
      project={project}
      manifest={manifest}
      routes={routes}
      only="app"
      trigger={
        <Button variant={variant} size="lg" disabled={!manifest}>
          <Plus />
          Add app
        </Button>
      }
    />
  );
}

/**
 * Deploy, for a project: one app deploys straight away (its GitHub branch) or
 * opens the deploy tray (a starter or a git URL); several apps ask which first.
 */
export function DeployButton({ project, apps, hasVersions }: { project: string; apps: Array<[string, ManifestApp]>; hasVersions: (app: string) => boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [tray, setTray] = useState<string | null>(null);
  const fromGitHub = useMutation({
    mutationFn: ({ app }: { app: string; branch?: string }) => deployGitHub(project, app),
    onSuccess: (d, { app, branch }) => {
      refreshApp(qc, project, app);
      toast({
        title: (
          <>
            Deploying {branch ?? "the branch"} ({shortSha(d.commit)}) to {app}.
          </>
        ),
        detail: d.message ? `“${d.message}”${d.author ? ` by ${d.author}` : ""}` : "It builds on the box and switches traffic once it’s healthy.",
        action: { label: "Watch the build", run: () => void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id: d.id } }) },
      });
    },
    onError: (e, { app }) => toast({ title: <>{app} didn’t start deploying.</>, detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });
  const go = (app: string, spec: ManifestApp) => (spec.git ? fromGitHub.mutate({ app, branch: spec.git.branch ?? "main" }) : setTray(app));
  const traySpec = apps.find(([a]) => a === tray)?.[1];
  if (apps.length === 0) return null;
  return (
    <>
      {apps.length === 1 ? (
        <Button variant="primary" size="lg" disabled={fromGitHub.isPending} onClick={() => go(apps[0][0], apps[0][1])}>
          {fromGitHub.isPending ? "Starting…" : `Deploy ${apps[0][0]}`}
        </Button>
      ) : (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="primary" size="lg" disabled={fromGitHub.isPending}>
              {fromGitHub.isPending ? "Starting…" : "Deploy"}
              <ChevronDown className="-mr-1" />
            </Button>
          </MenuTrigger>
          <MenuContent align="end" className="min-w-60">
            <MenuLabel>Which app?</MenuLabel>
            {apps.map(([a, spec]) => (
              <MenuItem key={a} className="h-auto py-1.5" onSelect={() => go(a, spec)}>
                {spec.git ? <GitHubMark /> : <span className="size-4" aria-hidden />}
                <span className="flex min-w-0 flex-col leading-[1.15rem]">
                  <span className="truncate text-ink">{a}</span>
                  <span className="truncate text-xs text-ink-3">
                    {spec.git ? `${spec.git.branch ?? "main"} from ${spec.git.repo}` : `${frameworkName(spec.framework)}: a starter or a git URL`}
                  </span>
                </span>
              </MenuItem>
            ))}
          </MenuContent>
        </Menu>
      )}
      {tray && (
        <DeployTray
          project={project}
          app={tray}
          framework={traySpec?.framework}
          open={!!tray}
          onOpenChange={(o) => !o && setTray(null)}
          suggest={nextDeployFor(project, tray)}
          hasVersions={hasVersions(tray)}
        />
      )}
    </>
  );
}
