import { Plus } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { jsonLine } from "@/components/data-parts";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { ValueGrid, type GridRow } from "./grid";
import { asJSON, jsonProblem, streamTime, unixHint } from "./words";
import { useKv } from "./write";

/** One page-merged view of a key's value, as the editors need it. */
export type Items =
  | { type: "string"; text: string; truncated: boolean }
  | { type: "hash"; pairs: Array<[string, string]> }
  | { type: "list" | "set"; items: string[] }
  | { type: "zset"; pairs: Array<[string, number]> }
  | { type: "stream"; entries: Array<[string, string[]]> };

type Paging = { more: boolean; loadMore: () => void };
const quiet = () => undefined;

/** A field value as one line: JSON compacted, a Unix time with its date. */
function Cell({ text }: { text: string }) {
  const j = asJSON(text);
  const hint = unixHint(text);
  return (
    <>
      {j ? jsonLine(j) : text}
      {hint && <span className="ml-2 font-sans text-xs text-ink-3">{hint}</span>}
    </>
  );
}

/** A short form under a grid: inputs, then the button. */
function AddRow({ children, onSubmit, label, disabled }: { children: ReactNode; onSubmit: () => Promise<unknown>; label: string; disabled?: boolean }) {
  const [busy, setBusy] = useState(false);
  return (
    <form
      className="mt-2.5 flex flex-wrap items-center gap-2"
      onSubmit={async (e: FormEvent) => {
        e.preventDefault();
        setBusy(true);
        try {
          await onSubmit();
        } catch {
          // the toast said why
        } finally {
          setBusy(false);
        }
      }}
    >
      {children}
      <Button type="submit" size="sm" disabled={disabled || busy}>
        <Plus />
        {label}
      </Button>
    </form>
  );
}

function Field({ className, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      spellCheck={false}
      className={cn(
        "h-7 min-w-0 rounded-[6px] border border-rule-2 bg-paper-raised px-2 font-mono text-[0.78125rem] text-ink outline-none placeholder:font-sans placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]",
        className,
      )}
      {...props}
    />
  );
}

// ------------------------------------------------------------------ text / JSON

export function TextEditor({ k, text, truncated }: { k: string; text: string; truncated: boolean }) {
  const { run, canWrite } = useKv();
  const [draft, setDraft] = useState(text);
  const [base, setBase] = useState(text);
  const [busy, setBusy] = useState(false);
  if (text !== base) {
    // The value changed on the box (or after Undo): show it, unless mid-edit.
    setBase(text);
    if (draft === base) setDraft(text);
  }
  const dirty = draft !== text;
  const json = asJSON(draft);
  const problem = jsonProblem(draft);
  const save = async () => {
    if (!dirty || busy) return;
    setBusy(true);
    try {
      await run("set", { key: k, value: draft }, `Saved ${k}`);
    } catch {
      // the toast said why
    } finally {
      setBusy(false);
    }
  };
  if (truncated)
    return (
      <div>
        <pre className="max-h-[50vh] overflow-auto rounded-[10px] border border-rule-2 bg-paper-sunk px-3.5 py-3 font-mono text-[0.78125rem] leading-5 whitespace-pre-wrap text-ink">
          {text}
        </pre>
        <p className="mt-2 text-sm text-ink-3">Over 1 MB, so only the start is shown and it can't be edited here. Change it from your app or the Console.</p>
      </div>
    );
  return (
    <div>
      <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
        <div className="flex items-center justify-between gap-2 border-b border-rule bg-paper-sunk px-3 py-1">
          <span className="text-xs text-ink-3">{json ? "JSON" : "Text"}</span>
          {json !== null && canWrite && (
            <Button size="sm" variant="ghost" className="h-6" onClick={() => setDraft(JSON.stringify(json, null, 2))}>
              Format
            </Button>
          )}
        </div>
        <textarea
          aria-label={`Value of ${k}`}
          value={draft}
          readOnly={!canWrite}
          spellCheck={false}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void save();
            }
            if (e.key === "Escape" && dirty) {
              e.preventDefault();
              setDraft(text);
            }
          }}
          rows={Math.min(22, Math.max(6, draft.split("\n").length + 1))}
          className="block w-full resize-y bg-transparent px-3.5 py-3 font-mono text-[0.78125rem] leading-5 text-ink outline-none"
        />
      </div>
      {problem && (
        <p className="mt-2 text-sm text-warn-ink" role="status">
          Not valid JSON ({problem}). It saves as plain text.
        </p>
      )}
      {canWrite && (
        <div className="mt-2.5 flex items-center gap-2">
          <Button size="sm" variant="primary" disabled={!dirty || busy} onClick={save} title="Save (⌘↵)">
            {busy ? "Saving…" : "Save"}
          </Button>
          {dirty && (
            <Button size="sm" variant="ghost" onClick={() => setDraft(text)} title="Revert (Esc)">
              Revert
            </Button>
          )}
          <span className="ml-auto text-xs text-ink-3 tnum">{int(new TextEncoder().encode(draft).length)} bytes</span>
        </div>
      )}
    </div>
  );
}

