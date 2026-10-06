import { useEffect, useRef, useSyncExternalStore } from "react";

/**
 * Keyboard shortcuts and page commands, in one registry the shell, ⌘K and
 * every page share.
 *
 *   useShortcut("g d", "Go to the database", () => …)      a key or a two-key sequence
 *   useCommand({ id: "new-table", label: "New table", run, keys: "n" })
 *
 * A page registers while it is mounted. Shortcuts never fire while you type
 * in a field or while a dialog or menu is open; `?` lists them all. Commands
 * appear in ⌘K under "On this page", and ⌘K's "New table", "New key",
 * "New schedule", "Upload file" and "Connect…" open the right page and then
 * run the command with that id once the page registers it (`requestCommand`).
 */
export type Shortcut = { keys: string; label: string; group: string; run: () => void };
export type Command = { id: string; label: string; keywords?: string[]; keys?: string; run: () => void };

let shortcuts: Shortcut[] = [];
let commands: Command[] = [];
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());
const subscribe = (f: () => void) => (subs.add(f), () => subs.delete(f));

/** Registers a shortcut while the calling component is mounted. Later registrations of the same keys win. */
export function useShortcut(keys: string, label: string, run: () => void, group = "On this page", enabled = true) {
  const ref = useRef(run);
  useEffect(() => {
    ref.current = run;
  });
  useEffect(() => {
    if (!enabled) return;
    const s: Shortcut = { keys, label, group, run: () => ref.current() };
    shortcuts = [...shortcuts, s];
    emit();
    return () => {
      shortcuts = shortcuts.filter((x) => x !== s);
      emit();
    };
  }, [keys, label, group, enabled]);
}

/** Registers a ⌘K command (and its shortcut, when it has keys) while mounted. */
export function useCommand(c: Command | null) {
  const ref = useRef(c?.run);
  useEffect(() => {
    ref.current = c?.run;
  });
  const id = c?.id;
  const label = c?.label;
  const keys = c?.keys;
  const kw = c?.keywords?.join(" ");
  useEffect(() => {
    if (!id || !label) return;
    const cmd: Command = { id, label, keys, keywords: kw ? kw.split(" ") : undefined, run: () => ref.current?.() };
    commands = [...commands, cmd];
    const s: Shortcut | null = keys ? { keys, label, group: "On this page", run: cmd.run } : null;
    if (s) shortcuts = [...shortcuts, s];
    emit();
    if (pending && pending.id === id && Date.now() - pending.at < 8000) {
      pending = null;
      setTimeout(cmd.run, 0);
    }
    return () => {
      commands = commands.filter((x) => x !== cmd);
      if (s) shortcuts = shortcuts.filter((x) => x !== s);
      emit();
    };
  }, [id, label, keys, kw]);
}

export const useShortcuts = () => useSyncExternalStore(subscribe, () => shortcuts);
export const useCommands = () => useSyncExternalStore(subscribe, () => commands);

/**
 * Keys a page handles itself (a grid's arrows, an editor's ⌘↵), listed in
 * the `?` sheet under the page's own heading while it is mounted:
 *   useKeyHelp("Table", KEYS)   with KEYS a constant [["↑ ↓ ← →", "Move between cells"], …]
 */
export type KeyHelp = { group: string; keys: Array<[string, string]> };
let help: KeyHelp[] = [];
export function useKeyHelp(group: string, keys: Array<[string, string]>) {
  useEffect(() => {
    const h = { group, keys };
    help = [...help, h];
    emit();
    return () => {
      help = help.filter((x) => x !== h);
      emit();
    };
  }, [group, keys]);
}
export const useKeyHelpList = () => useSyncExternalStore(subscribe, () => help);

let pending: { id: string; at: number } | null = null;
/** Runs the command `id` now if a page has it, else as soon as the page you are going to registers it. */
export function requestCommand(id: string) {
  const c = commands.findLast((x) => x.id === id);
  if (c) return c.run();
  pending = { id, at: Date.now() };
}

/**
 * True when a key press belongs to what you are typing, to a grid (which owns
 * its keys), to anything marked data-own-keys, or to an open dialog or menu.
 */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target;
  if (t instanceof Element && t.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"], [role="textbox"], [role="combobox"], [role="grid"], [role="treegrid"], [data-own-keys]')) return true;
  return !!document.querySelector('[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]');
}

/** How a shortcut's keys read on screen: "g d" → ["g", "d"], "mod+k" → ["⌘K"]. */
export function keyCaps(keys: string): string[] {
  const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  return keys.split(" ").map((k) => k.replace("mod+", mac ? "⌘" : "Ctrl ").replace("shift+", "⇧").replace(/^(.)$/, (c) => c.toUpperCase()).replace(/(⌘|Ctrl )(.)$/, (_, m, c) => m + c.toUpperCase()));
}

/**
 * The one keyboard listener (the Shell mounts it): single keys and two-key
 * sequences pressed within a second ("g" then "d").
 */
export function listen(): () => void {
  let prev = "";
  let at = 0;
  const onKey = (e: KeyboardEvent) => {
    if (e.metaKey || e.ctrlKey || e.altKey || e.isComposing || e.repeat || typing(e)) return;
    const key = e.key;
    const seq = Date.now() - at < 1000 && prev ? `${prev} ${key}` : "";
    const hit = (seq && shortcuts.findLast((s) => s.keys === seq)) || shortcuts.findLast((s) => s.keys === key);
    if (hit) {
      e.preventDefault();
      prev = "";
      hit.run();
      return;
    }
    if (shortcuts.some((s) => s.keys.startsWith(`${key} `))) {
      prev = key;
      at = Date.now();
    } else prev = "";
  };
  window.addEventListener("keydown", onKey);
  return () => window.removeEventListener("keydown", onKey);
}
