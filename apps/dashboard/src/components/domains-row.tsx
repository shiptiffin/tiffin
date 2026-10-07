import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, ChevronDown, Copy, CornerDownRight, MoreHorizontal, Pencil, RefreshCw, Trash2, X } from "lucide-react";
import { Accordion as A } from "radix-ui";
import { useState, type ReactNode } from "react";
import type { Manifest } from "@/api/client";
import { Breaker } from "@/components/breaker";
import { Confirm } from "@/components/confirm";
import { DomainDns } from "@/components/domains-dns";
import { AddPath, ChangeAppDialog, dropPath } from "@/components/domains-edit";
import { agoWords, kindOf, routesOf, shortDate, untilWords, useNow } from "@/components/domains-parts";
import { DomainSetup } from "@/components/domains-setup";
import { StatusDot } from "@/components/project-domains";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { full, relative, sinceWhen } from "@/lib/time";
import { recheckDomain, removeDomain, stateWords, type Domain } from "@/lib/domains";
import { change, pendingFor, usePending } from "@/lib/staged";

/** Domain · Serves · Status · HTTPS · actions. Phones stack the middle under the name. */
export const DOMAIN_COLS = "sm:grid-cols-[minmax(0,1.55fr)_minmax(0,0.85fr)_minmax(0,1.05fr)_minmax(0,0.95fr)_5.5rem]";

type Props = {
  project: string;
  d: Domain;
  www?: Domain;
  writer: boolean;
  local: boolean;
  every: number;
  open: boolean;
  onToggle: () => void;
  apps: string[];
  manifest?: Manifest;
  /** A box admin: can edit the zone's records when a connected provider holds it. */
  admin: boolean;
};

/**
 * One of the project's domains, with its www redirect folded under it: the
 * state in words, which app it shows, and its certificate. Opening it shows
 * where it is on the way to live with the exact records, or, once live, the
 * certificate, what DNS says, its paths and the www switch.
 */
