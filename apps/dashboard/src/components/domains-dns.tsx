import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Lock, Pencil, Plus, Search, Trash } from "lucide-react";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { dnsApi, type DnsLookupType, type ZoneRecord } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { DnsRecordDialog } from "@/components/domains-dns-dialog";
import { relName, toFullName } from "@/components/domains-parts";
import { zoneOf } from "@/components/domains-setup";
import { InfoTip } from "@/components/info-tip";
import { ProblemNote } from "@/components/problem";
import { Skeleton } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { providerName, type Domain } from "@/lib/domains";

/**
 * "DNS records" under an open domain: the other records its zone needs,
 * such as a verification TXT for Google or GitHub, SPF and DMARC for mail.
 * With the zone on a connected provider, a box admin lists and edits them
 * here (the box's own records are locked). Otherwise they live at the DNS
 * host, and this explains that and offers a lookup to check one.
 */
export function DomainDns({ d, admin }: { d: Domain; admin: boolean }) {
  const connected = !!d.managedBy;
  return (
    <section className="mt-6" aria-label={`DNS records for ${d.domain}`}>
      <h3 className="label mb-2.5">DNS records</h3>
      {connected && admin ? <ZoneTable d={d} /> : <AtHost d={d} admin={admin} connected={connected} />}
      <Lookup d={d} className={connected && admin ? "mt-5" : "mt-4"} quiet={connected && admin} />
    </section>
  );
}

// ───────────────────────── connected: the zone's records ─────────────────────────

const under = (name: string, base: string) => name === base || name.endsWith(`.${base}`);

