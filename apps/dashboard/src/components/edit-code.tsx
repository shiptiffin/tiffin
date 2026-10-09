import { Code } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { Button } from "@/components/ui/button";
import { useCommand } from "@/lib/shortcuts";

const LazyDialog = lazy(() => import("./edit-code-dialog").then((m) => ({ default: m.EditCodeDialog })));

/**
 * Edit code: the project header's way to its code, like Connect is a part's
 * way to its data. The dialog has the steps for each app (a starter's: pull,
 * change, deploy; GitHub's: clone and push) and copies them as text for a
 * coding agent. ⌘K's "Edit code" opens it too. Its code loads on first open.
 */
export function EditCodeButton({ project, apps, app, size = "lg", command = true }: { project: string; apps: string[]; app?: string; size?: "md" | "lg"; command?: boolean }) {
  const [open, setOpen] = useState(false);
  useCommand(command ? { id: "edit-code", label: "Edit code", keywords: ["source", "pull", "cli", "clone", "agent"], run: () => setOpen(true) } : null);
  return (
    <>
      <Button variant="secondary" size={size} onClick={() => setOpen(true)} onMouseEnter={() => void import("./edit-code-dialog")} aria-haspopup="dialog">
        <Code className="text-ink-3" />
        Edit code
      </Button>
      {open && (
        <Suspense fallback={null}>
          <LazyDialog project={project} apps={apps} initial={app} open={open} onOpenChange={setOpen} />
        </Suspense>
      )}
    </>
  );
}
