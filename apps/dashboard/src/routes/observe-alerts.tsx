import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type AlertRule } from "@/api/modules";
import { q as core } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { Alarm, Group, healthCrumbs, Rows, StateLine } from "@/components/health-kit";
import { ShowMore } from "@/components/more";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { countWords, dec, duration, NNBSP, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayLabel, full, relative } from "@/lib/time";

type Kind = AlertRule["kind"];
const kinds: Array<{ v: Kind; label: string; unit: string; dflt: number }> = [
  { v: "disk", label: "A disk is filling up", unit: "% full", dflt: 85 },
  { v: "memory", label: "Memory is running out", unit: "% used", dflt: 90 },
  { v: "error_spike", label: "Errors spike", unit: "errors in 5 min", dflt: 20 },
  { v: "unit_down", label: "A service stops", unit: "", dflt: 0 },
  { v: "unit_restarts", label: "A service keeps restarting", unit: "restarts in 15 min", dflt: 3 },
  { v: "backup_age", label: "Backups fall behind", unit: "hours old", dflt: 26 },
  { v: "offsite_age", label: "Copies off the box fall behind", unit: "hours old", dflt: 26 },
  { v: "drill_failed", label: "A restore drill fails", unit: "", dflt: 0 },
  { v: "cert_expiry", label: "A certificate is about to expire", unit: "hours left", dflt: 72 },
  { v: "promql", label: "A metric crosses a line (PromQL)", unit: "", dflt: 1 },
];
const kindOf = (k: Kind) => kinds.find((x) => x.v === k) ?? kinds[0];

/** The rule as a sentence: "A disk is more than 85 % full." */
function condition(r: Pick<AlertRule, "kind" | "threshold" | "forSeconds" | "project" | "expr">) {
  const t = dec(r.threshold, 1);
  const hold = r.forSeconds ? ` for ${duration(r.forSeconds)}` : "";
  switch (r.kind) {
    case "disk":
      return `A disk is more than ${t}${NNBSP}% full${hold}.`;
    case "memory":
      return `Memory is more than ${t}${NNBSP}% used${hold}.`;
    case "error_spike":
      return `More than ${t} errors in 5${NNBSP}min from ${r.project ? `${r.project}’s apps` : "any project"}${hold}.`;
    case "unit_down":
      return `A box service is not running${hold || " (checked every minute)"}.`;
    case "unit_restarts":
      return `A box service restarts more than ${countWords(r.threshold, "time")} in 15${NNBSP}min${hold}.`;
    case "backup_age":
      return `The newest backup is more than ${t} hours old${hold}.`;
    case "offsite_age":
      return `The newest copy of the backups off the box is more than ${t} hours old${hold}.`;
    case "drill_failed":
      return `The last restore drill failed${hold}.`;
    case "cert_expiry":
      return `An HTTPS certificate has less than ${t} hours left${hold}.`;
    case "promql":
      return `${r.expr ?? "The expression"} is above ${t}${hold}.`;
  }
}

