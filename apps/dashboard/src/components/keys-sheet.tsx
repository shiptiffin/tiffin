import { Fragment } from "react";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { keyCaps, useShortcuts, type Shortcut } from "@/lib/shortcuts";

const ORDER = ["Anywhere", "Go to", "On this page"];

/** `?`: every shortcut that works right now, grouped, with how to press it. */
export function KeysSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const list = useShortcuts();
  const all: Shortcut[] = [{ keys: "mod+k", label: "Search and commands", group: "Anywhere", run: () => {} }, ...list];
  // One row per keys: the latest registration is the one that runs.
  const rows = [...new Map(all.map((s) => [s.keys, s])).values()];
  const groups = [...new Set([...ORDER, ...rows.map((s) => s.group)])].map((g) => [g, rows.filter((s) => s.group === g)] as const).filter(([, l]) => l.length);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Press a letter, or G then a letter. They’re off while you type in a field.</DialogDescription>
        </DialogHeader>
        <DialogBody className="grid gap-6">
          {groups.map(([g, l]) => (
            <section key={g} aria-label={g}>
              <h3 className="label mb-1.5">{g}</h3>
              <dl className="divide-y divide-rule border-y border-rule">
                {l.map((s) => (
                  <div key={s.keys} className="flex items-center justify-between gap-4 py-2">
                    <dt className="text-[0.875rem] text-ink-2">{s.label}</dt>
                    <dd className="flex shrink-0 items-center gap-1 text-xs text-ink-3">
                      {keyCaps(s.keys).map((k, i) => (
                        <Fragment key={i}>
                          {i > 0 && <span className="px-0.5">then</span>}
                          <kbd className="kbd">{k}</kbd>
                        </Fragment>
                      ))}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
