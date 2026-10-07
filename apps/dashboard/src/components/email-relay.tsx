import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, CircleAlert, Send, TriangleAlert } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { emailApi, emailQ, mod, mq, type EmailPreset, type EmailProvider, type EmailStatus, type EmailWebhook } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { PROVIDER_NAME } from "@/components/email-parts";
import { BoxMailSection } from "@/components/email-box";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem, Select } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { useWho } from "@/lib/me";
import { relative } from "@/lib/time";

// Settings › Email: how mail leaves the box. Pick the mail service, paste
// its key, send a test; then, for SendGrid, Resend and Postmark, send the
// delivery events back so each message shows delivered, bounced or marked
// as spam. Box-wide: one relay serves every project.

type Relay = NonNullable<EmailStatus["relay"]>;
type Wrap = (p: { id?: string; title: string; note?: string; children: ReactNode }) => ReactNode;

const ORDER: EmailProvider[] = ["sendgrid", "resend", "postmark", "ses", "mailgun", "brevo", "cloudflare", "other"];

export function BoxEmailSection({ admin, Wrap }: { admin: boolean; Wrap: Wrap }) {
  const st = useQuery(mq.emailStatus);
  const presets = useQuery({ ...emailQ.providers, enabled: admin });
  // An older box (no providers) or a laptop dev server without the email module: say nothing.
  if (st.isError && st.error instanceof ApiError && [404, 501].includes(st.error.status)) return null;
  const relay = st.data?.mode === "relay" ? st.data.relay : undefined;
  return (
    <Wrap
      id="email"
      title="Email"
      note="How every project’s mail leaves the box. One mail service serves them all."
    >
      {st.isError ? (
        <ProblemNote error={st.error} />
      ) : !st.data || (admin && !presets.data && !presets.isError) ? (
        <div className="space-y-3" aria-busy>
          <Skeleton className="h-5 w-80" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <>
          <EmailSetup status={st.data} relay={relay} presets={presets.data ?? []} admin={admin} />
          <BoxMailSection admin={admin} />
        </>
      )}
    </Wrap>
  );
}

