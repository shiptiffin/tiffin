import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { mod, type FilesResult, type FilesWrites } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { bytes, count } from "@/lib/format";
import { useMe } from "@/lib/me";

/**
 * Every change to files goes through run(): the API call, then a toast that
 * says what changed with Undo (the box keeps deleted files for an hour and
 * undoes a move by moving back). Deleting a folder comes back 428 first:
 * the dialog names how many files and how much goes, then it runs.
 */
type Runner = {
  project: string;
  bucket: string;
  canWrite: boolean;
  run: <K extends keyof FilesWrites>(op: K, body: FilesWrites[K], done: (r: FilesResult) => string) => Promise<FilesResult | undefined>;
  refresh: () => void;
};

const Ctx = createContext<Runner | null>(null);

export function useFiles(): Runner {
  const r = useContext(Ctx);
  if (!r) throw new Error("useFiles outside FilesWrites");
  return r;
}

type Preview = { prefix?: string; count?: number; bytes?: number; examples?: string[] };
type Ask = { preview: Preview; go: () => Promise<FilesResult>; resolve: (r: FilesResult | undefined) => void };

export function problemWords(e: unknown): { title: string; detail?: string } {
  const p = e instanceof ApiError ? e.problem : undefined;
  return { title: sentence(p?.detail ?? (e instanceof Error ? e.message : "Something went wrong")), detail: p?.hint ? sentence(p.hint) : undefined };
}

export function FilesWrites({ project, bucket, children }: { project: string; bucket: string; children: ReactNode }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [ask, setAsk] = useState<Ask | null>(null);
  const result = useRef<FilesResult | undefined>(undefined);

  const refresh = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["objects", project, bucket] });
    void qc.invalidateQueries({ queryKey: ["storage", project] });
  }, [qc, project, bucket]);

  const undo = useCallback(
    async (id: string) => {
      try {
        const r = await mod.filesUndo(project, id);
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
    async <K extends keyof FilesWrites>(op: K, body: FilesWrites[K], done: (r: FilesResult) => string) => {
      const said = (r: FilesResult) => {
        refresh();
        toast({ title: done(r), action: r.undo ? { label: "Undo", run: () => undo(r.undo!) } : undefined });
        return r;
      };
      try {
        return said(await mod.filesWrite(project, bucket, op, body));
      } catch (e) {
        if (e instanceof ApiError && e.status === 428 && e.problem.confirm) {
          const confirm = e.problem.confirm;
          const r = await new Promise<FilesResult | undefined>((resolve) => {
            result.current = undefined;
            setAsk({ preview: (e.problem.preview ?? {}) as Preview, go: () => mod.filesWrite(project, bucket, op, { ...body, confirm }), resolve });
          });
          return r && said(r);
        }
        refresh();
        toast({ ...problemWords(e), tone: "danger" });
        throw e;
      }
    },
    [project, bucket, refresh, undo],
  );

  const value = useMemo(() => ({ project, bucket, canWrite: can("apply:reversible"), run, refresh }), [project, bucket, can, run, refresh]);
  const p = ask?.preview ?? {};
  const n = p.count ?? 0;
  const folder = (p.prefix ?? "").replace(/\/$/, "");

  return (
    <Ctx.Provider value={value}>
      {children}
      <Confirm
        open={!!ask}
        onClose={() => {
          ask?.resolve(result.current);
          setAsk(null);
        }}
        title={`Delete the ${folder.slice(folder.lastIndexOf("/") + 1)} folder?`}
        body={
          <>
            <span className="block">
              {n === 0 ? "It’s empty." : `${count(n, "file")}, ${bytes(p.bytes ?? 0)}, inside ${folder}/ and its folders.`} You can undo this for an
              hour; after that they’re gone for good.
            </span>
            {(p.examples ?? []).length > 0 && (
              <span className="mt-2 block font-mono text-sm break-all text-ink-3">
                {(p.examples ?? []).join("  ")}
                {n > (p.examples ?? []).length && " …"}
              </span>
            )}
          </>
        }
        action={n === 0 ? "Delete folder" : `Delete ${count(n, "file")}`}
        run={async () => {
          result.current = await ask!.go();
        }}
        done={() => undefined}
      />
    </Ctx.Provider>
  );
}
