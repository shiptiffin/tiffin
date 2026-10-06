import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Eye } from "lucide-react";
import { Tabs as T } from "radix-ui";
import { useState, type ReactNode } from "react";
import { mod, mq, type KVConnection, type StorageInfo } from "@/api/modules";
import { Command, CopyButton } from "@/components/copy";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { PARTS } from "@/lib/names";
import type { ConnectPart } from "./connect";

type Env = { name: string; what: string; value?: string; secret?: boolean };
type Snippet = { label: string; code: string };
type Spec = {
  title: string;
  sub: string;
  env: Env[];
  /** Fills in the real values (they carry passwords): needs full access to the project. */
  reveal?: () => Promise<Record<string, string>>;
  snippets: Snippet[];
  computer: ReactNode;
  anywhere: ReactNode;
};

const DOTS = "••••••••";
/** p_my_shop: the project's database role, KV user and key prefix (datakit.Ident). */
const ident = (p: string) => `p_${p.replace(/-/g, "_")}`;
const envName = (b: string) => b.toUpperCase().replace(/-/g, "_");

const kvWhat: Record<string, string> = {
  REDIS_URL: "The connection, password included",
  VALKEY_URL: "The same, for Valkey clients",
  VALKEY_PREFIX: "Every key starts with this; the project can’t reach other keys",
  UPSTASH_REDIS_REST_URL: "For @upstash/redis and @vercel/kv; keys need no prefix",
  UPSTASH_REDIS_REST_TOKEN: "Its token",
  KV_REST_API_URL: "The same, by Vercel KV’s names",
  KV_REST_API_TOKEN: "Its token",
  KV_REST_API_READ_ONLY_TOKEN: "A token that can only read",
};