function EmailSetup({ status, relay, presets, admin }: { status: EmailStatus; relay?: Relay; presets: EmailPreset[]; admin: boolean }) {
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const qc = useQueryClient();
  const who = useWho();
  const preset = relay ? presets.find((p) => p.id === relay.provider) : undefined;
  const hooks = status.webhooks ?? [];

  return (
    <div className="min-w-0">
      {relay ? (
        <div className="border-y border-rule py-3.5">
          <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2">
            <div className="min-w-0">
              <p className="flex items-center gap-2 text-[0.9375rem] text-ink">
                <span aria-hidden className="size-[7px] shrink-0 rounded-full bg-ok" />
                Sending through <b className="font-[550]">{PROVIDER_NAME[relay.provider ?? "other"] ?? relay.host}</b>
              </p>
              <p className="mt-1 text-[0.8125rem] text-ink-3">
                <span className="ident">
                  {relay.host}:{relay.port}
                </span>
                {" · "}
                {relay.tls === "none" ? "no encryption" : relay.tls === "tls" ? "TLS" : "STARTTLS"}
                {relay.username ? <> · user <span className="ident">{relay.username}</span></> : null}
                {relay.region ? <> · {relay.region}</> : null}
              </p>
              <p className="mt-0.5 text-[0.8125rem] text-ink-3">
                {relay.passwordSet ? "Key stored encrypted, never shown." : "No key stored."} Changed {relative(relay.updatedAt)}
                {relay.updatedBy ? ` by ${who(relay.updatedBy)}` : ""}.
              </p>
            </div>
            {admin && !editing && (
              <span className="flex shrink-0 gap-1">
                <Button size="sm" onClick={() => setEditing(true)}>
                  Replace
                </Button>
                <Button size="sm" variant="danger-quiet" onClick={() => setRemoving(true)}>
                  Remove…
                </Button>
              </span>
            )}
          </div>
          {status.queued > 0 && <p className="mt-2 text-[0.8125rem] text-warn-ink">{int(status.queued)} waiting to go out.</p>}
        </div>
      ) : (
        <p className="max-w-[40rem] text-[0.875rem] text-ink-2">
          <span className="text-ink">Nothing leaves the box yet.</span> Connect the mail service you use and production mail goes out for real. Mail from preview
          deployments always stays in the dev inbox.
        </p>
      )}

      {admin && (!relay || editing) && (
        <RelayForm
          presets={presets}
          relay={relay}
          onDone={() => setEditing(false)}
          onCancel={relay ? () => setEditing(false) : undefined}
        />
      )}
      {!admin && <p className="mt-3 text-[0.8125rem] text-ink-3">Only the box’s owner or an admin can change how mail is sent.</p>}

      {relay && admin && !editing && <TestSend relay={relay} preset={preset} />}

      {relay && !editing && !preset?.events && relay.provider && (
        <p className="mt-6 max-w-[40rem] text-[0.8125rem] text-ink-3">
          {preset?.eventsNote ?? "Delivery events come back from SendGrid, Resend and Postmark."} With {PROVIDER_NAME[relay.provider] ?? "this relay"}, a message’s
          status stops at Sent: the relay accepted it.
        </p>
      )}
      {!editing &&
        hooks.map((h) => (
          <Events key={h.provider} hook={h} preset={presets.find((p) => p.id === h.provider)} admin={admin} current={relay?.provider === h.provider} />
        ))}

      <Confirm
        open={removing}
        onClose={() => setRemoving(false)}
        title="Remove the mail service?"
        body="The box goes back to catching every message in the dev inbox. Mail already queued waits until you connect one again. The key is forgotten."
        action="Remove"
        tone="normal"
        run={() => mod.removeRelay()}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["email-status"] });
          toast({ title: "Mail stays on the box now.", detail: "Every project’s mail goes to its dev inbox." });
        }}
      />
    </div>
  );
}

// ------------------------------------------------------------------ picking a provider and pasting its key

function RelayForm({ presets, relay, onDone, onCancel }: { presets: EmailPreset[]; relay?: Relay; onDone: () => void; onCancel?: () => void }) {
  const qc = useQueryClient();
  const [pick, setPick] = useState<EmailProvider | undefined>(relay?.provider);
  const preset = presets.find((p) => p.id === pick);
  const ordered = ORDER.map((id) => presets.find((p) => p.id === id)).filter((p): p is EmailPreset => !!p);

  return (
    <div className={cn("mt-5", relay && "rounded-[12px] border border-rule-2 bg-paper-raised p-4 sm:p-5")}>
      <p id="mail-service" className="text-[0.875rem] font-[550] text-ink">
        {relay ? "Replace the mail service" : "Your mail service"}
      </p>
      <RadioGroup
        aria-labelledby="mail-service"
        orientation="horizontal"
        value={pick ?? ""}
        onValueChange={(v) => setPick(v as EmailProvider)}
        className="mt-2.5 flex flex-wrap gap-1.5"
      >
        {ordered.map((p) => (
          <RadioItem
            key={p.id}
            value={p.id}
            className="inline-flex h-8 items-center gap-1.5 rounded-[7px] border border-rule-2 bg-paper-raised px-3 text-[0.84375rem] text-ink-2 transition-colors duration-[var(--dur-state)] outline-hidden hover:border-rule-3 hover:text-ink focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] data-[state=checked]:border-ink data-[state=checked]:font-[550] data-[state=checked]:text-ink data-[state=checked]:shadow-[inset_0_0_0_0.5px_var(--ink)]"
          >
            {p.id === "cloudflare" ? "Cloudflare" : p.name}
            {p.beta && <span className="rounded-[4px] bg-paper-sunk px-1 text-[0.6875rem] font-[450] text-ink-3 ring-1 ring-rule-2">Beta</span>}
          </RadioItem>
        ))}
      </RadioGroup>
      {preset ? (
        <PresetFields key={preset.id} preset={preset} relay={relay?.provider === preset.id ? relay : undefined} onDone={onDone} qc={qc} />
      ) : (
        <p className="mt-3 text-[0.8125rem] text-ink-3">Pick one and the box fills in its server, port and user name. You paste one key.</p>
      )}
      {onCancel && (
        <button type="button" onClick={onCancel} className="mt-3 text-[0.8125rem] text-ink-3 hover:text-ink">
          Keep {PROVIDER_NAME[relay?.provider ?? "other"] ?? "the current relay"}
        </button>
      )}
    </div>
  );
}