/** Alerts: what's firing, the rules that watch the box (each a breaker), where alerts go, and what has fired. */
export function AlertsPage() {
  useTitle("Alerts");
  const qc = useQueryClient();
  const alerts = useInfiniteQuery(mq.alertPages);
  const rules = useQuery(mq.rules);
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings });
  const { admin } = useMe();
  const [editing, setEditing] = useState<AlertRule | "new" | null>(null);
  const test = useMutation({
    mutationFn: mod.testAlert,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["alerts"] });
      toast({ title: "Sent a test alert.", detail: delivery ? `It went to ${delivery}.` : "It shows in the history below; nothing delivers it yet." });
    },
  });
  const put = (r: AlertRule, enabled: boolean) =>
    mod.putRule(r.name, { kind: r.kind, threshold: r.threshold, enabled, description: r.description, expr: r.expr, forSeconds: r.forSeconds, project: r.project });
  const toggle = useMutation({
    mutationFn: (r: AlertRule) => put(r, !r.enabled),
    onSuccess: (_x, r) => {
      qc.invalidateQueries({ queryKey: ["rules"] });
      toast({
        title: r.enabled ? `Turned off ${r.name}. It won't fire until you turn it back on.` : `Turned on ${r.name}.`,
        action: { label: "Undo", run: () => put(r, r.enabled).then(() => qc.invalidateQueries({ queryKey: ["rules"] })) },
      });
    },
  });
  if (alerts.isError && notOnBox(alerts.error)) return <NotOnBox what="Alerts" />;
  const firing = alerts.data?.pages[0]?.firing ?? [];
  const seen = new Set<number>();
  const history = (alerts.data?.pages ?? []).flatMap((p) => p.history ?? []).filter((h) => !seen.has(h.id) && seen.add(h.id));
  const list = rules.data ?? [];
  const on = list.filter((r) => r.enabled).length;
  const s = settings.data;
  let delivery: string | null = null;
  if (s?.webhook) {
    try {
      delivery = `a webhook at ${new URL(s.webhook).host}`;
    } catch {
      delivery = "a webhook";
    }
  } else if (s?.emailProject) delivery = `email from ${s.emailProject}${s.email ? ` to ${s.email}` : ""}`;

  // History grouped by day, newest first.
  const days: Array<{ label: string; items: typeof history }> = [];
  for (const h of history) {
    const label = dayLabel(h.at);
    const last = days[days.length - 1];
    if (last?.label === label) last.items.push(h);
    else days.push({ label, items: [h] });
  }

  return (
    <Page wide>
      <PageHeader
        eyebrow={healthCrumbs}
        title="Alerts"
        actions={
          admin && (
            <>
              <Button size="lg" variant="ghost" onClick={() => test.mutate()} disabled={test.isPending}>
                {test.isPending ? "Sending…" : "Send a test alert"}
              </Button>
              <Button size="lg" onClick={() => setEditing("new")}>
                New rule
              </Button>
            </>
          )
        }
      />
      {alerts.isSuccess && (
        <StateLine danger={firing.length > 0}>
          {firing.length === 0
            ? `Nothing is firing. ${rules.data ? `${countWords(on, "rule is", "rules are", true)} watching the box.` : ""}`
            : firing.length === 1
              ? `${sentence(firing[0].summary).replace(/\.$/, "")}, for ${relative(firing[0].since).replace(/ ago$/, "")}.`
              : `${words(firing.length, true)} alerts are firing.`}
        </StateLine>
      )}
      {test.isError && <ProblemNote className="mt-4" error={test.error} />}

      {(firing.length > 0 || (s && !delivery)) && (
        <Rows className="mt-8">
          {firing.map((a) => (
            <Alarm key={a.rule + a.subject} title={sentence(a.summary)} detail={`Firing since ${relative(a.since)} · ${a.rule}${a.project ? ` · ${a.project}` : ""}`} />
          ))}
          {s && !delivery && (
            <Alarm
              tone="warn"
              title="Alerts only show here: nothing delivers them yet."
              detail={
                <>
                  Send them to a webhook (Slack, Discord, ntfy) or email:{" "}
                  <code className="ident text-ink">tiffin observe settings set --webhook URL</code>
                </>
              }
            />
          )}
        </Rows>
      )}
      {delivery && <p className="mt-3 text-[0.84375rem] text-ink-3">Alerts go to {delivery}.</p>}

      <Group label="Rules" id="rules" aside={rules.data ? (on === list.length ? `all ${words(on)} on` : `${words(on, true)} of ${words(list.length)} on`) : undefined}>
        {rules.isPending && <Skeleton className="h-60" />}
        {rules.isError && <ProblemNote error={rules.error} />}
        <Rows>
          {list.map((r) => (
            <li key={r.name} className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-x-3 py-2.5 sm:grid-cols-[2rem_11rem_minmax(0,1fr)_auto] sm:gap-x-4">
              <span className="flex items-center">
                <Breaker label={r.name} state={r.enabled ? "on" : "off"} disabled={!admin || toggle.isPending} onFlip={() => toggle.mutate(r)} />
              </span>
              <span className="min-w-0">
                <span className={cn("block truncate text-[0.875rem]", r.enabled ? "text-ink" : "text-ink-3")}>{kindOf(r.kind).label}</span>
                <span className="ident block truncate text-[0.71875rem] text-ink-3">
                  {r.name}
                  {r.project ? ` · ${r.project}` : ""}
                </span>
              </span>
              <span className={cn("col-span-2 col-start-2 row-start-2 text-[0.84375rem] sm:col-span-1 sm:col-start-auto sm:row-start-auto", r.enabled ? "text-ink-2" : "text-ink-3")}>
                {condition(r)}
                {!r.enabled && <span className="text-ink-3"> Off.</span>}
              </span>
              {admin && (
                <Button size="sm" variant="ghost" className="col-start-3 row-start-1 sm:col-start-auto sm:row-start-auto" onClick={() => setEditing(r)} aria-label={`Edit ${r.name}`}>
                  Edit
                </Button>
              )}
            </li>
          ))}
        </Rows>
        <p className="mt-3 text-[0.8125rem] text-ink-3">
          Error spikes count what{" "}
          <Link to="/errors" search={{}} className="text-brass-ink underline-offset-4 hover:underline">
            Errors
          </Link>{" "}
          receives; disk, memory and services come from{" "}
          <Link to="/metrics" className="text-brass-ink underline-offset-4 hover:underline">
            Metrics
          </Link>
          . The box checks every minute.
        </p>
      </Group>

      <Group label="History" id="history" aside={history.length > 1 && alerts.hasNextPage ? `the latest ${words(history.length)}` : undefined}>
        {history.length === 0 ? (
          <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">Nothing has fired yet.</p>
        ) : (
          days.map((d) => (
            <div key={d.label} className="mb-5">
              <p className="day mb-1 text-ink-2">{d.label}</p>
              <Rows>
                {d.items.map((h) => (
                  <li key={h.id} className="grid grid-cols-[3.25rem_minmax(0,1fr)] gap-x-3 py-2.5">
                    <time className="pt-px text-[0.78125rem] leading-5 text-ink-3 tnum" title={full(h.at)} dateTime={h.at}>
                      {clock(h.at)}
                    </time>
                    <div className="min-w-0">
                      <p className={cn("text-[0.875rem]", h.state === "firing" ? "text-danger" : "text-ink")}>
                        {sentence(h.summary)}
                        {h.state === "resolved" && <span className="text-ink-3"> Resolved.</span>}
                        {h.state === "test" && <span className="text-ink-3"> A test.</span>}
                      </p>
                      <p className="mt-0.5 text-[0.8125rem] text-ink-3">
                        <span className="ident">{h.rule}</span> · {sentence(h.delivery.replace(/ \(tiffin observe settings.*\)$/, ""))}
                      </p>
                    </div>
                  </li>
                ))}
              </Rows>
            </div>
          ))
        )}
        <ShowMore query={alerts} label="Show earlier alerts" />
      </Group>

      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-w-lg">{editing && <RuleForm rule={editing === "new" ? undefined : editing} taken={list.map((r) => r.name)} onClose={() => setEditing(null)} />}</DialogContent>
      </Dialog>
    </Page>
  );
}

