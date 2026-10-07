import { Code2 } from "lucide-react";
import { useState } from "react";
import type { AnalyticsSetup } from "@/api/modules";
import { CodeBox } from "@/components/analytics-kit";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";

/**
 * How visits get here, from the box's own setup read: page views need
 * nothing; a Next.js app adds the script (for navigations in the browser and
 * events) and <WebVitals /> in its root layout; track() on the server. The
 * snippets follow docs/guide/analytics.md and the SDK's exports.
 */

type Kind = "next" | "other";

function nextLayout(s: AnalyticsSetup) {
  const imp = s.vitals.split(";")[0]?.trim() || `import { WebVitals } from "@shiptiffin/sdk/next/vitals"`;
  return `${imp};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <head>
        ${s.snippet}
      </head>
      <body>
        <WebVitals />
        {children}
      </body>
    </html>
  );
}`;
}

const serverTrack = (s: AnalyticsSetup) => s.track.replace("; ", ";\n\n// in a route handler or server action, with the incoming request\n");

function Step({ n, title, children, last }: { n: number; title: string; children: React.ReactNode; last?: boolean }) {
  return (
    <li className="relative grid grid-cols-[1.5rem_minmax(0,1fr)] gap-x-3">
      {!last && <span aria-hidden className="absolute top-7 bottom-0 left-[0.71875rem] w-px bg-rule-2" />}
      <span aria-hidden className="grid size-6 place-items-center rounded-full border border-rule-2 bg-paper-raised text-[0.75rem] font-[550] text-ink-2 tnum">
        {n}
      </span>
      <div className={cn("min-w-0", !last && "pb-6")}>
        <p className="pt-0.5 text-[0.875rem] font-[550] text-ink">{title}</p>
        <div className="mt-1 text-[0.84375rem] text-ink-2">{children}</div>
      </div>
    </li>
  );
}

/** The steps, for a Next.js app or any other site. */
export function SetupGuide({ setup, className }: { setup: AnalyticsSetup; className?: string }) {
  const [kind, setKind] = useState<Kind>("next");
  const hosts = setup.hosts ?? [];
  return (
    <div className={className}>
      <Segmented
        label="Kind of app"
        value={kind}
        onChange={setKind}
        options={[
          { value: "next", label: "Next.js" },
          { value: "other", label: "Any other site" },
        ]}
      />
      <ol className="mt-5">
        <Step n={1} title="Page views: nothing to add">
          <p>The box counts every page people open on your apps’ addresses, at its own edge, so ad blockers can’t hide them. They show here within seconds.</p>
          {hosts.length > 0 && (
            <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
              {hosts.map((h) => (
                <li key={h}>
                  <a href={`https://${h}`} target="_blank" rel="noopener noreferrer" className="font-mono text-[0.78rem] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                    {h}
                  </a>
                </li>
              ))}
            </ul>
          )}
        </Step>
        {kind === "next" ? (
          <Step n={2} title="Add the script and Web Vitals to the root layout">
            <p>Next.js moves between pages in the browser, so the edge only sees the first page of a visit. The 1.3 KB script counts the rest, plus outbound links, downloads and your events. {"<WebVitals />"} reports page speed.</p>
            <CodeBox className="mt-3" name="app/layout.tsx" code={nextLayout(setup)} />
          </Step>
        ) : (
          <Step n={2} title="Optional: add the script">
            <p>For single-page apps, outbound links, downloads and your own events. It uses no cookies and no storage. For speed readings, call reportWebVitals() from @shiptiffin/sdk/vitals in browser code.</p>
            <CodeBox className="mt-3" name="index.html" code={setup.snippet} />
          </Step>
        )}
        <Step n={3} title="Track your own events" last>
          <p>Sign-ups, checkouts, anything you want to count. They show under Events with their properties.</p>
          <CodeBox className="mt-3" name="in the browser" code={setup.browser} />
          <CodeBox className="mt-2" name="on the server" code={serverTrack(setup)} />
        </Step>
      </ol>
    </div>
  );
}

/** "Setup" in the header: the guide and what is stored. */
export function SetupDialog({ setup, open, onOpenChange }: { setup: AnalyticsSetup; open?: boolean; onOpenChange?: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="md" variant="secondary">
          <Code2 aria-hidden className="size-4" />
          Setup
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-[46rem]">
        <DialogHeader className="pb-2">
          <DialogTitle>Add analytics to your apps</DialogTitle>
          <DialogDescription className="text-[0.875rem] text-ink-3">Cookieless and kept on this box. No consent banner is needed for it.</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <SetupGuide setup={setup} className="pt-2" />
          <div className="mt-6 border-t border-rule pt-5">
            <h3 className="text-[0.84375rem] font-[550] text-ink">What’s stored, and what isn’t</h3>
            <p className="mt-1.5 text-[0.8125rem] leading-5 text-ink-2">{setup.privacy}</p>
            <p className="mt-3 text-[0.75rem] text-ink-3">
              Countries come from{" "}
              <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="text-brass-ink underline decoration-brass-ink/40 underline-offset-4 hover:decoration-brass-ink">
                IP Geolocation by DB-IP
              </a>{" "}
              (CC BY 4.0). Days are UTC.
            </p>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
