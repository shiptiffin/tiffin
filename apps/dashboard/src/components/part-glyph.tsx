import { AppWindow, BarChart3, Clock, Database, FolderOpen, KeyRound, Mail, Zap } from "lucide-react";
import type { ReactNode } from "react";
import { partName } from "@/lib/names";

/** One small line icon per part of a project, the same everywhere (tiles, cards, Add). */
export const PART_ICON: Record<string, ReactNode> = {
  app: <AppWindow />,
  postgres: <Database />,
  valkey: <Zap />,
  storage: <FolderOpen />,
  email: <Mail />,
  auth: <KeyRound />,
  analytics: <BarChart3 />,
  jobs: <Clock />,
};

/** A quiet row of a project's parts: an app glyph per app, then one per service. */
export function PartGlyphs({ apps, services, className }: { apps: number; services: string[]; className?: string }) {
  const order = ["postgres", "storage", "auth", "email", "analytics", "valkey"];
  const list = [...services].sort((a, b) => order.indexOf(a) - order.indexOf(b));
  if (apps === 0 && list.length === 0) return null;
  const label = [apps > 0 ? `${apps} ${apps === 1 ? "app" : "apps"}` : "", ...list.map(partName)].filter(Boolean).join(", ");
  return (
    <span className={className} role="img" aria-label={label} title={label}>
      <span className="inline-flex items-center gap-1.5 text-ink-3 [&_svg]:size-3.5" aria-hidden>
        {Array.from({ length: Math.min(apps, 3) }, (_, i) => (
          <span key={`a${i}`}>{PART_ICON.app}</span>
        ))}
        {list.map((s) => (
          <span key={s}>{PART_ICON[s]}</span>
        ))}
      </span>
    </span>
  );
}
