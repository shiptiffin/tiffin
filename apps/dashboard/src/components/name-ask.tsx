import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/api/client";
import { useMe } from "@/lib/me";
import { ProblemNote } from "./problem";
import { toast } from "./toast";
import { Button } from "./ui/button";

const KEY = "tiffin.name-asked";
const asked = () => {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
};
const remember = () => {
  try {
    localStorage.setItem(KEY, "1");
  } catch {
    /* storage blocked: we'll ask again next time, which is harmless */
  }
};

/** Renames the signed-in person; the Ledger signs their changes with it from then on. */
function useRename(onDone?: () => void) {
  const qc = useQueryClient();
  const { me } = useMe();
  return useMutation({
    mutationFn: (name: string) => api.updatePerson(me!.person!, { name: name.trim() }),
    onSuccess: (p) => {
      remember();
      void qc.invalidateQueries({ queryKey: ["whoami"] });
      void qc.invalidateQueries({ queryKey: ["people"] });
      toast({ title: `Hello, ${p.name}.`, detail: "Your changes are signed with this name from now on." });
      onDone?.();
    },
  });
}

/**
 * The first visit only: the owner starts out called "Owner". One line to
 * give a real name (skippable, asked once). Settings › People can change it
 * any time (<RenameSelf>).
 */
export function NameAsk() {
  const { me, role } = useMe();
  const [hidden, setHidden] = useState(asked);
  const [name, setName] = useState("");
  const rename = useRename(() => setHidden(true));
  const isDefault = !!me?.person && (me.personName ?? "").toLowerCase() === "owner";
  if (hidden || !isDefault || (role !== "owner" && role !== "admin")) return null;
  return (
    <form
      className="mb-8 flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-3 shadow-raised"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) rename.mutate(name);
      }}
    >
      <label htmlFor="name-ask" className="min-w-0 flex-1 basis-60">
        <span className="block text-[0.9375rem] font-[550] text-ink">What should we call you?</span>
        <span className="block text-[0.8125rem] text-ink-3">History shows your changes with it. Right now it says “Owner”.</span>
      </label>
      <input
        id="name-ask"
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={64}
        autoComplete="name"
        placeholder="Your name"
        className="h-8 w-48 rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.875rem] text-ink placeholder:text-ink-3 focus-visible:border-brass focus-visible:outline-none"
      />
      <Button type="submit" variant="primary" disabled={!name.trim() || rename.isPending}>
        Save
      </Button>
      <Button
        type="button"
        variant="ghost"
        onClick={() => {
          remember();
          setHidden(true);
        }}
      >
        Not now
      </Button>
      {rename.isError && <ProblemNote error={rename.error} className="w-full" />}
    </form>
  );
}

/** "Rename" beside your own name in Settings › People. */
export function RenameSelf({ current }: { current: string }) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(current);
  const rename = useRename(() => setEditing(false));
  if (!editing)
    return (
      <button type="button" className="text-[0.8125rem] font-[550] text-brass-ink hover:underline hover:underline-offset-4" onClick={() => setEditing(true)}>
        Change your name
      </button>
    );
  return (
    <form
      className="mt-1 flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) rename.mutate(name);
      }}
    >
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={64}
        autoFocus
        aria-label="Your name"
        className="h-8 w-48 rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.875rem] text-ink focus-visible:border-brass focus-visible:outline-none"
      />
      <Button type="submit" variant="primary" size="sm" disabled={!name.trim() || rename.isPending}>
        Save
      </Button>
      <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
        Cancel
      </Button>
      {rename.isError && <ProblemNote error={rename.error} className="w-full" />}
    </form>
  );
}
