import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState, type ClipboardEvent, type ReactNode } from "react";
import { api, type Manifest } from "@/api/client";
import { Breaker } from "@/components/breaker";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox, Radio, RadioGroup } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { undoChange } from "@/lib/staged";
import { builtIn, ENV_KEY, looksSecret, parseDotenv, pathsFor, removePath, sameScope, savePlain, scopeWords, setByBox, valueAt, type EnvRow, type Scope } from "./env-model";

export const envField =
  "w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 transition-[border-color,box-shadow] hover:border-rule-3 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger disabled:cursor-not-allowed disabled:opacity-60";

/** "LOG_LEVEL" from whatever was typed: capitals, digits, underscores. */
export const cleanKey = (s: string) => s.toUpperCase().replace(/[^A-Z0-9_]/g, "_");

/** Pasted text that is .env, not just a name: "KEY=value", or several lines. */
const looksLikeDotenv = (t: string) => /\n/.test(t.trim()) || /^\s*(export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=/.test(t);

/**
 * Add a variable, or edit one: name, value, Secret, and scope. Nothing
 * changes until Save. A plain value is written into the project's config
 * (History and Undo work); a secret goes to the box, which encrypts it, and
 * is never shown again: the form forgets it once it's sent.
 *
 * Pasting "KEY=value" into Name fills both fields; pasting several lines
 * hands them to the .env import (onPasteMany).
 */
export function EnvDialog({
  project,
  manifest,
  apps,
  secretsOn,
  secretNames,
  row,
  open,
  onOpenChange,
  onPasteMany,
}: {
  project: string;
  manifest?: Manifest;
  apps: string[];
  /** The box keeps secrets (it doesn't when it runs without --box). */
  secretsOn: boolean;
  secretNames: string[];
  /** The variable being edited; none to add one. */
  row?: EnvRow;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onPasteMany?: (text: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        {open && <Form project={project} manifest={manifest} apps={apps} secretsOn={secretsOn} secretNames={secretNames} row={row} close={() => onOpenChange(false)} onPasteMany={onPasteMany} />}
      </DialogContent>
    </Dialog>
  );
}

function Form({
  project,
  manifest,
  apps,
  secretsOn,
  secretNames,
  row,
  close,
  onPasteMany,
}: {
  project: string;
  manifest?: Manifest;
  apps: string[];
  secretsOn: boolean;
  secretNames: string[];
  row?: EnvRow;
  close: () => void;
  onPasteMany?: (text: string) => void;
}) {
  const uid = useId();
  const qc = useQueryClient();
  const editing = !!row;
  const [key, setKey] = useState(row?.key ?? "");
  const [value, setValue] = useState(row?.secret ? "" : (row?.value ?? ""));
  // Secret follows what the name and value look like until the person flips it.
  const [flipped, setFlipped] = useState<boolean | null>(row ? row.secret : null);
  const [some, setSome] = useState(row ? row.scope !== "all" : false);
  const [picked, setPicked] = useState<string[]>(row && row.scope !== "all" ? row.scope : []);

  const secret = secretsOn && (flipped ?? looksSecret(key, value));
  const scope: Scope = secret || !some ? "all" : apps.filter((a) => picked.includes(a));
  const valid = ENV_KEY.test(key);
  const noApps = scope !== "all" && scope.length === 0;
  const already = !editing && valid && !secret && pathsFor(key, scope).some((p) => valueAt(manifest, p) !== undefined);
  const secretExists = valid && secretNames.includes(key) && !row?.secret;
  const unchanged = editing && !row.secret && !secret && value === row.value && sameScope(scope, row.scope);
  const ready = valid && !noApps && !unchanged && (secret ? value !== "" : true);

  const saveSecret = useMutation({
    mutationFn: (v: string) => api.setSecret(project, key, v),
    onSuccess: (r) => {
      // A plain value that became a secret: the plain one goes.
      if (row && !row.secret) for (const p of row.paths) removePath(project, manifest, key, p);
      void qc.invalidateQueries({ queryKey: ["secrets", project] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      const id = r?.change;
      toast({
        title: <>{id ? (secretNames.includes(key) ? `Replaced ${key}.` : `Saved ${key} as a secret.`) : `${key} already had that value.`}</>,
        detail: id ? (builtIn(key) ? `Web apps in ${project} rebuild with it.` : `Apps in ${project} restart with it, one copy at a time.`) : "Nothing changed.",
        action: id ? { label: "Undo", run: () => undoChange(id) } : undefined,
      });
      close();
    },
  });

  const save = () => {
    if (!ready) return;
    if (secret) {
      // Sent once, then forgotten: the field is cleared before the request goes.
      const v = value;
      setValue("");
      return saveSecret.mutate(v);
    }
    savePlain(project, manifest, key, value, scope, row?.paths ?? []);
    close();
  };

  const pasteName = (e: ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData("text");
    if (!looksLikeDotenv(text)) return;
    const parsed = parseDotenv(text);
    if (parsed.length === 0) return;
    e.preventDefault();
    if (parsed.length > 1 && onPasteMany) return onPasteMany(text);
    setKey(cleanKey(parsed[0].k));
    setValue(parsed[0].v);
  };

  const notes: ReactNode[] = [];
  if (valid && builtIn(key)) notes.push(secret ? <>Values named like this ship to browsers, so anyone can read them. They can’t really be secret.</> : <>Built into browser code: anyone can read it, and saving rebuilds the web apps.</>);
  if (valid && setByBox(key)) notes.push(<>Tiffin already sets {key}. Yours replaces Tiffin’s value.</>);
  if (already) notes.push(<>{key} is already set for {scope === "all" ? "the whole project" : scopeWords(scope)}. Saving replaces it.</>);
  if (secretExists && secret) notes.push(<>A secret named {key} exists. Saving replaces it.</>);
  if (secretExists && !secret) notes.push(<>A secret named {key} exists, and it wins over a plain value.</>);

  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <DialogHeader>
        <DialogTitle>{editing ? (row.secret ? `Replace ${row.key}` : `Edit ${row.key}`) : "Add a variable"}</DialogTitle>
        <DialogDescription className="text-[0.875rem]">
          {editing && row.secret ? "Its current value can’t be shown. Enter a new one to replace it." : editing ? `Apps in ${project} read it when they start.` : "Tip: paste NAME=value, or a whole .env file, into Name."}
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="grid gap-4">
        <label className="block">
          <span className="mb-1 block text-xs font-[550] text-ink-2">Name</span>
          <input
            autoFocus={!editing}
            value={key}
            disabled={editing}
            onChange={(e) => setKey(cleanKey(e.target.value))}
            onPaste={pasteName}
            placeholder="LOG_LEVEL"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={key !== "" && !valid}
            className={cn(envField, "ident h-9")}
          />
          {key !== "" && !valid && <span className="mt-1 block text-xs text-danger">Capitals, digits and underscores, not starting with a digit. Like LOG_LEVEL.</span>}
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-[550] text-ink-2">{editing && row.secret ? "New value" : "Value"}</span>
          <textarea
            autoFocus={editing}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            rows={value.includes("\n") ? 5 : 2}
            placeholder={secret ? "Paste it here" : "info"}
            autoComplete="off"
            spellCheck={false}
            className={cn(envField, "ident block min-h-[4.25rem] resize-y py-2 leading-5")}
          />
        </label>

        {secretsOn && (
          <div className="flex items-start gap-3">
            <Breaker
              label="Secret"
              state={secret ? "on" : "off"}
              onFlip={(next) => setFlipped(next === "on")}
              disabled={row?.secret}
              className="mt-0.5"
              aria-describedby={`${uid}-secret`}
            />
            <span>
              <span className="block text-[0.875rem] font-[550] text-ink">Secret</span>
              <span id={`${uid}-secret`} className="block text-[0.8125rem] text-ink-3">
                {row?.secret
                  ? "Encrypted on the box. Nobody can read it back, you included."
                  : flipped === null && secret
                    ? "On because this looks like a key or password. Encrypted on the box and never shown again, even to you."
                    : "Encrypted on the box and never shown again, even to you."}
              </span>
            </span>
          </div>
        )}

        {apps.length > 1 && (
          <fieldset>
            <legend className="mb-1.5 text-xs font-[550] text-ink-2">Scope</legend>
            {secret ? (
              <p className="text-[0.8125rem] text-ink-3">Secrets apply to the whole project. Secrets for one app aren’t supported yet.</p>
            ) : (
              <RadioGroup value={some ? "some" : "all"} onValueChange={(v) => setSome(v === "some")} className="grid gap-2">
                <label className="flex cursor-pointer items-center gap-2.5 text-[0.875rem] text-ink">
                  <Radio value="all" />
                  Whole project
                </label>
                <label className="flex cursor-pointer items-center gap-2.5 text-[0.875rem] text-ink">
                  <Radio value="some" />
                  Only some apps
                </label>
              </RadioGroup>
            )}
            {!secret && some && (
              <div className="mt-2 ml-6 flex flex-wrap gap-x-4 gap-y-2">
                {apps.map((a) => (
                  <label key={a} className="flex cursor-pointer items-center gap-2 text-[0.875rem] text-ink">
                    <Checkbox checked={picked.includes(a)} onCheckedChange={(v) => setPicked((p) => (v === true ? [...p, a] : p.filter((x) => x !== a)))} />
                    <span className="ident text-[0.8125rem]">{a}</span>
                  </label>
                ))}
                {noApps && <span className="w-full text-xs text-ink-3">Pick at least one app.</span>}
              </div>
            )}
          </fieldset>
        )}

        {notes.length > 0 && (
          <ul className="grid gap-1 text-[0.8125rem] text-ink-2">
            {notes.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
        )}
        {saveSecret.isError && <ProblemNote error={saveSecret.error} />}
      </DialogBody>
      <DialogFooter>
        <span className="text-xs text-ink-3 sm:mr-auto">{valid && builtIn(key) ? "Saving rebuilds the web apps." : "Saving restarts the apps."}</span>
        <Button type="button" variant="ghost" onClick={close}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ready || saveSecret.isPending}>
          {saveSecret.isPending ? "Saving…" : editing ? (row.secret ? "Replace value" : "Save") : valid ? `Add ${key}` : "Add"}
        </Button>
      </DialogFooter>
    </form>
  );
}
