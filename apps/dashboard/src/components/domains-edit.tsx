import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import type { Manifest } from "@/api/client";
import { routesOf } from "@/components/domains-parts";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { addDomain, type Domain } from "@/lib/domains";
import { changeMany, type StagedEdit } from "@/lib/staged";

/**
 * Editing a domain is editing the apps' routes in the manifest: "example.com"
 * on the app that serves it, "example.com/api" on the app that serves that
 * path. These go through plan and apply like every other change, with Undo.
 */

type Apps = Record<string, { routes?: string[] | null; role?: string }>;
const appsOf = (m?: Manifest) => ((m as unknown as { apps?: Apps } | undefined)?.apps ?? {}) as Apps;

/** "example.com" and "example.com/" are the root; "example.com/api/" is /api. */
function split(rt: string): [string, string] {
  const i = rt.indexOf("/");
  if (i < 0) return [rt.toLowerCase(), "/"];
  const rest = rt.slice(i + 1).replace(/^\/+|\/+$/g, "");
  return [rt.slice(0, i).toLowerCase(), rest ? `/${rest}` : "/"];
}

/** The edits that move one route (domain + path) from one app to another, or drop it (to = ""). */
function moveRoute(m: Manifest | undefined, domain: string, path: string, to: string): StagedEdit[] {
  const apps = appsOf(m);
  const edits: StagedEdit[] = [];
  let from = "";
  let kept = "";
  for (const [name, a] of Object.entries(apps)) {
    const routes = a.routes ?? [];
    const hit = routes.find((r) => {
      const [h, p] = split(r);
      return h === domain && p === path;
    });
    if (!hit) continue;
    from = name;
    kept = hit;
    const rest = routes.filter((r) => r !== hit);
    edits.push({
      kind: "set",
      path: ["apps", name, "routes"],
      from: routes,
      to: rest.length ? rest : undefined,
      what: to ? `Serve ${domain}${path === "/" ? "" : path} from ${to} instead of ${name}` : `Stop sending ${domain}${path} to ${name}`,
      undo: to ? `${domain}${path === "/" ? "" : path} goes back to ${name}` : `${domain}${path} goes to ${name} again`,
    });
  }
  if (to && to !== from) {
    const routes = apps[to]?.routes ?? [];
    edits.push({
      kind: "set",
      path: ["apps", to, "routes"],
      from: routes,
      to: [...routes, kept || (path === "/" ? domain : `${domain}${path}`)],
      what: `Serve ${domain}${path === "/" ? "" : path} from ${to}`,
      undo: `${to} stops serving ${domain}${path === "/" ? "" : path}`,
    });
  }
  return edits;
}

/** Point a domain (its root) at another app. */
export function ChangeAppDialog({
  project,
  d,
  apps,
  manifest,
  open,
  onOpenChange,
}: {
  project: string;
  d: Domain;
  apps: string[];
  manifest?: Manifest;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const now = routesOf(d).root ?? "";
  const [to, setTo] = useState(now);
  const save = (e: FormEvent) => {
    e.preventDefault();
    if (!to || to === now) return onOpenChange(false);
    const edits = moveRoute(manifest, d.domain, "/", to);
    if (edits.length) changeMany(project, edits);
    onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <form onSubmit={save} className="contents">
          <DialogHeader>
            <DialogTitle>Change the app for {d.domain}</DialogTitle>
            <DialogDescription>Visitors see the new app as soon as the change applies. DNS and the certificate stay as they are.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <label htmlFor="domain-app" className="text-[0.8125rem] font-[550] text-ink">
              Served by
            </label>
            <div className="mt-1.5">
              <Select id="domain-app" value={to} onValueChange={setTo} options={apps.map((a) => ({ value: a, label: a === now ? `${a} (now)` : a }))} />
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!to || to === now || !manifest}>
              {!to || to === now ? "Save" : `Serve from ${to}`}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Stop sending one path of a domain to its app (the root app serves it again). */
export function dropPath(project: string, manifest: Manifest | undefined, domain: string, path: string) {
  const edits = moveRoute(manifest, domain, path, "");
  if (edits.length) changeMany(project, edits);
}

/** "Send /api to api": one path of the domain to another app. */
export function AddPath({ project, d, apps, onDone }: { project: string; d: Domain; apps: string[]; onDone: () => void }) {
  const qc = useQueryClient();
  const root = routesOf(d).root;
  const others = apps.filter((a) => a !== root);
  const [path, setPath] = useState("");
  const [app, setApp] = useState(others[0] ?? "");
  const clean = `/${path.trim().replace(/^\/+|\/+$/g, "")}`;
  const ok = /^\/[a-zA-Z0-9._~\-/]+$/.test(clean) && clean !== "/" && !!app;
  const add = useMutation({
    mutationFn: () => addDomain(project, { domain: d.domain, app, path: clean } as Parameters<typeof addDomain>[1]),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["domains", project] });
      for (const k of [["manifest", project], ["project", project], ["changes"]]) void qc.invalidateQueries({ queryKey: k });
      toast({ title: `${d.domain}${clean} now goes to ${app}.` });
      onDone();
    },
  });
  if (others.length === 0) return <p className="text-[0.8125rem] text-ink-3">This project has one web app, so every path already goes to it.</p>;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) add.mutate();
      }}
      className="rounded-[8px] border border-rule-2 bg-paper-raised p-3"
    >
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <div className="flex min-w-0 flex-1 items-center rounded-[7px] border border-rule-2 bg-paper focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
          <span className="ident shrink-0 pl-2.5 text-[0.8125rem] text-ink-3">{d.domain}</span>
          <input
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/api"
            aria-label="Path"
            autoFocus
            spellCheck={false}
            autoCapitalize="off"
            className="ident h-9 min-w-0 flex-1 bg-transparent pr-2.5 pl-0.5 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:outline-hidden"
          />
        </div>
        <span className="text-[0.8125rem] text-ink-3">goes to</span>
        <div className="sm:w-36">
          <Select value={app} onValueChange={setApp} options={others.map((a) => ({ value: a, label: a }))} aria-label="App" />
        </div>
        <div className="flex gap-1.5">
          <Button type="button" variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!ok || add.isPending}>
            {add.isPending ? "Adding…" : "Add"}
          </Button>
        </div>
      </div>
      <p className="mt-2 text-[0.8125rem] text-ink-3">Everything under that path goes to the app, like {d.domain}/api/users. The rest stays with {root ?? "the main app"}.</p>
      {add.isError && <ProblemNote className="mt-2" error={add.error} />}
    </form>
  );
}
