import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plug, Plus } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mq, type KVStats } from "@/api/modules";
import { Reading, Readings } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { Crumbs, Empty, NotOnBox, Page, PageHeader, Tabs } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { bytes, bytesParts, int } from "@/lib/format";
import { PARTS } from "@/lib/names";
import { relative } from "@/lib/time";
import { KvConnect } from "./connect";
import { KvConsole } from "./console";
import { KeyPanel } from "./key";
import { NewKeyDialog } from "./new-key";
import { typing } from "./parts";
import { KeyBrowser, type BrowserHandle, type Filters } from "./tree";
import { KvWrites } from "./write";

/**
 * KV: the project's key-value store. Keys (a browser grouped by ":" and an
 * editor per type) and Console (commands as in valkey-cli). Kept keys stay
 * until deleted; keys with an expiry are cache and go first when memory runs
 * short. Every change is a toast with Undo.
 */
export function KvPage({ project, match, k, tab = "keys", isNew }: { project: string; match?: string; k?: string; tab?: "keys" | "console"; isNew?: boolean }) {
  const name = PARTS.valkey.name;
  useTitle(`${project} · ${name}`);
  const navigate = useNavigate();
  const stats = useQuery(mq.kvStats(project));
  const [filters, setFilters] = useState<Filters>({ search: match ?? "", type: "", expiry: "" });
  const [making, setMaking] = useState(!!isNew);
  const [connect, setConnect] = useState(false);
  const [help, setHelp] = useState(false);
  const browser = useRef<BrowserHandle>(null);

  const go = useCallback(
    (o: { key?: string; match?: string }) =>
      void navigate({ to: "/projects/$project/data/kv", params: { project }, search: { match: o.match || undefined, key: o.key || undefined } }),
    [navigate, project],
  );
  const onFilters = useCallback(
    (f: Filters) => {
      setFilters(f);
      if (f.search !== (match ?? "")) go({ match: f.search, key: k });
    },
    [go, match, k],
  );
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (typing(e)) return;
      if (e.key === "/" && tab === "keys") {
        e.preventDefault();
        browser.current?.focusSearch();
      } else if (e.key === "n") {
        e.preventDefault();
        setMaking(true);
      } else if (e.key === "?") {
        e.preventDefault();
        setHelp(true);
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [tab]);

  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Key-value stores" />;
  const missing = stats.error instanceof ApiError && stats.error.status === 409;
  const s = stats.data;
  const groupOf = (key?: string) => (key && key.includes(":") ? key.slice(0, key.lastIndexOf(":") + 1) : "");

  return (
    <KvWrites project={project}>
      <Page full>
        <PageHeader
          eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project }, mono: true }]} />}
          title={name}
          lede={PARTS.valkey.sub}
          actions={
            !missing && (
              <>
                <Button onClick={() => setConnect(true)}>
                  <Plug />
                  Connect
                </Button>
                <Button variant="primary" onClick={() => setMaking(true)} title="New key (N)">
                  <Plus />
                  New key
                </Button>
              </>
            )
          }
        />
        {missing ? (
          <Empty className="mt-10" title="This project has no KV yet">
            Add <code className="font-mono text-ink">services: {"{ valkey: {} }"}</code> to tiffin.config.ts, then plan and apply.
          </Empty>
        ) : (
          <>
            {stats.isError && <ProblemNote className="mt-8" error={stats.error} />}
            {s && <Meters s={s} />}
            <Tabs
              items={[
                { to: "/projects/$project/data/kv", params: { project }, label: "Keys", exact: true },
                { to: "/projects/$project/data/kv/console", params: { project }, label: "Console" },
              ]}
            />
            <div className="pt-6">
              {tab === "console" ? (
                <KvConsole project={project} />
              ) : (
                <div className="grid gap-x-10 gap-y-6 lg:grid-cols-[minmax(17rem,22rem)_minmax(0,1fr)]">
                  <KeyBrowser
                    ref={browser}
                    selected={k}
                    onOpen={(key) => go({ key, match })}
                    filters={filters}
                    onFilters={onFilters}
                    className={k ? "h-[60vh] max-lg:hidden lg:h-[max(26rem,calc(100dvh-24rem))]" : "h-[64vh] lg:h-[max(26rem,calc(100dvh-24rem))]"}
                  />
                  <div className="min-w-0">
                    {k ? (
                      <KeyPanel
                        key={k}
                        k={k}
                        onBack={() => go({ match })}
                        onGone={() => go({ match })}
                        onRenamed={(to) => go({ key: to, match })}
                      />
                    ) : (
                      <div className="hidden h-full min-h-48 place-items-center rounded-[10px] border border-dashed border-rule-3 p-8 text-center lg:grid">
                        <p className="text-base text-ink-2">Pick a key to see and change its value.</p>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          </>
        )}
      </Page>
      <NewKeyDialog open={making} onOpenChange={setMaking} prefix={groupOf(k)} onMade={(key) => go({ key, match: undefined })} />
      <KvConnect project={project} open={connect} onOpenChange={setConnect} />
      <Shortcuts open={help} onOpenChange={setHelp} />
    </KvWrites>
  );
}

/** Memory against the cap, kept versus cache, and how it's saved to disk. */
function Meters({ s }: { s: KVStats }) {
  const cap = s.enforcedBytes || (s.maxMemoryMB ?? 0) * 1024 * 1024;
  const mem = bytesParts(s.memoryBytes);
  const keys = Math.max(1, s.keys);
  return (
    <>
      <Readings className="grid-cols-1 sm:grid-cols-3">
        <Reading label="Memory" value={mem.value} unit={cap > 0 ? `${mem.unit} of ${bytes(cap, 0)}` : mem.unit} sub={cap > 0 ? (s.approximate ? "Estimated from a sample of keys" : undefined) : "No limit set for this project"}>
          {cap > 0 && (
            <SegMeter
              className="mt-2.5"
              label="Memory used of the project's limit"
              value={s.memoryBytes}
              max={cap}
              warnAt={0.8}
              fullAt={0.95}
              valueText={`${bytes(s.memoryBytes)} of ${bytes(cap, 0)}`}
            />
          )}
        </Reading>
        <Reading label="Kept · cache" value={int(s.keptKeys)} unit={`kept · ${int(s.cacheKeys)} cache`}>
          <div
            className="mt-2.5 flex h-2 gap-0.5 overflow-hidden rounded-full bg-paper-sunk"
            role="img"
            aria-label={`${int(s.keptKeys)} keys kept until deleted, ${int(s.cacheKeys)} with an expiry`}
          >
            {s.keptKeys > 0 && <span className="h-full min-w-[3px] rounded-l-full bg-ink-3" style={{ width: `${(s.keptKeys / keys) * 100}%` }} />}
            {s.cacheKeys > 0 && <span className="h-full min-w-[3px] flex-1 rounded-r-full bg-rule-3" />}
          </div>
        </Reading>
        <Reading
          label="On disk"
          value={<span className="block pt-1 text-[0.9375rem] leading-[1.375rem] tracking-normal">{s.server.aofEnabled ? "Saved every second" : "Snapshots only"}</span>}
          sub={s.server.lastSaveAt ? `Last snapshot ${relative(s.server.lastSaveAt)}` : undefined}
        />
      </Readings>
      {s.writesRefused && (
        <p role="alert" className="mt-4 rounded-[10px] border border-warn bg-warn-wash px-4 py-2.5 text-base text-ink">
          New writes are refused: the KV is over its {bytes(cap, 0)} limit with keys that never expire. Reads and deletes still work. Delete keys or give them an
          expiry.
        </p>
      )}
      {!s.writesRefused && s.overCap && <p className="mt-4 text-sm text-warn-ink">Over its limit: keys with an expiry are being cleared first.</p>}
    </>
  );
}

const SHORTCUTS: Array<[string, string]> = [
  ["/", "Find keys"],
  ["N", "New key"],
  ["↑ ↓", "Move through keys, fields or items"],
  ["→ ←", "Open or close a group"],
  ["Enter", "Open a key, or edit the value in a cell"],
  ["Esc", "Cancel an edit"],
  ["Delete", "Delete the key, group, field or item (with Undo)"],
  ["⌘ Enter", "Save a text value"],
  ["?", "These shortcuts"],
];

function Shortcuts({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <dl className="divide-y divide-rule border-y border-rule">
            {SHORTCUTS.map(([keys, what]) => (
              <div key={keys} className="flex items-center justify-between gap-4 py-2">
                <dt className="text-base text-ink-2">{what}</dt>
                <dd className="flex shrink-0 gap-1">
                  {keys.split(" ").map((x) => (
                    <kbd key={x} className="kbd">
                      {x}
                    </kbd>
                  ))}
                </dd>
              </div>
            ))}
          </dl>
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
