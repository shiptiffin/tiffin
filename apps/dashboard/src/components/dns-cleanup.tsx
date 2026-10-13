import type { DnsCleanup, DnsRecord } from "@/lib/domains";

/**
 * What removing a domain or deleting a project does to its DNS records:
 * the ones Tiffin created and deletes, the ones to delete at the DNS host,
 * the ones left alone. Spans only: it sits inside dialog text.
 */
export function DnsCleanupNote({ dns, done = false }: { dns?: DnsCleanup | null; done?: boolean }) {
  if (!dns || !dns.hosts?.length) return null;
  const del = (done ? dns.deleted : dns.delete) ?? [];
  const byHand = dns.byHand ?? [];
  const kept = dns.kept ?? [];
  if (!del.length && !byHand.length && !kept.length && !dns.error) {
    return <span className="mt-2 block text-sm">No DNS record points {dns.hosts.length > 1 ? "these names" : dns.hosts[0]} at your box.</span>;
  }
  return (
    <span className="mt-2 block space-y-2 text-sm">
      {del.length > 0 && <Records title={done ? "Tiffin deleted the DNS records it created:" : "Tiffin also deletes the DNS records it created:"} recs={del} />}
      {dns.error && <span className="block text-warn-ink">Your DNS provider didn’t answer, so these are listed to delete by hand.</span>}
      {byHand.length > 0 && <Records title="Delete these at your DNS host, or they keep pointing at your box:" recs={byHand} />}
      {kept.length > 0 && <Records title={dns.keepDns ? "Kept, as asked:" : "Left alone, as they point elsewhere now:"} recs={kept} />}
    </span>
  );
}

function Records({ title, recs }: { title: string; recs: DnsRecord[] }) {
  return (
    <span className="block">
      {title}
      {recs.map((r) => (
        <span key={`${r.type} ${r.name} ${r.value}`} className="ident block truncate pl-3 text-[0.75rem] text-ink">
          {r.type} {r.name} {r.value}
        </span>
      ))}
    </span>
  );
}

/** One line for a toast after the change: what happened to DNS. */
export function dnsCleanupWords(dns?: DnsCleanup | null): string | undefined {
  if (!dns) return undefined;
  const parts: string[] = [];
  const n = dns.deleted?.length ?? 0;
  if (n) parts.push(`Deleted ${n} DNS record${n === 1 ? "" : "s"} Tiffin created.`);
  const h = dns.byHand?.length ?? 0;
  if (h) parts.push(`Delete ${h === 1 ? "one record" : `${h} records`} at your DNS host: ${dns.byHand!.map((r) => `${r.name} ${r.type} ${r.value}`).join(", ")}.`);
  return parts.join(" ") || undefined;
}