export function DomainRow({ project, d, www, writer, local, every, open, onToggle, apps, manifest, admin }: Props) {
  const qc = useQueryClient();
  const [removing, setRemoving] = useState(false);
  const [changing, setChanging] = useState(false);
  const pending = usePending(project);
  const staged = pendingFor(pending, `set:domains/${d.domain}`);
  const wwwOn = !!www || d.wwwRedirect;
  const apex = kindOf(d.domain) === "apex" && !d.redirectTo;
  const anyPending = d.state !== "live" || (www && www.state !== "live");
  const check = useMutation({
    mutationFn: () => Promise.all([d, www].filter((x): x is Domain => !!x && x.state !== "live").map((x) => recheckDomain(project, x.domain))),
    onSettled: () => qc.invalidateQueries({ queryKey: ["domains", project] }),
  });

  const flipWww = (to: "on" | "off") =>
    change(
      project,
      {
        kind: "set",
        path: ["domains", d.domain],
        from: wwwOn ? { www: "redirect" } : undefined,
        to: to === "on" ? { www: "redirect" } : undefined,
        what: to === "on" ? `Redirect www.${d.domain} to ${d.domain}` : `Stop redirecting www.${d.domain}`,
        undo: to === "on" ? `www.${d.domain} stops redirecting` : `www.${d.domain} redirects again`,
      },
      { immediate: true },
    );

  const menu = (
    <RowMenu
      label={`More for ${d.domain}`}
      items={[
        d.state === "live" && { icon: <ArrowUpRight />, label: `Open ${d.domain}`, run: () => window.open(d.url, "_blank", "noopener") },
        { icon: <Copy />, label: "Copy address", run: () => void copyText(d.url).then((ok) => ok && toast({ title: "Copied the address." })) },
        writer && anyPending && { icon: <RefreshCw />, label: "Check DNS now", run: () => check.mutate() },
        writer && apps.length > 1 && { icon: <Pencil />, label: "Change app…", run: () => setChanging(true) },
        writer && apex && { icon: <CornerDownRight />, label: wwwOn ? `Stop redirecting www` : `Redirect www here`, run: () => flipWww(wwwOn ? "off" : "on") },
        writer && "sep",
        writer && { icon: <Trash2 />, label: "Remove…", run: () => setRemoving(true), danger: true },
      ]}
    />
  );

  return (
    <A.Item value={d.domain} asChild>
    <li className={cn("relative", open ? "bg-paper-sunk/40" : "")}>
      <Line d={d} open={open} onToggle={onToggle} project={project} actions={menu} checking={check.isPending} onCheck={writer && d.state !== "live" ? () => check.mutate() : undefined} />
      {www && (
        <Line
          d={www}
          child
          open={open}
          onToggle={onToggle}
          project={project}
          checking={check.isPending}
          onCheck={writer && www.state !== "live" ? () => check.mutate() : undefined}
          actions={
            <RowMenu
              label={`More for ${www.domain}`}
              items={[
                { icon: <Copy />, label: "Copy address", run: () => void copyText(www.url).then((ok) => ok && toast({ title: "Copied the address." })) },
                writer && { icon: <X />, label: "Stop redirecting www", run: () => flipWww("off") },
              ]}
            />
          }
        />
      )}

      <A.Content className="pb-5 sm:pl-[22px]">
          {anyPending && (
            <div className="rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-4 shadow-[var(--top-light)] sm:px-5">
              <DomainSetup project={project} d={d} www={www} writer={writer} local={local} every={every} />
            </div>
          )}
          <Details
            project={project}
            d={d}
            www={www}
            writer={writer}
            apps={apps}
            manifest={manifest}
            onChangeApp={() => setChanging(true)}
            wwwSwitch={
              apex && (
                <label className="inline-flex items-center gap-2.5 text-[0.875rem] text-ink-2">
                  <Breaker
                    label={`Redirect www.${d.domain} here`}
                    state={wwwOn ? "on" : "off"}
                    staged={staged && staged.kind === "set" ? (staged.to === undefined ? "off" : "on") : undefined}
                    onFlip={flipWww}
                    disabled={!writer}
                  />
                  <span>
                    {wwwOn ? "On" : "Off"}
                    <span className="text-ink-3">
                      {" "}
                      · <span className="ident text-[0.75rem]">www.{d.domain}</span> {wwwOn ? "sends visitors here (308)" : "isn’t served"}
                    </span>
                  </span>
                </label>
              )
            }
          />
          {!local && <DomainDns d={d} admin={admin} />}
          {writer && (
            <div className="mt-4 flex justify-end">
              <Button variant="danger-quiet" size="sm" onClick={() => setRemoving(true)}>
                <Trash2 className="size-3.5!" />
                Remove {d.domain}
              </Button>
            </div>
          )}
      </A.Content>

      <ChangeAppDialog key={changing ? "open" : "closed"} project={project} d={d} apps={apps} manifest={manifest} open={changing} onOpenChange={setChanging} />
      <Confirm
        open={removing}
        onClose={() => setRemoving(false)}
        title={`Remove ${d.domain}?`}
        body={
          <>
            Visitors to {d.domain}
            {wwwOn ? ` and www.${d.domain}` : ""} get an error page from now on{routesOf(d).paths.length ? ", including the paths sent to other apps" : ""}. Your DNS records stay as they are; delete them at your DNS host if you no longer need them.
            You can undo this from History.
          </>
        }
        action="Remove domain"
        run={() => removeDomain(project, d.domain)}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["domains", project] });
          for (const k of [["manifest", project], ["project", project], ["changes"]]) void qc.invalidateQueries({ queryKey: k });
          toast({ title: `Removed ${d.domain} from ${project}.` });
        }}
      />
    </li>
    </A.Item>
  );
}

