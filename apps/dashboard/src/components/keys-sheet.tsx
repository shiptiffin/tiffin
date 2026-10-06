import { Fragment } from "react";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { keyCaps, useKeyHelpList, useShortcuts, type Shortcut } from "@/lib/shortcuts";

const ORDER = ["On this page", "Go to", "Anywhere"];

/**
 * `?`: every shortcut that works right now, grouped: this page's own first
 * (its shortcuts, then the keys its grid or editor handles), then Go to and
 * Anywhere.
 */
export function KeysSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const list = useShortcuts();
  const help = useKeyHelpList();
  const all: Shortcut[] = [{ keys: "mod+k", label: "Search and commands", group: "Anywhere", run: () => {} }, ...list];
  // One row per keys: the latest registration is the one that runs.
  const rows = [...new Map(all.map((s) => [s.keys, s])).values()];
  const groups = [...new Set([...ORDER, ...rows.map((s) => s.group)])].map((g) => [g, rows.filter((s) => s.group === g)] as const).filter(([, l]) => l.length);
  const page = groups.filter(([g]) => g === "On this page");
  const rest = groups.filter(([g]) => g !== "On this page");
  const caps = (keys: string) => keyCaps(keys).map((k, i) => (
    <Fragment key={i}>
      {i > 0 && <span className="px-0.5">then</span>}
      <kbd className="kbd">{k}</kbd>
    </Fragment>
  ));
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Press a letter, or G then a letter. They’re off while you type in a field.</DialogDescription>
        </DialogHeader>
        <DialogBody className="grid gap-6">
          {page.map(([g, l]) => (
            <Group key={g} title={g} rows={l.map((s) => [s.keys, s.label, caps(s.keys)])} />
          ))}
          {help.map((h) => (
            <Group
              key={h.group}
              title={h.group}
              rows={h.keys.map(([k, label]) => [
                k,
                label,
                k.split(" ").map((x, i) => (
                  <kbd key={i} className="kbd">
                    {x}
                  </kbd>
                )),
              ])}
            />
          ))}
          {rest.map(([g, l]) => (
            <Group key={g} title={g} rows={l.map((s) => [s.keys, s.label, caps(s.keys)])} />
          ))}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

function Group({ title, rows }: { title: string; rows: Array<[string, string, React.ReactNode]> }) {
  return (
    <section aria-label={title}>
      <h3 className="label mb-1.5">{title}</h3>
      <dl className="divide-y divide-rule border-y border-rule">
        {rows.map(([k, label, keys]) => (
          <div key={k + label} className="flex items-center justify-between gap-4 py-2">
            <dt className="text-[0.875rem] text-ink-2">{label}</dt>
            <dd className="flex shrink-0 items-center gap-1 text-xs text-ink-3">{keys}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