// ------------------------------------------------------------------ hash

export function HashEditor({ k, pairs, more, loadMore }: { k: string; pairs: Array<[string, string]> } & Paging) {
  const { run, canWrite } = useKv();
  const [field, setField] = useState("");
  const [val, setVal] = useState("");
  const rows: GridRow[] = pairs.map(([f, v]) => ({ id: f, cells: [f, <Cell key="v" text={v} />], edit: [undefined, v] }));
  return (
    <>
      <ValueGrid
        label={`Fields of ${k}`}
        cols={[
          { label: "Field", width: "minmax(7rem,0.6fr)", quiet: true },
          { label: "Value", width: "minmax(10rem,1.4fr)" },
        ]}
        rows={rows}
        more={more}
        loadMore={loadMore}
        onSave={canWrite ? (r, _c, text) => run("hash/set", { key: k, fields: { [pairs[r][0]]: text } }, `Saved ${pairs[r][0]} on ${k}`) : undefined}
        onDelete={canWrite ? (r) => void run("hash/delete", { key: k, fields: [pairs[r][0]] }, `Deleted ${pairs[r][0]} from ${k}`).catch(quiet) : undefined}
        deleteLabel={(r) => `Delete field ${pairs[r][0]}`}
      />
      {canWrite && (
        <AddRow
          label="Add field"
          disabled={!field}
          onSubmit={async () => {
            await run("hash/set", { key: k, fields: { [field]: val } }, `Saved ${field} on ${k}`);
            setField("");
            setVal("");
          }}
        >
          <Field aria-label="New field" placeholder="field" value={field} onChange={(e) => setField(e.target.value)} className="w-36" />
          <Field aria-label="Its value" placeholder="value" value={val} onChange={(e) => setVal(e.target.value)} className="flex-1 basis-40" />
        </AddRow>
      )}
    </>
  );
}

// ------------------------------------------------------------------ list

export function ListEditor({ k, items, more, loadMore }: { k: string; items: string[] } & Paging) {
  const { run, canWrite } = useKv();
  const [val, setVal] = useState("");
  const rows: GridRow[] = items.map((v, i) => ({ id: String(i), cells: [int(i), <Cell key="v" text={v} />], edit: [undefined, v] }));
  return (
    <>
      <ValueGrid
        label={`Items of ${k}`}
        cols={[
          { label: "#", width: "3.5rem", quiet: true },
          { label: "Item", width: "minmax(10rem,1fr)" },
        ]}
        rows={rows}
        more={more}
        loadMore={loadMore}
        onSave={canWrite ? (r, _c, text) => run("list/set", { key: k, index: r, value: text }, `Changed item ${r} of ${k}`) : undefined}
        onDelete={canWrite ? (r) => void run("list/remove", { key: k, index: r }, `Removed item ${r} from ${k}`).catch(quiet) : undefined}
        deleteLabel={(r) => `Remove item ${r}`}
      />
      {canWrite && (
        <form className="mt-2.5 flex flex-wrap items-center gap-2" onSubmit={(e) => e.preventDefault()}>
          <Field aria-label="New item" placeholder="new item" value={val} onChange={(e) => setVal(e.target.value)} className="flex-1 basis-48" />
          {(["tail", "head"] as const).map((end) => (
            <Button
              key={end}
              type={end === "tail" ? "submit" : "button"}
              size="sm"
              disabled={!val}
              onClick={async () => {
                try {
                  await run("list/push", { key: k, values: [val], head: end === "head" }, `Added an item to the ${end === "head" ? "start" : "end"} of ${k}`);
                  setVal("");
                } catch {
                  // the toast said why
                }
              }}
            >
              <Plus />
              {end === "tail" ? "Add to end" : "Add to start"}
            </Button>
          ))}
        </form>
      )}
    </>
  );
}

