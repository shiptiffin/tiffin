import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import { useId, useState } from "react";
import { signInApi, type ProviderState } from "@/api/modules";
import { ProblemNote } from "@/components/problem";
import { guideFor } from "@/components/signin-guides";
import { PasteField, TestedTag } from "@/components/signin-parts";
import { KeyField } from "@/components/signin-providers";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Sheet } from "@/routes/data/sheet";

// "This app's own keys" for one provider: the guided setup on a project's
// Auth page. People then see the app's own name on the provider's sign-in
// screen. The app needs ONE redirect URI, on its sign-in host: sign-ins on
// its other addresses and previews come back through it.

export function AppKeysSheet({
  project,
  st,
  name,
  hosts,
  open,
  onOpenChange,
}: {
  project: string;
  st: ProviderState;
  name: string;
  /** Every host of the app, to say which ones the one redirect URI covers. */
  hosts: string[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const guide = guideFor(st.id);
  const qc = useQueryClient();
  const formId = useId();
  const own = st.keys === "project";
  const [values, setValues] = useState<Record<string, string>>({});
  const [fileName, setFileName] = useState("");
  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {};
      for (const [k, v] of Object.entries(values)) if (v.trim()) body[k] = k === "privateKey" ? v : v.trim();
      return signInApi.setAppKeys(project, st.id, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["auth", project] });
      toast({ title: own ? `Updated ${project}’s own ${name} keys.` : `${project} signs in with its own ${name} keys now.` });
      onOpenChange(false);
    },
  });
  if (!guide) return null;
  const signInHost = (() => {
    try {
      return new URL(st.appCallbackUrl).host;
    } catch {
      return "";
    }
  })();
  const others = hosts.filter((h) => h !== signInHost.split(":")[0]);
  // Fields to fill: all required ones on first setup; with keys saved, empty keeps them.
  const missing = !own && guide.fields.some((f) => !f.optional && !values[f.key]?.trim());

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      wide
      title={own ? `${project}’s own ${name} keys` : `Use ${project}’s own ${name} keys`}
      sub={
        <span className="inline-flex flex-wrap items-center gap-2">
          People see your app’s name on {name}’s sign-in screen.
          <TestedTag tested={guide.tested} />
        </span>
      }
      footer={
        <>
          <Button type="submit" form={formId} variant="primary" disabled={save.isPending || missing}>
            {save.isPending ? "Saving…" : own ? `Save ${name} keys` : `Use these ${name} keys`}
          </Button>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          {own && (
            <a
              href={st.testUrl}
              target="_blank"
              rel="noreferrer"
              className="ml-auto inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 underline decoration-rule-3 underline-offset-[3px] hover:text-ink"
            >
              Test sign-in
              <ArrowUpRight className="size-3.5" aria-hidden />
            </a>
          )}
        </>
      }
    >
      <div className="space-y-7">
        <section aria-labelledby={`${formId}-cb`}>
          <h3 id={`${formId}-cb`} className="text-[0.875rem] font-[550] text-ink">
            Redirect URI
          </h3>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">Register this one address in {name}. It is the only one {project} needs.</p>
          <PasteField value={st.appCallbackUrl} label={`Copy ${project}’s ${name} redirect URI`} className="mt-2" strong />
          {others.length > 0 && (
            <p className="mt-2 text-[0.8125rem] text-ink-3 [overflow-wrap:anywhere]">
              Sign-ins on {others.join(", ")} and every preview come back through <span className="text-ink-2">{signInHost}</span>, so adding a domain or a preview never
              means another trip to {name}’s console. If {project} gets a new custom domain, this address changes and this page says so.
            </p>
          )}
        </section>

        <section aria-labelledby={`${formId}-how`}>
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h3 id={`${formId}-how`} className="text-[0.875rem] font-[550] text-ink">
              In {guide.name}
            </h3>
            <a href={guide.console.href} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-[0.8125rem] text-brass-ink underline decoration-brass/40 underline-offset-[3px] hover:decoration-brass">
              Open {guide.console.label}
              <ArrowUpRight className="size-3.5" aria-hidden />
            </a>
          </div>
          <p className="mt-1 text-[0.8125rem] text-ink-3">Make the app under your own account, with your app’s name: that is the name people see. Where it says the callback URL above, use the redirect URI.</p>
          <ol className="mt-3 space-y-2.5">
            {guide.steps.map((s, i) => (
              <li key={i} className="grid grid-cols-[1.375rem_minmax(0,1fr)] gap-x-2 text-[0.8125rem] leading-[1.55] text-ink-2">
                <span aria-hidden className="mt-px grid size-[1.125rem] place-items-center rounded-full border border-rule-2 text-[0.6875rem] font-[600] text-ink-3 tnum">
                  {i + 1}
                </span>
                <span>{s}</span>
              </li>
            ))}
          </ol>
          {guide.pitfalls.length > 0 && (
            <ul className="mt-3 list-disc space-y-1 rounded-[8px] border border-rule bg-paper-sunk py-2.5 pr-3 pl-7 text-[0.8125rem] leading-[1.5] text-ink-2 marker:text-ink-4">
              {guide.pitfalls.map((p, i) => (
                <li key={i}>{p}</li>
              ))}
            </ul>
          )}
        </section>

        <section aria-labelledby={`${formId}-keys`}>
          <h3 id={`${formId}-keys`} className="text-[0.875rem] font-[550] text-ink">
            Keys from {guide.name}
          </h3>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            Saved as {project}’s own secrets: encrypted, never shown again, and a change in History you can undo.
            {own ? " Leave a field empty to keep what is saved." : ""}
          </p>
          <form
            id={formId}
            className="mt-3 grid gap-3.5"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <fieldset disabled={save.isPending} className="contents">
              {guide.fields.map((f) => (
                <KeyField
                  key={f.key}
                  f={f}
                  formId={formId}
                  value={values[f.key] ?? ""}
                  stored={own}
                  fileName={fileName}
                  onFile={setFileName}
                  onChange={(v) => setValues((s) => ({ ...s, [f.key]: v }))}
                />
              ))}
            </fieldset>
          </form>
          {save.isError && <ProblemNote className="mt-3" error={save.error} title={`Couldn’t save the ${name} keys`} />}
        </section>
      </div>
    </Sheet>
  );
}
