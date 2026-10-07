import { CornerDownLeft } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { mod, type KVCommandResult } from "@/api/modules";
import { Breaker } from "@/components/breaker";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { ms } from "@/lib/format";
import { useKv } from "./write";

type Entry = { id: number; text: string; results?: KVCommandResult[]; error?: unknown };

/** A reply the way valkey-cli prints it: "text", (integer) 3, (nil), numbered lists. */
export function replyLines(v: unknown, pad = ""): string[] {
  if (v === null || v === undefined) return ["(nil)"];
  if (typeof v === "number") return [`(integer) ${v}`];
  if (typeof v === "string") return [JSON.stringify(v)];
  if (Array.isArray(v)) {
    if (v.length === 0) return ["(empty array)"];
    const w = String(v.length).length;
    return v.flatMap((x, i) => {
      const n = `${String(i + 1).padStart(w)}) `;
      const inner = replyLines(x, pad + " ".repeat(n.length));
      return [n + inner[0], ...inner.slice(1).map((l) => " ".repeat(n.length) + l)];
    });
  }
  return [JSON.stringify(v)];
}

const historyKey = (p: string) => `tiffin.kv.history.${p}`;
function loadHistory(p: string): string[] {
  try {
    return JSON.parse(localStorage.getItem(historyKey(p)) ?? "[]");
  } catch {
    return [];
  }
}

/**
 * The Console: commands as in valkey-cli, run as the project's own user, so
 * keys are written and shown without the project prefix. Reading is always
 * allowed; changing data needs Allow changes, and each change can be undone.
 */
export function KvConsole({ project }: { project: string }) {
  const { canWrite, undo, refresh } = useKv();
  const [text, setText] = useState("");
  const [entries, setEntries] = useState<Entry[]>([]);
  const [write, setWrite] = useState(false);
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<string[]>(() => loadHistory(project));
  const [cursor, setCursor] = useState<number | null>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const log = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // Keep the newest output in view without moving the page.
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [entries]);

  const runIt = async () => {
    const t = text.trim();
    if (!t || busy) return;
    const id = Date.now();
    setBusy(true);
    setEntries((e) => [...e, { id, text: t }]);
    setText("");
    setCursor(null);
    const h = [...history.filter((x) => x !== t), t].slice(-100);
    setHistory(h);
    try {
      localStorage.setItem(historyKey(project), JSON.stringify(h));
    } catch {
      // private mode: history lasts the visit
    }
    try {
      const r = await mod.kvCommand(project, t, write);
      setEntries((e) => e.map((x) => (x.id === id ? { ...x, results: r.results ?? [] } : x)));
      if ((r.results ?? []).some((x) => x.write && !x.error)) refresh();
    } catch (err) {
      setEntries((e) => e.map((x) => (x.id === id ? { ...x, error: err } : x)));
    } finally {
      setBusy(false);
      input.current?.focus();
    }
  };

  const recall = (dir: -1 | 1) => {
    if (history.length === 0) return false;
    const i = cursor === null ? (dir < 0 ? history.length - 1 : null) : cursor + dir;
    if (i === null || i < 0) return false;
    if (i >= history.length) {
      setCursor(null);
      setText("");
      return true;
    }
    setCursor(i);
    setText(history[i]);
    return true;
  };

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <p className="text-sm text-ink-3">
          Runs as {project}'s own user: write keys as your apps name them. <span className="kbd">↑</span> recalls earlier commands.
        </p>
        <div className="flex items-center gap-3">
          {entries.length > 0 && (
            <Button size="sm" variant="ghost" onClick={() => setEntries([])}>
              Clear
            </Button>
          )}
          {canWrite && <Breaker label="Allow changes" printed="beside" state={write ? "on" : "off"} onFlip={(n) => setWrite(n === "on")} />}
        </div>
      </div>
      <div
        ref={log}
        role="log"
        aria-label="Console output"
        aria-live="polite"
        className="max-h-[min(58vh,36rem)] min-h-56 overflow-auto rounded-[10px] border border-rule-2 bg-paper-sunk px-3.5 py-3 font-mono text-[0.78125rem] leading-5"
      >
        {entries.length === 0 && (
          <div className="text-ink-3">
            <p>Try</p>
            {["SCAN 0 COUNT 20", "DBSIZE", "TYPE leaderboard", "ZRANGE leaderboard 0 9 REV WITHSCORES"].map((ex) => (
              <button key={ex} type="button" onClick={() => setText(ex)} className="block text-left text-ink-2 hover:text-ink">
                {ex}
              </button>
            ))}
          </div>
        )}
        {entries.map((e) => (
          <div key={e.id} className="mb-3 last:mb-0">
            {(e.results ?? e.text.split("\n").map((l) => ({ command: [l], reply: undefined }) as unknown as KVCommandResult)).map((r, i) => (
              <div key={i} className="mb-1.5">
                <p className="text-ink-2">
                  <span aria-hidden className="mr-2 text-ink-4 select-none">
                    ›
                  </span>
                  {(r.command ?? []).map((w) => (/[\s"']/.test(w) || w === "" ? JSON.stringify(w) : w)).join(" ")}
                </p>
                {e.results && (
                  <div className="pl-4">
                    {r.error ? (
                      <p className="text-danger">(error) {r.error}</p>
                    ) : (
                      <pre className="whitespace-pre-wrap break-all text-ink">{replyLines(r.reply).join("\n")}</pre>
                    )}
                    <p className="mt-0.5 flex items-center gap-3 font-sans text-xs text-ink-3">
                      <span className="tnum">{ms(r.ms)}</span>
                      {r.undo && (
                        <button type="button" className="font-[550] text-brass-ink hover:underline" onClick={() => void undo(r.undo!)}>
                          Undo
                        </button>
                      )}
                    </p>
                  </div>
                )}
              </div>
            ))}
            {!e.results && !e.error && <p className="pl-4 font-sans text-xs text-ink-3">Running…</p>}
            {e.error ? <ProblemNote className="mt-1 font-sans" error={e.error} /> : null}
          </div>
        ))}
      </div>
      <div className="flex items-end gap-2">
        <div className="flex min-w-0 flex-1 items-start gap-2 rounded-[10px] border border-rule-2 bg-paper-raised px-3 py-2 focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
          <span aria-hidden className="pt-px font-mono text-[0.8125rem] text-ink-3 select-none">
            ›
          </span>
          <textarea
            ref={input}
            aria-label="Command"
            value={text}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            rows={Math.min(8, Math.max(1, text.split("\n").length))}
            placeholder="HGETALL session:u_2041"
            onChange={(e) => {
              setText(e.target.value);
              setCursor(null);
            }}
            onKeyDown={(e) => {
              const el = e.currentTarget;
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void runIt();
              } else if (e.key === "ArrowUp" && !text.slice(0, el.selectionStart).includes("\n")) {
                if (recall(-1)) e.preventDefault();
              } else if (e.key === "ArrowDown" && !text.slice(el.selectionEnd).includes("\n") && cursor !== null) {
                if (recall(1)) e.preventDefault();
              }
            }}
            className={cn("min-h-5 w-full resize-none bg-transparent font-mono text-[0.8125rem] leading-5 text-ink outline-hidden placeholder:text-ink-4")}
          />
        </div>
        <Button variant="primary" onClick={runIt} disabled={!text.trim() || busy} title="Run (Enter; Shift+Enter for a new line)">
          <CornerDownLeft />
          Run
        </Button>
      </div>
    </div>
  );
}