function ZoneTable({ d }: { d: Domain }) {
  const qc = useQueryClient();
  const z = useQuery({ queryKey: ["dns-zone", d.domain], queryFn: () => dnsApi.records(d.domain), retry: false, staleTime: 15_000 });
  const [whole, setWhole] = useState(false);
  const [editing, setEditing] = useState<ZoneRecord | "new" | null>(null);
  const [deleting, setDeleting] = useState<ZoneRecord | null>(null);
  const all = z.data?.records ?? [];
  const mine = all.filter((r) => under(r.name, d.domain));
  const shown = whole ? all : mine;
  const zone = z.data?.zone ?? zoneOf(d.domain);

  if (z.isError)
    return z.error instanceof ApiError && z.error.status === 412 ? (
      <AtHost d={d} admin connected={false} />
    ) : (
      <ProblemNote error={z.error} title={`The records can’t be read from ${providerName(d.managedBy)} right now.`} />
    );

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <p className="text-[0.8125rem] text-ink-3">
          {zone} is at <span className="text-ink-2">{z.data?.providerLabel ?? providerName(d.managedBy)}</span>, connected to your box. Add verification and mail records here; the box keeps its own.
        </p>
        <Button size="sm" onClick={() => setEditing("new")} disabled={!z.data}>
          <Plus className="size-3.5!" />
          Add record
        </Button>
      </div>
      <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised" role="table" aria-label={`Records in ${zone}`}>
        <div role="row" className="hidden grid-cols-[3.75rem_minmax(0,0.9fr)_minmax(0,2fr)_6.5rem] gap-x-4 border-b border-rule bg-paper-sunk px-3.5 py-1.5 text-xs text-ink-3 sm:grid">
          <span role="columnheader">Type</span>
          <span role="columnheader">Name</span>
          <span role="columnheader">Value</span>
          <span role="columnheader" className="sr-only">
            Actions
          </span>
        </div>
        {!z.data ? (
          <div className="grid gap-2 p-3">
            <Skeleton className="h-7" />
            <Skeleton className="h-7" />
          </div>
        ) : shown.length === 0 ? (
          <p className="px-3.5 py-4 text-[0.8125rem] text-ink-3">No records for {d.domain} at the DNS host yet.</p>
        ) : (
          <div className="divide-y divide-rule">
            {shown.map((r, i) => (
              <RecordLine key={`${r.type} ${r.name} ${r.value} ${i}`} r={r} base={d.domain} onEdit={() => setEditing(r)} onDelete={() => setDeleting(r)} />
            ))}
          </div>
        )}
      </div>
      {z.data && all.length > mine.length && (
        <button type="button" onClick={() => setWhole(!whole)} className="mt-2 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
          {whole ? `Only ${d.domain} and names under it` : `Show all ${all.length} records in ${zone}`}
        </button>
      )}

      {z.data && (
        <DnsRecordDialog
          key={editing === null ? "closed" : editing === "new" ? "new" : `${editing.type} ${editing.name} ${editing.value}`}
          open={editing !== null}
          onOpenChange={(o) => !o && setEditing(null)}
          base={d.domain}
          zone={zone}
          records={all}
          editing={editing === "new" ? undefined : (editing ?? undefined)}
          onEdit={setEditing}
          onDone={() => {
            void qc.invalidateQueries({ queryKey: ["dns-zone", d.domain] });
            void qc.invalidateQueries({ queryKey: ["domains"] });
          }}
        />
      )}
      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete this ${deleting?.type ?? ""} record?`}
        body={
          deleting ? (
            <>
              <span className="ident text-[0.8125rem] break-all text-ink">
                {deleting.name} {deleting.type} {deleting.value}
              </span>{" "}
              is removed at {z.data?.providerLabel ?? "the DNS host"}. If a service verified the domain with it, it may ask again. Other records stay.
            </>
          ) : null
        }
        action="Delete record"
        run={() => dnsApi.deleteRecords([{ type: deleting!.type, name: deleting!.name, value: deleting!.value }])}
        done={() => void qc.invalidateQueries({ queryKey: ["dns-zone", d.domain] })}
      />
    </div>
  );
}

function RecordLine({ r, base, onEdit, onDelete }: { r: ZoneRecord; base: string; onEdit: () => void; onDelete: () => void }) {
  const locked = !!r.managed;
  const name = relName(r.name, base);
  return (
    <div role="row" className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 px-3.5 py-2 sm:grid-cols-[3.75rem_minmax(0,0.9fr)_minmax(0,2fr)_6.5rem]">
      <span role="cell" className="ident text-[0.8125rem] text-ink max-sm:col-start-1 max-sm:row-start-1">
        {r.type}
        <span className="sm:hidden">
          {" "}
          <span className="text-ink-4">·</span> {name}
        </span>
      </span>
      <span role="cell" className="ident truncate text-[0.8125rem] text-ink-2 max-sm:hidden" title={r.name}>
        {name}
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-0.5 max-sm:col-span-2 max-sm:row-start-2">
        <code className={cn("ident min-w-0 truncate text-[0.8125rem]", locked ? "text-ink-3" : "text-ink")} title={r.value}>
          {r.value}
        </code>
        <CopyButton value={r.value} label={`Copy ${r.value}`} className="size-6" />
      </span>
      <span role="cell" className="flex items-center justify-end gap-0.5 max-sm:col-start-2 max-sm:row-start-1">
        {locked ? (
          <span className="inline-flex items-center gap-1 text-xs text-ink-3">
            <Lock className="size-3" aria-hidden />
            {r.managed === "tiffin" ? "Tiffin" : "DNS host"}
            <InfoTip label={r.managed === "tiffin" ? "Managed by Tiffin" : "The DNS host’s own record"}>
              {r.why} {r.managed === "tiffin" ? "To change it, remove or change the domain here instead." : "Change it at the DNS host if you ever need to."}
            </InfoTip>
          </span>
        ) : (
          <>
            <Button variant="ghost" size="icon-sm" aria-label={`Edit ${r.type} ${r.name}`} title="Edit" onClick={onEdit}>
              <Pencil className="size-3.5!" />
            </Button>
            <Button variant="ghost" size="icon-sm" aria-label={`Delete ${r.type} ${r.name}`} title="Delete" onClick={onDelete}>
              <Trash className="size-3.5!" />
            </Button>
          </>
        )}
      </span>
    </div>
  );
}

// ───────────────────────── not connected: at the DNS host ─────────────────────────

function AtHost({ d, admin, connected }: { d: Domain; admin: boolean; connected: boolean }) {
  const zone = zoneOf(d.domain);
  return (
    <div className="rounded-[10px] bg-paper-sunk px-4 py-3.5 text-[0.875rem] text-ink-2">
      <p>
        Other records for {zone}, such as a TXT record to verify the domain with Google or GitHub, or SPF and DMARC for email, are added {connected ? `at ${providerName(d.managedBy)}` : "where you manage its DNS"}, the way that service tells you. Tiffin doesn’t need to know about them. Just leave the records that point {d.domain} at your box.
      </p>
      {connected ? (
        <p className="mt-2 text-[0.8125rem] text-ink-3">{providerName(d.managedBy)} holds {zone} and is connected to this box. A box admin can add and edit its records here.</p>
      ) : (
        admin && (
          <p className="mt-2 text-[0.8125rem] text-ink-3">
            Is {zone} at Cloudflare?{" "}
            <Link to="/settings/dns" className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
              Connect it
            </Link>{" "}
            and the box can add these records for you, here.
          </p>
        )
      )}
    </div>
  );
}

// ───────────────────────── check a record ─────────────────────────

const TYPES: DnsLookupType[] = ["TXT", "A", "AAAA", "CNAME", "MX", "CAA", "NS"];

/** Type a name and a type, see what public DNS answers now. */
function Lookup({ d, className, quiet }: { d: Domain; className?: string; quiet: boolean }) {
  const [open, setOpen] = useState(!quiet);
  const [name, setName] = useState("@");
  const [type, setType] = useState<DnsLookupType>("TXT");
  const full = toFullName(name, d.domain, zoneOf(d.domain));
  const look = useMutation({ mutationFn: () => dnsApi.lookup(full, type) });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (full) look.mutate();
  };
  if (!open)
    return (
      <button type="button" onClick={() => setOpen(true)} className={cn("inline-flex items-center gap-1.5 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink", className)}>
        <Search className="size-3.5" aria-hidden />
        Check what public DNS answers for a record
      </button>
    );
  return (
    <form onSubmit={submit} className={className} aria-label="Check a record">
      <p className="text-[0.875rem] font-[550] text-ink">Check a record</p>
      <p className="mt-0.5 text-[0.8125rem] text-ink-3">See what public DNS answers now, for example after adding a verification record.</p>
      <div className="mt-2.5 flex flex-col gap-2 sm:flex-row sm:items-center">
        <div className="flex h-9 min-w-0 flex-1 items-center rounded-[7px] border border-rule-2 bg-paper-raised focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-label="Name"
            placeholder="@ or _github-pages-challenge-you"
            spellCheck={false}
            autoCapitalize="off"
            className="ident h-full min-w-0 flex-1 bg-transparent pl-3 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:outline-hidden"
          />
          {full !== name.trim().toLowerCase() && <span className="ident shrink-0 truncate pr-3 text-[0.75rem] text-ink-3">{name.trim() === "" || name.trim() === "@" ? d.domain : `.${d.domain}`}</span>}
        </div>
        <div className="flex gap-2">
          <div className="w-28">
            <Select value={type} onValueChange={(v) => setType(v as DnsLookupType)} options={TYPES.map((t) => ({ value: t, label: t }))} aria-label="Record type" />
          </div>
          <Button type="submit" disabled={!full || look.isPending} className="h-9">
            {look.isPending ? "Looking…" : "Look up"}
          </Button>
        </div>
      </div>
      <div role="status" aria-live="polite">
        {look.isError && <ProblemNote className="mt-3" error={look.error} />}
        {look.data && (
          <div className="mt-3 rounded-[8px] border border-rule-2 bg-paper-raised px-3.5 py-2.5 text-[0.8125rem]">
            <p className="text-ink-3">
              <span className="ident text-[0.75rem] text-ink-2">
                {look.data.name} {look.data.type}
              </span>
              {look.data.alias && <> · an alias of {look.data.alias}</>}
            </p>
            {(look.data.values ?? []).length === 0 ? (
              <p className="mt-1 text-ink-2">No {look.data.type} record yet. A new record usually shows up within a few minutes; check the name if it doesn’t.</p>
            ) : (
              <ul className="mt-1 space-y-0.5">
                {(look.data.values ?? []).map((v) => (
                  <li key={v} className="flex min-w-0 items-center gap-0.5">
                    <code className="ident min-w-0 text-[0.8125rem] break-all text-ink">{v}</code>
                    <CopyButton value={v} label={`Copy ${v}`} className="size-6" />
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>
    </form>
  );
}
