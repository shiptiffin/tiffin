import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";

export type SettingsSection = { id: string; label: string };

/** The section in view: the URL's #hash when it names one, else the first. */
export function useSettingsSection(sections: SettingsSection[]): string {
  const hash = useLocation({ select: (l) => l.hash });
  return sections.some((s) => s.id === hash) ? hash : (sections[0]?.id ?? "");
}

/**
 * A project's settings, one section at a time: a column of links on wide
 * screens, a dropdown on phones. The section lives in the URL's #hash, so a
 * link can open one ("…/settings#keys") and Back works.
 */
export function SettingsNav({ project, sections, active }: { project: string; sections: SettingsSection[]; active: string }) {
  const navigate = useNavigate();
  return (
    <>
      <div className="lg:hidden">
        <Select
          aria-label="Settings section"
          value={active}
          onValueChange={(v) => void navigate({ to: "/projects/$project/settings", params: { project }, hash: v, resetScroll: false })}
          options={sections.map((s) => ({ value: s.id, label: s.label }))}
          className="sm:w-60"
        />
      </div>
      <nav aria-label="Settings sections" className="hidden lg:block">
        <ul className="sticky top-6 grid gap-0.5">
          {sections.map((s) => {
            const on = s.id === active;
            return (
              <li key={s.id}>
                <Link
                  to="/projects/$project/settings"
                  params={{ project }}
                  hash={s.id}
                  resetScroll={false}
                  aria-current={on ? "page" : undefined}
                  className={cn(
                    "flex h-8 items-center rounded-[7px] px-2.5 text-[0.875rem] transition-colors duration-[var(--dur-state)]",
                    on ? "bg-paper-press font-[550] text-ink" : "text-ink-3 hover:bg-paper-hover hover:text-ink",
                  )}
                >
                  {s.label}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
    </>
  );
}