// ------------------------------------------------------------------ set

export function SetEditor({ k, items, more, loadMore }: { k: string; items: string[] } & Paging) {
  const { run, canWrite } = useKv();
  const [val, setVal] = useState("");
  return (
    <>
      <ValueGrid
        label={`Members of ${k}`}
        cols={[{ label: "Member", width: "minmax(10rem,1fr)" }]}
        rows={items.map((m) => ({ id: m, cells: [<Cell key="m" text={m} />] }))}
        more={more}
        loadMore={loadMore}
        onDelete={canWrite ? (r) => void run("set/remove", { key: k, members: [items[r]] }, `Removed ${items[r]} from ${k}`).catch(quiet) : undefined}
        deleteLabel={(r) => `Remove ${items[r]}`}
      />
      {canWrite && (
        <AddRow
          label="Add member"
          disabled={!val}
          onSubmit={async () => {
            await run("set/add", { key: k, members: [val] }, `Added ${val} to ${k}`);
            setVal("");
          }}
        >
          <Field aria-label="New member" placeholder="member" value={val} onChange={(e) => setVal(e.target.value)} className="flex-1 basis-48" />
        </AddRow>
      )}
    </>
  );
}

// ------------------------------------------------------------------ sorted set

const scoreText = (n: number) => (Number.isInteger(n) ? int(n) : String(n));

export function ZsetEditor({ k, pairs, more, loadMore, ranked }: { k: string; pairs: Array<[string, number]>; ranked: boolean } & Paging) {
  const { run, canWrite } = useKv();
  const [member, setMember] = useState("");
  const [score, setScore] = useState("");
  return (
    <>
      <ValueGrid
        label={`Members of ${k}, highest score first`}
        cols={[
          ...(ranked ? [{ label: "Rank", width: "3.5rem", quiet: true }] : []),
          { label: "Member", width: "minmax(8rem,1fr)" },
          { label: "Score", width: "minmax(6rem,0.4fr)", align: "right" as const },
        ]}
        rows={pairs.map(([m, s], i) => ({
          id: m,
          cells: [...(ranked ? [int(i + 1)] : []), <Cell key="m" text={m} />, scoreText(s)],
          edit: [...(ranked ? [undefined] : []), undefined, String(s)],
        }))}
        more={more}
        loadMore={loadMore}
        onSave={
          canWrite
            ? async (r, _c, text) => {
                const n = Number(text.trim());
                if (text.trim() === "" || !Number.isFinite(n)) throw new Error("not a number");
                return run("zset/add", { key: k, members: [{ member: pairs[r][0], score: n }] }, `Set ${pairs[r][0]}'s score to ${text.trim()} in ${k}`);
              }
            : undefined
        }
        onDelete={canWrite ? (r) => void run("zset/remove", { key: k, members: [pairs[r][0]] }, `Removed ${pairs[r][0]} from ${k}`).catch(quiet) : undefined}
        deleteLabel={(r) => `Remove ${pairs[r][0]}`}
      />
      {canWrite && (
        <AddRow
          label="Add member"
          disabled={!member || !Number.isFinite(Number(score)) || score.trim() === ""}
          onSubmit={async () => {
            await run("zset/add", { key: k, members: [{ member, score: Number(score) }] }, `Added ${member} to ${k}`);
            setMember("");
            setScore("");
          }}
        >
          <Field aria-label="New member" placeholder="member" value={member} onChange={(e) => setMember(e.target.value)} className="flex-1 basis-40" />
          <Field aria-label="Its score" placeholder="score" inputMode="decimal" value={score} onChange={(e) => setScore(e.target.value)} className="w-24 text-right" />
        </AddRow>
      )}
    </>
  );
}

