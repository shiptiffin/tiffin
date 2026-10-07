import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import type { Manifest } from "@/api/client";
import { InfoTip } from "@/components/info-tip";
import { cn } from "@/lib/cn";
import { count } from "@/lib/format";
import { PARTS } from "@/lib/names";
import type { ProjectPage } from "@/lib/sections";

/** value: the literal value, shown in mono; say: what it holds, in words. Neither: hidden. */
type Var = { name: string; value?: string; say?: string };
/** from: where these come from, in a sentence. */
type Group = { id: string; label: string; sub: string; from: string; to?: ProjectPage; vars: Var[] };

const DOTS = "••••••••";
const envName = (b: string) => b.toUpperCase().replace(/-/g, "_");

/**
 * What the box gives every app, by part. The dashboard has no endpoint that
 * lists an app's environment, so this follows the parts the project has
 * (each module's Env in the Go code); the values stay hidden here, and each
 * part's Connect shows them to someone with full access.
 */
export function boxGroups(project: string, man?: Manifest): Group[] {
  const svc = (man?.services ?? {}) as Record<string, { buckets?: Record<string, unknown> } | undefined>;
  const next = Object.values(man?.apps ?? {}).some((a) => a.framework === "next");
  const id = `p_${project.replace(/-/g, "_")}`;
  const g: Group[] = [
    {
      id: "every",
      from: "The box sets these for every app, so it knows which project and app it is and where it answers.",
      label: "Every app",
      sub: "Who and where it is",
      vars: [
        { name: "TIFFIN_PROJECT", value: project },
        { name: "TIFFIN_APP", say: "The app’s name" },
        { name: "TIFFIN_URL", say: "The app’s address" },
        { name: "TIFFIN_DOMAIN", say: "The box’s apps domain" },
        { name: "PORT", say: "Chosen by the box" },
        ...(next ? [{ name: "NEXT_PUBLIC_TIFFIN_URL", say: "The app’s address (Next.js)" }] : []),
      ],
    },
    {
      id: "health",
      from: "Every app reports errors and traces to Health on the box. The key in them only sends; it can’t read anything.",
      label: PARTS.health.name,
      sub: "Errors and traces",
      to: "/projects/$project/observability",
      vars: [
        { name: "SENTRY_DSN" },
        { name: "TIFFIN_PUBLIC_SENTRY_DSN" },
        ...(next ? [{ name: "NEXT_PUBLIC_SENTRY_DSN" }] : []),
        { name: "SENTRY_ENVIRONMENT", value: "production" },
        { name: "OTEL_EXPORTER_OTLP_ENDPOINT" },
        { name: "OTEL_EXPORTER_OTLP_HEADERS" },
        { name: "OTEL_EXPORTER_OTLP_PROTOCOL", value: "http/protobuf" },
        { name: "OTEL_TRACES_EXPORTER", value: "otlp" },
        { name: "OTEL_SERVICE_NAME", say: "The app’s name" },
        { name: "OTEL_RESOURCE_ATTRIBUTES" },
        { name: "TIFFIN_OTLP_PUBLIC_ENDPOINT" },
      ],
    },
    {
      id: "jobs",
      from: "Every app can send background jobs. Each app gets its own key, so History and Jobs show which app sent what.",
      label: PARTS.jobs.name,
      sub: "Send background work",
      to: "/projects/$project/jobs",
      vars: [{ name: "TIFFIN_QUEUE_URL" }, { name: "TIFFIN_QUEUE_KEY" }, { name: "TIFFIN_QUEUE_SIGNING_SECRET" }],
    },
  ];
  if ("postgres" in svc)
    g.push({
      id: "postgres",
      from: `Because ${project} has a database. The box made its own user and password, which reach only this project’s database.`,
      label: PARTS.postgres.name,
      sub: PARTS.postgres.sub,
      to: "/projects/$project/data",
      vars: [
        { name: "DATABASE_URL" },
        { name: "DIRECT_DATABASE_URL" },
        { name: "DATABASE_POOL_MAX", say: "Set per app" },
        { name: "PGHOST" },
        { name: "PGPORT" },
        { name: "PGUSER", value: id },
        { name: "PGPASSWORD" },
        { name: "PGDATABASE", value: id },
      ],
    });
  if ("valkey" in svc)
    g.push({
      id: "valkey",
      from: `Because ${project} has KV. Its own user can only touch keys that start with ${id}:.`,
      label: PARTS.valkey.name,
      sub: "Redis-compatible",
      to: "/projects/$project/data/kv",
      vars: [
        { name: "REDIS_URL" },
        { name: "VALKEY_URL" },
        { name: "VALKEY_PREFIX", value: `${id}:` },
        { name: "UPSTASH_REDIS_REST_URL" },
        { name: "UPSTASH_REDIS_REST_TOKEN" },
        { name: "KV_REST_API_URL" },
        { name: "KV_REST_API_TOKEN" },
        { name: "KV_REST_API_READ_ONLY_TOKEN" },
      ],
    });
  if ("storage" in svc) {
    const buckets = Object.keys(svc.storage?.buckets ?? {});
    g.push({
      id: "storage",
      from: `Because ${project} has files. Its own access key reaches only its buckets.`,
      label: PARTS.storage.name,
      sub: PARTS.storage.sub,
      to: "/projects/$project/storage",
      vars: [
        { name: "S3_ENDPOINT" },
        { name: "S3_PUBLIC_ENDPOINT" },
        { name: "S3_REGION", value: "us-east-1" },
        { name: "S3_ACCESS_KEY_ID" },
        { name: "S3_SECRET_ACCESS_KEY" },
        ...(buckets.length === 1 ? [{ name: "S3_BUCKET", value: `${project}-${buckets[0]}` }] : buckets.map((b) => ({ name: `S3_BUCKET_${envName(b)}`, value: `${project}-${b}` }))),
        { name: "AWS_ENDPOINT_URL" },
        { name: "AWS_ACCESS_KEY_ID" },
        { name: "AWS_SECRET_ACCESS_KEY" },
        { name: "AWS_REGION", value: "us-east-1" },
        { name: "TIFFIN_FILES_URL" },
      ],
    });
  }
  if ("email" in svc)
    g.push({
      id: "email",
      from: `Because ${project} has email. Its own login sends through the box.`,
      label: PARTS.email.name,
      sub: "SMTP",
      to: "/projects/$project/email",
      vars: [{ name: "SMTP_URL" }, { name: "SMTP_HOST" }, { name: "SMTP_PORT" }, { name: "SMTP_USER" }, { name: "SMTP_PASSWORD" }, { name: "SMTP_SECURE" }, { name: "EMAIL_FROM" }],
    });
  if ("auth" in svc)
    g.push({
      id: "auth",
      from: `Because ${project} has auth: where sign-ins go and how apps check a session.`,
      label: PARTS.auth.name,
      sub: "Sign-in for your users",
      to: "/projects/$project/users",
      vars: [{ name: "TIFFIN_AUTH_URL" }, { name: "TIFFIN_AUTH_INTERNAL_URL" }, { name: "TIFFIN_AUTH_JWKS_URL" }, { name: "TIFFIN_AUTH_HOST" }],
    });
  if ("analytics" in svc)
    g.push({
      id: "analytics",
      from: `Because ${project} has analytics: where apps send page views.`,
      label: PARTS.analytics.name,
      sub: PARTS.analytics.sub,
      to: "/projects/$project/analytics",
      vars: [{ name: "TIFFIN_ANALYTICS_URL" }, { name: "TIFFIN_ANALYTICS_KEY" }, { name: "TIFFIN_ANALYTICS_SCRIPT" }],
    });
  return g;
}

