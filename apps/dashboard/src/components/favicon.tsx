import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";

const mark = (stroke: string) =>
  `<path d='M8.5 7.5V5.25a3.5 3.5 0 0 1 7 0V7.5' stroke='${stroke}'/><path d='M5 8.25h14' stroke='${stroke}'/><rect x='5.5' y='8.25' width='13' height='13' rx='2.25' stroke='${stroke}'/><path d='M5.5 12.6h13M5.5 16.9h13' stroke='${stroke}'/>`;

function icon(ok: boolean | undefined, alarm: boolean) {
  const brass = "#c9a24a";
  const red = "#e0533f";
  const dot = ok === false && !alarm ? `<circle cx='19' cy='5' r='4.6' fill='${red}' stroke='white' stroke-width='1.4'/>` : "";
  // Under attack: the whole mark turns red on a red tile, so the tab is hard to miss.
  const bg = alarm ? `<rect x='0.5' y='0.5' width='23' height='23' rx='5' fill='${red}' stroke='none'/>` : "";
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke-width='1.9' stroke-linecap='round' stroke-linejoin='round'>${bg}${mark(alarm ? "white" : brass)}${dot}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** The tab icon shows box health: the plain mark when all is well, a red dot when not. */
export function useFavicon() {
  const { data } = useQuery(q.status());
  const ok = data?.ok;
  const alarm = !!useQuery(mq.protect).data?.underAttack.on;
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    link.type = "image/svg+xml";
    link.href = icon(ok, alarm);
  }, [ok, alarm]);
}

/** "Activity · Tiffin", prefixed while the box is degraded. */
export function useTitle(page: string) {
  const { data } = useQuery(q.status());
  const degraded = data && !data.ok;
  const alarm = !!useQuery(mq.protect).data?.underAttack.on;
  useEffect(() => {
    document.title = `${alarm ? "Under attack · " : degraded ? "Degraded · " : ""}${page} · Tiffin`;
  }, [page, degraded, alarm]);
}
