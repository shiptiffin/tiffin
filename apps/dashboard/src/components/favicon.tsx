import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { MARK_PATHS } from "./logo";

// Colours are literal here: a favicon can't read the page's CSS variables.
const INK = "#2b2620";
const INK_DARK = "#ece8e0";
const BRASS = "#b8862f";
const RED = "#c8412f";

function icon(state: "plain" | "waiting" | "down" | "alarm") {
  const paths = MARK_PATHS.map((d) => `<path d='${d}'/>`).join("");
  const dot =
    state === "waiting" || state === "down"
      ? `<circle cx='26' cy='6' r='5.5' fill='${state === "down" ? RED : BRASS}' stroke='none'/>`
      : "";
  if (state === "alarm") {
    // Under attack: white mark on a red tile, hard to miss in a row of tabs.
    return `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><rect width='32' height='32' rx='7' fill='${RED}'/><g fill='none' stroke='white' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'>${paths}</g></svg>`;
  }
  return `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><style>g{stroke:${INK}}@media (prefers-color-scheme:dark){g{stroke:${INK_DARK}}}</style><g fill='none' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'>${paths}</g>${dot}</svg>`;
}

/**
 * The tab icon carries the box's state: the plain mark when all is well, a
 * brass dot while something waits for you, a red dot when something is down,
 * a red tile under attack. The title says it in words too (useTitle).
 */
export function useFavicon() {
  const { data } = useQuery(q.status());
  const alarm = !!useQuery(mq.protect).data?.underAttack.on;
  const state = alarm ? "alarm" : data && !data.ok ? "down" : "plain";
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    link.type = "image/svg+xml";
    link.href = `data:image/svg+xml,${encodeURIComponent(icon(state))}`;
  }, [state]);
}

/** "Box · Tiffin", prefixed while the box needs you: "(1) Box · Tiffin", "Degraded · …". */
export function useTitle(page: string) {
  const { data } = useQuery(q.status());
  const degraded = data && !data.ok;
  const alarm = !!useQuery(mq.protect).data?.underAttack.on;
  useEffect(() => {
    const lead = alarm ? "Under attack · " : degraded ? "Degraded · " : "";
    document.title = `${lead}${page} · Tiffin`;
  }, [page, degraded, alarm]);
}
