// The table editor's API: a table in full, pages of rows, row edits with
// Undo, making and changing tables, saved queries.
import { queryOptions } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import { sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import type { components } from "@/api/schema";

type S = components["schemas"];
/** The schema marks every list as nullable (Go's nil slice); these operations always send lists. */
type Lists<T> = T extends null ? never : T extends (infer U)[] ? Lists<U>[] : T extends object ? { [K in keyof T]: Lists<T[K]> } : T;
export type TableDetail = Lists<S["PostgresPGTableDetail"]>;
export type ColumnDetail = Lists<S["PostgresPGColumnDetail"]>;
export type ForeignKey = Lists<S["PostgresPGForeignKey"]>;
export type Filter = S["PostgresPGFilter"];
export type Sort = S["PostgresPGSort"];
export type RowsRequest = S["PostgresPGRowsRequest"];
export type Rows = Lists<S["PostgresPGRows"]>;
export type RowChange = S["PostgresPGRowChange"];
export type Edit = Lists<S["PostgresPGEdit"]>;
export type EditResult = Lists<S["PostgresPGEditResult"]>;
export type NewColumn = S["PostgresPGNewColumn"];
export type ColumnChange = S["PostgresPGColumnChange"];
export type TableCreate = S["PostgresPGTableCreate"];
export type TableAlter = S["PostgresPGTableAlter"];
export type DDLResult = S["PostgresPGDDLResult"];
export type SavedQuery = S["PostgresPGSavedQuery"];

const e = encodeURIComponent;
const T = (p: string, schema: string, table: string) => `/v1/projects/${e(p)}/tables/${e(schema)}/${e(table)}`;
const branchQ = (b?: string) => (b ? `?branch=${e(b)}` : "");
const withBranch = <B extends object>(body: B, branch?: string) => (branch ? { ...body, branch } : body);

export const db = {
  table: (p: string, schema: string, table: string, branch?: string) => request<TableDetail>("GET", `${T(p, schema, table)}${branchQ(branch)}`),
  rows: (p: string, schema: string, table: string, body: RowsRequest, signal?: AbortSignal) => request<Rows>("POST", `${T(p, schema, table)}/rows/query`, body, signal),
  insert: (p: string, schema: string, table: string, values: Record<string, unknown>, branch?: string) =>
    request<EditResult>("POST", `${T(p, schema, table)}/rows`, withBranch({ values }, branch)),
  update: (p: string, schema: string, table: string, changes: RowChange[], branch?: string) =>
    request<EditResult>("PATCH", `${T(p, schema, table)}/rows`, withBranch({ changes }, branch)),
  remove: (p: string, schema: string, table: string, keys: Array<Record<string, unknown>>, branch?: string) =>
    request<EditResult>("POST", `${T(p, schema, table)}/rows/delete`, withBranch({ keys }, branch)),
  edits: (p: string) => request<Edit[] | null>("GET", `/v1/projects/${e(p)}/postgres/edits?limit=100`).then((x) => x ?? []),
  undo: (p: string, id: string, force?: boolean) => request<EditResult>("POST", `/v1/projects/${e(p)}/postgres/edits/${e(id)}/undo`, force ? { force } : {}),
  createTable: (p: string, body: TableCreate) => request<DDLResult>("POST", `/v1/projects/${e(p)}/tables`, body),
  alterTable: (p: string, schema: string, table: string, body: TableAlter) => request<DDLResult>("PATCH", T(p, schema, table), body),
  dropTable: (p: string, schema: string, table: string, branch?: string, confirm?: string) =>
    request<DDLResult>("POST", `${T(p, schema, table)}/drop`, withBranch(confirm ? { confirm } : {}, branch)),
  queries: (p: string) => request<SavedQuery[] | null>("GET", `/v1/projects/${e(p)}/postgres/queries`).then((x) => x ?? []),
  saveQuery: (p: string, name: string, sql: string) => request<SavedQuery>("PUT", `/v1/projects/${e(p)}/postgres/queries/${e(name)}`, { sql }),
  deleteQuery: (p: string, name: string) => request<void>("DELETE", `/v1/projects/${e(p)}/postgres/queries/${e(name)}`),
};

export const dq = {
  table: (p: string, schema: string, table: string, branch?: string) =>
    queryOptions({ queryKey: ["pg-table", p, branch ?? "", schema, table], queryFn: () => db.table(p, schema, table, branch) }),
  edits: (p: string) => queryOptions({ queryKey: ["pg-edits", p], queryFn: () => db.edits(p), retry: false }),
  queries: (p: string) => queryOptions({ queryKey: ["pg-queries", p], queryFn: () => db.queries(p) }),
};

/** Says an API problem in a toast: the detail, then the fix. */
export function problemToast(e: unknown, fallback = "That didn't work.") {
  const p = e instanceof ApiError ? e.problem : undefined;
  const detail = p?.detail ?? (e instanceof Error ? e.message : fallback);
  toast({ title: sentence(detail), detail: p?.hint ? sentence(p.hint) : undefined, tone: "danger", duration: 12_000 });
}

/** An edit as History says it: "Changed price on row 27 in books". */
export function editWords(e: Edit): string {
  const t = slugOf({ schema: e.schema, name: e.table });
  const where = e.branch ? ` (copy ${e.branch})` : "";
  const one = e.rows === 1 && e.keys?.[0] ? `row ${e.keys[0]}` : `${e.rows} rows`;
  if (e.undoOf) return `Undid an edit to ${t}${where}`;
  if (e.kind === "insert") return `${e.rows === 1 ? "Added a row" : `Added ${e.rows} rows`} to ${t}${where}`;
  if (e.kind === "delete") return `Deleted ${one} from ${t}${where}`;
  const cols = e.columns ?? [];
  return `Changed ${cols.length === 1 ? cols[0] : cols.length === 2 ? cols.join(" and ") : `${cols.length} columns`} on ${one} in ${t}${where}`;
}

/** "books", or "auth.users" outside public: how tables are named in URLs and words. */
export const slugOf = (t: { schema: string; name: string }) => (t.schema === "public" ? t.name : `${t.schema}.${t.name}`);
export function parseSlug(slug: string): { schema: string; name: string } {
  const i = slug.indexOf(".");
  return i > 0 ? { schema: slug.slice(0, i), name: slug.slice(i + 1) } : { schema: "public", name: slug };
}
