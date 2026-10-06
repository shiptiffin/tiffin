// What the table view shows, kept in the URL so a filtered view can be
// shared and survives a reload: ?f=price.lt.30,in_stock.eq.true&s=title.desc&h=meta
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { ColumnDetail, Filter, Sort } from "./api";

export type View = { filters: Filter[]; sort: Sort[]; hidden: string[] };
export type DataSearch = { branch?: string; f?: string; s?: string; h?: string; new?: string };

const enc = (s: string) => encodeURIComponent(s).replace(/\./g, "%2E");
const dec = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};

export const OPS: Record<Filter["op"], string> = {
  eq: "is",
  neq: "is not",
  lt: "<",
  lte: "≤",
  gt: ">",
  gte: "≥",
  contains: "contains",
  startsWith: "starts with",
  isNull: "is empty",
  notNull: "is not empty",
  in: "is one of",
};

/** The operators that make sense for a column. */
export function opsFor(c: Pick<ColumnDetail, "category" | "nullable">): Filter["op"][] {
  const empty: Filter["op"][] = c.nullable ? ["isNull", "notNull"] : [];
  switch (c.category) {
    case "number":
    case "date":
    case "time":
    case "timestamp":
      return ["eq", "neq", "lt", "lte", "gt", "gte", "in", ...empty];
    case "bool":
    case "enum":
      return ["eq", "neq", "in", ...empty];
    case "json":
    case "array":
      return ["contains", ...empty];
    default:
      return ["contains", "eq", "neq", "startsWith", "in", ...empty];
  }
}

export function parseView(s: DataSearch): View {
  const filters: Filter[] = [];
  for (const part of (s.f ?? "").split(",").filter(Boolean)) {
    const [c, op, ...rest] = part.split(".");
    if (!c || !(op in OPS)) continue;
    const value = dec(rest.join("."));
    const f: Filter = { column: dec(c), op: op as Filter["op"] };
    if (op === "in") f.values = value.split("|").map(dec);
    else if (op !== "isNull" && op !== "notNull") f.value = value;
    filters.push(f);
  }
  const sort = (s.s ?? "")
    .split(",")
    .filter(Boolean)
    .map((p) => {
      const desc = p.endsWith(".desc");
      return { column: dec(desc ? p.slice(0, -5) : p.replace(/\.asc$/, "")), desc };
    });
  const hidden = (s.h ?? "").split(",").filter(Boolean).map(dec);
  return { filters, sort, hidden };
}

export function viewSearch(v: View): Pick<DataSearch, "f" | "s" | "h"> {
  const f = v.filters
    .map((x) => {
      const val = x.op === "in" ? (x.values ?? []).map((y) => encodeURIComponent(y)).join("|") : x.op === "isNull" || x.op === "notNull" ? "" : (x.value ?? "");
      return `${enc(x.column)}.${x.op}${val === "" && (x.op === "isNull" || x.op === "notNull") ? "" : "." + enc(val)}`;
    })
    .join(",");
  const s = v.sort.map((x) => `${enc(x.column)}${x.desc ? ".desc" : ""}`).join(",");
  const h = v.hidden.map(enc).join(",");
  return { f: f || undefined, s: s || undefined, h: h || undefined };
}

/** The search of the Database pages (branch, view), read loosely so any data route can use it. */
export function useDataSearch(): DataSearch {
  const s = useSearch({ strict: false }) as Record<string, unknown>;
  const str = (v: unknown) => (typeof v === "string" && v ? v : typeof v === "number" ? String(v) : undefined);
  return { branch: str(s.branch), f: str(s.f), s: str(s.s), h: str(s.h), new: str(s.new) };
}

/** The copy (preview branch) the Database pages look at; "" is production. */
export function useBranch(): string {
  return useDataSearch().branch ?? "";
}

/** Changes the search of the current page, keeping the rest. */
export function useSetSearch() {
  const navigate = useNavigate();
  return (patch: Partial<DataSearch>, replace = true) =>
    navigate({ to: ".", search: ((prev: Record<string, unknown>) => clean({ ...prev, ...patch })) as never, replace });
}

function clean(o: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== ""));
}
