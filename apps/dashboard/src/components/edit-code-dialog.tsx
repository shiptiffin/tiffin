import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Copy } from "lucide-react";
import { Tabs as T } from "radix-ui";
import { useState } from "react";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { allDeploysQuery } from "@/components/deploy-parts";
import { Skeleton } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { copyText } from "@/lib/clipboard";
import { editSteps, editText, type CodeSource, type EditInput, type Step } from "@/lib/edit-steps";
import { runtimeQuery } from "@/lib/pulse";
import { startersQuery } from "@/lib/starters";

const dashboard = () => (typeof location === "undefined" ? "" : location.origin);

/**
 * Where an app's code is, from what the box knows: a GitHub repository in its
 * config, else its live version's source (a starter, a git URL, or a folder
 * sent from the CLI).
 */
function useSources(project: string, apps: string[]): Array<CodeSource | undefined> {
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const ds = useQueries({ queries: apps.map((a) => allDeploysQuery(project, a)) });
  const starters = useQuery(startersQuery);
  return apps.map((app, i) => {
    const spec = m.data?.manifest?.apps?.[app];
    if (spec?.git?.repo) return { kind: "github", repo: spec.git.repo, branch: spec.git.branch ?? "main", path: spec.git.path };
    const d = ds[i].data;
    if (!d || !m.data) return undefined;
    const live = d.find((x) => !x.preview && x.status === "live");
    if (live?.source === "template") {
      const edit = starters.data?.find((s) => s.id === live.template)?.edit;
      const dir = spec?.path && spec.path !== "." ? spec.path.replace(/^\.\/|\/+$/g, "") : "";
      return { kind: "starter", edit: edit && (dir ? `${dir}/${edit}` : edit) };
    }
    if (live?.source === "git" && live.repo) return { kind: "git", url: live.repo };
    return { kind: "folder" };
  });
}

/**
 * Edit code: how to get an app's code, change it and put it live again, as
 * numbered steps with this box's address, the project and the app filled in.
 * Copy steps copies the same text, for a coding agent or a note.
 */
export function EditCodeDialog({ project, apps, initial, open, onOpenChange }: { project: string; apps: string[]; initial?: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const sources = useSources(project, apps);
  // Opens on the app asked for, else the first that runs a starter (the one people come here to change).
  const [picked, setApp] = useState(initial && apps.includes(initial) ? initial : undefined);
  const app = picked ?? apps[sources.findIndex((x) => x?.kind === "starter")] ?? apps[0];
  // Until every app's source is known, the starter isn't either: wait rather than open on another app and jump.
  const source = !picked && sources.includes(undefined) ? undefined : sources[apps.indexOf(app)];
  const rt = useQuery(runtimeQuery(project, app));
  const input: EditInput | undefined = source && { project, app, dashboard: dashboard(), live: rt.data?.production?.url, source };
  const e = input && editSteps(input);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[40rem]">
        <DialogHeader className="pb-4">
          <DialogTitle>Edit code</DialogTitle>
          <DialogDescription className="text-[0.9375rem]">{e ? e.lede : <Skeleton className="h-5 w-72 max-w-full" />}</DialogDescription>
        </DialogHeader>
        {apps.length > 1 && (
          <T.Root value={app} onValueChange={setApp}>
            <T.List aria-label="App" className="flex gap-1 overflow-x-auto border-b border-rule px-6 [scrollbar-width:none]">
              {apps.map((a) => (
                <T.Trigger
                  key={a}
                  value={a}
                  className="relative -mb-px flex h-10 shrink-0 items-center px-2.5 text-[0.875rem] text-ink-3 transition-colors first:pl-0 hover:text-ink data-[state=active]:font-[550] data-[state=active]:text-ink after:absolute after:inset-x-2.5 after:-bottom-px after:h-[2px] after:rounded-full first:after:left-0 data-[state=active]:after:bg-ink"
                >
                  {a}
                </T.Trigger>
              ))}
            </T.List>
          </T.Root>
        )}
        <DialogBody className={apps.length > 1 ? "pt-5" : "pt-1"}>
          {e ? (
            <ol className="grid gap-5" aria-label={`Steps for ${app}`}>
              {e.steps.map((s, n) => (
                <StepRow key={s.title} n={n + 1} step={s} close={() => onOpenChange(false)} />
              ))}
            </ol>
          ) : (
            <div className="grid gap-4">
              <Skeleton className="h-14" />
              <Skeleton className="h-14" />
              <Skeleton className="h-14" />
            </div>
          )}
        </DialogBody>
        <DialogFooter className="sm:gap-6">
          <p className="text-xs text-ink-3 sm:mr-auto">{e?.safety}</p>
          <CopySteps text={input ? editText(input) : ""} />
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function StepRow({ n, step: s, close }: { n: number; step: Step; close: () => void }) {
  const [before, after] = s.keys && s.note ? s.note.split("Settings › API keys") : [s.note];
  return (
    <li className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3">
      <span className="ident pt-[3px] text-[0.6875rem] text-ink-4" aria-hidden>
        {String(n).padStart(2, "0")}
      </span>
      <div className="min-w-0">
        <p className="text-[0.875rem] font-[550] text-ink">{s.title}</p>
        {s.note && (
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            {before}
            {after !== undefined && (
              <>
                <Link to="/settings/keys" search={{ create: true }} onClick={close} className="font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                  Settings › API keys
                </Link>
                {after}
              </>
            )}
          </p>
        )}
        {s.cmds && (
          <div className="mt-2 grid gap-1.5">
            {s.cmds.map((c) => (
              <Command key={c} cmd={c} wrap />
            ))}
          </div>
        )}
      </div>
    </li>
  );
}

function CopySteps({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      variant="primary"
      size="lg"
      disabled={!text}
      className="shrink-0"
      onClick={async () => {
        if (await copyText(text)) {
          setDone(true);
          setTimeout(() => setDone(false), 1600);
        }
      }}
    >
      {done ? <Check className="animate-pop" /> : <Copy />}
      <span aria-live="polite">{done ? "Copied" : "Copy steps"}</span>
    </Button>
  );
}
