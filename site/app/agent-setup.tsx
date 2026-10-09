"use client";

// The setup prompt for a coding agent, shown in full and copied in one click.
// The text is public/agent-setup.md (from docs/guide/agent-onboarding.md),
// read at build time by the page.
import { useEffect, useRef, useState } from "react";

export function AgentSetup({ prompt }: { prompt: string }) {
  const [copied, setCopied] = useState<"" | "ok" | "failed">("");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function copy() {
    try {
      await navigator.clipboard.writeText(prompt);
      setCopied("ok");
    } catch {
      setCopied("failed");
    }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(""), 2400);
  }

  return (
    <div className="code agent-setup" role="group" aria-label="Setup prompt for a coding agent">
      <div className="code-bar">
        <span>Prompt for your agent</span>
        <button type="button" className="btn btn-quiet btn-sm agent-copy" onClick={copy}>
          {copied === "ok" ? "Copied" : "Copy prompt"}
        </button>
      </div>
      <pre tabIndex={0}>{prompt}</pre>
      <p className="agent-setup-note" aria-live="polite">
        {copied === "failed" ? (
          "Your browser didn’t allow copying. Select the text above instead."
        ) : (
          <>
            Or paste one line: <code>
              Set up ShipTiffin for me: follow <span className="nowrap">https://shiptiffin.com/agent-setup.md</span>
            </code>
          </>
        )}
      </p>
    </div>
  );
}
