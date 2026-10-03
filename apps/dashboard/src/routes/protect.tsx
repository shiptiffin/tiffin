import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, BrickWall, Gauge, ShieldAlert, ShieldCheck, ShieldHalf, Swords } from "lucide-react";
import { useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod2, mq, type Limit, type ProtectDecision, type ProtectStatus } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { num, plainWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";

export function ProtectPage() {
  useTitle("Protection");
  const qc = useQueryClient();
  const st = useQuery(mq.protect);
  const { admin } = useMe();
  const refresh = () => qc.invalidateQueries({ queryKey: ["protect"] });
  if (st.isError && notOnBox(st.error)) return <NotOnBox what="Protection" />;
  if (st.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-64" />
        <Skeleton className="mt-8 h-40" />
      </Page>
    );
  if (st.isError)
    return (
      <Page wide>
        <ProblemNote error={st.error} />
      </Page>
    );
  const s = st.data;
  return (
    <Page wide>
      <PageHeader
        title="Protection"
        lede="Rate limits, a proof-of-work challenge, CrowdSec bans and a firewall stand between the internet and your apps. They're on by default."
      />
      <AttackSwitch s={s} admin={admin} onDone={refresh} />
      {!s.edge.applied && s.edge.error && (
        <ProblemNote className="mt-4" error={new Error(s.edge.error)} title="The edge hasn't applied the latest settings" />
      )}

      <div className="mt-8 grid gap-4 md:grid-cols-2">
        <Limits s={s} admin={admin} onDone={refresh} />
        <Layer
          icon={<ShieldHalf />}
          title="CrowdSec"
          ok={s.crowdsec.running && s.crowdsec.enforced}
          badge={s.crowdsec.enforced ? "enforcing" : s.crowdsec.detecting ? "watching only" : "off"}
        >
          <p className="text-base text-ink-2">
            {s.crowdsec.running
              ? s.crowdsec.decisions
                ? `Running, with ${num(s.crowdsec.decisions)} ${s.crowdsec.decisions === 1 ? "ban" : "bans"} in place.`
                : "Running. Nobody is banned."
              : sentence(plainWords(s.crowdsec.detail))}
          </p>
          <p className="mt-2 text-sm text-ink-3">Reads the edge's access log, spots brute force and scanning, and bans the source at the edge.</p>
        </Layer>
        <Layer
          icon={<BrickWall />}
          title="Firewall"
          ok={s.firewall.active}
          badge={s.firewall.off ? "switched off" : s.firewall.active ? "active" : "inactive"}
        >
          <p className="text-base text-ink-2">
            Everything is dropped except these ports{s.firewall.interfaces?.length ? ` on ${s.firewall.interfaces.join(", ")}` : ""}:
          </p>
          <p className="mt-2 flex flex-wrap gap-1.5">
            {(s.firewall.openPorts ?? []).map((p) => (
              <span key={p} className="rounded-md border border-rule bg-paper px-2 py-0.5 font-mono text-xs text-ink">
                {p}
              </span>
            ))}
          </p>
        </Layer>
        <Waf s={s} admin={admin} onDone={refresh} />
      </div>

      <Decisions admin={admin} />
      <Alerts />
    </Page>
  );
}

function AttackSwitch({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const [minutes, setMinutes] = useState(60);
  const [confirmOff, setConfirmOff] = useState(false);
  const on = useMutation({ mutationFn: () => mod2.underAttack(true, minutes), onSuccess: onDone });
  const a = s.underAttack;
  if (a.on)
    return (
      <section className="mt-8 overflow-hidden rounded-2xl border border-irr-rule bg-irr-wash" aria-live="polite">
        <div aria-hidden className="h-1.5 bg-[repeating-linear-gradient(135deg,var(--irr)_0_10px,transparent_10px_18px)] opacity-80" />
        <div className="flex flex-col gap-5 p-6 sm:flex-row sm:items-center">
          <span className="grid size-14 shrink-0 place-items-center rounded-2xl bg-irr text-white">
            <Swords className="size-7" />
          </span>
          <div className="flex-1">
            <p className="display text-2xl text-ink">Under-attack mode is on</p>
            <p className="mt-1 text-base text-ink-2">
              Every visitor solves a small challenge before reaching your apps, and limits are tight ({s.settings.underAttack.limits.app.requests}{" "}
              requests per {s.settings.underAttack.limits.app.windowSeconds} s). It switches itself off in {a.minutesLeft} min
              {a.by ? `; turned on by ${a.by}` : ""}
              {a.since ? ` ${relative(a.since)}` : ""}.
            </p>
          </div>
          {admin && (
            <Button variant="secondary" size="lg" onClick={() => setConfirmOff(true)}>
              Turn it off
            </Button>
          )}
        </div>
        <Confirm
          open={confirmOff}
          onClose={() => setConfirmOff(false)}
          title="Turn under-attack mode off?"
          body="Visitors stop seeing the challenge and normal limits come back."
          action="Turn it off"
          tone="normal"
          run={() => mod2.underAttack(false)}
          done={onDone}
        />
      </section>
    );
  return (
    <section className="mt-8 grid gap-px overflow-hidden rounded-2xl border border-rule bg-rule sm:grid-cols-[1.2fr_1fr]">
      <div className="flex items-start gap-4 bg-raised p-6">
        <span className="grid size-12 shrink-0 place-items-center rounded-2xl bg-rev-wash text-rev">
          <ShieldCheck className="size-6" />
        </span>
        <div>
          <p className="display text-2xl text-ink">All calm.</p>
          <p className="mt-1 text-base text-ink-2">
            Rate limits{s.firewall.active ? " and the firewall" : ""} are on
            {s.crowdsec.running ? (s.crowdsec.decisions ? `; ${s.crowdsec.decisions} ${s.crowdsec.decisions === 1 ? "address is" : "addresses are"} banned` : "; nobody is banned") : ""}.
            The challenge is off until you need it.
          </p>
        </div>
      </div>
      <div className="bg-raised p-6">
        <p className="flex items-center gap-2 text-base font-medium text-ink">
          <Swords className="size-4 text-ink-3" />
          Under a flood?
        </p>
        <p className="mt-1 text-sm text-ink-3">
          Under-attack mode puts a proof-of-work challenge in front of every app and tightens limits. People barely notice; bots stall.
        </p>
        {admin ? (
          <div className="mt-4 flex flex-wrap items-center gap-2">
            <select
              value={minutes}
              onChange={(e) => setMinutes(Number(e.target.value))}
              aria-label="For how long"
              className="h-8 rounded-md border border-rule bg-paper px-2 text-sm text-ink outline-none focus-visible:border-brass"
            >
              {[30, 60, 120, 240, 720].map((m) => (
                <option key={m} value={m}>
                  for {m < 60 ? `${m} min` : `${m / 60} h`}
                </option>
              ))}
            </select>
            <Button size="sm" variant="danger-quiet" className="border border-irr-rule" onClick={() => on.mutate()} disabled={on.isPending}>
              <ShieldAlert />
              Turn on under-attack mode
            </Button>
          </div>
        ) : (
          <p className="mt-3 text-sm text-ink-3">The box owner and admins can switch it on.</p>
        )}
        {on.isError && <ProblemNote className="mt-3" error={on.error} />}
      </div>
    </section>
  );
}

function Layer({ icon, title, ok, badge, children }: { icon: ReactNode; title: string; ok: boolean; badge: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border border-rule bg-raised/60 p-5">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="text-ink-3 [&_svg]:size-4">{icon}</span>
        <h2 className="text-md font-medium text-ink">{title}</h2>
        <span className={cn("ml-auto flex items-center gap-1.5 text-xs", ok ? "text-rev" : "text-ink-3")}>
          <span className={cn("size-1.5 rounded-full", ok ? "bg-rev" : "bg-ink-4")} />
          {badge}
        </span>
      </div>
      {children}
    </section>
  );
}

function Limits({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const [draft, setDraft] = useState(s.settings.limits);
  const dirty = JSON.stringify(draft) !== JSON.stringify(s.settings.limits);
  const save = useMutation({ mutationFn: () => mod2.setProtect({ limits: draft }), onSuccess: onDone });
  const row = (k: keyof typeof draft, label: string, hint: string) => (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-2.5">
      <span>
        <span className="block text-base text-ink">{label}</span>
        <span className="block text-xs text-ink-3">{hint}</span>
      </span>
      <span className="flex items-center gap-1.5 text-sm text-ink-3">
        <NumberBox
          value={draft[k].requests}
          disabled={!admin}
          label={`${label}: requests`}
          onChange={(v) => setDraft({ ...draft, [k]: { ...draft[k], requests: v } as Limit })}
        />
        per
        <NumberBox
          value={draft[k].windowSeconds}
          disabled={!admin}
          label={`${label}: window in seconds`}
          onChange={(v) => setDraft({ ...draft, [k]: { ...draft[k], windowSeconds: v } as Limit })}
        />
        s
      </span>
    </li>
  );
  return (
    <Layer icon={<Gauge />} title="Rate limits" ok badge="per visitor IP">
      <ul className="divide-y divide-rule/70">
        {row("app", "Apps", "every request to your apps")}
        {row("auth", "Sign-in", "login, sign-up and password routes")}
        {row("dashboard", "Dashboard and API", "this page included")}
      </ul>
      {admin && dirty && (
        <div className="mt-3 flex justify-end gap-2">
          <Button size="sm" variant="ghost" onClick={() => setDraft(s.settings.limits)}>
            Undo edits
          </Button>
          <Button size="sm" variant="primary" onClick={() => save.mutate()} disabled={save.isPending}>
            Save limits
          </Button>
        </div>
      )}
      {save.isError && <ProblemNote className="mt-3" error={save.error} />}
    </Layer>
  );
}

function NumberBox({ value, onChange, disabled, label }: { value: number; onChange: (v: number) => void; disabled?: boolean; label: string }) {
  return (
    <input
      value={value}
      onChange={(e) => onChange(Number(e.target.value.replace(/\D/g, "")) || 0)}
      disabled={disabled}
      inputMode="numeric"
      aria-label={label}
      className="h-8 w-16 rounded-md border border-rule bg-paper px-2 text-right font-mono text-sm text-ink tnum outline-none focus-visible:border-brass disabled:border-transparent disabled:bg-transparent"
    />
  );
}

function Waf({ s, admin, onDone }: { s: ProtectStatus; admin: boolean; onDone: () => void }) {
  const set = useMutation({ mutationFn: (waf: boolean) => mod2.setProtect({ waf }), onSuccess: onDone });
  return (
    <Layer icon={<ShieldAlert />} title="Web application firewall" ok={s.settings.waf} badge={s.settings.waf ? "on" : "off (opt-in)"}>
      <p className="text-base text-ink-2">Coraza with the OWASP core rules inspects every request for SQL injection, path traversal and friends.</p>
      <p className="mt-2 text-sm text-ink-3">
        It's off by default because it can block unusual but honest requests. Turn it on if you see attacks in the logs, and watch for false alarms.
      </p>
      {admin && (
        <Button
          size="sm"
          className="mt-3"
          variant={s.settings.waf ? "ghost" : "secondary"}
          onClick={() => set.mutate(!s.settings.waf)}
          disabled={set.isPending}
        >
          {s.settings.waf ? "Turn the WAF off" : "Turn the WAF on"}
        </Button>
      )}
      {set.isError && <ProblemNote className="mt-3" error={set.error} />}
    </Layer>
  );
}

function Decisions({ admin }: { admin: boolean }) {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["protect-decisions"], queryFn: mod2.decisions, refetchInterval: 20_000 });
  const [ip, setIp] = useState("");
  const [duration, setDuration] = useState("24h");
  const [reason, setReason] = useState("");
  const [unbanning, setUnbanning] = useState<ProtectDecision | null>(null);
  const ban = useMutation({
    mutationFn: () => mod2.ban(ip.trim(), duration, reason.trim() || "banned by hand"),
    onSuccess: () => {
      setIp("");
      setReason("");
      qc.invalidateQueries({ queryKey: ["protect-decisions"] });
      qc.invalidateQueries({ queryKey: ["protect"] });
    },
  });
  const rows = list.data ?? [];
  return (
    <section className="mt-12" aria-labelledby="bans">
      <h2 id="bans" className="display-italic mb-3 text-xl text-ink">
        Banned right now
      </h2>
      <div className="overflow-hidden rounded-xl border border-rule bg-raised/60">
        {rows.length === 0 ? (
          <p className="px-5 py-6 text-base text-ink-3">Nobody. Bans show up here when CrowdSec catches something, or when you add one.</p>
        ) : (
          <ul className="divide-y divide-rule/70">
            {rows.map((d) => (
              <li
                key={d.id}
                className="grid grid-cols-[1rem_minmax(0,1fr)_auto] items-center gap-x-3 px-4 py-3 sm:grid-cols-[1rem_12rem_minmax(0,1fr)_7rem_auto]"
              >
                <Ban className="size-4 text-irr" />
                <code className="font-mono text-[0.8125rem] text-ink">{d.value}</code>
                <span className="col-span-2 min-w-0 truncate text-sm text-ink-2 sm:col-span-1" title={d.scenario}>
                  {d.scenario}
                  <span className="text-ink-4">
                    {" "}
                    · {d.origin === "cscli" ? "by hand" : d.origin}
                    {d.country ? ` · ${d.country}` : ""}
                    {d.as ? ` · ${d.as}` : ""}
                  </span>
                </span>
                <span className="text-right text-xs text-ink-3 tnum">
                  {d.expiresIn.replace(/(\d+)h(\d+)m[\d.]+s/, "$1 h $2 min").replace(/(\d+)m[\d.]+s/, "$1 min")} left
                </span>
                {admin && (
                  <Button size="sm" variant="ghost" onClick={() => setUnbanning(d)}>
                    Unban
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {admin && (
          <form
            className="flex flex-col gap-2 border-t border-rule bg-paper-sunk/50 p-3 sm:flex-row"
            onSubmit={(e) => {
              e.preventDefault();
              if (ip.trim()) ban.mutate();
            }}
          >
            <Input
              value={ip}
              onChange={(e) => setIp(e.target.value)}
              placeholder="203.0.113.7 or 198.51.100.0/24"
              className="h-8 font-mono sm:w-64"
              aria-label="IP or range to ban"
            />
            <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why (shown here)" className="h-8" aria-label="Reason" />
            <select
              value={duration}
              onChange={(e) => setDuration(e.target.value)}
              aria-label="For how long"
              className="h-8 rounded-md border border-rule bg-paper px-2 text-sm text-ink"
            >
              {["1h", "4h", "24h", "168h"].map((d) => (
                <option key={d} value={d}>
                  {d === "168h" ? "a week" : d.replace("h", " h")}
                </option>
              ))}
            </select>
            <Button size="sm" type="submit" disabled={!ip.trim() || ban.isPending} className="h-8">
              <Ban />
              Ban
            </Button>
          </form>
        )}
        {ban.isError && <ProblemNote className="m-3" error={ban.error} />}
      </div>
      <Confirm
        open={!!unbanning}
        onClose={() => setUnbanning(null)}
        title={`Unban ${unbanning?.value}?`}
        body="Requests from it reach your apps again right away. CrowdSec may ban it again if it keeps misbehaving."
        action="Unban"
        tone="normal"
        run={() => mod2.unban(unbanning!.value)}
        done={() => {
          qc.invalidateQueries({ queryKey: ["protect-decisions"] });
          qc.invalidateQueries({ queryKey: ["protect"] });
        }}
      />
    </section>
  );
}

function Alerts() {
  const list = useQuery({ queryKey: ["protect-alerts"], queryFn: mod2.protectAlerts, refetchInterval: 30_000 });
  const rows = list.data ?? [];
  return (
    <section className="mt-10" aria-labelledby="palerts">
      <h2 id="palerts" className="display-italic mb-3 text-xl text-ink">
        What CrowdSec noticed
      </h2>
      {rows.length === 0 ? (
        <p className="text-base text-ink-3">Nothing suspicious yet.</p>
      ) : (
        <Untrusted label="Scenario names and sources come from traffic. Shown as plain text.">
          <ul className="divide-y divide-rule/70">
            {rows.map((a) => (
              <li key={a.id} className="grid grid-cols-[1rem_minmax(0,1fr)_auto] items-start gap-3 px-4 py-2.5 text-sm">
                <ShieldAlert className="mt-0.5 size-3.5 text-ink-3" />
                <span className="min-w-0">
                  <span className="block truncate text-ink">{a.message}</span>
                  <span className="block font-mono text-xs text-ink-3">
                    {a.source} · {a.events} {a.events === 1 ? "event" : "events"} · {a.decisions} {a.decisions === 1 ? "ban" : "bans"}
                  </span>
                </span>
                <span className="text-xs text-ink-3">{relative(a.at)}</span>
              </li>
            ))}
          </ul>
        </Untrusted>
      )}
    </section>
  );
}