function PresetFields({ preset, relay, onDone, qc }: { preset: EmailPreset; relay?: Relay; onDone: () => void; qc: ReturnType<typeof useQueryClient> }) {
  const other = preset.id === "other";
  const regions = preset.regions ?? [];
  const [region, setRegion] = useState(relay?.region ?? regions[0]?.id ?? "");
  const [username, setUsername] = useState(relay?.username && !preset.username ? relay.username : "");
  const [key, setKey] = useState("");
  const [host, setHost] = useState(other ? (relay?.host ?? "") : "");
  const [tls, setTls] = useState<"starttls" | "tls" | "none">(relay?.tls ?? preset.tls ?? "starttls");
  const [port, setPort] = useState(String(relay?.port ?? preset.port ?? 587));
  const [tuning, setTuning] = useState(false);
  const keyKept = !!relay?.passwordSet;
  const needsUser = !!preset.usernameLabel || other;
  const shownHost = other ? host : (regions.find((r) => r.id === region)?.host ?? preset.host ?? "");
  const customPort = other || tuning;
  const ready = (other ? host.trim() !== "" : true) && (!preset.usernameLabel || username.trim() !== "") && (key.trim() !== "" || keyKept || other);

  const save = useMutation({
    mutationFn: () =>
      emailApi.setRelay({
        provider: preset.id,
        region: regions.length ? region : undefined,
        host: other ? host.trim() : undefined,
        port: customPort ? Number(port) || undefined : undefined,
        tls: customPort ? tls : undefined,
        username: needsUser ? username.trim() || undefined : undefined,
        password: key.trim() ? key.trim() : undefined,
      }),
    onSuccess: (st) => {
      qc.setQueryData(mq.emailStatus.queryKey, st);
      setKey("");
      onDone();
      toast({ title: `${preset.id === "other" ? "The relay" : preset.name} is connected.`, detail: "Send a test to check the key works." });
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (ready && !save.isPending) save.mutate();
  };

  return (
    <form onSubmit={submit} className="mt-5 grid max-w-[36rem] gap-4">
      {preset.note && (
        <p className="flex items-start gap-2 rounded-[8px] bg-paper-sunk px-3 py-2.5 text-[0.8125rem] text-ink-2">
          <CircleAlert className="mt-0.5 size-4 shrink-0 text-ink-3" />
          {preset.note}
        </p>
      )}
      {!other && (
        <p className="text-[0.8125rem] text-ink-3">
          Filled in for you:{" "}
          <span className="ident text-ink-2">
            {shownHost || "…"}:{customPort ? port : preset.port}
          </span>
          , {(customPort ? tls : preset.tls) === "tls" ? "TLS" : (customPort ? tls : preset.tls) === "none" ? "no encryption" : "STARTTLS"}
          {preset.username ? (
            <>
              , user <span className="ident text-ink-2">{preset.username}</span>
            </>
          ) : preset.usernameIsKey ? (
            ", the token as user name and password"
          ) : null}
          .{" "}
          {!tuning && (
            <button type="button" className="text-ink-3 underline decoration-rule-3 underline-offset-[3px] hover:text-ink" onClick={() => setTuning(true)}>
              Change the port
            </button>
          )}
        </p>
      )}

      {other && (
        <Field id="r-host" label="SMTP host">
          <Input id="r-host" value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" autoComplete="off" spellCheck={false} className="ident" />
        </Field>
      )}
      {customPort && (
        <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3">
          <Field id="r-port" label="Port">
            <Input id="r-port" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} inputMode="numeric" className="tnum" />
          </Field>
          <Field id="r-tls" label="Security">
            <Select
              id="r-tls"
              value={tls}
              onValueChange={(v) => setTls(v as typeof tls)}
              options={[
                { value: "starttls", label: "STARTTLS (587, 2587, 2525)" },
                { value: "tls", label: "TLS (465, 2465)" },
                { value: "none", label: "None: local test servers only" },
              ]}
            />
          </Field>
        </div>
      )}
      {regions.length > 0 && (
        <Field id="r-region" label="Region" hint={preset.id === "ses" ? "Where your SES identities and SMTP credentials live." : undefined}>
          <Select id="r-region" value={region} onValueChange={setRegion} options={regions.map((r) => ({ value: r.id, label: `${r.label} · ${r.id}` }))} />
        </Field>
      )}
      {needsUser && (
        <Field id="r-user" label={preset.usernameLabel ?? "Username"} hint={preset.usernameHint}>
          <Input id="r-user" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} className="ident" />
        </Field>
      )}
      {!other && (preset.keyUrl || preset.domainUrl) && (
        <ul className="grid gap-2 border-y border-rule py-3 text-[0.8125rem]">
          {preset.keyUrl && (
            <li className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4">
              <span className="min-w-0 text-ink-2">
                <span className="text-ink">The key needs:</span> {preset.permission}
              </span>
              <Ext href={preset.keyUrl}>Create one</Ext>
            </li>
          )}
          {preset.domainUrl && (
            <li className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4">
              <span className="min-w-0 text-ink-2">Mail is sent from addresses on a domain you verified with {preset.name}.</span>
              <Ext href={preset.domainUrl}>Verify a domain</Ext>
            </li>
          )}
        </ul>
      )}

      <Field id="r-key" label={preset.keyLabel} hint={preset.keyHint}>
        <Input
          id="r-key"
          type="password"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          autoComplete="new-password"
          spellCheck={false}
          placeholder={keyKept ? "Leave empty to keep the stored one" : ""}
          className="ident"
        />
      </Field>

      {save.isError && <ProblemNote error={save.error} />}
      <div className="flex flex-col-reverse items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-[0.8125rem] text-ink-3">Stored encrypted on the box and never shown again.</p>
        <Button type="submit" variant="primary" disabled={!ready || save.isPending}>
          {save.isPending ? "Saving…" : `Connect ${other ? (host.trim() || "the relay") : preset.name}`}
        </Button>
      </div>
    </form>
  );
}

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-ink-3">{hint}</p>}
    </div>
  );
}

