import { KeyRound } from "lucide-react";
import { CopyButton } from "@/components/copy";
import { GitHubMark } from "@/components/github-mark";
import { cn } from "@/lib/cn";

// Small pieces shared by Box settings → Sign-in providers and a project's
// sign-in settings.

const MONO: Record<string, string> = {
  google: "G",
  apple: "A",
  microsoft: "M",
  discord: "D",
  facebook: "f",
  twitter: "X",
  linkedin: "in",
  gitlab: "GL",
  slack: "S",
  twitch: "T",
};

/** A provider's mark: a quiet monogram tile (GitHub's own mark), never brand colours. */
export function ProviderMark({ id, className }: { id: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid [:where(&)]:size-8 shrink-0 place-items-center [:where(&)]:rounded-[8px] border border-rule-2 bg-paper text-[0.8125rem] font-[600] tracking-[-0.02em] text-ink-2 shadow-[var(--top-light)]",
        className,
      )}
    >
      {id === "github" ? <GitHubMark className="size-[15px]" /> : id === "oidc" ? <KeyRound className="size-[15px]" /> : (MONO[id] ?? id.slice(0, 1).toUpperCase())}
    </span>
  );
}

/** "Tested" once someone signed in through the box with it; "Not tested yet" until then. */
export function TestedTag({ tested }: { tested: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center rounded-[5px] border px-1.5 text-[0.6875rem] font-[550] whitespace-nowrap",
        tested ? "border-ok/40 text-ok" : "border-rule-2 text-ink-3",
      )}
    >
      {tested ? "Tested" : "Not tested yet"}
    </span>
  );
}

/** A URL to paste somewhere, with a copy button. */
export function PasteField({ value, label, className, strong }: { value: string; label: string; className?: string; strong?: boolean }) {
  return (
    <div className={cn("flex min-w-0 items-center gap-1 rounded-[7px] border py-1 pr-1 pl-2.5", strong ? "border-brass/50 bg-brass-wash/40" : "border-rule bg-paper", className)}>
      <code className="min-w-0 flex-1 truncate font-mono text-[0.75rem] text-ink" title={value}>
        {value}
      </code>
      <CopyButton value={value} label={label} className="size-6" />
    </div>
  );
}
