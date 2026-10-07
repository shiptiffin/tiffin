import { Plus, X } from "lucide-react";
import { useState } from "react";
import { RadioGroup, RadioItem, Select } from "@/components/ui/choice";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { Segmented } from "./parts";
import { jsonProblem, TYPES, UNITS, type KVType } from "./words";
import { useKv } from "./write";

type Pair = [string, string];

const input =
  "h-8 w-full min-w-0 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 font-mono text-[0.8125rem] text-ink outline-none placeholder:font-sans placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

/** New key: choose the type first, then a name, the value and whether it's kept or cache. */
export function NewKeyDialog({ open, onOpenChange, prefix, onMade }: { open: boolean; onOpenChange: (o: boolean) => void; prefix: string; onMade: (key: string) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">{open && <NewKey prefix={prefix} onMade={onMade} close={() => onOpenChange(false)} />}</DialogContent>
    </Dialog>
  );
}

function NewKey({ prefix, onMade, close }: { prefix: string; onMade: (key: string) => void; close: () => void }) {
  const { run } = useKv();
  const [type, setType] = useState<KVType | null>(null);
  const [name, setName] = useState(prefix);
  const [text, setText] = useState("");
  const [pairs, setPairs] = useState<Pair[]>([["", ""]]);
  const [life, setLife] = useState<"kept" | "cache">("kept");
  const [n, setN] = useState("1");
  const [unit, setUnit] = useState<string>("hours");
  const [busy, setBusy] = useState(false);
  const lines = text.split("\n").filter((l) => l !== "");
  const filled = pairs.filter(([a]) => a);
  const scoresOk = type !== "zset" || filled.every(([, s]) => s.trim() !== "" && Number.isFinite(Number(s)));
  const ready =
    !!type &&
    !!name.trim() &&
    (type === "string" ? true : type === "list" || type === "set" ? lines.length > 0 : filled.length > 0 && scoresOk) &&
    (life === "kept" || Number(n) > 0);

  const create = async () => {
    if (!ready || !type) return;
    const key = name.trim();
    const base = { key, create: true, ...(life === "cache" ? { ttlSeconds: Math.round(Number(n) * (UNITS.find((u) => u.unit === unit)?.s ?? 3600)) } : {}) };
    const said = `Made ${key}`;
    setBusy(true);
    try {
      const r =
        type === "string"
          ? await run("set", { ...base, value: text }, said)
          : type === "hash"
            ? await run("hash/set", { ...base, fields: Object.fromEntries(filled) }, said)
            : type === "list"
              ? await run("list/push", { ...base, values: lines }, said)
              : type === "set"
                ? await run("set/add", { ...base, members: lines }, said)
                : type === "zset"
                  ? await run("zset/add", { ...base, members: filled.map(([member, s]) => ({ member, score: Number(s) })) }, said)
                  : await run("stream/add", { ...base, fields: filled.map(([field, value]) => ({ field, value })) }, said);
      if (r) {
        close();
        onMade(key);
      }
    } catch {
      // the toast said why
    } finally {
      setBusy(false);
    }
  };

  const pairLabels: Record<string, [string, string, string]> = {
    hash: ["field", "value", "Another field"],
    zset: ["member", "score", "Another member"],
    stream: ["field", "value", "Another field"],
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void create();
      }}
      className="flex min-h-0 flex-col"
    >
      <DialogHeader>
        <DialogTitle>New key</DialogTitle>
        <DialogDescription>{type ? "Name it and give it a first value." : "What kind of value will it hold?"}</DialogDescription>
      </DialogHeader>
      <DialogBody className="space-y-5">
        <RadioGroup aria-label="Type of value" value={type ?? ""} onValueChange={(v) => setType(v as KVType)} className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          {TYPES.map((t) => (
            <RadioItem
              key={t.type}
              value={t.type}
              className="group flex flex-col items-start rounded-[8px] border border-rule-2 bg-paper-raised px-3 py-2 text-left transition-colors hover:border-rule-3 data-[state=checked]:border-brass data-[state=checked]:bg-brass-wash"
            >
              <span className="text-[0.875rem] font-[550] text-ink">{t.name}</span>
              <span className="text-xs text-ink-3 group-data-[state=checked]:text-ink-2">{t.sub}</span>
            </RadioItem>
          ))}
        </RadioGroup>

        {type && (
          <>
            <div>
              <label htmlFor="kv-new-name" className="mb-1.5 block text-sm font-medium text-ink">
                Name
              </label>
              <input
                id="kv-new-name"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. settings:theme"
                spellCheck={false}
                className={input}
              />
              <p className="mt-1 text-xs text-ink-3">Use : to group keys, like session:u_2041.</p>
            </div>

            <div>
              <p className="mb-1.5 text-sm font-medium text-ink" id="kv-new-value">
                {type === "string" ? "Value" : type === "list" ? "Items, one per line" : type === "set" ? "Members, one per line" : type === "stream" ? "First entry" : type === "zset" ? "Members and scores" : "Fields"}
              </p>
              {type === "string" || type === "list" || type === "set" ? (
                <>
                  <textarea
                    aria-labelledby="kv-new-value"
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    rows={5}
                    spellCheck={false}
                    placeholder={type === "string" ? 'Text, or JSON like {"theme": "dark"}' : undefined}
                    className={cn(input, "h-auto resize-y py-2 leading-5")}
                  />
                  {type === "string" && jsonProblem(text) && <p className="mt-1 text-xs text-warn-ink">Not valid JSON ({jsonProblem(text)}). It saves as plain text.</p>}
                </>
              ) : (
                <div className="space-y-1.5" role="group" aria-labelledby="kv-new-value">
                  {pairs.map(([a, b], i) => (
                    <div key={i} className="flex gap-1.5">
                      <input
                        aria-label={`${pairLabels[type][0]} ${i + 1}`}
                        placeholder={pairLabels[type][0]}
                        value={a}
                        spellCheck={false}
                        onChange={(e) => setPairs(pairs.map((p, j) => (j === i ? [e.target.value, p[1]] : p)))}
                        className={cn(input, "flex-1")}
                      />
                      <input
                        aria-label={`${pairLabels[type][1]} ${i + 1}`}
                        placeholder={pairLabels[type][1]}
                        value={b}
                        spellCheck={false}
                        inputMode={type === "zset" ? "decimal" : undefined}
                        onChange={(e) => setPairs(pairs.map((p, j) => (j === i ? [p[0], e.target.value] : p)))}
                        className={cn(input, type === "zset" ? "w-24 text-right" : "flex-[1.4]")}
                      />
                      {pairs.length > 1 && (
                        <Button type="button" size="icon-sm" variant="ghost" aria-label={`Remove row ${i + 1}`} onClick={() => setPairs(pairs.filter((_, j) => j !== i))}>
                          <X />
                        </Button>
                      )}
                    </div>
                  ))}
                  <Button type="button" size="sm" variant="ghost" onClick={() => setPairs([...pairs, ["", ""]])}>
                    <Plus />
                    {pairLabels[type][2]}
                  </Button>
                </div>
              )}
            </div>

            <div>
              <p className="mb-1.5 text-sm font-medium text-ink" id="kv-new-life">
                How long it stays
              </p>
              <div className="flex flex-wrap items-center gap-2">
                <Segmented
                  label="How long it stays"
                  value={life}
                  onChange={setLife}
                  options={[
                    { value: "kept", label: "Until deleted" },
                    { value: "cache", label: "Expires" },
                  ]}
                />
                {life === "cache" && (
                  <span className="flex items-center gap-1.5 text-sm text-ink-2">
                    after
                    <input
                      aria-label="Expires after"
                      inputMode="numeric"
                      value={n}
                      onChange={(e) => setN(e.target.value.replace(/[^\d.]/g, ""))}
                      className="h-7 w-14 rounded-[6px] border border-rule-2 bg-paper-raised px-2 text-right text-ink tnum outline-none focus-visible:border-brass"
                    />
                    <Select size="sm" aria-label="Unit" value={unit} onValueChange={setUnit} className="w-24 font-mono" options={UNITS.map((u) => ({ value: u.unit, label: u.unit }))} />
                  </span>
                )}
              </div>
              <p className="mt-1 text-xs text-ink-3">
                {life === "kept" ? "Kept: never dropped to make room." : "Cache: dropped first when memory runs short, and gone when it expires."}
              </p>
            </div>
          </>
        )}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={close}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ready || busy}>
          {busy ? "Making…" : "Make key"}
        </Button>
      </DialogFooter>
    </form>
  );
}
