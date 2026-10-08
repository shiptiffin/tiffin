import { useState, type ReactNode } from "react";
import type { Backup } from "@/api/modules";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup, Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { fromLocalInput, inRange, minInput, shortDate, toLocalInput, utcClock, utcOffset, dayName } from "@/lib/backup-days";
import { clock, relative } from "@/lib/time";

/** What a restore goes back to: a backup set, or (time) a moment, with "latest" as the id. */
export type RestorePick = { id: string; time?: string };

type Range = { earliest: string; latest: string } | null | undefined;

/**
 * The first step of a restore: where to go back to. Latest (the default),
 * one backup, or a moment within the restorable range in local time. Picking
 * leads on to the confirm dialog, which says exactly what is overwritten.
 */
export function RestoreChooser({
  open,
  onOpenChange,
  sets,
  range,
  onPick,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  /** Successful sets, newest first. */
  sets: Backup[];
  range: Range;
  onPick: (p: RestorePick) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        {open && <Choose sets={sets} range={range} onPick={onPick} onCancel={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  );
}

function Choose({ sets, range, onPick, onCancel }: { sets: Backup[]; range: Range; onPick: (p: RestorePick) => void; onCancel: () => void }) {
  const [kind, setKind] = useState<"latest" | "set" | "time">("latest");
  const [set, setSet] = useState(sets[0]?.id ?? "");
  const [local, setLocal] = useState(() => (range ? toLocalInput(range.latest) : ""));
  const moment = fromLocalInput(local);
  const ok = kind === "latest" ? !!sets[0] : kind === "set" ? !!set : inRange(moment, range);
  const next = () => onPick(kind === "latest" ? { id: "latest" } : kind === "set" ? { id: set } : { id: "latest", time: moment! });

  return (
    <>
      <DialogHeader>
        <DialogTitle>Restore the box</DialogTitle>
        <DialogDescription>Choose where to go back to. Nothing changes until you confirm on the next step.</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <RadioGroup
          value={kind}
          onValueChange={(v) => setKind(v as typeof kind)}
          aria-label="Restore to"
          className="flex flex-col divide-y divide-rule border-y border-rule"
        >
          <Choice value="latest" label="Latest" hint={sets[0] ? `The newest backup, from ${relative(sets[0].startedAt)}.` : "No backup yet."} />
          <Choice value="set" label="A backup" hint="One of the backups in the history.">
            {kind === "set" && (
              <Select
                className="mt-2"
                aria-label="Backup"
                value={set}
                onValueChange={setSet}
                options={sets.map((b) => ({
                  value: b.id,
                  label: `${dayName(b.startedAt)}, ${clock(b.startedAt)} · ${b.kind === "full" ? "full" : "changes"}${b.trigger === "pre-restore" ? " · safety copy" : ""}`,
                }))}
              />
            )}
          </Choice>
          <Choice
            value="time"
            label="A moment"
            hint={range ? `Any minute since ${shortDate(range.earliest)}, ${clock(range.earliest)}.` : "Needs a backup first."}
            disabled={!range}
          >
            {kind === "time" && range && (
              <div className="mt-2">
                <Input
                  type="datetime-local"
                  aria-label="Moment to restore to, in your time"
                  value={local}
                  min={minInput(range.earliest)}
                  max={toLocalInput(range.latest)}
                  onChange={(e) => setLocal(e.target.value)}
                  className="tnum w-full sm:w-64"
                />
                <p className="mt-1.5 text-[0.8125rem] text-ink-3">
                  {moment ? `Your time (${utcOffset(moment)}); the box keeps UTC: ${utcClock(moment)} UTC.` : "Pick a day and a time."}
                  {moment && !inRange(moment, range) && <span className="block text-danger">That’s outside what can be restored.</span>}
                </p>
                <p className="mt-1.5 text-[0.8125rem] text-ink-2">
                  The database goes back to that minute. KV and files keep no history between backups, so they go back to the newest backup before it.
                </p>
              </div>
            )}
          </Choice>
        </RadioGroup>
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button disabled={!ok} onClick={next}>
          Continue…
        </Button>
      </DialogFooter>
    </>
  );
}

function Choice({
  value,
  label,
  hint,
  disabled,
  children,
}: {
  value: string;
  label: string;
  hint: string;
  disabled?: boolean;
  children?: ReactNode;
}) {
  const id = `restore-${value}`;
  return (
    <div className="grid grid-cols-[1rem_minmax(0,1fr)] gap-x-3 py-3">
      <Radio id={id} value={value} disabled={disabled} className="mt-0.5" />
      <div className="min-w-0">
        <label htmlFor={id} className="block cursor-pointer text-[0.875rem] text-ink">
          {label}
        </label>
        <p className="text-[0.8125rem] text-ink-3">{hint}</p>
        {children}
      </div>
    </div>
  );
}
