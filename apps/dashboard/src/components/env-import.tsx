import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { api, type Manifest } from "@/api/client";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox, Radio, RadioGroup } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { count } from "@/lib/format";
import { envField } from "./env-dialog";
import { builtIn, looksSecret, parseDotenv, pathsFor, savePlain, setByBox, valueAt, type Scope } from "./env-model";

type Line = { i: number; k: string; v: string; valid: boolean; secret: boolean; state: "new" | "replaces" | "same" | "invalid" | "dup" };

/**
 * Paste a .env file (or pick one): every line becomes a variable. Names that
 * sound like keys or passwords start as secrets; each can be switched. Nothing
 * changes until Save.
 */
export function EnvImport({
  project,
  manifest,
  apps,
  secretsOn,
  secretNames,
  initial = "",
  open,
  onOpenChange,
}: {
  project: string;
  manifest?: Manifest;
  apps: string[];
  secretsOn: boolean;
  secretNames: string[];
  /** Text already pasted (into Add's Name field). */
  initial?: string;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        {open && <Form project={project} manifest={manifest} apps={apps} secretsOn={secretsOn} secretNames={secretNames} initial={initial} close={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  );
}

function Form({ project, manifest, apps, secretsOn, secretNames, initial, close }: { project: string; manifest?: Manifest; apps: string[]; secretsOn: boolean; secretNames: string[]; initial: string; close: () => void }) {
  const qc = useQueryClient();
  const file = useRef<HTMLInputElement>(null);
  const [text, setText] = useState(initial);
  const [flip, setFlip] = useState<Record<string, boolean>>({});
  const [some, setSome] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const scope = useMemo<Scope>(() => (some ? apps.filter((a) => picked.includes(a)) : "all"), [some, apps, picked]);

  const lines = useMemo<Line[]>(
    () =>
      parseDotenv(text).map(({ k, v, valid, dup }, i) => {
        const secret = secretsOn && valid && (flip[k] ?? (looksSecret(k, v) || secretNames.includes(k)));
        const now = secret ? [] : pathsFor(k, scope === "all" || scope.length ? scope : "all").map((p) => valueAt(manifest, p));
        const state: Line["state"] = !valid
          ? "invalid"
          : dup
            ? "dup"
            : secret
              ? secretNames.includes(k)
                ? "replaces"
                : "new"
              : now.every((x) => x === v)
                ? "same"
                : now.some((x) => x !== undefined)
                  ? "replaces"
                  : "new";
        return { k, v, valid, secret, state, i };
      }),
    [text, flip, secretsOn, secretNames, manifest, scope],
  );

  const todo = lines.filter((l) => l.state === "new" || l.state === "replaces");
  const plain = todo.filter((l) => !l.secret);
  const secrets = todo.filter((l) => l.secret);
  const noApps = some && plain.length > 0 && (scope as string[]).length === 0;

  const save = useMutation({
    mutationFn: async () => {
      // Secrets one by one (each is its own change); plain values as one change.
      for (const l of secrets) await api.setSecret(project, l.k, l.v);
      for (const l of plain) savePlain(project, manifest, l.k, l.v, scope);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["secrets", project] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
    },
    onSuccess: () => {
      if (secrets.length) toast({ title: `Saved ${count(secrets.length, "secret")}.`, detail: `Apps in ${project} restart with them. Each one can be undone from History.` });
      close();
    },
  });

  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (todo.length && !noApps) save.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>Paste a .env file</DialogTitle>
        <DialogDescription className="text-[0.875rem]">NAME=value lines. Comments, export and quotes are fine, and quoted values can span lines.</DialogDescription>
      </DialogHeader>
      <DialogBody className="grid gap-4">
        <div>
          <textarea
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={6}
            placeholder={"LOG_LEVEL=info\nSTRIPE_SECRET_KEY=sk_live_…"}
            spellCheck={false}
            autoComplete="off"
            aria-label=".env contents"
            className={cn(envField, "ident block resize-y py-2 leading-5")}
          />
          <div className="mt-1.5 flex items-center gap-2 text-xs text-ink-3">
            <button type="button" onClick={() => file.current?.click()} className="font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
              Choose a file
            </button>
            <span>The file stays on your computer; only the values you save reach the box.</span>
            <input
              ref={file}
              type="file"
              className="hidden"
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (f) setText(await f.text());
                e.target.value = "";
              }}
            />
          </div>
        </div>

        {lines.length > 0 && (
          <div>
            <div className="mb-1.5 flex items-baseline justify-between gap-3">
              <h3 className="label">{count(lines.filter((l) => l.state !== "dup").length, "variable")}</h3>
              {secretsOn && <span className="text-xs text-ink-3">Keys and passwords start as secrets. Change any.</span>}
            </div>
            <ul className="max-h-[16rem] divide-y divide-rule overflow-y-auto border-y border-rule">
              {lines.map((l) => (
                <li key={`${l.k}-${l.i}`} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 py-2 sm:grid-cols-[minmax(0,13rem)_minmax(0,1fr)_auto_7.5rem]">
                  <span className={cn("ident truncate text-[0.8125rem]", !l.valid ? "text-danger" : l.state === "dup" ? "text-ink-4 line-through decoration-ink-4" : "text-ink")}>{l.k}</span>
                  <span className="ident truncate text-[0.75rem] text-ink-3 max-sm:col-start-1 max-sm:row-start-2">{l.secret ? "••••••••" : l.v || "(empty)"}</span>
                  {secretsOn ? (
                    <label className="flex cursor-pointer items-center gap-1.5 text-xs text-ink-2 max-sm:row-span-2">
                      <Checkbox checked={l.secret} disabled={!l.valid || l.state === "dup"} onCheckedChange={(v) => setFlip((f) => ({ ...f, [l.k]: v === true }))} aria-label={`${l.k} is a secret`} />
                      Secret
                    </label>
                  ) : (
                    <span />
                  )}
                  <span className={cn("text-right text-xs max-sm:hidden", l.state === "invalid" ? "text-danger" : l.state === "replaces" ? "text-ink-2" : "text-ink-3")}>
                    {l.state === "invalid" ? "Bad name, skipped" : l.state === "dup" ? "Repeated below" : l.state === "replaces" ? "Replaces yours" : l.state === "same" ? "Unchanged" : setByBox(l.k) ? "Replaces Tiffin’s" : "New"}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {apps.length > 1 && plain.length > 0 && (
          <fieldset>
            <legend className="mb-1.5 text-xs font-[550] text-ink-2">Scope of the plain values</legend>
            <RadioGroup value={some ? "some" : "all"} onValueChange={(v) => setSome(v === "some")} className="flex flex-wrap gap-x-5 gap-y-2">
              <label className="flex cursor-pointer items-center gap-2.5 text-[0.875rem] text-ink">
                <Radio value="all" />
                Whole project
              </label>
              <label className="flex cursor-pointer items-center gap-2.5 text-[0.875rem] text-ink">
                <Radio value="some" />
                Only some apps
              </label>
            </RadioGroup>
            {some && (
              <div className="mt-2 ml-6 flex flex-wrap gap-x-4 gap-y-2">
                {apps.map((a) => (
                  <label key={a} className="flex cursor-pointer items-center gap-2 text-[0.875rem] text-ink">
                    <Checkbox checked={picked.includes(a)} onCheckedChange={(v) => setPicked((p) => (v === true ? [...p, a] : p.filter((x) => x !== a)))} />
                    <span className="ident text-[0.8125rem]">{a}</span>
                  </label>
                ))}
              </div>
            )}
          </fieldset>
        )}
        {lines.some((l) => l.valid && builtIn(l.k)) && (
          <p className="text-[0.8125rem] text-ink-2">NEXT_PUBLIC_, VITE_ and PUBLIC_ values ship to browsers, so anyone can read them. They can’t really be secret.</p>
        )}
        {secrets.length > 0 && apps.length > 1 && <p className="text-[0.8125rem] text-ink-3">Secrets apply to the whole project. Secrets for one app aren’t supported yet.</p>}
        {save.isError && <ProblemNote error={save.error} />}
      </DialogBody>
      <DialogFooter>
        <span className="text-xs text-ink-3 sm:mr-auto">Saving restarts the apps.</span>
        <Button type="button" variant="ghost" onClick={close}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!todo.length || noApps || save.isPending}>
          {save.isPending ? "Saving…" : todo.length ? `Save ${count(todo.length, "variable")}` : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}