function Ext({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className="inline-flex shrink-0 items-center gap-0.5 font-[550] text-ink hover:underline hover:underline-offset-[3px]">
      {children}
      <ArrowUpRight className="size-3.5 text-ink-3" aria-hidden />
    </a>
  );
}

// ------------------------------------------------------------------ send a test

function TestSend({ relay, preset }: { relay: Relay; preset?: EmailPreset }) {
  const [to, setTo] = useState("");
  const test = useMutation({ mutationFn: (addr: string) => mod.testRelay(addr) });
  const name = preset && preset.id !== "other" ? preset.name : relay.host;
  const ok = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(to.trim());
  const r = test.data;
  return (
    <div className="mt-6">
      <p className="label mb-2">Send a test</p>
      <form
        className="flex max-w-[36rem] flex-col gap-2 sm:flex-row"
        onSubmit={(e) => {
          e.preventDefault();
          if (ok && !test.isPending) test.mutate(to.trim());
        }}
      >
        <Input value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" type="email" aria-label="Send a test email to" />
        <Button type="submit" disabled={!ok || test.isPending}>
          <Send />
          {test.isPending ? "Sending…" : "Send a test"}
        </Button>
      </form>
      <div aria-live="polite">
        {r?.ok && (
          <p className="mt-2.5 flex items-start gap-2 text-[0.8125rem] text-ink-2">
            <Check className="mt-0.5 size-4 shrink-0 text-ok" strokeWidth={2.5} />
            <span>
              <span className="text-ink">{name} accepted it.</span> Check the inbox of {test.variables}; if it isn’t there, look in spam.
              <span className="ident mt-0.5 block text-xs break-all text-ink-3">{r.detail}</span>
            </span>
          </p>
        )}
        {r && !r.ok && (
          <div className="mt-2.5 max-w-[40rem] rounded-[8px] border border-danger-rule bg-danger-wash px-3.5 py-2.5 text-[0.8125rem]">
            <p className="flex items-start gap-2 text-ink">
              <CircleAlert className="mt-0.5 size-4 shrink-0 text-danger" />
              {r.hint || "The test didn’t go through."}
            </p>
            <p className="ident mt-1 pl-6 text-xs leading-5 break-words text-ink-2">{r.detail}</p>
          </div>
        )}
        {test.isError && <ProblemNote className="mt-2" error={test.error} />}
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ delivery events

type HookState = "off" | "waiting" | "receiving";
function hookState(h: EmailWebhook): HookState {
  if (!h.keySet) return "off";
  return h.receiving ? "receiving" : "waiting";
}

function Events({ hook, preset, admin, current }: { hook: EmailWebhook; preset?: EmailPreset; admin: boolean; current: boolean }) {
  const qc = useQueryClient();
  const [replacing, setReplacing] = useState(false);
  const [turningOff, setTurningOff] = useState(false);
  const [key, setKey] = useState("");
  const [secretUrl, setSecretUrl] = useState<string | null>(null);
  const ev = preset?.events;
  const name = PROVIDER_NAME[hook.provider];
  const state = hookState(hook);
  const save = useMutation({
    mutationFn: () => emailApi.setWebhook(hook.provider, ev?.security === "basic" ? undefined : key.trim()),
    onSuccess: (r) => {
      setKey("");
      setReplacing(false);
      if (r.secretUrl) setSecretUrl(r.secretUrl);
      void qc.invalidateQueries({ queryKey: ["email-status"] });
      if (!r.secretUrl) toast({ title: `${name} events are on.`, detail: "The box shows Receiving events when the first one arrives." });
    },
  });
  const refused = hook.lastRejectedAt && (!hook.lastEventAt || hook.lastRejectedAt > hook.lastEventAt) ? hook : null;
  const showSteps = admin && (state === "off" || replacing);
  const basic = ev?.security === "basic";

  return (
    <section className="mt-8 border-t border-rule pt-5" aria-labelledby={`ev-${hook.provider}`}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h3 id={`ev-${hook.provider}`} className="text-[0.9375rem] font-[550] text-ink">
          {current ? "Delivery events" : `${name} delivery events`}
        </h3>
        <StatePill state={state} hook={hook} />
      </div>
      {state === "off" && (
        <p className="mt-1 max-w-[40rem] text-[0.8125rem] text-ink-2">
          Without them, the box only knows {name} accepted a message. With them, each message shows whether it was delivered, bounced or marked as spam, and
          addresses that bounce or complain are added to the project’s suppression list for you.
        </p>
      )}
      {!current && state !== "off" && (
        <p className="mt-1 text-[0.8125rem] text-ink-3">Still on for mail sent through {name} before. Turn it off when that mail has settled.</p>
      )}
      {state === "receiving" && hook.lastEventAt && (
        <p className="mt-1 text-[0.8125rem] text-ink-2">
          Last request {relative(hook.lastEventAt)}. {int(hook.requests)} {hook.requests === 1 ? "request" : "requests"} since the key was saved,{" "}
          {int(hook.matched)} {hook.matched === 1 ? "event" : "events"} matched to messages.
        </p>
      )}
      {state === "waiting" && (
        <p className="mt-1 text-[0.8125rem] text-ink-2">
          The key is saved{hook.configuredAt ? ` (${relative(hook.configuredAt)})` : ""}. Nothing signed has arrived yet: send a test from {name}, or send a message.
        </p>
      )}
      {!hook.public && (
        <p className="mt-3 flex max-w-[40rem] items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-[0.8125rem] text-ink">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
          <span>
            {name} can’t reach this box at <span className="ident">{new URL(hook.url).host}</span>: events need an address on the internet. Set the box’s domain
            above first.
          </span>
        </p>
      )}
      {refused && (
        <p className="mt-3 flex max-w-[40rem] items-start gap-2 rounded-[8px] border border-rule-2 bg-paper-sunk px-3 py-2.5 text-[0.8125rem] text-ink">
          <CircleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
          <span>
            The last request was refused {relative(refused.lastRejectedAt!)}: <span className="text-ink-2">{refused.lastRejection}</span>.
            {state !== "off" && !basic ? ` Check the ${ev?.keyLabel?.toLowerCase() ?? "key"} is this webhook’s.` : ""}
          </span>
        </p>
      )}

      {!(ev && showSteps) && <WebhookAddress url={hook.url} user={hook.user} basic={basic} />}
      {ev && showSteps && (
        <div className="mt-4 max-w-[40rem]">
          <p className="text-[0.8125rem]">
            <Ext href={ev.setupUrl}>Open {name}’s webhook settings</Ext>
          </p>
          <ol className="mt-2.5 grid gap-1.5 text-[0.8125rem] text-ink-2">
            {(ev.steps ?? []).map((s, i) => (
              <li key={i} className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-2.5">
                <span className="ident mt-px grid size-5 place-items-center rounded-full border border-rule-2 text-[0.6875rem] text-ink-3">{i + 1}</span>
                <span className="pt-0.5">{s}</span>
              </li>
            ))}
          </ol>
          <WebhookAddress url={hook.url} user={hook.user} basic={basic} />
          <p className="mt-3 text-[0.8125rem] text-ink-3">Events to turn on:</p>
          <ul className="mt-1.5 flex flex-wrap gap-1.5">
            {(ev.enable ?? []).map((x) => {
              const optional = x.endsWith(" (optional)");
              return (
                <li
                  key={x}
                  className={cn(
                    "rounded-[5px] border px-1.5 py-0.5",
                    // Resend's event names are identifiers (email.delivered); SendGrid's and Postmark's are words.
                    x.includes(".") ? "font-mono text-[0.75rem]" : "text-[0.8125rem]",
                    optional ? "border-dashed border-rule-2 text-ink-3" : "border-rule-2 bg-paper-sunk text-ink-2",
                  )}
                >
                  {x.replace(" (optional)", "")}
                  {optional && <span className="sr-only"> (optional)</span>}
                </li>
              );
            })}
          </ul>
          {ev.openNotes && <p className="mt-1.5 text-xs text-ink-3">Dashed ones are optional. {ev.openNotes}</p>}
        </div>
      )}

      {admin && secretUrl && (
        <div className="mt-4 max-w-[40rem] rounded-[10px] border border-brass/50 bg-brass-wash/40 px-3.5 py-3">
          <p className="text-[0.8125rem] font-[550] text-ink">Paste this address into Postmark now. It is shown this once.</p>
          <div className="mt-2 flex items-center gap-1 rounded-[7px] border border-rule-2 bg-paper py-1 pr-1 pl-2.5">
            <code className="min-w-0 flex-1 truncate font-mono text-[0.75rem] text-ink" title="The webhook address, with its password">
              {secretUrl}
            </code>
            <CopyButton value={secretUrl} label="Copy the address with its password" />
          </div>
          <p className="mt-1.5 text-xs text-ink-3">It carries the password Postmark sends with each event. Making a new one retires this one.</p>
        </div>
      )}

      {admin && showSteps && ev && (
        <form
          className="mt-4 max-w-[40rem]"
          onSubmit={(e) => {
            e.preventDefault();
            if ((basic || key.trim()) && !save.isPending) save.mutate();
          }}
        >
          {basic ? (
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {save.isPending ? "Making it…" : "Make the webhook address"}
            </Button>
          ) : (
            <>
              <Label htmlFor={`ev-key-${hook.provider}`}>{ev.keyLabel}</Label>
              <div className="mt-1.5 flex flex-col gap-2 sm:flex-row">
                <Input
                  id={`ev-key-${hook.provider}`}
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={ev.security === "svix" ? "whsec_…" : "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE…"}
                  className="ident text-[0.8125rem] sm:flex-1"
                />
                <Button type="submit" variant="primary" disabled={!key.trim() || save.isPending}>
                  {save.isPending ? "Saving…" : "Save the key"}
                </Button>
              </div>
              {ev.keyHint && <p className="mt-1.5 text-xs text-ink-3">{ev.keyHint}</p>}
            </>
          )}
          {save.isError && <ProblemNote className="mt-3" error={save.error} />}
          {replacing && (
            <button type="button" onClick={() => setReplacing(false)} className="mt-3 block text-[0.8125rem] text-ink-3 hover:text-ink">
              Keep the saved {basic ? "address" : (ev.keyLabel?.toLowerCase() ?? "key")}
            </button>
          )}
        </form>
      )}

      {admin && state !== "off" && !replacing && (
        <div className="mt-3 flex flex-wrap gap-1">
          <Button size="sm" variant="ghost" className="-ml-2.5" onClick={() => setReplacing(true)}>
            {basic ? "Make a new address" : `Replace the ${ev?.keyLabel?.toLowerCase() ?? "key"}`}
          </Button>
          <Button size="sm" variant="danger-quiet" onClick={() => setTurningOff(true)}>
            Turn off…
          </Button>
        </div>
      )}

      <Confirm
        open={turningOff}
        onClose={() => setTurningOff(false)}
        title={`Turn off ${name} events?`}
        body={`The box forgets the key and refuses ${name}’s event requests. Remove the webhook in ${name} too, or it keeps retrying.`}
        action="Turn off"
        tone="normal"
        run={() => emailApi.removeWebhook(hook.provider)}
        done={() => {
          setSecretUrl(null);
          void qc.invalidateQueries({ queryKey: ["email-status"] });
        }}
      />
    </section>
  );
}

function StatePill({ state, hook }: { state: HookState; hook: EmailWebhook }) {
  const s = {
    off: { word: "Off", dot: "shadow-[inset_0_0_0_1.5px_var(--ink-4)]", text: "text-ink-3" },
    waiting: { word: "Waiting for the first event", dot: "bg-warn", text: "text-warn-ink" },
    receiving: { word: "Receiving events", dot: "bg-ok", text: "text-ink" },
  }[state];
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-[0.8125rem]", s.text)} title={hook.lastEventAt ? `Last request ${relative(hook.lastEventAt)}` : undefined}>
      <span aria-hidden className={cn("size-[7px] rounded-full", s.dot)} />
      {s.word}
    </span>
  );
}

function WebhookAddress({ url, user, basic }: { url: string; user?: string; basic: boolean }) {
  return (
    <div className="mt-4 max-w-[40rem]">
      <p className="text-[0.8125rem] text-ink-3">{basic ? `Webhook address (Postmark adds the user name ${user ?? "tiffin"} and the password)` : "Webhook address"}</p>
      <div className="mt-1.5 flex items-center gap-1 rounded-[7px] border border-rule-2 bg-paper-sunk py-1 pr-1 pl-2.5">
        <code className="min-w-0 flex-1 font-mono text-[0.8125rem] break-all text-ink">{url}</code>
        <CopyButton value={url} label="Copy the webhook address" />
      </div>
    </div>
  );
}
