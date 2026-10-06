import { Cable } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { Button } from "@/components/ui/button";
import { useCommand } from "@/lib/shortcuts";

export type ConnectPart = "database" | "kv" | "files" | "jobs";

const LazyDialog = lazy(() => import("./connect-dialog").then((m) => ({ default: m.ConnectDialog })));
const commandId: Record<ConnectPart, string> = { database: "connect-postgres", kv: "connect-valkey", files: "connect-storage", jobs: "connect-jobs" };
const words: Record<ConnectPart, string> = { database: "the database", kv: "KV", files: "files", jobs: "jobs" };

/**
 * The Connect button for a part's page header: how your apps, your computer
 * and (later) anywhere else reach this part. ⌘K's "Connect to …" opens it
 * too. The dialog's code loads on first open.
 *
 *   <PageHeader actions={<ConnectButton part="database" project={project} />} … />
 */
export function ConnectButton({ part, project }: { part: ConnectPart; project: string }) {
  const [open, setOpen] = useState(false);
  useCommand({ id: commandId[part], label: `Connect to ${words[part]}`, keywords: ["connection", "url", "env", "tunnel"], run: () => setOpen(true) });
  return (
    <>
      <Button variant="secondary" size="md" onClick={() => setOpen(true)} onMouseEnter={() => void import("./connect-dialog")} aria-haspopup="dialog">
        <Cable className="text-ink-3" />
        Connect
      </Button>
      {open && (
        <Suspense fallback={null}>
          <LazyDialog part={part} project={project} open={open} onOpenChange={setOpen} />
        </Suspense>
      )}
    </>
  );
}