/** One line of the list: the name, what it shows, its state and its certificate. */
function Line({
  d,
  child,
  open,
  onToggle,
  actions,
  checking,
  onCheck,
}: {
  d: Domain;
  child?: boolean;
  open: boolean;
  onToggle: () => void;
  project: string;
  actions: ReactNode;
  checking: boolean;
  onCheck?: () => void;
}) {
  const s = stateWords(d);
  const { root, paths } = routesOf(d);
  const word = d.state === "live" ? (s.detail ? "Live, with a warning" : "Live") : d.state === "error" ? "Problem" : s.word;
  const sub = d.state === "error" ? s.word.replace(/^Problem: /, "") : d.state === "live" ? s.detail : d.state === "issuing" ? "Usually under a minute" : waitingSub(d);
  return (
    <Wrap child={child} className={cn("relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-0.5", DOMAIN_COLS, child ? "pb-3 pt-0" : "py-3")} onToggle={onToggle}>
      {/* The whole line opens the details (Radix Accordion); links and buttons sit above it. The www line under it opens the same details by click. */}
      {!child && (
        <A.Trigger className="absolute inset-0 z-0 cursor-pointer rounded-[6px] focus-visible:outline-offset-[-2px]">
          <span className="sr-only">{open ? `Hide details for ${d.domain}` : `Show details for ${d.domain}`}</span>
        </A.Trigger>
      )}

      <div className="pointer-events-none relative flex min-w-0 items-center gap-2.5">
        {child ? <CornerDownRight className="ml-[1px] size-3.5 shrink-0 text-ink-4" aria-hidden /> : <StatusDot tone={s.tone} className="mx-[3px]" />}
        <span className="min-w-0">
          <span className="flex min-w-0 items-center gap-1">
            {d.state === "live" ? (
              <a
                href={d.url}
                target="_blank"
                rel="noopener noreferrer"
                className={cn("group pointer-events-auto ident inline-flex min-w-0 items-center gap-1 hover:text-brass-ink", child ? "text-[0.8125rem] text-ink-2" : "text-[0.875rem] text-ink")}
              >
                <span className="truncate">{d.domain}</span>
                <ArrowUpRight className="size-3.5 shrink-0 text-ink-4 group-hover:text-brass-ink" />
              </a>
            ) : (
              <span className={cn("ident truncate", child ? "text-[0.8125rem] text-ink-2" : "text-[0.875rem] text-ink")}>{d.domain}</span>
            )}
          </span>
          {/* Phones: state under the name. */}
          <span className={cn("block truncate text-xs sm:hidden", toneText(s.tone))}>
            {child && <StatusDot tone={s.tone} className="mr-1.5 size-1.5!" />}
            {word}
            {d.redirectTo ? <span className="text-ink-3"> · redirects to {d.redirectTo}</span> : root && <span className="text-ink-3"> · shows {root}</span>}
          </span>
        </span>
      </div>

      <div className="pointer-events-none relative min-w-0 text-[0.8125rem] max-sm:hidden">
        {d.redirectTo ? (
          <span className="text-ink-3">
            Redirects to <span className="ident text-[0.75rem] text-ink-2">{d.redirectTo}</span>
          </span>
        ) : (
          <>
            <span className="ident text-[0.75rem] text-ink">{root ?? "—"}</span>
            {paths.length > 0 && <span className="block truncate text-xs text-ink-3">{paths.map((p) => `${p.path} → ${p.app}`).join(", ")}</span>}
          </>
        )}
      </div>

      <div className="pointer-events-none relative min-w-0 max-sm:hidden">
        <span className={cn("flex items-center gap-2 text-[0.8125rem]", toneText(s.tone))}>
          {word}
        </span>
        {sub && <span className={cn("block truncate text-xs", d.state === "live" && s.detail ? "text-warn-ink" : "text-ink-3")} title={sub}>{sub}</span>}
      </div>

      <div className="pointer-events-none relative min-w-0 text-[0.8125rem] max-sm:hidden">
        <Https d={d} />
      </div>

      <div className="relative flex items-center justify-end gap-0.5 max-sm:row-span-1">
        {onCheck && (
          <Button variant="ghost" size="icon-sm" onClick={onCheck} disabled={checking} aria-label={`Check DNS for ${d.domain} now`} title="Check DNS now">
            <RefreshCw className={cn("size-3.5!", checking ? "animate-spin" : "")} />
          </Button>
        )}
        {actions}
        {!child ? (
          <span aria-hidden className="pointer-events-none grid size-7 place-items-center text-ink-3">
            <ChevronDown className={cn("size-4 transition-transform duration-[var(--dur-state)]", open ? "rotate-180" : "")} />
          </span>
        ) : (
          <span aria-hidden className="size-7" />
        )}
      </div>
    </Wrap>
  );
}