// ------------------------------------------------------------------ stream

export function StreamEditor({ k, entries, more, loadMore, length }: { k: string; entries: Array<[string, string[]]>; length: number } & Paging) {
  const { run, canWrite } = useKv();
  const [fields, setFields] = useState<Array<[string, string]>>([["", ""]]);
  const [keep, setKeep] = useState("");
  const ok = fields.some(([f]) => f);
  return (
    <>
      <ValueGrid
        label={`Entries of ${k}, newest first`}
        cols={[
          { label: "Entry", width: "minmax(9rem,0.5fr)", quiet: true },
          { label: "Fields", width: "minmax(10rem,1.5fr)" },
        ]}
        rows={entries.map(([id, fv]) => ({
          id,
          cells: [
            <span key="id" title={id}>
              {streamTime(id) ?? id}
            </span>,
            fv.reduce<string[]>((out, x, i) => (i % 2 ? out : [...out, `${x}=${fv[i + 1] ?? ""}`]), []).join("  "),
          ],
        }))}
        more={more}
        loadMore={loadMore}
        onDelete={canWrite ? (r) => void run("stream/delete", { key: k, ids: [entries[r][0]] }, `Deleted entry ${entries[r][0]} from ${k}`).catch(quiet) : undefined}
        deleteLabel={(r) => `Delete entry ${entries[r][0]}`}
      />
      {canWrite && (
        <div className="mt-2.5 space-y-2">
          <AddRow
            label="Add entry"
            disabled={!ok}
            onSubmit={async () => {
              await run("stream/add", { key: k, fields: fields.filter(([f]) => f).map(([field, value]) => ({ field, value })) }, `Added an entry to ${k}`);
              setFields([["", ""]]);
            }}
          >
            <div className="flex flex-1 basis-full flex-col gap-1.5 sm:basis-0">
              {fields.map(([f, v], i) => (
                <div key={i} className="flex gap-2">
                  <Field
                    aria-label={`Field ${i + 1}`}
                    placeholder="field"
                    value={f}
                    onChange={(e) => setFields(fields.map((x, j) => (j === i ? [e.target.value, x[1]] : x)))}
                    className="w-32"
                  />
                  <Field
                    aria-label={`Value ${i + 1}`}
                    placeholder="value"
                    value={v}
                    onChange={(e) => setFields(fields.map((x, j) => (j === i ? [x[0], e.target.value] : x)))}
                    className="min-w-0 flex-1"
                  />
                </div>
              ))}
            </div>
            <Button type="button" size="sm" variant="ghost" onClick={() => setFields([...fields, ["", ""]])}>
              Another field
            </Button>
          </AddRow>
          <form
            className="flex flex-wrap items-center gap-2 text-sm text-ink-2"
            onSubmit={async (e) => {
              e.preventDefault();
              try {
                await run("stream/trim", { key: k, maxLen: Number(keep) }, `Kept the newest ${keep} entries of ${k}`);
                setKeep("");
              } catch {
                // the toast said why
              }
            }}
          >
            <label htmlFor="kv-keep">Keep only the newest</label>
            <Field id="kv-keep" inputMode="numeric" placeholder={String(Math.max(0, length - 1))} value={keep} onChange={(e) => setKeep(e.target.value.replace(/\D/g, ""))} className="w-20 text-right" />
            <span>entries</span>
            <Button type="submit" size="sm" variant="ghost" disabled={!keep || Number(keep) >= length}>
              Trim
            </Button>
          </form>
        </div>
      )}
    </>
  );
}
