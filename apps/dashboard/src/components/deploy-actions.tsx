import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import type { ManifestApp } from "@/api/client";
import { GitHubMark } from "@/components/github-mark";
import { DeployTray } from "@/components/start-deploy-tray";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { deployGitHub, shortSha } from "@/lib/github";
import { nextDeployFor } from "@/lib/starters";
import { refreshApp } from "./deploy-parts";

/**
 * Deploy an app's GitHub branch now, for the rare time a push didn't (the
 * first deploy after connecting it, say). Pushes deploy on their own.
 */
export function useDeployBranch(project: string) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
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
}

/**
 * The Deployments page before anything is deployed: for each app, how its
 * first version gets there. An app connected to GitHub deploys on a push to
 * its branch (with a quiet Deploy now); any other app deploys from a starter
 * or a git URL, in the deploy tray.
 */
export function FirstDeploy({ project, apps }: { project: string; apps: Array<[string, ManifestApp]> }) {
  const branch = useDeployBranch(project);
  const [tray, setTray] = useState<string | null>(null);
  const traySpec = apps.find(([a]) => a === tray)?.[1];
  const named = apps.length > 1;
  return (
    <>
      <ul className="divide-y divide-rule">
        {apps.map(([a, spec]) => {
          const b = spec.git?.branch ?? "main";
          return (
            <li key={a} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 py-3 first:pt-0 last:pb-0">
              {spec.git ? (
                <>
                  <p className="flex min-w-0 items-center gap-2 text-sm text-ink-2">
                    <GitHubMark className="shrink-0 text-ink-3" />
                    <span className="min-w-0">
                      Push to <span className="ident text-[0.8125rem] text-ink">{b}</span> to deploy{named ? ` ${a}` : ""}.
                    </span>
                  </p>
                  <button
                    type="button"
                    disabled={branch.isPending}
                    onClick={() => branch.mutate({ app: a, branch: b })}
                    className="text-[0.8125rem] font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink disabled:opacity-50"
                  >
                    {branch.isPending && branch.variables?.app === a ? "Starting…" : named ? `Deploy ${a} now` : "Deploy now"}
                  </button>
                </>
              ) : (
                <>
                  <p className="min-w-0 text-sm text-ink-2">Deploy {a} from a starter or a git URL.</p>
                  <Button size="md" variant={named ? "secondary" : "primary"} aria-label={`Deploy ${a}`} onClick={() => setTray(a)}>
                    Deploy…
                  </Button>
                </>
              )}
            </li>
          );
        })}
      </ul>
      {tray && (
        <DeployTray
          project={project}
          app={tray}
          framework={traySpec?.framework}
          open={!!tray}
          onOpenChange={(o) => !o && setTray(null)}
          suggest={nextDeployFor(project, tray)}
          hasVersions={false}
        />
      )}
    </>
  );
}