/** The main line is the Accordion header; the www line under it is a plain block that opens the same details on click. */
function Wrap({ child, className, onToggle, children }: { child?: boolean; className: string; onToggle: () => void; children: ReactNode }) {
  if (child)
    return (
      <div className={cn(className, "cursor-pointer")} onClick={onToggle}>
        {children}
      </div>
    );
  return (
    <A.Header asChild>
      <div className={className}>{children}</div>
    </A.Header>
  );
}

function waitingSub(d: Domain): string | undefined {
  if (!d.reason || /not checked/i.test(d.reason)) return "Not checked yet";
  if (/no A or AAAA/i.test(d.reason)) return "No records found yet";
  if (/no public ip/i.test(d.reason)) return "Needs a box on a server";
  return stateWords(d).detail;
}

const toneText = (t: string) => (t === "bad" ? "text-danger" : t === "busy" ? "text-brass-ink" : t === "ok" ? "text-ink" : "text-ink-2");

/** The certificate in one or two short lines. */
function Https({ d }: { d: Domain }) {
  const c = d.certificate;
  if (d.state !== "live" || !c) return <span className="text-ink-4">{d.state === "issuing" ? "Being issued" : "After DNS"}</span>;
  if (c.renewing) return <span className="text-ink-2">Renewing now</span>;
  if (!c.notAfter) return <span className="text-ink-2">{c.issuer || "Valid"}</span>;
  return (
    <span title={`${c.issuer ?? "Certificate"} · expires ${shortDate(c.notAfter)}`}>
      <span className="text-ink-2">Until {shortDate(c.notAfter)}</span>
      <span className="block text-xs text-ink-3">Renews itself · {untilWords(c.notAfter)}</span>
    </span>
  );
}

