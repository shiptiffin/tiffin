import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { q } from "@/api/queries";
import type { AuthOverview, ProviderState } from "@/api/modules";
import { Breaker } from "@/components/breaker";
import { CopyButton } from "@/components/copy";
import { ArrowUpRight } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { signInApi } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { ProblemNote } from "@/components/problem";
import { AppKeysSheet } from "@/components/signin-app-keys";
import { GUIDES } from "@/components/signin-guides";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup } from "@/components/ui/choice";
import { PasteField, ProviderMark } from "@/components/signin-parts";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { change, pendingFor, usePending } from "@/lib/staged";
import { Sheet } from "@/routes/data/sheet";

// How people sign in to a project's apps: the methods, teams, email
// verification, and the endpoint. Each switch is a change to
// tiffin.config.ts (services.auth), applied at once with Undo.

const AUTH = ["services", "auth"];

/** The ways to sign in that the box runs itself. */
const OWN: Array<{ id: string; name: string; about: string; email?: boolean }> = [
  { id: "email", name: "Email and password", about: "The classic: an address and a password they choose." },
  { id: "magic-link", name: "Magic link", about: "A one-time sign-in link sent by email. No password.", email: true },
  { id: "otp", name: "One-time code", about: "A short code sent by email, typed in to sign in.", email: true },
  { id: "passkey", name: "Passkeys", about: "Face ID, Touch ID, Windows Hello or a security key." },
];

export const METHODS: Array<{ id: string; name: string }> = [...OWN, ...GUIDES.map((g) => ({ id: g.id, name: g.id === "oidc" ? "Single sign-on (OIDC)" : g.name }))];

export const methodName = (id: string) => METHODS.find((m) => m.id === id)?.name ?? id;

const verifyNote: Record<string, string> = {
  manifest: "Set for this project.",
  relay: "On by itself: the box sends real mail, so new people confirm their address.",
  "no-relay": "Off by itself while mail only reaches the dev inbox, so test sign-ups work at once. It turns on once the box has an SMTP relay.",
  "no-email": "Off: the project has no email service to send the confirmation.",
};

type AuthSpec = { methods?: string[]; organizations?: boolean; emailVerification?: boolean };

/** Re-reads the auth overview once a staged auth change has landed. */
function useRefreshAfter(project: string, keys: string[]) {
  const qc = useQueryClient();
  const pending = usePending(project);
  const busy = keys.some((k) => pendingFor(pending, k));
  const was = useRef(busy);
  useEffect(() => {
    if (was.current && !busy) void qc.invalidateQueries({ queryKey: ["auth", project] });
    was.current = busy;
  }, [busy, qc, project]);
  return pending;
}

