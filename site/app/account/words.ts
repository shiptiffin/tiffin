// The account page's few sentences about boxes we no longer manage, shared by
// the server-rendered card and the live "being deleted" card.
import { boxDomain } from "@/lib/cloud/names";

/** "9 Oct 2026" */
export const shortDay = (d: Date) => d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });

/** What a deleted box's row says: one line, and one more when its data volume stays. */
export function goneLines(b: { name: string | null; deletedAt: Date | null; dataDeleted: boolean }): string[] {
  const what = ["The server", "its firewall", ...(b.dataDeleted ? ["its data"] : []), ...(b.name ? [boxDomain(b.name)] : [])];
  const list = `${what.slice(0, -1).join(", ")} and ${what.at(-1)}`;
  const when = b.deletedAt ? `Deleted on ${shortDay(b.deletedAt)}. ` : "";
  return [
    `${when}${list} are gone; nothing is billed.`,
    ...(b.dataDeleted ? [] : ["Its data volume stays in your Hetzner project, and Hetzner bills it until you delete it there."]),
  ];
}

export const gone = (b: Parameters<typeof goneLines>[0]) => goneLines(b).join(" ");

/** What a released box's card says. */
export const releasedLine = (at: Date | null) =>
  `You stopped the managed service${at ? ` on ${shortDay(at)}` : ""}. The server still runs in your Hetzner account; we no longer manage or monitor it.`;