/** "Set by Tiffin": read-only, one folded row per part; open one for its names. */
export function SetByTiffin({ project, manifest, own }: { project: string; manifest?: Manifest; own: Set<string> }) {
  const groups = boxGroups(project, manifest);
  return (
    <section aria-labelledby="set-by-tiffin" className="mt-14">
      <div className="flex items-center gap-1">
        <h2 id="set-by-tiffin" className="text-[0.9375rem] font-[550] text-ink">
          Set by Tiffin
        </h2>
        <InfoTip label="About the box’s variables">
          The box gives your apps these for the parts {project} has, so you never paste a database password. Values stay hidden here: open a part and use Connect to see them. A variable of yours with the same name replaces the box’s.
        </InfoTip>
      </div>
      <p className="mt-0.5 mb-3 text-[0.8125rem] text-ink-3">Every app gets these. You can’t edit them here.</p>
      <ul className="divide-y divide-rule border-y border-rule">
        {groups.map((gr) => {
          const replaced = gr.vars.filter((v) => own.has(v.name)).map((v) => v.name);
          return (
            <li key={gr.id}>
              <details className="group">
                <summary className="grid cursor-pointer list-none grid-cols-[1rem_minmax(0,1fr)_auto] items-baseline gap-x-2 py-3 select-none [&::-webkit-details-marker]:hidden sm:grid-cols-[1rem_minmax(0,13rem)_minmax(0,1fr)_auto]">
                  <ChevronRight className="size-3.5 translate-y-0.5 text-ink-3 transition-transform group-open:rotate-90" aria-hidden />
                  <span className="min-w-0">
                    <span className="text-[0.875rem] font-[550] text-ink">{gr.label}</span>
                    <span className="ml-2 text-xs text-ink-3">{gr.sub}</span>
                  </span>
                  <span className="ident truncate text-[0.75rem] text-ink-3 max-sm:hidden">{gr.vars.map((v) => v.name).join(", ")}</span>
                  <span className="text-right text-xs text-ink-3 tnum">{replaced.length ? <span className="text-ink-2">{replaced.length} replaced by yours</span> : count(gr.vars.length, "variable")}</span>
                </summary>
                <div className="pb-3 pl-6">
                  <p className="mb-2 max-w-[44rem] text-[0.8125rem] text-ink-2">{gr.from}</p>
                  <dl className="grid grid-cols-[minmax(0,1fr)] gap-x-6 gap-y-1 sm:grid-cols-[minmax(0,17rem)_minmax(0,1fr)]">
                    {gr.vars.map((v) => (
                      <div key={v.name} className="contents">
                        <dt className={cn("ident truncate text-[0.75rem] leading-6", own.has(v.name) ? "text-ink-3 line-through decoration-ink-4" : "text-ink")}>{v.name}</dt>
                        <dd className="truncate text-[0.8125rem] leading-6 text-ink-3 max-sm:mb-1.5">
                          {own.has(v.name) ? "Replaced by your variable above" : v.value ? <span className="ident text-[0.75rem]">{v.value}</span> : v.say ? v.say : <span aria-label="Hidden">{DOTS}</span>}
                        </dd>
                      </div>
                    ))}
                  </dl>
                  {gr.to && (
                    <Link to={gr.to} params={{ project }} className="mt-2 inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 hover:text-ink">
                      Open {gr.label}
                      <ChevronRight className="size-3.5 text-ink-4" />
                    </Link>
                  )}
                </div>
              </details>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