function spec(part: ConnectPart, project: string, st?: StorageInfo, kv?: KVConnection): Spec {
  const buckets = (st?.buckets ?? []).map((b) => b.name);
  const id = ident(project);
  const origin = typeof location === "undefined" ? "" : location.origin;
  switch (part) {
    case "database":
      return {
        title: "Connect to the database",
        sub: PARTS.postgres.sub,
        env: [
          { name: "DATABASE_URL", what: "The whole connection, password included", value: `postgresql://${id}:${DOTS}@127.0.0.1:5432/${id}?sslmode=disable`, secret: true },
          { name: "DIRECT_DATABASE_URL", what: "The same, for migration tools (Prisma’s directUrl, drizzle-kit)", secret: true },
          { name: "DATABASE_POOL_MAX", what: "How many connections each copy of an app should open", value: "set per app" },
          { name: "PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE", what: "The same, split up, for psql and other libpq tools" },
        ],
        reveal: async () => {
          const c = await mod.pgConnection(project);
          const pw = decodeURIComponent(new URL(c.databaseUrl).password);
          return { DATABASE_URL: c.databaseUrl, DIRECT_DATABASE_URL: c.databaseUrl, PGHOST: "127.0.0.1", PGPORT: "5432", PGUSER: id, PGPASSWORD: pw, PGDATABASE: id };
        },
        snippets: [
          {
            label: "Drizzle",
            code: `import { drizzle } from "drizzle-orm/postgres-js";
import postgres from "postgres";

const client = postgres(process.env.DATABASE_URL!, { max: Number(process.env.DATABASE_POOL_MAX) || 10 });
export const db = drizzle(client);`,
          },
          {
            label: "Prisma",
            code: `// prisma.config.ts: migrations use the direct URL
import { defineConfig, env } from "prisma/config";
export default defineConfig({ schema: "prisma/schema.prisma", datasource: { url: env("DIRECT_DATABASE_URL") } });

// db.ts
import { PrismaPg } from "@prisma/adapter-pg";
import { PrismaClient } from "./generated/prisma/client";
const adapter = new PrismaPg({ connectionString: process.env.DATABASE_URL, max: Number(process.env.DATABASE_POOL_MAX) || 10 });
export const prisma = new PrismaClient({ adapter });`,
          },
          {
            label: "postgres.js",
            code: `import postgres from "postgres";

export const sql = postgres(process.env.DATABASE_URL!, { max: Number(process.env.DATABASE_POOL_MAX) || 10 });
const books = await sql\`select * from books limit 10\`;`,
          },
          {
            label: "Bun.sql",
            code: `import { SQL } from "bun";

// Bun reads DATABASE_URL by itself.
export const sql = new SQL({ max: Number(process.env.DATABASE_POOL_MAX) || 10 });
const books = await sql\`select * from books limit 10\`;`,
          },
          {
            label: "Python",
            code: `import os
import psycopg

with psycopg.connect(os.environ["DATABASE_URL"]) as conn:
    rows = conn.execute("select * from books limit 10").fetchall()`,
          },
        ],
        computer: (
          <Tunnel
            cmd={`tiffin db tunnel ${project}`}
            port={15432}
            tools="TablePlus, psql or an app running on your computer"
            extra={
              <>
                Add <code className="ident text-ink-2">--branch pr-12</code> to reach a copy instead, or <code className="ident text-ink-2">--port</code> to use another local port.
              </>
            }
          />
        ),
        anywhere: <NotYet what="database" />,
      };
    case "kv":
      return {
        title: "Connect to KV",
        sub: PARTS.valkey.sub,
        // What the box sets, from kv-connection (read access; secrets only with reveal).
        env: (kv?.env ?? [{ name: "REDIS_URL", secret: true }, { name: "VALKEY_PREFIX", value: `${id}:`, secret: false }]).map((e) => ({
          name: e.name,
          what: kvWhat[e.name] ?? "",
          value: e.value || (e.secret ? undefined : ""),
          secret: e.secret,
        })),
        reveal: async () => Object.fromEntries(((await mod.kvConnection(project, true)).env ?? []).map((e) => [e.name, e.value ?? ""])),
        snippets: [
          {
            label: "ioredis",
            code: `import Redis from "ioredis";

export const redis = new Redis(process.env.REDIS_URL!, { keyPrefix: process.env.VALKEY_PREFIX });
await redis.set("session:42", JSON.stringify({ user: 42 }), "EX", 3600); // with an expiry: cache`,
          },
          {
            label: "Bun.redis",
            code: `import { redis } from "bun"; // reads REDIS_URL

const key = (k: string) => process.env.VALKEY_PREFIX + k;
await redis.set(key("flags"), JSON.stringify({ beta: true })); // no expiry: kept
const flags = await redis.get(key("flags"));`,
          },
          {
            label: "@upstash/redis",
            code: `import { Redis } from "@upstash/redis";

// Reads UPSTASH_REDIS_REST_URL and _TOKEN; the box adds the prefix for you.
export const redis = Redis.fromEnv();
await redis.set("flags", { beta: true });`,
          },
        ],
        computer: (
          <Tunnel
            cmd={`tiffin kv tunnel ${project}`}
            port={16379}
            tools="redis-cli, a KV browser or an app running on your computer"
            extra={
              <>
                Keys start with <code className="ident text-ink-2">{id}:</code>, as they do for your apps.
              </>
            }
          />
        ),
        anywhere: <NotYet what="KV store" />,
      };
    case "files": {
      const b = buckets[0] ?? "uploads";
      return {
        title: "Connect to files",
        sub: PARTS.storage.sub,
        env: [
          { name: "S3_ENDPOINT, S3_REGION", what: "Where the buckets are on the box (AWS_ENDPOINT_URL, AWS_REGION too)", value: `${st?.internalEndpoint ?? "…"}, us-east-1` },
          { name: "S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY", what: "The project’s own key (AWS_* twins too)", secret: true },
          ...(buckets.length === 1
            ? [{ name: "S3_BUCKET", what: "The bucket, as Bun.s3’s default", value: `${project}-${b}` }]
            : buckets.map((x) => ({ name: `S3_BUCKET_${envName(x)}`, what: `The ${x} bucket’s S3 name`, value: `${project}-${x}` }))),
          { name: "S3_PUBLIC_ENDPOINT", what: "The address browsers and tools off the box use, for signed links", value: st?.endpoint },
        ],
        reveal: () => mod.credentials(project),
        snippets: [
          {
            label: "Bun.s3",
            code:
              buckets.length === 1
                ? `import { s3 } from "bun"; // reads S3_* (and S3_BUCKET)

await s3.write("hello.txt", "Hello from ${project}");
const url = s3.presign("hello.txt", { expiresIn: 3600 });`
                : `import { S3Client } from "bun"; // reads S3_*

const files = new S3Client({ bucket: process.env.S3_BUCKET_${envName(b)} });
await files.write("hello.txt", "Hello from ${project}");`,
          },
          {
            label: "AWS SDK",
            code: `import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";

// Endpoint, region and key come from AWS_*.
const s3 = new S3Client({ forcePathStyle: true });
await s3.send(new PutObjectCommand({ Bucket: process.env.${buckets.length === 1 ? "S3_BUCKET" : `S3_BUCKET_${envName(b)}`}, Key: "hello.txt", Body: "Hello" }));`,
          },
          {
            label: "tiffin-sdk",
            code: `import { signedUrl, upload } from "tiffin-sdk/storage";

await upload("${b}", "avatars/ada.png", photo, { contentType: "image/png" });
const src = signedUrl("${b}", "avatars/ada.png", { width: 256 }); // resized, works for an hour`,
          },
        ],
        computer: (
          <Section>
            <p>The buckets take signed requests at the box’s S3 address. Print the key and that address for the AWS CLI, Cyberduck or a local app:</p>
            <Command className="mt-3" cmd={`tiffin storage credentials ${project}`} />
            <Needs project={project} />
          </Section>
        ),
        anywhere: (
          <Section>
            <p>
              Already: the S3 address answers anywhere, with the project’s key. To hand one file to someone, make a link that expires (on a file’s page, or{" "}
              <code className="ident text-ink-2">tiffin storage presign</code>). Public buckets are readable by address with no key.
            </p>
          </Section>
        ),
      };
    }
    case "jobs":
      return {
        title: "Connect to jobs",
        sub: PARTS.jobs.sub,
        env: [
          { name: "TIFFIN_QUEUE_URL", what: "Where apps send jobs on the box" },
          { name: "TIFFIN_QUEUE_KEY", what: "Sends as this app", secret: true },
          { name: "TIFFIN_QUEUE_SIGNING_SECRET", what: "Checks that a delivery came from the box", secret: true },
        ],
        snippets: [
          {
            label: "Send",
            code: `import { queue } from "tiffin-sdk/queue";

await queue.send("emails", { to: "sam@example.com" }, { delay: "10m" });`,
          },
          {
            label: "Receive",
            code: `// The box POSTs each job to /queues/<name> on your app, signed, and retries until it answers 2xx.
import { defineHandler } from "tiffin-sdk/queue";

export const POST = defineHandler(async (job) => {
  await sendWelcome(job.payload.to);
});`,
          },
          {
            label: "A URL outside the box",
            code: `// Schedules and queues can call any web address, signed the same way (tiffin queue signing-secret ${project}).
import { verifyRequest } from "tiffin-sdk/verify";

export async function POST(req: Request) {
  const call = await verifyRequest(req, process.env.TIFFIN_SIGNING_SECRET!);
  if (!call) return new Response("bad signature", { status: 401 });
  return new Response(null, { status: 204 }); // 2xx: done; anything else retries
}`,
          },
          {
            label: "Verify by hand",
            code: `import { createHmac, timingSafeEqual } from "node:crypto";

const body = await req.text();
const sig = Object.fromEntries((req.headers.get("tiffin-signature") ?? "").split(",").map((p) => p.split("=")));
const want = createHmac("sha256", process.env.TIFFIN_QUEUE_SIGNING_SECRET!).update(\`\${sig.t}.\${body}\`).digest("hex");
const fresh = Math.abs(Date.now() / 1000 - Number(sig.t)) < 300;
if (!fresh || sig.v1?.length !== want.length || !timingSafeEqual(Buffer.from(sig.v1), Buffer.from(want))) {
  return new Response("bad signature", { status: 401 });
}`,
          },
        ],
        computer: (
          <Section>
            <p>Send a job from your terminal; History records who sent it:</p>
            <Command className="mt-3" wrap cmd={`tiffin queue send ${project} --name emails --payload '{"to":"sam@example.com"}'`} />
          </Section>
        ),
        anywhere: (
          <Section>
            <p>Already: any program with an API key that can change {project} sends jobs through the box’s API.</p>
            <Command
              className="mt-3"
              wrap
              cmd={`curl -X POST ${origin}/v1/projects/${project}/queue/send -H "Authorization: Bearer $TIFFIN_TOKEN" -H "Content-Type: application/json" -d '{"name":"emails","payload":{"to":"sam@example.com"}}'`}
            />
            <p className="mt-3">
              <Link to="/settings/keys" search={{ create: true }} className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                Make a key for it
              </Link>{" "}
              that reaches only {project}.
            </p>
          </Section>
        ),
      };
  }
}

