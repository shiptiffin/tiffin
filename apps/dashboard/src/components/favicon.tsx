import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { markSvg } from "./logo";

// Colours are literal here: a favicon can't read the page's CSS variables.
const INK = "#2b2620";
const BRASS = "#b8862f";
const RED = "#c8412f";
const LIGHT = { line: INK, handle: INK, hand: "#a4a8ac", body: ["#a4a8ac", "#a4a8ac", "#a4a8ac", "#a4a8ac"] as [string, string, string, string] };
const DARK = { line: "#4a4642", handle: "#c9cdd1", hand: "#c4c8cc", body: ["#9ea3a8", "#e4e6e8", "#c3c7cb", "#8f949a"] as [string, string, string, string] };

function icon(state: "plain" | "waiting" | "down" | "alarm") {
  const dot =
    state === "waiting" || state === "down"
      ? `<circle cx='27' cy='5' r='4.5' fill='${state === "down" ? RED : BRASS}' stroke='none'/>`
      : "";
  if (state === "alarm") {
    // Under attack: the mark on a red tile, hard to miss in a row of tabs.
    return `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><rect width='32' height='32' rx='7' fill='${RED}'/>${markSvg({ line: "white", handle: "white", hand: RED, body: [RED, RED, RED, RED] }, "a")}</svg>`;
  }
  return `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><g class='l'>${markSvg(LIGHT, "l")}</g><g class='d'>${markSvg(DARK, "d")}</g><style>.d{display:none}@media (prefers-color-scheme:dark){.l{display:none}.d{display:inline}}</style>${dot}</svg>`;
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
