import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, FileKey2 } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { signInApi, signInQ, type BoxProvider, type BoxProviderInput } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { GUIDES, type Guide, type ProviderField } from "@/components/signin-guides";
import { PasteField, ProviderMark, TestedTag } from "@/components/signin-parts";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { Sheet } from "@/routes/data/sheet";

// Box settings → Sign-in providers: each provider's keys, set once for every
// project on the box. A project that turns the method on uses them unless
// it has its own. Each opens a side sheet with a short setup guide and the
// one callback URL to give the provider.

/** What the provider shows people with these keys: projects choosing between these and their own see it. */
const CONSENT: ProviderField = {
  key: "consentName",
  label: "Name people see",
  optional: true,
  placeholder: "Acme Labs",
  hint: "Your OAuth app’s name, as the provider shows it when people sign in. Projects see it when they choose between these keys and their own.",
};

const when = (iso?: string) => (iso ? new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "");

/** The list, for the Box settings page. `admin`: may set and remove keys. */
export function SignInProviders({ admin }: { admin: boolean }) {
  const list = useQuery(signInQ.providers);
  const [open, setOpen] = useState<string | null>(null);
  const [removing, setRemoving] = useState<BoxProvider | null>(null);
  const qc = useQueryClient();

  if (list.isError && notOnBox(list.error)) {
    return <p className="text-[0.875rem] text-ink-3">Sign-in providers are set on a box. Open this dashboard on your box to set them.</p>;
  }
  const byId = new Map((list.data?.providers ?? []).map((p) => [p.id, p]));
  const setCount = [...byId.values()].filter((p) => p.set).length;
  const guide = open ? GUIDES.find((g) => g.id === open) : undefined;

  return (
    <div>
      {list.data && (
        <p className="mb-3 text-[0.8125rem] text-ink-3">
          {setCount === 0 ? "None set yet." : `${setCount} of ${GUIDES.length} set.`} Every provider calls back to{" "}
          <code className="ident text-ink-2 [overflow-wrap:anywhere]">{list.data.callbackBase}/api/auth/callback/…</code>, whatever the project or its domain. Make these OAuth apps in your own provider accounts: the box never comes with keys of its own. Google and GitHub keys also put their button on this dashboard’s login page, for people already on the box.
        </p>
      )}
      {list.isError && <ProblemNote error={list.error} title="Couldn’t load the sign-in providers" />}
      <ul className="divide-y divide-rule border-y border-rule" aria-busy={list.isPending || undefined}>
        {list.isPending &&
          GUIDES.slice(0, 5).map((g) => (
            <li key={g.id} className="flex items-center gap-3 py-3">
              <Skeleton className="size-8 rounded-[8px]" />
              <Skeleton className="h-4 w-40" />
            </li>
          ))}
        {list.data &&
          GUIDES.map((g) => {
            const p = byId.get(g.id);
            return (
              <li key={g.id} className="flex items-center gap-3 py-2.5">
                <ProviderMark id={g.id} />
                <button type="button" onClick={() => setOpen(g.id)} className="group min-w-0 flex-1 text-left">
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="text-[0.875rem] font-[550] text-ink group-hover:underline group-hover:decoration-rule-3 group-hover:underline-offset-[3px]">{g.name}</span>
                    {g.tested && <TestedTag tested />}
                  </span>
                  <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[0.8125rem] text-ink-3">
                    {p?.set ? (
                      <>
                        <Check className="size-3.5 shrink-0 text-ok" aria-hidden />
                        <span className="truncate">
                          <span className="text-ink-2">Set · box-wide</span>
                          {p.usedBy && p.usedBy.length > 0 ? ` · used by ${p.usedBy.length === 1 ? p.usedBy[0] : `${p.usedBy.length} projects`}` : " · no project uses it yet"}
                        </span>
                      </>
                    ) : (
                      <span className="truncate">Not set</span>
                    )}
                    {!g.tested && <span className="shrink-0 text-ink-4 max-sm:hidden">· not tested yet</span>}
                  </span>
                </button>
                {admin && (
                  <div className="flex shrink-0 items-center gap-1">
                    {p?.set ? (
                      <>
                        <Button size="sm" variant="ghost" onClick={() => setOpen(g.id)}>
                          Replace
                        </Button>
                        <Button size="sm" variant="ghost" className="hover:text-danger max-sm:hidden" onClick={() => setRemoving(p)}>
                          Remove
                        </Button>
                      </>
                    ) : (
                      <Button size="sm" onClick={() => setOpen(g.id)}>
                        Set up
                      </Button>
                    )}
                  </div>
                )}
              </li>
            );
          })}
      </ul>
      {list.data && !admin && <p className="mt-3 text-[0.8125rem] text-ink-3">Only the box’s owner or an admin can set these. Open one to see how it’s set up.</p>}
      {guide && list.data && (
        <ProviderSheet
          key={guide.id}
          guide={guide}
          state={byId.get(guide.id)}
          callback={`${list.data.callbackBase}/api/auth/callback/${guide.id}`}
          admin={admin}
          open={!!open}
          onOpenChange={(o) => !o && setOpen(null)}
          onRemove={(p) => setRemoving(p)}
        />
      )}
      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Remove the box-wide ${removing ? (GUIDES.find((g) => g.id === removing.id)?.name ?? removing.id) : ""} keys?`}
        body={
          removing && removing.usedBy && removing.usedBy.length > 0 ? (
            <>
              {removing.usedBy.join(", ")} {removing.usedBy.length === 1 ? "signs" : "sign"} in with these keys. {removing.usedBy.length === 1 ? "Its" : "Their"} button stops
              working until you set keys again, here or on the project.
            </>
          ) : (
            "No project uses them right now. You can set them again any time."
          )
        }
        action="Remove keys"
        run={() => signInApi.remove(removing!.id)}
        done={() => {
          const name = GUIDES.find((g) => g.id === removing?.id)?.name ?? removing?.id;
          setRemoving(null);
          setOpen(null);
          void qc.invalidateQueries({ queryKey: signInQ.providers.queryKey });
          void qc.invalidateQueries({ queryKey: ["auth"] });
          toast({ title: `Removed the box-wide ${name} keys.` });
        }}
      />
    </div>
  );
}

/** One provider: its guide, the callback URL, and the form for its keys. */
function ProviderSheet({
  guide,
  state,
  callback,
  admin,
  open,
  onOpenChange,
  onRemove,
}: {
  guide: Guide;
  state?: BoxProvider;
  callback: string;
  admin: boolean;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onRemove: (p: BoxProvider) => void;
}) {
  const qc = useQueryClient();
  const formId = useId();
  const set = !!state?.set;
  const [values, setValues] = useState<Record<string, string>>(() => ({
    clientId: state?.clientId ?? "",
    tenantId: state?.tenantId ?? "",
    issuer: state?.issuer ?? "",
    label: state?.label ?? "",
    teamId: state?.teamId ?? "",
    keyId: state?.keyId ?? "",
    consentName: state?.consentName ?? "",
  }));
  const [fileName, setFileName] = useState("");
  const save = useMutation({
    mutationFn: () => {
      const body: BoxProviderInput = { clientId: values.clientId?.trim() ?? "" };
      for (const f of guide.fields) {
        const v = values[f.key]?.trim();
        if (f.key !== "clientId" && v) body[f.key] = f.key === "privateKey" ? values[f.key] : v;
      }
      if (values.consentName?.trim()) body.consentName = values.consentName.trim();
      return signInApi.set(guide.id, body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: signInQ.providers.queryKey });
      void qc.invalidateQueries({ queryKey: ["auth"] });
      toast({ title: set ? `Replaced the box-wide ${guide.name} keys.` : `${guide.name} is set up for every project on the box.` });
      onOpenChange(false);
    },
  });
  const missing = guide.fields.some((f) => !f.optional && !(f.secret && set) && !values[f.key]?.trim());

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      wide
      title={set ? `${guide.name} sign-in` : `Set up ${guide.name}`}
      sub={
        <span className="inline-flex flex-wrap items-center gap-2">
          {set ? `Box-wide keys, set ${when(state?.updatedAt)}` : "Box-wide keys: one setup for every project"}
          <TestedTag tested={guide.tested} />
        </span>
      }
      footer={
        admin ? (
          <>
            <Button type="submit" form={formId} variant="primary" disabled={save.isPending || missing}>
              {save.isPending ? "Saving…" : set ? `Replace ${guide.name} keys` : `Save ${guide.name} keys`}
            </Button>
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            {set && state && (
              <Button variant="danger-quiet" className="ml-auto" onClick={() => onRemove(state)}>
                Remove keys…
              </Button>
            )}
          </>
        ) : undefined
      }
    >
      <div className="space-y-7">
        <section aria-labelledby={`${formId}-cb`}>
          <h3 id={`${formId}-cb`} className="text-[0.875rem] font-[550] text-ink">
            Callback URL
          </h3>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">Paste this into {guide.name} exactly. It is the same for every project on this box.</p>
          <PasteField value={callback} label={`Copy the ${guide.name} callback URL`} className="mt-2" strong />
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
          <p className="mt-3 text-[0.8125rem] text-ink-3">
            Sign-in asks for <span className="text-ink-2">{guide.scopes}</span>.{" "}
            <a href={guide.docs} target="_blank" rel="noreferrer" className="underline decoration-rule-3 underline-offset-2 hover:text-ink">
              {guide.name}’s docs
            </a>
          </p>
        </section>

        {guide.pitfalls.length > 0 && (
          <section aria-labelledby={`${formId}-watch`} className="rounded-[8px] border border-rule bg-paper-sunk px-3.5 py-3">
            <h3 id={`${formId}-watch`} className="text-[0.8125rem] font-[550] text-ink">
              Good to know
            </h3>
            <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[0.8125rem] leading-[1.5] text-ink-2 marker:text-ink-4">
              {guide.pitfalls.map((p, i) => (
                <li key={i}>{p}</li>
              ))}
            </ul>
          </section>
        )}

        <section aria-labelledby={`${formId}-keys`}>
          <h3 id={`${formId}-keys`} className="text-[0.875rem] font-[550] text-ink">
            Keys from {guide.name}
          </h3>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            {admin
              ? "Stored encrypted on the box. Secrets are never shown again; leave one empty to keep the stored value."
              : "Only the box’s owner or an admin can change these."}
          </p>
          <form
            id={formId}
            className="mt-3 grid gap-3.5"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <fieldset disabled={!admin || save.isPending} className="contents">
              {guide.fields.map((f) => (
                <KeyField
                  key={f.key}
                  f={f}
                  formId={formId}
                  value={values[f.key] ?? ""}
                  stored={set && (f.key === "privateKey" || f.key === "clientSecret") && !!state?.secretSet}
                  fileName={fileName}
                  onFile={setFileName}
                  onChange={(v) => setValues((s) => ({ ...s, [f.key]: v }))}
                />
              ))}
              <KeyField
                f={CONSENT}
                formId={formId}
                value={values.consentName ?? ""}
                stored={false}
                fileName=""
                onFile={() => {}}
                onChange={(v) => setValues((s) => ({ ...s, consentName: v }))}
              />
            </fieldset>
          </form>
          {save.isError && <ProblemNote className="mt-3" error={save.error} title={`Couldn’t save the ${guide.name} keys`} />}
          {state?.appleSecretExpires && (
            <p className="mt-3 text-[0.8125rem] text-ink-3">The client secret the box signed runs until {when(state.appleSecretExpires)}; a new one is made a month before.</p>
          )}
        </section>

        {set && state?.usedBy && (
          <Row label="Used by">{state.usedBy.length ? state.usedBy.join(", ") : "No project has turned it on yet."}</Row>
        )}
      </div>
    </Sheet>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 border-t border-rule pt-3 text-[0.8125rem]">
      <span className="text-ink-3">{label}</span>
      <span className="min-w-0 text-ink-2 [overflow-wrap:anywhere]">{children}</span>
    </div>
  );
}

export function KeyField({
  f,
  formId,
  value,
  stored,
  fileName,
  onFile,
  onChange,
}: {
  f: ProviderField;
  formId: string;
  value: string;
  stored: boolean;
  fileName: string;
  onFile: (name: string) => void;
  onChange: (v: string) => void;
}) {
  const id = `${formId}-${f.key}`;
  const hint = stored ? "Stored. Leave empty to keep it." : f.hint;
  if (f.file) {
    return (
      <div>
        <Label htmlFor={id} className="text-[0.8125rem]">
          {f.label}
        </Label>
        <label
          htmlFor={id}
          className={cn(
            "mt-1.5 flex cursor-pointer items-center gap-3 rounded-[8px] border border-dashed px-3 py-2.5 text-[0.8125rem] transition-colors hover:bg-paper-hover",
            value ? "border-rule-2 text-ink" : "border-rule-3 text-ink-3",
          )}
        >
          <FileKey2 className="size-4 shrink-0 text-ink-3" aria-hidden />
          <span className="min-w-0 flex-1 truncate">{value ? `${fileName || "Key"} loaded` : stored ? "Stored. Choose a new .p8 file to replace it." : "Choose the AuthKey_….p8 file"}</span>
          <span className="shrink-0 text-ink-2 underline decoration-rule-3 underline-offset-2">Choose file</span>
        </label>
        <input
          id={id}
          type="file"
          accept=".p8,application/pkcs8,text/plain"
          className="sr-only"
          onChange={async (e) => {
            const file = e.target.files?.[0];
            if (!file) return;
            onFile(file.name);
            onChange(await file.text());
          }}
        />
      </div>
    );
  }
  return (
    <div>
      <div className="flex items-baseline justify-between gap-3">
        <Label htmlFor={id} className="text-[0.8125rem]">
          {f.label}
        </Label>
        {f.optional && <span className="text-[0.75rem] text-ink-4">Optional</span>}
      </div>
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        type={f.secret ? "password" : "text"}
        autoComplete="off"
        spellCheck={false}
        placeholder={stored ? "••••••••  (stored)" : f.placeholder}
        aria-describedby={hint ? `${id}-hint` : undefined}
        className="mt-1.5 font-mono text-[0.8125rem]"
      />
      {hint && (
        <p id={`${id}-hint`} className="mt-1 text-[0.75rem] text-ink-3">
          {hint}
        </p>
      )}
    </div>
  );
}
