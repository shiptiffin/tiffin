"use client";

// A button that copies a one-line setup prompt for a coding agent. The line is in the
// page too, so it works without JavaScript (select and copy it by hand).
import { useEffect, useRef, useState } from "react";

export const AGENT_LINE = "Set up ShipTiffin for me: follow https://shiptiffin.com/agent-setup.md";

export function CopyPrompt({ className = "btn btn-quiet" }: { className?: string }) {
  const [state, setState] = useState<"" | "ok" | "failed">("");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function copy() {
    try {
      await navigator.clipboard.writeText(AGENT_LINE);
      setState("ok");
    } catch {
      setState("failed");
    }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState(""), 2400);
  }

  return (
    <button type="button" className={className} onClick={copy} aria-live="polite">
      {state === "ok" ? "Copied. Paste it into your agent" : state === "failed" ? "Couldn’t copy: see below" : "Copy prompt for your agent"}
    </button>
  );
}
