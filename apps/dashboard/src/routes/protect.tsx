import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod2, mq, type Limit, type ProtectDecision, type ProtectPatch, type ProtectStatus } from "@/api/modules";
import { Breaker } from "@/components/breaker";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { Group, healthCrumbs, Rows, StateLine } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { countWords, duration, int, NNBSP, plainWords, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";

/** Go durations ("3h11m46s") to seconds. */
function goSeconds(s: string) {
  const m = s.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?$/);
  return m ? Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0) : NaN;
}
const per = (l: Limit) => `${int(l.requests)} per ${duration(l.windowSeconds)}`;
const windowsFor = [30, 60, 120, 240, 720];
const forWords = (m: number) => (m < 60 ? `${m} minutes` : m === 60 ? "an hour" : `${m / 60} hours`);

/**
 * Protection: a calm overview of what stands between the internet and the
 * apps, layer by layer, each with honest words about what it does. The
 * under-attack switch is a guarded lever: lift the guard, then throw it.
 */
export function ProtectPage() {
  useTitle("Protection");
  const qc = useQueryClient();
  const st = useQuery(mq.protect);
  const { admin } = useMe();
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["protect"] });
    qc.invalidateQueries({ queryKey: ["protect-decisions"] });
  };
  if (st.isError && notOnBox(st.error)) return <NotOnBox what="Protection" />;
  if (st.isPending)
    return (
      <Page wide>
        <Skeleton className="h-4 w-20" />
        <Skeleton className="mt-3 h-8 w-48" />
        <Skeleton className="mt-10 h-28" />
      </Page>
    );
  if (st.isError)
    return (
      <Page wide>
        <PageHeader eyebrow={healthCrumbs} title="Protection" />
        <ProblemNote className="mt-8" error={st.error} />
      </Page>
    );
  const s = st.data;
  const bans = s.crowdsec.decisions;
  const challenged = s.effective.challengeHosts ?? [];
  const line = s.underAttack.on
    ? `Under-attack mode is on: every visitor solves a challenge first, for ${duration(s.underAttack.minutesLeft * 60)} more.`
    : `Normal. ${bans ? `${countWords(bans, "address is", "addresses are", true)} banned` : "Nobody is banned"}; ${
        challenged.length ? "the challenge is on for some apps." : "the challenge is off until you need it."
      }`;

  return (
    <Page wide>
      <PageHeader eyebrow={healthCrumbs} title="Protection" />
      <StateLine danger={s.underAttack.on}>{line}</StateLine>
      {!s.edge.applied && s.edge.error && <ProblemNote className="mt-5" error={new Error(s.edge.error)} title="The edge is serving without the latest protection settings" />}

      <AttackLever s={s} admin={admin} onDone={refresh} />

      <Group label="Layers" id="layers" aside="From the outside in">
        <Rows>
          <Layer
            name="Firewall"
            sub="On the machine"
            note="Only the edge’s ports and SSH answer from outside; Postgres, Valkey and the apps’ own ports stay private."
            status={
              s.firewall.active ? (
                <>
                  Drops everything except ports {(s.firewall.openPorts ?? []).join(", ")}
                  {s.firewall.interfaces?.length ? ` on ${s.firewall.interfaces.join(", ")}` : ""}.
                </>
              ) : (
                <span className="text-danger">{s.firewall.off ? "Switched off: every port the box listens on is reachable." : sentence(plainWords(s.firewall.detail ?? "Not active."))}</span>
              )
            }
          />
          <Layer
            name="CrowdSec"
            sub="Bans abusive addresses"
            note="Reads the edge’s access log, spots password guessing and scanners, and bans the source at the edge."
            status={
              s.crowdsec.running ? (
                s.crowdsec.enforced ? (
                  bans ? `Enforcing, with ${countWords(bans, "ban", "bans")} in place.` : "Enforcing. Nobody is banned right now."
                ) : (
                  <span className="text-warn-ink">Watching only: it spots trouble but doesn’t ban.</span>
                )
              ) : (
                <span className="text-danger">{sentence(plainWords(s.crowdsec.detail ?? "Not running."))}</span>
              )
            }
          />
          <Limits s={s} admin={admin} onDone={refresh} />
          <Challenge s={s} admin={admin} onDone={refresh} />
          <Waf s={s} admin={admin} onDone={refresh} />
        </Rows>
      </Group>

      <Bans admin={admin} onDone={refresh} />
      <Noticed />
    </Page>
  );
}

