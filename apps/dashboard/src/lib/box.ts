import type { StatusReport } from "@/api/client";

/** The box's name for the nameplate: its hostname, without Lima's prefix. */
export function boxName(s: StatusReport | undefined): string {
  if (!s) return "tiffin";
  return s.host.hostname.replace(/^lima-/, "") || "tiffin";
}

/** Where it runs, in words: "Mac · Lima", "Linux · arm64". */
export function whereItRuns(s: StatusReport | undefined): string | undefined {
  if (!s) return undefined;
  if (s.host.hostname.startsWith("lima-")) return "Mac · Lima";
  const os = s.host.os === "linux" ? "Linux" : s.host.os === "darwin" ? "macOS" : s.host.os;
  return `${os} · ${s.host.arch}`;
}

/** "Tiffin 0.4.2", or "Tiffin dev" for a development build. */
export function versionLabel(s: StatusReport | undefined): string | undefined {
  if (!s) return undefined;
  return `Tiffin ${s.version.replace(/^v/, "")}`;
}

/** The box's domain, from any URL it serves: "https://web.tiffin.localhost:8470" → "tiffin.localhost". */
export function domainFrom(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const host = new URL(url).hostname;
    const parts = host.split(".");
    return parts.length > 2 ? parts.slice(1).join(".") : host;
  } catch {
    return undefined;
  }
}
