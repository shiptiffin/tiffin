import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { q } from "@/api/queries";

const mark = (stroke: string) =>
  `<path d='M8.5 7.5V5.25a3.5 3.5 0 0 1 7 0V7.5' stroke='${stroke}'/><path d='M5 8.25h14' stroke='${stroke}'/><rect x='5.5' y='8.25' width='13' height='13' rx='2.25' stroke='${stroke}'/><path d='M5.5 12.6h13M5.5 16.9h13' stroke='${stroke}'/>`;

function icon(ok: boolean | undefined) {
  const brass = "#c9a24a";
  const dot = ok === false ? `<circle cx='19' cy='5' r='4.6' fill='#e0533f' stroke='white' stroke-width='1.4'/>` : "";
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke-width='1.9' stroke-linecap='round' stroke-linejoin='round'>${mark(brass)}${dot}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** The tab icon shows box health: the plain mark when all is well, a red dot when not. */
export function useFavicon() {
  const { data } = useQuery(q.status());
  const ok = data?.ok;
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    link.type = "image/svg+xml";
    link.href = icon(ok);
  }, [ok]);
}

/** "Activity · Tiffin", prefixed while the box is degraded. */
export function useTitle(page: string) {
  const { data } = useQuery(q.status());
  const degraded = data && !data.ok;
  useEffect(() => {
    document.title = `${degraded ? "Degraded · " : ""}${page} · Tiffin`;
  }, [page, degraded]);
}