const holds = [
  { s: 0, label: "Straight away" },
  { s: 60, label: "For 1 minute" },
  { s: 300, label: "For 5 minutes" },
  { s: 900, label: "For 15 minutes" },
  { s: 3600, label: "For an hour" },
];

function RuleForm({ rule, taken, onClose }: { rule?: AlertRule; taken: string[]; onClose: () => void }) {
  const qc = useQueryClient();
  const projects = useQuery(core.projects);
  const [kind, setKind] = useState<Kind>(rule?.kind ?? "disk");
  const [name, setName] = useState(rule?.name ?? "");
  const [threshold, setThreshold] = useState(String(rule?.threshold ?? kindOf("disk").dflt));
  const [forSeconds, setFor] = useState(rule?.forSeconds ?? 0);
  const [project, setProject] = useState(rule?.project ?? "");
  const [expr, setExpr] = useState(rule?.expr ?? "");
  const [removing, setRemoving] = useState(false);
  const k = kindOf(kind);
  const slug = name.trim();
  const valid = /^[a-z0-9][a-z0-9-]{0,62}$/.test(slug) && (rule || !taken.includes(slug)) && Number.isFinite(Number(threshold)) && (kind !== "promql" || expr.trim());
  const save = useMutation({
    mutationFn: () =>
      mod.putRule(slug, {
        kind,
        threshold: Number(threshold),
        forSeconds: forSeconds || undefined,
        project: kind === "error_spike" && project ? project : undefined,
        expr: kind === "promql" ? expr.trim() : undefined,
        enabled: rule?.enabled ?? true,
        description: condition({ kind, threshold: Number(threshold), forSeconds, project, expr }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["rules"] });
      toast({ title: rule ? `Saved ${slug}.` : `Added ${slug}. It starts watching within a minute.` });
      onClose();
    },
  });
  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) save.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>{rule ? `Edit ${rule.name}` : "New alert rule"}</DialogTitle>
        <DialogDescription>Say what to watch and when it should tell someone.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="rule-kind">Tell me when</Label>
          <Select
            id="rule-kind"
            value={kind}
            onValueChange={(v) => {
              setKind(v as Kind);
              if (!rule) setThreshold(String(kindOf(v as Kind).dflt));
            }}
            options={kinds.map((x) => ({ value: x.v, label: x.label }))}
          />
        </div>
        {kind === "promql" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="rule-expr">Expression</Label>
            <Input id="rule-expr" className="ident" value={expr} onChange={(e) => setExpr(e.target.value)} placeholder="rate(http_requests_total{status=~&quot;5..&quot;}[5m])" spellCheck={false} />
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          {kind !== "unit_down" && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="rule-threshold">{kind === "cert_expiry" ? "Below" : "Above"}</Label>
              <span className="flex items-center gap-2">
                <Input id="rule-threshold" inputMode="decimal" className="max-w-24 text-right tnum" value={threshold} onChange={(e) => setThreshold(e.target.value.replace(/[^\d.]/g, ""))} />
                <span className="text-[0.84375rem] whitespace-nowrap text-ink-3">{k.unit}</span>
              </span>
            </div>
          )}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="rule-for">How long first</Label>
            <Select id="rule-for" value={String(forSeconds)} onValueChange={(v) => setFor(Number(v))} options={holds.map((h) => ({ value: String(h.s), label: h.label }))} />
          </div>
        </div>
        {kind === "error_spike" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="rule-project">Which project</Label>
            <Select
              id="rule-project"
              value={project || "*"}
              onValueChange={(v) => setProject(v === "*" ? "" : v)}
              options={[{ value: "*", label: "Any project" }, ...(projects.data ?? []).map((p) => ({ value: p.name, label: p.name }))]}
            />
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="rule-name">Name</Label>
          <Input
            id="rule-name"
            className="ident"
            value={name}
            disabled={!!rule}
            onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
            placeholder="disk-nearly-full"
            spellCheck={false}
            autoComplete="off"
          />
          {!rule && taken.includes(slug) && <span className="text-[0.8125rem] text-danger">A rule called {slug} already exists; edit that one instead.</span>}
        </div>
        <p className="rounded-[8px] bg-paper-sunk px-3.5 py-2.5 text-[0.875rem] text-ink-2">
          Fires when: <span className="text-ink">{condition({ kind, threshold: Number(threshold) || 0, forSeconds, project, expr: expr || undefined })}</span>
        </p>
        {save.isError && <ProblemNote error={save.error} />}
      </DialogBody>
      <DialogFooter>
        {rule && (
          <Button type="button" variant="danger-quiet" className="mr-auto" onClick={() => setRemoving(true)}>
            Delete rule
          </Button>
        )}
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!valid || save.isPending}>
          {save.isPending ? "Saving…" : rule ? "Save rule" : "Add rule"}
        </Button>
      </DialogFooter>
      {rule && (
        <Confirm
          open={removing}
          onClose={() => setRemoving(false)}
          title={`Delete ${rule.name}?`}
          body="It stops watching straight away. Its past alerts stay in the history."
          action="Delete rule"
          run={() => mod.deleteRule(rule.name)}
          done={() => {
            qc.invalidateQueries({ queryKey: ["rules"] });
            onClose();
          }}
        />
      )}
    </form>
  );
}
