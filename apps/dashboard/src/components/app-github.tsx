import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import type { ManifestApp } from "@/api/client";
import { GitHubMark } from "@/components/github-mark";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { deployGitHub, githubQuery, shortSha } from "@/lib/github";
import { change } from "@/lib/staged";

type Git = NonNullable<ManifestApp["git"]>;

const previewWords: Record<string, string> = {
  "same-repo": "a preview for each pull request",
  forks: "a preview for each pull request, forks too",
  off: "no previews",
};

/** Redeploy an app from its GitHub branch: acts at once; the toast links to the build. */
export function useRedeploy(project: string, app: string, git?: Git) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useMutation({
    mutationFn: () => deployGitHub(project, app),
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
      void qc.invalidateQueries({ queryKey: ["runtime", project, app] });
      toast({
        title: (
          <>
            Deploying {git?.branch ?? "the branch"} ({shortSha(d.commit)}) to {app}.
          </>
        ),
        detail: d.message ? `“${d.message}”${d.author ? ` by ${d.author}` : ""}` : "It builds on the box and switches traffic once it’s healthy.",
        action: { label: "Watch the build", run: () => void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id: d.id } }) },
      });
    },
    onError: (e) => toast({ title: <>{app} didn’t start deploying.</>, detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });
}

/** Where an app's code comes from on GitHub, with Redeploy and Disconnect. */
export function AppRepo({ project, app, git, writer }: { project: string; app: string; git: Git; writer: boolean }) {
  const gh = useQuery(githubQuery);
  const url = `${gh.data?.githubUrl ?? "https://github.com"}/${git.repo}`;
  const branch = git.branch ?? "main";
  const connected = gh.data?.connected;
  return (
    <section aria-label="Repository">
      <h2 className="label mb-1.5">From GitHub</h2>
      <div className="border-y border-rule py-2.5">
        <a href={url} target="_blank" rel="noopener noreferrer" className="group flex min-w-0 items-center gap-2 text-[0.875rem] text-ink hover:text-brass-ink">
          <GitHubMark className="text-ink-2" />
          <span className="truncate">{git.repo}</span>
          <ArrowUpRight className="size-3.5 shrink-0 text-ink-3 transition-transform group-hover:translate-x-0.5 group-hover:-translate-y-0.5" />
        </a>
        <dl className="mt-2 grid grid-cols-[5.5rem_minmax(0,1fr)] gap-y-1 text-[0.8125rem]">
          <dt className="text-ink-3">Branch</dt>
          <dd className="ident min-w-0 truncate text-[0.75rem] leading-5 text-ink">{branch}</dd>
          {git.path && (
            <>
              <dt className="text-ink-3">Folder</dt>
              <dd className="ident min-w-0 truncate text-[0.75rem] leading-5 text-ink">{git.path}</dd>
            </>
          )}
          <dt className="text-ink-3">Pushes</dt>
          <dd className="min-w-0 text-ink-2">deploy {branch}; {previewWords[git.previews ?? "same-repo"]}</dd>
        </dl>
        {gh.isSuccess && !connected && <p className="mt-2 text-xs text-warn-ink">GitHub isn’t connected to this box, so pushes don’t deploy. Connect it in Settings › Git.</p>}
      </div>
      {writer && (
        <div className="mt-2.5 flex flex-wrap items-center gap-2">
          <Button
            size="md"
            variant="ghost"
            onClick={() =>
              change(project, { kind: "set", path: ["apps", app, "git"], from: git, to: undefined, what: `Disconnect ${app} from ${git.repo}`, undo: `${app} deploys from ${git.repo} again` }, { immediate: true })
            }
          >
            Disconnect repo
          </Button>
        </div>
      )}
    </section>
  );
}