function Layer({
  name,
  sub,
  lever,
  status,
  note,
  action,
  children,
}: {
  name: string;
  sub: string;
  lever?: ReactNode;
  status: ReactNode;
  /** What it does, honestly: quieter, under the status. */
  note?: ReactNode;
  action?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <li className="py-3">
      <div className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-start gap-x-3 sm:grid-cols-[2rem_11rem_minmax(0,1fr)_auto] sm:gap-x-4">
        <span className="flex pt-0.5">{lever}</span>
        <span className="min-w-0">
          <span className="block text-[0.875rem] text-ink">{name}</span>
          <span className="block text-xs text-ink-3">{sub}</span>
        </span>
        <span className="col-span-2 col-start-2 row-start-2 mt-1.5 text-[0.84375rem] leading-5 text-ink-2 sm:col-span-1 sm:col-start-auto sm:row-start-auto sm:mt-0">
          {status}
          {note && <span className="mt-0.5 block max-w-[46rem] text-[0.8125rem] text-ink-3">{note}</span>}
        </span>
        <span className="col-start-3 row-start-1 sm:col-start-auto sm:row-start-auto">{action}</span>
      </div>
      {children}
    </li>
  );
}

/** Lift the guard (arm), then throw the switch. Turning it off is a plain button: that's the safe direction. */
function AttackLever({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const [minutes, setMinutes] = useState(60);
  const [armed, setArmed] = useState(false);
  const ua = s.settings.underAttack;
  const on = useMutation({
    mutationFn: () => mod2.underAttack(true, minutes),
    onSuccess: () => {
      setArmed(false);
      onDone();
      toast({ title: `Under-attack mode is on for ${forWords(minutes)}.`, action: { label: "Turn it off", run: () => mod2.underAttack(false).then(onDone) } });
    },
  });
  const off = useMutation({
    mutationFn: () => mod2.underAttack(false),
    onSuccess: () => {
      onDone();
      toast({ title: "Under-attack mode is off. Normal limits are back." });
    },
  });
  const a = s.underAttack;
  if (a.on) {
    const total = a.since && a.until ? (new Date(a.until).getTime() - new Date(a.since).getTime()) / 60_000 : Math.max(a.minutesLeft, 60);
    return (
      <section aria-live="polite" className="mt-8 rounded-[10px] border border-danger-rule bg-paper-raised px-5 py-4">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
          <div className="min-w-0 flex-1">
            <p className="text-[0.9375rem] font-[550] text-danger">Under-attack mode is on</p>
            <p className="mt-1 text-[0.84375rem] text-ink-2">
              Every visitor solves a small proof-of-work puzzle before reaching an app, and limits are down to {per(ua.limits.app)} per visitor
              {a.by ? `. Turned on by ${a.by}` : ""}
              {a.since ? ` ${relative(a.since)}` : ""}.
            </p>
            <div className="mt-3 grid max-w-[28rem] grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
              <SegMeter value={a.minutesLeft} max={total} segments={20} label="Time left in under-attack mode" valueText={`${a.minutesLeft} minutes left`} scale={["0", "", forWords(Math.round(total))]} />
              <span className="text-[0.8125rem] text-ink-2 tnum">{duration(a.minutesLeft * 60)} left</span>
            </div>
          </div>
          {admin && (
            <Button size="lg" onClick={() => off.mutate()} disabled={off.isPending}>
              {off.isPending ? "Turning off…" : "Turn it off now"}
            </Button>
          )}
        </div>
        {off.isError && <ProblemNote className="mt-3" error={off.error} />}
      </section>
    );
  }
  return (
    <section
      aria-label="Under-attack mode"
      className={cn(
        "mt-8 rounded-[10px] border bg-paper-raised px-5 py-4 transition-[border-color] duration-[var(--dur-state)] ease-[var(--ease-out)]",
        armed ? "border-danger" : "border-rule-2",
      )}
    >
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center">
        <div className="min-w-0 flex-1">
          <p className="text-[0.9375rem] font-[550] text-ink">Under-attack mode</p>
          <p className="mt-1 max-w-[40rem] text-[0.84375rem] text-ink-2">
            For a flood. Puts a proof-of-work challenge in front of every app and tightens limits to {per(ua.limits.app)} per visitor
            {ua.limits.auth ? ` (sign-in ${per(ua.limits.auth)})` : ""}. People wait about a second; scripts and bots stall. It switches itself off.
          </p>
        </div>
        {admin ? (
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={minutes}
              onChange={(e) => setMinutes(Number(e.target.value))}
              aria-label="For how long"
              className="h-[38px] rounded-[8px] border border-rule-2 bg-paper px-2.5 text-[0.84375rem] text-ink outline-none focus-visible:border-brass"
            >
              {windowsFor.map((m) => (
                <option key={m} value={m}>
                  for {forWords(m)}
                </option>
              ))}
            </select>
            {armed ? (
              <>
                <Button size="lg" variant="ghost" onClick={() => setArmed(false)}>
                  Cancel
                </Button>
                <Button size="lg" variant="danger" onClick={() => on.mutate()} disabled={on.isPending} autoFocus>
                  {on.isPending ? "Turning on…" : `Turn on for ${forWords(minutes)}`}
                </Button>
              </>
            ) : (
              <Button size="lg" onClick={() => setArmed(true)} aria-describedby="ua-guard">
                Lift the guard
              </Button>
            )}
          </div>
        ) : (
          <p className="text-[0.84375rem] text-ink-3">The owner and admins can switch it on.</p>
        )}
      </div>
      {armed && (
        <p id="ua-guard" className="mt-3 border-t border-rule pt-3 text-[0.84375rem] text-ink-2">
          Every visitor, including you and any API clients, will see the challenge until it ends or you turn it off. Scripts that can’t run JavaScript are stopped.
        </p>
      )}
      {on.isError && <ProblemNote className="mt-3" error={on.error} />}
    </section>
  );
}

function useSet(onDone: () => void) {
  return useMutation({ mutationFn: (p: ProtectPatch) => mod2.setProtect(p), onSuccess: onDone });
}

function Limits({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(s.settings.limits);
  const save = useSet(onDone);
  const L = s.settings.limits;
  const zones: Array<[keyof typeof L, string, string]> = [
    ["app", "Apps", "every request to your apps"],
    ["auth", "Sign-in", "login, sign-up and password routes"],
    ["dashboard", "Dashboard and API", "this page included"],
  ];
  const dirty = JSON.stringify(draft) !== JSON.stringify(L);
  return (
    <Layer
      name="Rate limits"
      sub="Per visitor address"
      status={
        <>
          Apps {per(L.app)}; sign-in {per(L.auth)}; the dashboard {per(L.dashboard)}.
          {s.underAttack.on && <span className="text-warn-ink"> Tightened while under attack.</span>}
        </>
      }
      note="A visitor over a limit is told to slow down (429) until the window passes."
      action={
        admin &&
        !editing && (
          <Button size="sm" variant="ghost" onClick={() => (setDraft(L), setEditing(true))}>
            Edit
          </Button>
        )
      }
    >
      {editing && (
        <form
          className="mt-3 rounded-[8px] bg-paper-sunk px-4 py-3 sm:ml-[3rem]"
          onSubmit={(e) => {
            e.preventDefault();
            const before = L;
            save.mutate(
              { limits: draft },
              {
                onSuccess: () => {
                  setEditing(false);
                  toast({ title: "Saved the rate limits. The edge uses them now.", action: { label: "Undo", run: () => mod2.setProtect({ limits: before }).then(onDone) } });
                },
              },
            );
          }}
        >
          <ul className="divide-y divide-rule">
            {zones.map(([k, label, hint]) => (
              <li key={k} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 py-2">
                <span>
                  <span className="block text-[0.875rem] text-ink">{label}</span>
                  <span className="block text-xs text-ink-3">{hint}</span>
                </span>
                <span className="flex items-center gap-2 text-[0.84375rem] text-ink-3">
                  <NumberBox value={draft[k].requests} label={`${label}: requests`} onChange={(v) => setDraft({ ...draft, [k]: { ...draft[k], requests: v } })} />
                  requests per
                  <NumberBox value={draft[k].windowSeconds} label={`${label}: window in seconds`} onChange={(v) => setDraft({ ...draft, [k]: { ...draft[k], windowSeconds: v } })} />s
                </span>
              </li>
            ))}
          </ul>
          <div className="mt-2 flex justify-end gap-2">
            <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button type="submit" size="sm" variant="primary" disabled={!dirty || save.isPending}>
              Save limits
            </Button>
          </div>
          {save.isError && <ProblemNote className="mt-3" error={save.error} />}
        </form>
      )}
    </Layer>
  );
}

function NumberBox({ value, onChange, label }: { value: number; onChange: (v: number) => void; label: string }) {
  return (
    <input
      value={String(value)}
      onChange={(e) => onChange(Number(e.target.value.replace(/\D/g, "")) || 0)}
      inputMode="numeric"
      aria-label={label}
      className="h-8 w-[4.5rem] rounded-md border border-rule bg-paper px-2 text-right text-[0.875rem] text-ink tnum outline-none hover:border-rule-2 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
    />
  );
}

function Challenge({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const set = useSet(onDone);
  const hosts = s.settings.challenge.hosts ?? [];
  const all = hosts.includes("*");
  const flip = () => {
    const before = hosts;
    const next = all ? [] : ["*"];
    set.mutate(
      { challenge: { hosts: next } },
      {
        onSuccess: () =>
          toast({
            title: next.length ? "Every app now asks visitors to solve the challenge." : "The challenge is off for every app.",
            action: { label: "Undo", run: () => mod2.setProtect({ challenge: { hosts: before } }).then(onDone) },
          }),
      },
    );
  };
  return (
    <Layer
      name="Bot challenge"
      sub="Proof of work in the browser"
      lever={<Breaker label="Bot challenge on every app" state={hosts.length ? "on" : "off"} disabled={!admin || set.isPending} onFlip={flip} />}
      status={
        <>
          {all ? "On for every app." : hosts.length ? <>On for {hosts.map((h, i) => <span key={h}>{i > 0 && ", "}<span className="ident">{h}</span></span>)}.</> : "Off. Under-attack mode turns it on for every app."}
          {set.isError && <ProblemNote className="mt-2" error={set.error} />}
        </>
      }
      note={`The browser spends about a second on a puzzle (difficulty ${s.settings.challenge.difficulty}) before the page loads. People barely notice; scrapers pay for every request. Clients that can’t run JavaScript are stopped too, so keep it off for apps that serve an API.`}
    />
  );
}

function Waf({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const set = useSet(onDone);
  const on = s.settings.waf;
  return (
    <Layer
      name="Web application firewall"
      sub="Opt-in"
      lever={
        <Breaker
          label="Web application firewall"
          state={on ? "on" : "off"}
          disabled={!admin || set.isPending}
          onFlip={() =>
            set.mutate(
              { waf: !on },
              {
                onSuccess: () =>
                  toast({
                    title: on ? "The WAF is off." : "The WAF is on. Watch Logs for honest requests it blocks.",
                    action: { label: "Undo", run: () => mod2.setProtect({ waf: on }).then(onDone) },
                  }),
              },
            )
          }
        />
      }
      status={
        <>
          {on ? "On for every app." : "Off."} Coraza with the OWASP core rules.
          {set.isError && <ProblemNote className="mt-2" error={set.error} />}
        </>
      }
      note="Inspects every request for SQL injection, path traversal, script injection and the like. It can block unusual but honest requests (a product description with code in it, say), so it’s off until you turn it on; then watch the logs for false alarms."
    />
  );
}

function Bans({ admin, onDone }: { admin: boolean; onDone: () => void }) {
  const list = useQuery({ queryKey: ["protect-decisions"], queryFn: mod2.decisions, refetchInterval: 20_000 });
  const [ip, setIp] = useState("");
  const [length, setLength] = useState("24h");
  const [reason, setReason] = useState("");
  const [unbanning, setUnbanning] = useState<ProtectDecision | null>(null);
  const ban = useMutation({
    mutationFn: () => mod2.ban(ip.trim(), length, reason.trim() || "Banned by hand"),
    onSuccess: () => {
      toast({ title: `Banned ${ip.trim()} for ${length === "168h" ? "a week" : duration(goSeconds(length))}.` });
      setIp("");
      setReason("");
      onDone();
    },
  });
  const rows = list.data ?? [];
  return (
    <Group label="Banned right now" id="bans" aside={rows.length ? `${words(rows.length, true)} ${rows.length === 1 ? "address" : "addresses"}, answered with 403 at the edge` : undefined}>
      {rows.length === 0 && list.isSuccess ? (
        <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">Nobody. Bans show up here when CrowdSec catches something, or when you add one.</p>
      ) : (
        <Rows>
          {rows.map((d) => {
            const left = goSeconds(d.expiresIn);
            return (
              <li key={d.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-0.5 py-2.5 sm:grid-cols-[14rem_minmax(0,1fr)_7rem_5rem]">
                <span className="ident truncate text-ink">{d.value}</span>
                <span className="col-span-2 row-start-2 min-w-0 text-[0.84375rem] text-ink-2 sm:col-span-1 sm:row-start-auto">
                  {sentence(d.scenario)}
                  <span className="text-ink-3">
                    {" "}
                    {d.origin === "cscli" ? "Banned by hand" : "Caught by CrowdSec"}
                    {d.country ? ` · ${d.country}` : ""}
                    {d.as ? ` · ${d.as}` : ""}
                  </span>
                </span>
                <span className="hidden text-right text-[0.8125rem] text-ink-3 tnum sm:block">{Number.isFinite(left) ? `${duration(left)} left` : d.expiresIn}</span>
                <span className="text-right">
                  {admin && (
                    <Button size="sm" variant="ghost" onClick={() => setUnbanning(d)}>
                      Unban
                    </Button>
                  )}
                </span>
              </li>
            );
          })}
        </Rows>
      )}
      {admin && (
        <form
          className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center"
          onSubmit={(e) => {
            e.preventDefault();
            if (ip.trim()) ban.mutate();
          }}
        >
          <Input value={ip} onChange={(e) => setIp(e.target.value)} placeholder="203.0.113.7, or a range" className="ident sm:max-w-[14rem]" aria-label="Address or range to ban" spellCheck={false} />
          <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why (shown in this list)" aria-label="Why" />
          <select
            value={length}
            onChange={(e) => setLength(e.target.value)}
            aria-label="For how long"
            className="h-9 rounded-md border border-rule bg-paper px-2.5 text-[0.84375rem] text-ink outline-none focus-visible:border-brass"
          >
            {["1h", "4h", "24h", "168h"].map((d) => (
              <option key={d} value={d}>
                for {d === "168h" ? "a week" : d === "24h" ? "a day" : d.replace("h", `${NNBSP}h`)}
              </option>
            ))}
          </select>
          <Button type="submit" size="lg" className="h-9" disabled={!ip.trim() || ban.isPending}>
            {ip.trim() ? `Ban ${ip.trim().length > 20 ? "it" : ip.trim()}` : "Ban"}
          </Button>
        </form>
      )}
      {ban.isError && <ProblemNote className="mt-3" error={ban.error} />}
      <Confirm
        open={!!unbanning}
        onClose={() => setUnbanning(null)}
        title={`Unban ${unbanning?.value}?`}
        body="Requests from it reach your apps again straight away. CrowdSec bans it again if it keeps misbehaving."
        action="Unban"
        tone="normal"
        run={() => mod2.unban(unbanning!.value)}
        done={onDone}
      />
    </Group>
  );
}

function Noticed() {
  const list = useQuery({ queryKey: ["protect-alerts"], queryFn: mod2.protectAlerts, refetchInterval: 30_000 });
  const rows = list.data ?? [];
  return (
    <Group label="What CrowdSec noticed" id="noticed" aside={rows.length ? `the latest ${words(rows.length)}` : undefined}>
      {rows.length === 0 ? (
        <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">Nothing suspicious yet.</p>
      ) : (
        <Untrusted label="Scenario names and sources come from traffic. Shown as plain text.">
          <ul className="divide-y divide-rule">
            {rows.map((a) => (
              <li key={a.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 px-4 py-2.5">
                <span className="min-w-0">
                  <span className="block truncate text-[0.84375rem] text-ink">{a.message}</span>
                  <span className="block text-xs text-ink-3">
                    <span className="ident">{a.source}</span> · {countWords(a.events, "event")} · {countWords(a.decisions, "ban")}
                  </span>
                </span>
                <span className="text-xs text-ink-3">{relative(a.at)}</span>
              </li>
            ))}
          </ul>
        </Untrusted>
      )}
    </Group>
  );
}
