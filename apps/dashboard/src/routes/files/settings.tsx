import { Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import type { StorageBucket } from "@/api/modules";
import { Checkbox, Radio, RadioGroup, Select } from "@/components/ui/choice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { bytes } from "@/lib/format";
import { useMe } from "@/lib/me";
import { change, pendingFor, usePending, type StagedEdit } from "@/lib/staged";
import { Sheet } from "@/routes/data/sheet";
import { SIZE_STOPS, TYPE_FAMILIES, typesWords } from "./words";

const path = (b: string, field: string) => ["services", "storage", "buckets", b, field];

/**
 * A bucket's rules: who can read, the largest file, which types, which
 * websites may upload from the browser. Each control is a change to
 * tiffin.config.ts the moment it moves, with Undo in the toast (making a
 * bucket public asks first: it reaches outside the box).
 */
export function BucketSettings({
  project,
  b,
  open,
  onOpenChange,
}: {
  project: string;
  b: StorageBucket;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const { can } = useMe();
  const writer = can("apply:reversible");
  const edits = usePending(project);
  const staged = <T,>(field: string, live: T): T => {
    const e = pendingFor(edits, `set:${path(b.name, field).join("/")}`);
    return e?.kind === "set" ? ((e.to as T) ?? (Array.isArray(live) ? ([] as T) : (0 as T))) : live;
  };
  const access = pendingFor(edits, `bucket:${b.name}`);
  const isPublic = access?.kind === "bucket" ? access.to === "public" : b.public;
  const max = staged("maxFileSize", b.maxFileSize ?? 0);
  const types = staged<string[]>("allowedTypes", b.allowedTypes ?? []);
  const cors = staged<string[]>("cors", b.cors ?? []);
  const set = (field: string, from: unknown, to: unknown, what: string, undo: string) =>
    change(project, { kind: "set", path: path(b.name, field), from, to, what, undo } satisfies StagedEdit);

  const setMax = (v: number) =>
    set(
      "maxFileSize",
      b.maxFileSize || undefined,
      v || undefined,
      v ? `Limit files in ${b.name} to ${bytes(v)}` : `Let ${b.name} take files of any size`,
      b.maxFileSize ? `${b.name} takes files up to ${bytes(b.maxFileSize)} again` : `${b.name} takes files of any size again`,
    );
  const setTypes = (next: string[]) =>
    set(
      "allowedTypes",
      b.allowedTypes?.length ? b.allowedTypes : undefined,
      next.length ? next : undefined,
      next.length ? `Let ${b.name} take ${typesWords(next).toLowerCase()} only` : `Let ${b.name} take any type of file`,
      `${b.name} takes ${typesWords(b.allowedTypes).toLowerCase()} again`,
    );
  const setCors = (next: string[]) =>
    set(
      "cors",
      b.cors?.length ? b.cors : undefined,
      next.length ? next : undefined,
      next.length ? `Let ${next.join(", ")} upload to ${b.name}` : `Let only ${project}’s own apps upload to ${b.name}`,
      `${b.name}'s upload sites are back as they were`,
    );
  const sizes = SIZE_STOPS.includes(max) ? SIZE_STOPS : [...SIZE_STOPS, max].sort((x, y) => x - y);
  const custom = types.filter((t) => !TYPE_FAMILIES.some((f) => f.types.includes(t)));

  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={`${b.name} settings`} sub="Changes apply at once, with Undo.">
      <fieldset disabled={!writer} className="space-y-7">
        <section aria-labelledby="who">
          <h3 id="who" className="text-[0.9375rem] font-[550] text-ink">
            Who can read files
          </h3>
          <RadioGroup
            value={isPublic ? "public" : "private"}
            onValueChange={(v) =>
              change(
                project,
                { kind: "bucket", bucket: b.name, from: b.public ? "public" : "private", to: v as "public" | "private" },
                { immediate: true },
              )
            }
            className="mt-2.5 space-y-2"
            aria-labelledby="who"
          >
            <label className="flex items-start gap-2.5">
              <Radio value="private" className="mt-0.5" />
              <span>
                <span className="block text-base text-ink">Only with a link that expires</span>
                <span className="block text-sm text-ink-3">Your app makes signed links; they work for as long as it says.</span>
              </span>
            </label>
            <label className="flex items-start gap-2.5">
              <Radio value="public" className="mt-0.5" />
              <span>
                <span className="block text-base text-ink">Anyone with the link</span>
                <span className="block text-sm text-ink-3">
                  For images and downloads on public pages. Asks first: anyone who has a link can open it.
                </span>
              </span>
            </label>
          </RadioGroup>
        </section>

        <section aria-labelledby="max">
          <h3 id="max" className="text-[0.9375rem] font-[550] text-ink">
            Largest file
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">Uploads over this are refused, from your app and from here.</p>
          <Select
            aria-labelledby="max"
            value={String(max)}
            onValueChange={(v) => setMax(Number(v))}
            className="mt-2.5 w-48"
            options={sizes.map((s) => ({ value: String(s), label: s ? bytes(s, 0) : "No limit" }))}
          />
        </section>

        <section aria-labelledby="types">
          <h3 id="types" className="text-[0.9375rem] font-[550] text-ink">
            Types of file
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">
            {types.length ? `Takes ${typesWords(types).toLowerCase()} only.` : "Takes any type. Tick some to take only those."}
          </p>
          <div className="mt-2.5 flex flex-wrap gap-x-5 gap-y-2">
            {TYPE_FAMILIES.map((f) => {
              const on = f.types.every((t) => types.includes(t));
              return (
                <label key={f.label} className="flex items-center gap-2 text-base text-ink">
                  <Checkbox
                    checked={on}
                    onCheckedChange={() =>
                      setTypes(on ? types.filter((t) => !f.types.includes(t)) : [...types, ...f.types.filter((t) => !types.includes(t))])
                    }
                  />
                  {f.label}
                </label>
              );
            })}
          </div>
          <ListEditor
            label="Other types"
            placeholder="application/zip"
            items={custom}
            onAdd={(v) => setTypes([...types, v.toLowerCase()])}
            onRemove={(v) => setTypes(types.filter((t) => t !== v))}
            valid={(v) => /^[a-z0-9.+-]+\/([a-z0-9.+-]+|\*)$/i.test(v)}
            hint="A type such as application/zip, or a family such as font/*."
          />
        </section>

        <section aria-labelledby="sites">
          <h3 id="sites" className="text-[0.9375rem] font-[550] text-ink">
            Websites that may upload
          </h3>
          <p className="mt-0.5 text-sm text-ink-3">
            {cors.length
              ? "Only these sites may send files from the browser."
              : `${project}’s own apps (their addresses and previews) and localhost. Add a site to allow others instead.`}
          </p>
          <ListEditor
            label="Websites"
            placeholder="https://example.com"
            items={cors}
            onAdd={(v) => setCors([...cors, v.replace(/\/$/, "")])}
            onRemove={(v) => setCors(cors.filter((o) => o !== v))}
            valid={(v) => v === "*" || /^https?:\/\/(\*\.)?[a-z0-9.-]+(:\d+)?\/?$/i.test(v)}
            hint="An address such as https://example.com, https://*.example.com for its subdomains, or * for any site."
          />
        </section>

        <section className="border-t border-rule pt-5">
          <Button
            variant="danger-quiet"
            onClick={() => {
              onOpenChange(false);
              change(project, { kind: "bucket", bucket: b.name, from: b.public ? "public" : "private", to: "absent" }, { immediate: true });
            }}
            disabled={!can("apply:irreversible")}
          >
            <Trash2 />
            Delete bucket
          </Button>
          <p className="mt-1 text-sm text-ink-3">Asks first, saying how many files go. They wait in the trash for 7 days.</p>
        </section>
      </fieldset>
      {!writer && <p className="mt-6 text-sm text-ink-3">Changing settings needs a key that can make changes to {project}.</p>}
    </Sheet>
  );
}

/** A short list of values, each with Remove, and a field to add one. */
function ListEditor({
  label,
  placeholder,
  items,
  onAdd,
  onRemove,
  valid,
  hint,
}: {
  label: string;
  placeholder: string;
  items: string[];
  onAdd: (v: string) => void;
  onRemove: (v: string) => void;
  valid: (v: string) => boolean;
  hint: string;
}) {
  const [v, setV] = useState("");
  const [bad, setBad] = useState(false);
  const hintId = `hint-${label.toLowerCase().replace(/\W+/g, "-")}`;
  const add = () => {
    const x = v.trim();
    if (!x) return;
    if (!valid(x)) return setBad(true);
    if (!items.includes(x)) onAdd(x);
    setV("");
    setBad(false);
  };
  return (
    <div className="mt-3">
      {items.length > 0 && (
        <ul className="mb-2 flex flex-wrap gap-1.5" aria-label={label}>
          {items.map((x) => (
            <li
              key={x}
              className="inline-flex h-7 items-center gap-1 rounded-[6px] border border-rule-2 bg-paper-sunk pr-1 pl-2 font-mono text-[0.75rem] text-ink"
            >
              {x}
              <button
                type="button"
                onClick={() => onRemove(x)}
                aria-label={`Remove ${x}`}
                className="grid size-5 place-items-center rounded-[4px] text-ink-3 hover:bg-paper-press hover:text-ink"
              >
                <X className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          add();
        }}
      >
        <Input
          aria-label={`Add to ${label.toLowerCase()}`}
          aria-invalid={bad || undefined}
          aria-describedby={hintId}
          value={v}
          onChange={(e) => {
            setV(e.target.value);
            setBad(false);
          }}
          placeholder={placeholder}
          spellCheck={false}
          className="h-8 max-w-72 font-mono text-[0.8125rem]"
        />
        <Button size="md" type="submit" disabled={!v.trim()}>
          <Plus />
          Add
        </Button>
      </form>
      <p id={hintId} className={bad ? "mt-1 text-sm text-danger" : "mt-1 text-xs text-ink-3"}>
        {bad ? `That isn't right. ${hint}` : hint}
      </p>
    </div>
  );
}