/** The facts under an open row. */
function Details({
  project,
  d,
  www,
  writer,
  apps,
  manifest,
  onChangeApp,
  wwwSwitch,
}: {
  project: string;
  d: Domain;
  www?: Domain;
  writer: boolean;
  apps: string[];
  manifest?: Manifest;
  onChangeApp: () => void;
  wwwSwitch: ReactNode;
}) {
  const now = useNow(5000);
  const [addingPath, setAddingPath] = useState(false);
  const { root, paths } = routesOf(d);
  const c = d.certificate;
  const found = d.found ?? [];
  return (
    <dl className="mt-4 grid grid-cols-1 gap-x-8 text-[0.875rem] sm:grid-cols-2">
      <Fact label="Serves">
        {d.redirectTo ? (
          <>Redirects to {d.redirectTo}</>
        ) : (
          <div className="space-y-1.5">
            <div className="flex flex-wrap items-center gap-x-2">
              <span className="text-ink-3">Everything</span>
              <span className="text-ink-4">→</span>
              <span className="ident text-[0.8125rem] text-ink">{root ?? "no app"}</span>
              {writer && apps.length > 1 && (
                <button type="button" onClick={onChangeApp} className="ml-1 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                  Change
                </button>
              )}
            </div>
            {paths.map((p) => (
              <div key={p.path} className="flex items-center gap-x-2">
                <span className="ident text-[0.8125rem] text-ink-2">{p.path}</span>
                <span className="text-ink-4">→</span>
                <span className="ident text-[0.8125rem] text-ink">{p.app}</span>
                {writer && (
                  <Button variant="ghost" size="icon-sm" className="size-6" aria-label={`Stop sending ${p.path} to ${p.app}`} title="Remove this path" onClick={() => dropPath(project, manifest, d.domain, p.path)}>
                    <X className="size-3.5!" />
                  </Button>
                )}
              </div>
            ))}
            {writer && apps.length > 1 && !addingPath && (
              <button type="button" onClick={() => setAddingPath(true)} className="text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                Send a path to another app
              </button>
            )}
          </div>
        )}
      </Fact>
      {wwwSwitch && <Fact label="www">{wwwSwitch}</Fact>}
      {addingPath && (
        <div className="py-3 sm:col-span-2">
          <AddPath project={project} d={d} apps={apps} onDone={() => setAddingPath(false)} />
        </div>
      )}
      <Fact label="Certificate">
        {c && d.state === "live" ? (
          <>
            <span className="text-ink">{c.issuer || "Public certificate"}</span>
            {c.notAfter && (
              <span className="text-ink-2">
                {" "}
                · expires {shortDate(c.notAfter)} ({untilWords(c.notAfter, now)})
              </span>
            )}
            <span className="block text-[0.8125rem] text-ink-3">
              {c.renewing ? "Renewing now." : "Renews itself about 30 days before it runs out."}
              {www?.certificate?.state === "live" ? ` www.${d.domain} has its own.` : ""}
            </span>
            {c.error && <span className="mt-1 block text-[0.8125rem] text-warn-ink">Last renewal attempt failed: {c.error}</span>}
          </>
        ) : d.state === "issuing" ? (
          <span className="text-ink-2">Being issued by Let’s Encrypt now.</span>
        ) : (
          <span className="text-ink-3">Free from Let’s Encrypt, as soon as DNS points here.</span>
        )}
      </Fact>
      <Fact label={d.state === "live" ? "Live since" : d.state === "error" ? "Problem since" : d.state === "issuing" ? "Issuing since" : "Waiting since"}>
        <span className="text-ink-2" title={d.since ? full(d.since) : undefined}>
          {d.since ? (d.state === "live" ? sinceWhen(d.since) : relative(d.since)) : "—"}
        </span>
      </Fact>
      <Fact label="DNS now">
        {d.cname ? (
          <>
            CNAME to <span className="ident text-[0.8125rem]">{d.cname}</span>
          </>
        ) : found.length ? (
          <span className="ident text-[0.8125rem] break-all">{found.join(", ")}</span>
        ) : (
          <span className="text-ink-3">{d.checkedAt ? "No answer for this name yet" : "Not checked yet"}</span>
        )}
        {d.checkedAt && <span className="block text-[0.8125rem] text-ink-3">Checked {agoWords(d.checkedAt, now)}</span>}
      </Fact>
    </dl>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 border-b border-rule py-2.5">
      <dt className="text-[0.8125rem] text-ink-3">{label}</dt>
      <dd className="min-w-0 text-ink">{children}</dd>
    </div>
  );
}

type Item = { icon: ReactNode; label: string; run: () => void; danger?: boolean };

/** The ••• menu at the end of a line. */
function RowMenu({ label, items }: { label: string; items: Array<Item | "sep" | false | undefined> }) {
  const list = items.filter(Boolean) as Array<Item | "sep">;
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={label}>
          <MoreHorizontal />
        </Button>
      </MenuTrigger>
      <MenuContent align="end">
        {list.map((it, i) =>
          it === "sep" ? (
            i > 0 && i < list.length - 1 ? <MenuSeparator key={i} /> : null
          ) : (
            <MenuItem key={it.label} onSelect={it.run} variant={it.danger ? "danger" : "default"}>
              {it.icon}
              {it.label}
            </MenuItem>
          ),
        )}
      </MenuContent>
    </Menu>
  );
}
