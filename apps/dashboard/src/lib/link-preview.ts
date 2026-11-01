import type { LinkPreview, LinkPreviewChange, LinkPreviewStatus, LinkPreviewSummary } from "@/api/modules";

/** "All good", "1 warning", "1 problem, 2 warnings": a link preview in a few words. */
export function summaryWords(s: Pick<LinkPreviewSummary, "status" | "bad" | "warn">): string {
  if (s.status === "ok") return "All good";
  if (s.status === "unreadable") return "Couldn’t read the page";
  const n = (k: number, one: string) => `${k} ${one}${k === 1 ? "" : "s"}`;
  const bad = s.bad ?? 0;
  const warn = s.warn ?? 0;
  return [bad && n(bad, "problem"), warn && n(warn, "warning")].filter(Boolean).join(", ") || (s.status === "bad" ? "Problems" : "Warnings");
}

/** The text colour for a status, only when it isn't fine. */
export function statusTone(s: LinkPreviewStatus | "ok" | "warn" | "bad"): string {
  return s === "bad" || s === "unreadable" ? "text-danger" : s === "warn" ? "text-warn-ink" : "";
}

/** The host of an address, without www. */
export function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

/** An address the way Google writes it under a result: "https://dibs.page › blog › launch". */
export function crumb(url: string): string {
  try {
    const u = new URL(url);
    const parts = u.pathname.split("/").filter(Boolean).map(decodeURIComponent);
    return [`${u.protocol}//${u.hostname}`, ...parts].join(" › ");
  } catch {
    return url;
  }
}

const statusWords: Record<string, string> = { ok: "fine", warn: "a warning", bad: "a problem" };

/** One change in words: "Title: Old → New", "Share image removed (was …)", "Image size: fine → a problem". */
export function changeWords(c: LinkPreviewChange): string {
  const from = c.from && statusWords[c.from] && (!c.to || statusWords[c.to]) ? statusWords[c.from] : c.from;
  const to = c.to && statusWords[c.to] && (!c.from || statusWords[c.from]) ? statusWords[c.to] : c.to;
  if (!from) return `${c.field} added: ${to}`;
  if (!to) return `${c.field} removed (was ${from})`;
  return `${c.field}: ${from} → ${to}`;
}

/** What the comparison is against: "Changed since v12", "Differs from production (v12)". */
export function comparedWords(r: Pick<LinkPreview, "compared">): string | undefined {
  const c = r.compared;
  if (!c) return undefined;
  const v = c.version ? `v${c.version}` : "the version before";
  return c.production ? `Differs from production${c.version ? ` (v${c.version})` : ""}` : `Changed since ${v}`;
}

/** The title and description each surface shows, after its own fallbacks. */
export function shown(r: LinkPreview) {
  const host = hostOf(r.url);
  return {
    host,
    // Search results use the page's own title and description.
    searchTitle: r.pageTitle || r.title || host,
    searchText: r.metaDescription || r.description || "",
    // Social apps use og: first, then twitter:, then the page's.
    title: r.title || host,
    text: r.description || "",
    // X reads its own twitter: tags first.
    xTitle: r.xTitle || r.title || host,
    xText: r.xDescription || r.description || "",
    site: r.siteName || host,
    large: r.twitterCard === "summary_large_image",
  };
}

/** The raw tags as text, one per line, for copying. */
export function tagsText(tags: Array<{ name: string; value: string }>): string {
  return tags.map((t) => `${t.name}\t${t.value}`).join("\n");
}