/**
 * How to reach one part of a project: from its apps on the box (the env vars
 * the box sets, values hidden until you ask, and code for common clients),
 * from your computer (a tunnel) and from anywhere (what exists today).
 */
export function ConnectDialog({ part, project, open, onOpenChange }: { part: ConnectPart; project: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const storage = useQuery({ ...mq.storage(project), enabled: part === "files" });
  const kv = useQuery({ queryKey: ["kv-connection", project, false], queryFn: () => mod.kvConnection(project), enabled: part === "kv", staleTime: 60_000 });
  const s = spec(part, project, storage.data, kv.data);
  const [tab, setTab] = useState("apps");
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[44rem]">
        <DialogHeader className="pb-3">
          <DialogTitle>{s.title}</DialogTitle>
          <DialogDescription>
            <span className="ident text-ink-2">{project}</span>
            <span className="mx-1.5 text-ink-4" aria-hidden>
              ·
            </span>
            {s.sub}
          </DialogDescription>
        </DialogHeader>
        <T.Root value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col">
          <T.List aria-label="Connect from" className="flex gap-1 overflow-x-auto border-b border-rule px-6 [scrollbar-width:none]">
            {[
              ["apps", "From your apps", "Your apps"],
              ["computer", "From your computer", "Your computer"],
              ["anywhere", "From anywhere", "Anywhere"],
            ].map(([v, label, short]) => (
              <T.Trigger
                key={v}
                value={v}
                className="relative -mb-px flex h-10 shrink-0 items-center px-2.5 text-[0.875rem] text-ink-3 transition-colors first:pl-0 hover:text-ink data-[state=active]:font-[550] data-[state=active]:text-ink after:absolute after:inset-x-2.5 after:-bottom-px after:h-[2px] after:rounded-full first:after:left-0 data-[state=active]:after:bg-ink"
                aria-label={label}
              >
                <span className="max-sm:hidden">{label}</span>
                <span className="sm:hidden">{short}</span>
              </T.Trigger>
            ))}
          </T.List>
          <DialogBody className="min-h-[min(26rem,60dvh)] pt-5">
            <T.Content value="apps" className="outline-none">
              <FromApps spec={s} project={project} />
            </T.Content>
            <T.Content value="computer" className="outline-none">
              {s.computer}
            </T.Content>
            <T.Content value="anywhere" className="outline-none">
              {s.anywhere}
            </T.Content>
          </DialogBody>
        </T.Root>
      </DialogContent>
    </Dialog>
  );
}

