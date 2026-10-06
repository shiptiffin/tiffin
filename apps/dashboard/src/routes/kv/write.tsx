import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { mod, type KVWrite, type KVWrites } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { int } from "@/lib/format";
import { useMe } from "@/lib/me";

/**
 * Every change in the KV console goes through run(): the API call, then a
 * toast that says what changed with Undo (the API keeps a copy of what was
 * there for an hour). A write too big to copy, and deleting a whole prefix,
 * come back 428 first: the dialog names what goes, and only then it runs.
 */
type Runner = {
  project: string;
  /** May the signed-in person change data here (viewers can't). */
  canWrite: boolean;
  run: <K extends keyof KVWrites>(op: K, body: KVWrites[K], done: string) => Promise<KVWrite | undefined>;
  undo: (id: string) => Promise<void>;
  refresh: () => void;
};

const Ctx = createContext<Runner | null>(null);

export function useKv(): Runner {
  const r = useContext(Ctx);
  if (!r) throw new Error("useKv outside KvWrites");
  return r;
}

type Preview = { count?: number; prefix?: string; examples?: string[]; undo?: boolean; keys?: string[] };
type Ask = { op: string; preview: Preview; go: () => Promise<KVWrite>; resolve: (r: KVWrite | undefined) => void };

export function problemWords(e: unknown): { title: string; detail?: string } {
  const p = e instanceof ApiError ? e.problem : undefined;
  return { title: sentence(p?.detail ?? (e instanceof Error ? e.message : "Something went wrong")), detail: p?.hint ? sentence(p.hint) : undefined };
}

export function KvWrites({ project, children }: { project: string; children: ReactNode }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [ask, setAsk] = useState<Ask | null>(null);
  const result = useRef<KVWrite | undefined>(undefined);

  const refresh = useCallback(() => {
    for (const k of ["kv-key", "kv-tree", "kv-stats"]) void qc.invalidateQueries({ queryKey: [k, project] });
  }, [qc, project]);

  const undo = useCallback(
    async (id: string) => {
      try {
        const r = await mod.kvWrite(project, "undo", { id });
        refresh();
        toast({ title: "Put back as it was.", action: r.undo ? { label: "Redo", run: () => undo(r.undo!) } : undefined });
      } catch (e) {
        refresh();
        toast({ ...problemWords(e), tone: "danger" });
      }
    },
    [project, refresh],
  );

  const run = useCallback(
    async <K extends keyof KVWrites>(op: K, body: KVWrites[K], done: string) => {
      const said = (r: KVWrite) => {
        refresh();
        toast({ title: done, detail: r.undo ? undefined : "This one can't be undone.", action: r.undo ? { label: "Undo", run: () => undo(r.undo!) } : undefined });
        return r;
      };
      try {
        return said(await mod.kvWrite(project, op, body));
      } catch (e) {
        if (e instanceof ApiError && e.status === 428 && e.problem.confirm) {
          const confirm = e.problem.confirm;
          const r = await new Promise<KVWrite | undefined>((resolve) => {
            result.current = undefined;
            setAsk({ op, preview: (e.problem.preview ?? {}) as Preview, go: () => mod.kvWrite(project, op, { ...body, confirm }), resolve });
          });
          return r && said(r);
        }
        refresh();
        toast({ ...problemWords(e), tone: "danger" });
        throw e;
      }
    },
    [project, refresh, undo],
  );

  const value = useMemo(() => ({ project, canWrite: can("apply:reversible"), run, undo, refresh }), [project, can, run, undo, refresh]);
  const p = ask?.preview ?? {};
  const prefixDelete = ask?.op === "delete-prefix";
  const what = prefixDelete ? `${int(p.count ?? 0)} ${p.count === 1 ? "key" : "keys"} starting with ${p.prefix}` : (p.keys ?? []).join(", ");

  return (
    <Ctx.Provider value={value}>
      {children}
      <Confirm
        open={!!ask}
        onClose={() => {
          ask?.resolve(result.current);
          setAsk(null);
        }}
        title={prefixDelete ? `Delete ${what}?` : "Go ahead without Undo?"}
        body={
          prefixDelete ? (
            <>
              {(p.examples ?? []).length > 0 && (
                <span className="mb-2 block font-mono text-sm text-ink-2">
                  {(p.examples ?? []).join("  ")}
                  {(p.count ?? 0) > (p.examples ?? []).length && " …"}
                </span>
              )}
              {p.undo ? "You can undo this for an hour." : "There are too many to keep a copy of, so this can't be undone."}
            </>
          ) : (
            <>
              <span className="font-mono text-ink">{what}</span> holds more than 1 MB, too much to keep a copy of, so this change can't be undone.
            </>
          )
        }
        action={prefixDelete ? `Delete ${int(p.count ?? 0)} ${p.count === 1 ? "key" : "keys"}` : "Go ahead"}
        tone={prefixDelete || ask?.op === "delete" ? "danger" : "normal"}
        run={async () => {
          result.current = await ask!.go();
        }}
        done={() => undefined}
      />
    </Ctx.Provider>
  );
}