export function AuthSettings({ project, o, open, onOpenChange }: { project: string; o: AuthOverview; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { can } = useMe();
  const writer = can("apply:reversible");
  const manifest = useQuery({ ...q.manifest(project), enabled: open });
  const spec = (manifest.data?.manifest as { services?: { auth?: AuthSpec } } | undefined)?.services?.auth;
  const key = (f: string) => `set:${[...AUTH, f].join("/")}`;
  const pending = useRefreshAfter(project, [key("methods"), key("organizations"), key("emailVerification")]);
  const stagedMethods = pendingFor(pending, key("methods"));
  const methods = stagedMethods?.kind === "set" ? ((stagedMethods.to as string[] | undefined) ?? ["email", "magic-link"]) : (o.methods ?? []);
  const stagedOrgs = pendingFor(pending, key("organizations"));
  const stagedVerify = pendingFor(pending, key("emailVerification"));
  const noEmail = o.emailVerification.source === "no-email";

  const setMethods = (next: string[], what: string, undo: string) =>
    change(project, { kind: "set", path: [...AUTH, "methods"], from: spec?.methods, to: [...next].sort(), what, undo }, { immediate: true });
  const flip = (id: string, name: string, next: "on" | "off") =>
    setMethods(
      next === "on" ? [...methods, id] : methods.filter((x) => x !== id),
      next === "on" ? `Let people sign in to ${project} with ${lower(name)}` : `Stop offering ${lower(name)} on ${project}`,
      next === "on" ? `${lower(name)} is offered again` : `${lower(name)} is off again`,
    );
  const providers = providerStates(o);

  return (
    <Sheet open={open} onOpenChange={onOpenChange} title="Sign-in settings" sub="Changes apply at once, with Undo." wide>
      <fieldset disabled={!writer} className="min-w-0 space-y-8">
        <section aria-labelledby="ways">
          <h3 id="ways" className="text-[0.9375rem] font-[550] text-ink">
            Ways to sign in
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">What {project}’s sign-in screens offer. At least one stays on.</p>
          <ul className="mt-3 divide-y divide-rule border-y border-rule">
            {OWN.map((m) => {
              const on = methods.includes(m.id);
              const busy = stagedMethods?.kind === "set" && (o.methods ?? []).includes(m.id) !== on;
              return (
                <li key={m.id} className="py-3">
                  <div className="flex items-center gap-3">
                    <div className="min-w-0 flex-1">
                      <p className="text-[0.875rem] text-ink">{m.name}</p>
                      <p className="mt-0.5 text-xs text-ink-3">
                        {m.about}
                        {m.email && noEmail && <span className="text-warn-ink"> Needs the email service, which this project doesn’t have.</span>}
                        {(m.email || m.id === "email") && !noEmail && o.emailBlocked && (
                          <span className="text-warn-ink"> Refused in production until the box can send email.</span>
                        )}
                      </p>
                    </div>
                    <Breaker
                      label={m.name}
                      state={(o.methods ?? []).includes(m.id) ? "on" : "off"}
                      staged={busy ? (on ? "on" : "off") : undefined}
                      disabled={!writer || (on && methods.length === 1)}
                      onFlip={(next) => flip(m.id, m.name, next)}
                    />
                  </div>
                </li>
              );
            })}
          </ul>
        </section>

        <section aria-labelledby="providers">
          <h3 id="providers" className="text-[0.9375rem] font-[550] text-ink">
            Sign-in providers
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">
            Sign in with another account. Each uses either the box’s keys, a quick start that shows the box’s app name, or {project}’s own keys, which show your
            app’s name. Accounts and sessions stay in {project} either way.
          </p>
          <ul className="mt-3 divide-y divide-rule border-y border-rule">
            {providers.map((st) => {
              const name = METHODS.find((m) => m.id === st.id)?.name ?? st.name;
              const on = methods.includes(st.id);
              const busy = stagedMethods?.kind === "set" && (o.methods ?? []).includes(st.id) !== on;
              return (
                <li key={st.id} className="py-2.5">
                  <div className="flex items-center gap-3">
                    <ProviderMark id={st.id} className="size-7 rounded-[7px]" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-[0.875rem] text-ink">{name}</p>
                      <KeySource st={st} />
                    </div>
                    <Breaker
                      label={`Sign in with ${name}`}
                      state={(o.methods ?? []).includes(st.id) ? "on" : "off"}
                      staged={busy ? (on ? "on" : "off") : undefined}
                      disabled={!writer || (on && methods.length === 1)}
                      onFlip={(next) => flip(st.id, name, next)}
                    />
                  </div>
                  {on && <KeyChoice project={project} st={st} name={name} hosts={o.hosts ?? []} writer={writer} />}
                </li>
              );
            })}
          </ul>
        </section>

        <section aria-labelledby="rules" className="space-y-5">
          <h3 id="rules" className="text-[0.9375rem] font-[550] text-ink">
            Accounts
          </h3>
          <div className="flex items-start gap-3">
            <Breaker
              label="Require email verification"
              state={o.emailVerification.required ? "on" : "off"}
              staged={stagedVerify?.kind === "set" ? (stagedVerify.to ? "on" : "off") : undefined}
              disabled={!writer}
              className="mt-0.5"
              onFlip={(next) =>
                change(
                  project,
                  {
                    kind: "set",
                    path: [...AUTH, "emailVerification"],
                    from: o.emailVerification.source === "manifest" ? o.emailVerification.required : undefined,
                    to: next === "on",
                    what: next === "on" ? `Require email verification for ${project}` : `Let people sign in to ${project} without confirming their email`,
                    undo: next === "on" ? "new people sign in without confirming again" : "new people confirm their email again",
                  },
                  { immediate: true },
                )
              }
            />
            <div>
              <p className="text-[0.875rem] text-ink">Require email verification</p>
              <p className="mt-0.5 text-xs text-ink-3">{verifyNote[o.emailVerification.source] ?? ""}</p>
            </div>
          </div>
          <div className="flex items-start gap-3">
            <Breaker
              label="Organizations"
              state={o.organizations ? "on" : "off"}
              staged={stagedOrgs?.kind === "set" ? (stagedOrgs.to ? "on" : "off") : undefined}
              disabled={!writer}
              className="mt-0.5"
              onFlip={(next) =>
                change(
                  project,
                  {
                    kind: "set",
                    path: [...AUTH, "organizations"],
                    from: spec?.organizations,
                    to: next === "on",
                    what: next === "on" ? `Let people on ${project} make organizations` : `Turn organizations off for ${project}`,
                    undo: next === "on" ? "organizations are off again" : "organizations are on again",
                  },
                  { immediate: true },
                )
              }
            />
            <div>
              <p className="text-[0.875rem] text-ink">Organizations</p>
              <p className="mt-0.5 text-xs text-ink-3">Teams with owners, admins, members and viewers, and invitations to join them.</p>
            </div>
          </div>
        </section>

        <section aria-labelledby="where">
          <h3 id="where" className="text-[0.9375rem] font-[550] text-ink">
            Endpoint
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">
            Every app gets it as <code className="ident text-ink-2">TIFFIN_AUTH_URL</code>; each of the project’s addresses serves it at /api/auth.
          </p>
          <Field value={o.endpoint} label="Copy the auth endpoint" className="mt-2.5" />
          {(o.hosts ?? []).length > 1 && (
            <p className="mt-2 text-xs text-ink-3">
              Also on {(o.hosts ?? []).slice(1).map((h, i) => (
                <span key={h}>
                  {i > 0 && ", "}
                  <code className="ident">{h}</code>
                </span>
              ))}
              .
            </p>
          )}
        </section>
      </fieldset>
      {!writer && <p className="mt-6 text-sm text-ink-3">Changing these needs a key that can make changes to {project}.</p>}
    </Sheet>
  );
}

/** A method's name inside a sentence: the box's own ones in lower case, providers as they spell themselves. */
const lower = (name: string) => (OWN.some((m) => m.name === name) ? name.toLowerCase() : name);

/** Every provider's state; older boxes only said whether Google and GitHub have keys. */
function providerStates(o: AuthOverview): ProviderState[] {
  if (o.providers?.length) return o.providers;
  return GUIDES.map((g) => ({
    id: g.id,
    name: g.name,
    on: (o.methods ?? []).includes(g.id),
    keys: o.social?.[g.id] ? "project" : "none",
    boxKeys: false,
    env: g.id.toUpperCase(),
    callbackUrl: `${o.endpoint}/callback/${g.id}`,
    appCallbackUrl: `${o.endpoint}/callback/${g.id}`,
    callbackConfirmed: "",
    callbackChanged: false,
    testUrl: `${o.endpoint}/tiffin/test-sign-in?provider=${g.id}`,
  }));
}

/** Where a provider's keys come from, in a word or three. */
function KeySource({ st }: { st: ProviderState }) {
  if (st.keys === "box") return <p className="text-xs text-ink-3">{st.on ? "Box’s keys" : "Box’s keys ready"}</p>;
  if (st.keys === "project") return <p className="text-xs text-ink-3">This app’s own keys</p>;
  return <p className={cn("text-xs", st.on ? "font-[550] text-warn-ink" : "text-ink-4")}>Needs keys</p>;
}

/** For a provider that is on: the box's keys or this app's own, and what each needs. */
function KeyChoice({ project, st, name, hosts, writer }: { project: string; st: ProviderState; name: string; hosts: string[]; writer: boolean }) {
  const { admin } = useMe();
  const qc = useQueryClient();
  const [setup, setSetup] = useState(false);
  const [backToBox, setBackToBox] = useState(false);
  const confirm = useMutation({
    mutationFn: () => signInApi.confirmCallback(project, st.id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["auth", project] }),
  });
  const own = st.keys === "project";
  const value = own ? "own" : st.keys === "box" ? "box" : "";
  const boxName = st.boxConsentName ? `“${st.boxConsentName}”` : `the box’s ${name} app`;

  return (
    <div className="mt-2.5 ml-10 space-y-2">
      <RadioGroup
        value={value}
        onValueChange={(v) => {
          if (v === "own") setSetup(true);
          else if (own) setBackToBox(true);
        }}
        disabled={!writer}
        aria-label={`Keys for ${name}`}
        className="grid gap-1.5"
      >
        <Choice id={`${st.id}-box`} value="box" disabled={!st.boxKeys && !own} title="The box’s keys" checked={value === "box"}>
          {st.boxKeys ? (
            <>
              Shows {boxName} on {name}’s sign-in screen. Fine for side projects; nothing to set up.
            </>
          ) : admin ? (
            <>
              Not set on this box.{" "}
              <Link to="/settings" hash="sign-in" className="underline decoration-current/40 underline-offset-2 hover:text-ink">
                Set them in Box settings
              </Link>
              .
            </>
          ) : (
            "Not set on this box. The box’s owner can set them in Box settings."
          )}
        </Choice>
        <Choice id={`${st.id}-own`} value="own" title="This app’s own keys" checked={own}>
          {own ? (
            <>
              Shows your app’s name. Redirect URI <span className="ident text-ink-2 [overflow-wrap:anywhere]">{st.appCallbackUrl}</span>.
            </>
          ) : (
            <>Shows your app’s name. A few minutes in {name}’s console, once.</>
          )}
        </Choice>
      </RadioGroup>
      {own && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pl-6 text-xs">
          <button type="button" disabled={!writer} onClick={() => setSetup(true)} className="text-ink-2 underline decoration-rule-3 underline-offset-2 hover:text-ink disabled:opacity-45">
            Edit keys
          </button>
          <a href={st.testUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 text-ink-2 underline decoration-rule-3 underline-offset-2 hover:text-ink">
            Test sign-in
            <ArrowUpRight className="size-3" aria-hidden />
          </a>
        </div>
      )}
      {own && st.callbackChanged && (
        <div role="status" className="rounded-[8px] border border-warn/40 bg-warn-wash px-3 py-2.5">
          <p className="text-xs font-[550] text-warn-ink">{project}’s redirect URI changed. Sign-in with {name} fails until you update it.</p>
          <p className="mt-1 text-xs text-ink-2 [overflow-wrap:anywhere]">
            In {name}’s console, replace <span className="ident">{st.callbackConfirmed}</span> with:
          </p>
          <PasteField value={st.appCallbackUrl} label={`Copy the new ${name} redirect URI`} className="mt-1.5" />
          <Button size="sm" className="mt-2" disabled={!writer || confirm.isPending} onClick={() => confirm.mutate()}>
            I’ve updated it
          </Button>
          {confirm.isError && <ProblemNote className="mt-2" error={confirm.error} />}
        </div>
      )}
      {st.keys === "none" && (
        <p className="pl-6 text-xs font-[550] text-warn-ink">Not working yet: choose where {name}’s keys come from.</p>
      )}
      {setup && <AppKeysSheet project={project} st={st} name={name} hosts={hosts} open={setup} onOpenChange={setSetup} />}
      <Confirm
        open={backToBox}
        onClose={() => setBackToBox(false)}
        title={`Use the box’s ${name} keys for ${project}?`}
        body={`${project}’s own ${name} keys are deleted (History can undo it), and people see ${boxName} when they sign in. Their accounts stay as they are.`}
        action="Use the box’s keys"
        tone="normal"
        run={() => signInApi.removeAppKeys(project, st.id)}
        done={() => {
          setBackToBox(false);
          void qc.invalidateQueries({ queryKey: ["auth", project] });
          toast({ title: `${project} uses the box’s ${name} keys now.` });
        }}
      />
    </div>
  );
}

function Choice({ id, value, title, checked, disabled, children }: { id: string; value: string; title: string; checked: boolean; disabled?: boolean; children: ReactNode }) {
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex cursor-pointer items-start gap-2.5 rounded-[8px] border px-3 py-2 transition-colors",
        checked ? "border-rule-3 bg-paper-sunk" : "border-rule hover:bg-paper-hover",
        disabled ? "cursor-default opacity-70" : "",
      )}
    >
      <Radio id={id} value={value} disabled={disabled} className="mt-0.5" />
      <span className="min-w-0">
        <span className="block text-[0.8125rem] text-ink">{title}</span>
        <span className="block text-xs text-ink-3">{children}</span>
      </span>
    </label>
  );
}

function Field({ value, label, className }: { value: string; label: string; className?: string }) {
  return (
    <div className={cn("flex items-center gap-1 rounded-[7px] border border-rule bg-paper py-1 pr-1 pl-2.5", className)}>
      <code className="min-w-0 flex-1 truncate font-mono text-[0.75rem] text-ink-2" title={value}>
        {value}
      </code>
      <CopyButton value={value} label={label} className="size-6" />
    </div>
  );
}