function FromApps({ spec: s, project }: { spec: Spec; project: string }) {
  const [shown, setShown] = useState<Record<string, string> | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [lang, setLang] = useState(s.snippets[0].label);
  const snippet = s.snippets.find((x) => x.label === lang) ?? s.snippets[0];
  return (
    <div className="text-[0.875rem] text-ink-2">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <p>Every app in {project} already has these, so there is nothing to set.</p>
        {s.reveal && !shown && (
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setErr(null);
              try {
                setShown(await s.reveal!());
              } catch (e) {
                setErr(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            <Eye />
            {busy ? "Showing…" : "Show values"}
          </Button>
        )}
      </div>
      {err ? <ProblemNote className="mt-3" error={err} title="The values can’t be shown." /> : null}
      <dl className="mt-3 divide-y divide-rule border-y border-rule" aria-label="Environment variables">
        {s.env.map((e) => {
          // A row may list several names ("PGHOST, PGPORT…"): shown once all of them are.
          const names = e.name.split(", ");
          const got = shown && names.every((n) => shown[n] !== undefined) ? names.map((n) => shown[n]).join(", ") : undefined;
          const value = got ?? (e.secret && !e.value ? DOTS : e.value);
          const real = got !== undefined;
          return (
            <div key={e.name} className="grid gap-x-4 gap-y-0.5 py-2.5 sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)]">
              <dt className="min-w-0">
                <code className="ident block text-[0.78125rem] break-words text-ink">{e.name}</code>
                <span className="block text-xs text-ink-3">{e.what}</span>
              </dt>
              <dd className="flex min-w-0 items-start gap-1">
                {value && (
                  <code className={cn("ident min-w-0 flex-1 pt-0.5 text-[0.75rem] break-all", real ? "text-ink" : "text-ink-3")} aria-label={value.includes(DOTS) ? `${e.name}, hidden` : undefined}>
                    {value}
                  </code>
                )}
                {real && names.length === 1 && <CopyButton value={got} label={`Copy ${e.name}`} className="size-6" />}
              </dd>
            </div>
          );
        })}
      </dl>
      {shown && <p className="mt-2 text-xs text-ink-3">These carry passwords: keep them out of shared screens, chats and committed files. Each time they’re shown is recorded.</p>}

      <T.Root value={snippet.label} onValueChange={setLang} className="mt-6 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk">
        <div className="flex items-center gap-2 border-b border-rule pr-1.5">
          <T.List aria-label="Code for" className="flex min-w-0 flex-1 gap-0.5 overflow-x-auto px-1.5 py-1.5 [scrollbar-width:none]">
            {s.snippets.map((x) => (
              <T.Trigger
                key={x.label}
                value={x.label}
                className="h-7 shrink-0 rounded-[6px] px-2.5 text-[0.8125rem] whitespace-nowrap text-ink-3 transition-colors hover:text-ink data-[state=active]:bg-paper-raised data-[state=active]:font-[550] data-[state=active]:text-ink data-[state=active]:shadow-[var(--top-light)]"
              >
                {x.label}
              </T.Trigger>
            ))}
          </T.List>
          <CopyButton value={snippet.code} label={`Copy the ${snippet.label} code`} />
        </div>
        {s.snippets.map((x) => (
          <T.Content key={x.label} value={x.label} className="outline-none">
            <pre className="max-h-72 overflow-auto px-4 py-3 font-mono text-[0.75rem] leading-5 text-ink" tabIndex={0}>
              {x.code}
            </pre>
          </T.Content>
        ))}
      </T.Root>
    </div>
  );
}

function Section({ children }: { children: ReactNode }) {
  return <div className="max-w-[38rem] text-[0.875rem] leading-[1.375rem] text-ink-2">{children}</div>;
}

function Tunnel({ cmd, port, tools, extra }: { cmd: string; port: number; tools: string; extra?: ReactNode }) {
  const project = cmd.split(" ").pop()!;
  return (
    <Section>
      <p>
        Run this on the computer you set the box up from. It opens <code className="ident text-ink-2">localhost:{port}</code> over SSH and prints a URL for {tools}. It stays open until you press Ctrl-C.
      </p>
      <Command className="mt-3" cmd={cmd} />
      <p className="mt-3 text-[0.8125rem] text-ink-3">{extra}</p>
      <Needs project={project} />
    </Section>
  );
}

function Needs({ project }: { project: string }) {
  return <p className="mt-2 text-[0.8125rem] text-ink-3">The URL carries the password, so it needs full access to {project}. Each time it’s shown is recorded.</p>;
}

function NotYet({ what }: { what: string }) {
  return (
    <Section>
      <p className="font-[550] text-ink">Not yet.</p>
      <p className="mt-1">
        The {what} answers only inside the box, which keeps it private by default. Use the tunnel from your computer meanwhile. A public address you turn on per project, with its own
        password and a list of allowed IP addresses, comes later.
      </p>
    </Section>
  );
}
