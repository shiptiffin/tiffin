import { PartGate } from "@/components/part-gate";
import { createRootRouteWithContext, createRoute, createRouter, Link, Outlet, redirect, type ErrorComponentProps } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import type { Tier } from "@/api/client";
import emptyTin from "@/assets/illustrations/empty-inbox.webp";
import { Mascot } from "@/components/mascot";
import { Shell } from "@/components/shell";
import { Button } from "@/components/ui/button";
import type { ActivitySearch } from "@/routes/activity";
import type { GitSearch } from "@/routes/git-settings";
import type { NewSearch } from "@/routes/new";
import { jobsSearch, type JobsSearch } from "@/lib/jobs-search";
import { observeTab, type ObserveSearch } from "@/components/observe-search";
import { analyticsSearch, type AnalyticsSearch } from "@/routes/analytics-search";
import { HomePage } from "@/routes/home";
import { Page } from "@/components/page";
import { q } from "@/api/queries";
import { lazy, Suspense, type ComponentType, type ReactElement } from "react";
import { Skeleton } from "@/components/page";

// Every page but Projects (home) loads on demand, so the first paint ships
// only the shell and the home page. Hovering a link (intent) runs its route's
// loader, which fetches the page's code and warms its data, so the click is instant.
type Lazy<P> = ((props: P) => ReactElement) & { preload: () => Promise<void> };
function lz<P extends object = Record<string, never>>(load: () => Promise<Record<string, unknown>>, name: string): Lazy<P> {
  const L = lazy(() => load().then((m) => ({ default: m[name] as ComponentType<P> })));
  const Comp = function Lazy(props: P) {
    return (
      <Suspense fallback={<Loading />}>
        <L {...props} />
      </Suspense>
    );
  } as Lazy<P>;
  Comp.preload = () => load().then(() => undefined);
  return Comp;
}
/** Shown only if a page's code takes longer than 300 ms (it fades in then), shaped like a page head. */
function Loading() {
  return (
    <Page className="animate-[fade-in_200ms_300ms_both]">
      <Skeleton className="h-9 w-56 max-w-full" />
      <Skeleton className="mt-4 h-5 w-80 max-w-full opacity-60" />
    </Page>
  );
}
const LoginPage = lz<{ reason?: string; next?: string }>(() => import("@/routes/login"), "LoginPage");
const BoxPage = lz(() => import("@/routes/box"), "BoxPage");
const BoxUsagePage = lz(() => import("@/routes/box-usage"), "BoxUsagePage");
const ProjectUsagePage = lz<{ project: string }>(() => import("@/routes/project-usage"), "ProjectUsagePage");
const ProjectHistoryPage = lz<{ project: string }>(() => import("@/routes/project-history"), "ProjectHistoryPage");
const ProjectSettingsPage = lz<{ project: string }>(() => import("@/routes/project-settings"), "ProjectSettingsPage");
const ActivityPage = lz<{ search: ActivitySearch }>(() => import("@/routes/activity"), "ActivityPage");
const SettingsPage = lz(() => import("@/routes/box-settings"), "SettingsPage");
const NewProjectPage = lz<{ search: NewSearch }>(() => import("@/routes/new"), "NewProjectPage");
const GitSettingsPage = lz<{ search: GitSearch }>(() => import("@/routes/git-settings"), "GitSettingsPage");
const KitPage = lz(() => import("@/routes/kit"), "KitPage");
const ChangePage = lz<{ id: string }>(() => import("@/routes/change"), "ChangePage");
const StatusPage = lz(() => import("@/routes/status"), "StatusPage");
const KeysPage = lz<{ create?: boolean }>(() => import("@/routes/keys"), "KeysPage");
const ProjectPage = lz<{ project: string }>(() => import("@/routes/project"), "ProjectPage");
const DeploymentsPage = lz<{ project: string }>(() => import("@/routes/deployments"), "DeploymentsPage");
const EnvVarsPage = lz<{ project: string }>(() => import("@/routes/env-vars"), "EnvVarsPage");
const ObservabilityPage = lz<{ project: string; search: ObserveSearch }>(() => import("@/routes/project-observe"), "ObservabilityPage");
const SecretsPage = lz<{ project: string }>(() => import("@/routes/project-settings"), "SecretsPage");
const DomainsPage = lz<{ project: string }>(() => import("@/routes/domains"), "DomainsPage");
const DnsSettingsPage = lz(() => import("@/routes/dns-settings"), "DnsSettingsPage");
const PeoplePage = lz(() => import("@/routes/settings"), "PeoplePage");
const PasskeysPage = lz(() => import("@/routes/settings"), "PasskeysPage");
const SignInsPage = lz(() => import("@/routes/sign-ins"), "SignInsPage");
const FilesPage = lz<{ project: string; isNew?: boolean }>(() => import("@/routes/files"), "FilesPage");
const BucketPage = lz<{ project: string; bucket: string; prefix?: string; file?: string }>(() => import("@/routes/files"), "BucketPage");
const InboxPage = lz<{ project: string; q?: string; m?: string; status?: string }>(() => import("@/routes/email"), "InboxPage");
const EmailSettingsPage = lz<{ project: string }>(() => import("@/routes/email"), "EmailSettingsPage");
const DataPage = lz<{ project: string }>(() => import("@/routes/data"), "DataPage");
const TablePage = lz<{ project: string; table: string }>(() => import("@/routes/data").then((m) => m.panels.table().then(() => m)), "TablePage");
const SqlPage = lz<{ project: string }>(() => import("@/routes/data").then((m) => m.panels.sql().then(() => m)), "SqlPage");
const BranchesPage = lz<{ project: string }>(() => import("@/routes/data"), "BranchesPage");
const SchemaPage = lz<{ project: string }>(() => import("@/routes/data").then((m) => m.panels.schema().then(() => m)), "SchemaPage");
const RestorePage = lz<{ project: string }>(() => import("@/routes/data").then((m) => m.panels.restore().then(() => m)), "RestorePage");
// The Database pages keep the copy (?branch) and the table view (?f filters, ?s sort, ?h hidden columns) in the URL.
type DataSearch = { branch?: string; f?: string; s?: string; h?: string; new?: string };
const dataSearch = (s: Record<string, unknown>): DataSearch => ({ branch: str(s.branch), f: str(s.f), s: str(s.s), h: str(s.h), new: str(s.new) });
const KvPage = lz<{ project: string; match?: string; k?: string; tab?: "keys" | "console"; isNew?: boolean }>(() => import("@/routes/kv"), "KvPage");
const MetricsPage = lz(() => import("@/routes/observe-metrics"), "MetricsPage");
const LogsPage = lz<LogsSearch & { inProject?: boolean }>(() => import("@/routes/observe-logs"), "LogsPage");
const ErrorsPage = lz<{ project?: string; status?: string }>(() => import("@/routes/observe-errors"), "ErrorsPage");
const IssuePage = lz<{ id: string }>(() => import("@/routes/observe-errors"), "IssuePage");
const AlertsPage = lz(() => import("@/routes/observe-alerts"), "AlertsPage");
const TracesPage = lz<{ project?: string; since?: string; errors?: boolean }>(() => import("@/routes/observe-traces"), "TracesPage");
const TracePage = lz<{ project: string; id: string }>(() => import("@/routes/observe-traces"), "TracePage");
const BackupsPage = lz(() => import("@/routes/backups"), "BackupsPage");
const RunsTab = lz<{ project: string; search: JobsSearch }>(() => import("@/routes/jobs/runs"), "RunsTab");
const SchedulesTab = lz<{ project: string; search: JobsSearch }>(() => import("@/routes/jobs/schedules"), "SchedulesTab");
const QueuesTab = lz<{ project: string; search: JobsSearch }>(() => import("@/routes/jobs/queues"), "QueuesTab");
const FailedTab = lz<{ project: string; search: JobsSearch }>(() => import("@/routes/jobs/failed"), "FailedTab");
const WorkersTab = lz<{ project: string; search: JobsSearch }>(() => import("@/routes/jobs/workers"), "WorkersTab");
const JobOrRunPage = lz<{ project: string; id: string }>(() => import("@/routes/jobs/detail"), "JobOrRunPage");
const AnalyticsPage = lz<{ project: string; search: AnalyticsSearch }>(() => import("@/routes/analytics"), "AnalyticsPage");
const ProtectPage = lz(() => import("@/routes/protect"), "ProtectPage");
const AppsPage = lz<{ project: string }>(() => import("@/routes/apps"), "AppsPage");
const AppPage = lz<{ project: string; app: string; deploy?: boolean }>(() => import("@/routes/apps"), "AppPage");
const DeployPage = lz<{ project: string; app: string; id: string }>(() => import("@/routes/apps"), "DeployPage");
const AppLogsPage = lz<{ project: string; app: string }>(() => import("@/routes/apps"), "AppLogsPage");
const UsersPage = lz<{ project: string; search?: string }>(() => import("@/routes/users"), "UsersPage");
const UserPage = lz<{ project: string; id: string }>(() => import("@/routes/users"), "UserPage");
const OrgsPage = lz<{ project: string; search?: string }>(() => import("@/routes/users"), "OrgsPage");
const OrgPage = lz<{ project: string; id: string }>(() => import("@/routes/users"), "OrgPage");

export type LogsSearch = { q?: string; project?: string; since?: string; live?: boolean };
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

const root = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Outlet,
  notFoundComponent: NotFound,
});

const login = createRoute({
  getParentRoute: () => root,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): { reason?: string; next?: string } => ({
    ...(typeof s.reason === "string" ? { reason: s.reason } : {}),
    // Only a path on this dashboard: never an absolute URL somewhere else.
    ...(typeof s.next === "string" && s.next.startsWith("/") && !s.next.startsWith("//") ? { next: s.next } : {}),
  }),
  loader: () => void LoginPage.preload(),
  component: function Login() {
    const { reason, next } = login.useSearch();
    return <LoginPage reason={reason} next={next} />;
  },
});

const app = createRoute({ getParentRoute: () => root, id: "app", component: Shell });

const tiers: Tier[] = ["reversible", "outbound", "irreversible"];
const box = createRoute({
  getParentRoute: () => app,
  path: "/",
  // The old landing page was the change log at /?project=…&risk=…: send those links to the Ledger.
  beforeLoad: ({ search }) => {
    const s = search as Record<string, unknown>;
    if (typeof s.project === "string" || typeof s.risk === "string") throw redirect({ to: "/ledger", search: s as ActivitySearch });
  },
  component: HomePage,
});
const boxUsage = createRoute({ getParentRoute: () => app, path: "/usage", loader: () => void BoxUsagePage.preload(), component: BoxUsagePage });
const boxRoute = createRoute({ getParentRoute: () => app, path: "/settings/box", loader: () => void BoxPage.preload(), component: BoxPage });
const activity = createRoute({
  getParentRoute: () => app,
  path: "/ledger",
  validateSearch: (s: Record<string, unknown>): ActivitySearch => ({
    project: typeof s.project === "string" && s.project ? s.project : undefined,
    risk: tiers.includes(s.risk as Tier) ? (s.risk as Tier) : undefined,
    who: s.who === "people" || s.who === "agents" ? s.who : undefined,
  }),
  loader: () => void ActivityPage.preload(),
  component: function Activity() {
    return <ActivityPage search={activity.useSearch()} />;
  },
});

const change = createRoute({
  getParentRoute: () => app,
  path: "/changes/$id",
  loader: () => void ChangePage.preload(),
  component: function Change() {
    const { id } = change.useParams();
    return <ChangePage key={id} id={id} />;
  },
});

const status = createRoute({ getParentRoute: () => app, path: "/status", loader: () => void StatusPage.preload(),
  component: StatusPage });
const settings = createRoute({ getParentRoute: () => app, path: "/settings", loader: () => void SettingsPage.preload(),
  component: SettingsPage });
const newProject = createRoute({
  getParentRoute: () => app,
  path: "/new",
  validateSearch: (s: Record<string, unknown>): NewSearch => (typeof s.starter === "string" && s.starter ? { starter: s.starter.slice(0, 80) } : {}),
  loader: () => void NewProjectPage.preload(),
  component: function NewProject() {
    return <NewProjectPage search={newProject.useSearch()} />;
  },
});
const gitSettings = createRoute({
  getParentRoute: () => app,
  path: "/settings/git",
  validateSearch: (s: Record<string, unknown>): GitSearch => ({
    ...(s.installed === "1" || s.installed === 1 || s.installed === true ? { installed: true } : {}),
    ...(s.requested === "1" || s.requested === 1 || s.requested === true ? { requested: true } : {}),
    ...(typeof s.error === "string" && s.error ? { error: s.error.slice(0, 400) } : {}),
  }),
  loader: () => void GitSettingsPage.preload(),
  component: function GitSettings() {
    return <GitSettingsPage search={gitSettings.useSearch()} />;
  },
});
const dnsSettings = createRoute({ getParentRoute: () => app, path: "/settings/dns", loader: () => void DnsSettingsPage.preload(),
  component: DnsSettingsPage });
const kit = createRoute({ getParentRoute: () => app, path: "/_kit", loader: () => void KitPage.preload(),
  component: KitPage });

const keys = createRoute({
  getParentRoute: () => app,
  path: "/settings/keys",
  validateSearch: (s: Record<string, unknown>): { create?: boolean } => (s.create === true || s.create === "true" ? { create: true } : {}),
  loader: () => void KeysPage.preload(),
  component: function Keys() {
    return <KeysPage create={keys.useSearch().create} />;
  },
});
// Old links: the tokens page is Keys now; approvals no longer exist (History keeps every change).
const tokens = createRoute({
  getParentRoute: () => app,
  path: "/tokens",
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/settings/keys", search: (search as { create?: boolean }).create ? { create: true } : {} });
  },
});
const approvals = createRoute({
  getParentRoute: () => app,
  path: "/approvals/$",
  beforeLoad: () => {
    throw redirect({ to: "/ledger" });
  },
});
/** A warm-up failing is fine: the page fetches again and shows the error itself. */
const noop = () => {};

/** Warms a project page: its code and the project's state and config. */
const warm =
  (page: { preload: () => Promise<void> }) =>
  ({ params, context }: { params: { project: string }; context: { queryClient: QueryClient } }) => {
    void page.preload();
    void context.queryClient.query(q.project(params.project)).catch(noop);
    void context.queryClient.query(q.manifest(params.project)).catch(noop);
  };
const project = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project",
  loader: warm(ProjectPage),
  component: function Project() {
    const { project: p } = project.useParams();
    return <ProjectPage key={p} project={p} />;
  },
});
const projectUsage = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/usage",
  loader: warm(ProjectUsagePage),
  component: function Usage() {
    const { project: p } = projectUsage.useParams();
    return <ProjectUsagePage key={p} project={p} />;
  },
});
const deploymentsRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/deployments",
  validateSearch: (s: Record<string, unknown>): { app?: string; env?: "production" | "preview"; status?: "live" | "running" | "failed" | "past"; branch?: string } => ({
    app: str(s.app),
    env: s.env === "production" || s.env === "preview" ? s.env : undefined,
    status: s.status === "live" || s.status === "running" || s.status === "failed" || s.status === "past" ? s.status : undefined,
    branch: s.branch === undefined ? undefined : String(s.branch),
  }),
  loader: warm(DeploymentsPage),
  component: function Deployments() {
    const { project: p } = deploymentsRoute.useParams();
    return <DeploymentsPage key={p} project={p} />;
  },
});
const envRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/env",
  loader: warm(EnvVarsPage),
  component: function Env() {
    const { project: p } = envRoute.useParams();
    return <EnvVarsPage key={p} project={p} />;
  },
});
const observabilityRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/observability",
  validateSearch: (s: Record<string, unknown>): ObserveSearch => {
    const tab = observeTab(s.tab);
    const range = typeof s.range === "string" && ["1h", "7d", "30d"].includes(s.range) ? (s.range as ObserveSearch["range"]) : undefined;
    const app = typeof s.app === "string" && s.app ? s.app : undefined;
    return { ...(tab && { tab }), ...(range && { range }), ...(app && { app }) };
  },
  loader: warm(ObservabilityPage),
  component: function Observability() {
    const { project: p } = observabilityRoute.useParams();
    const search = observabilityRoute.useSearch();
    return <ObservabilityPage key={p} project={p} search={search} />;
  },
});
const projectHistory = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/history",
  loader: ({ params, context }) => {
    void ProjectHistoryPage.preload();
    void context.queryClient.prefetchInfiniteQuery(q.changePages({ project: params.project })).catch(noop);
  },
  component: function History() {
    const { project: p } = projectHistory.useParams();
    return <ProjectHistoryPage key={p} project={p} />;
  },
});
const projectSettings = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/settings",
  loader: warm(ProjectSettingsPage),
  component: function ProjectSettings() {
    const { project: p } = projectSettings.useParams();
    return <ProjectSettingsPage key={p} project={p} />;
  },
});
const secrets = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/secrets",
  loader: () => void SecretsPage.preload(),
  component: function Secrets() {
    const { project: p } = secrets.useParams();
    return <SecretsPage key={p} project={p} />;
  },
});
const domainsRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/domains",
  validateSearch: (s: Record<string, unknown>): { domain?: string } => (str(s.domain) ? { domain: str(s.domain) } : {}),
  loader: warm(DomainsPage),
  component: function Domains() {
    const { project: p } = domainsRoute.useParams();
    return <DomainsPage key={p} project={p} />;
  },
});
const storage = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/storage",
  validateSearch: (s: Record<string, unknown>): { new?: string } => ({ new: str(s.new) }),
  loader: () => void FilesPage.preload(),
  component: function Storage() {
    const { project: p } = storage.useParams();
    const { new: isNew } = storage.useSearch();
    return (
      <PartGate project={p} part="storage">
        <FilesPage key={p} project={p} isNew={isNew === "bucket"} />
      </PartGate>
    );
  },
});
const bucket = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/storage/$bucket",
  validateSearch: (s: Record<string, unknown>): { prefix?: string; file?: string } => ({ prefix: str(s.prefix), file: str(s.file) }),
  loader: () => void BucketPage.preload(),
  component: function Bucket() {
    const { project: p, bucket: b } = bucket.useParams();
    const { prefix, file } = bucket.useSearch();
    return (
      <PartGate project={p} part="storage">
        <BucketPage key={p + b} project={p} bucket={b} prefix={prefix} file={file} />
      </PartGate>
    );
  },
});
const inbox = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/email",
  validateSearch: (s: Record<string, unknown>): { q?: string; m?: string; status?: string } => ({ q: str(s.q), m: str(s.m), status: str(s.status) }),
  loader: () => void InboxPage.preload(),
  component: function Inbox() {
    const { project: p } = inbox.useParams();
    const { q, m, status } = inbox.useSearch();
    return (
      <PartGate project={p} part="email">
        <InboxPage key={p} project={p} q={q} m={m} status={status} />
      </PartGate>
    );
  },
});
const emailSettings = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/email/settings",
  loader: () => void EmailSettingsPage.preload(),
  component: function EmailSettings() {
    const { project: p } = emailSettings.useParams();
    return (
      <PartGate project={p} part="email">
        <EmailSettingsPage key={p} project={p} />
      </PartGate>
    );
  },
});
const data = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data",
  validateSearch: dataSearch,
  loader: () => void DataPage.preload(),
  component: function Data() {
    const { project: p } = data.useParams();
    return (
      <PartGate project={p} part="postgres">
        <DataPage key={p} project={p} />
      </PartGate>
    );
  },
});
const table = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/tables/$table",
  validateSearch: dataSearch,
  loader: () => void TablePage.preload(),
  component: function Table() {
    const { project: p, table: t } = table.useParams();
    return (
      <PartGate project={p} part="postgres">
        <TablePage key={p} project={p} table={t} />
      </PartGate>
    );
  },
});
const sqlRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/sql",
  validateSearch: (s: Record<string, unknown>): { branch?: string; sql?: string } => ({ branch: str(s.branch), sql: str(s.sql) }),
  loader: () => void SqlPage.preload(),
  component: function Sql() {
    const { project: p } = sqlRoute.useParams();
    return (
      <PartGate project={p} part="postgres">
        <SqlPage key={p} project={p} />
      </PartGate>
    );
  },
});
const branches = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/branches",
  validateSearch: dataSearch,
  loader: () => void BranchesPage.preload(),
  component: function Branches() {
    const { project: p } = branches.useParams();
    return (
      <PartGate project={p} part="postgres">
        <BranchesPage key={p} project={p} />
      </PartGate>
    );
  },
});
const schemaRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/schema",
  validateSearch: dataSearch,
  loader: () => void SchemaPage.preload(),
  component: function Schema() {
    const { project: p } = schemaRoute.useParams();
    return (
      <PartGate project={p} part="postgres">
        <SchemaPage key={p} project={p} />
      </PartGate>
    );
  },
});
const restoreRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/restore",
  validateSearch: dataSearch,
  loader: () => void RestorePage.preload(),
  component: function Restore() {
    const { project: p } = restoreRoute.useParams();
    return (
      <PartGate project={p} part="postgres">
        <RestorePage key={p} project={p} />
      </PartGate>
    );
  },
});
const kv = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/kv",
  validateSearch: (s: Record<string, unknown>): { match?: string; key?: string; new?: boolean } => ({
    match: str(s.match),
    key: str(s.key),
    ...(s.new === true || s.new === "true" ? { new: true } : {}),
  }),
  loader: () => void KvPage.preload(),
  component: function Kv() {
    const { project: p } = kv.useParams();
    const { match, key, new: isNew } = kv.useSearch();
    return (
      <PartGate project={p} part="valkey">
        <KvPage key={p} project={p} match={match} k={key} isNew={isNew} />
      </PartGate>
    );
  },
});
const kvConsole = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/kv/console",
  loader: () => void KvPage.preload(),
  component: function KvConsole() {
    const { project: p } = kvConsole.useParams();
    return (
      <PartGate project={p} part="valkey">
        <KvPage key={p} project={p} tab="console" />
      </PartGate>
    );
  },
});
const metrics = createRoute({ getParentRoute: () => app, path: "/metrics", loader: () => void MetricsPage.preload(),
  component: MetricsPage });
const logs = createRoute({
  getParentRoute: () => app,
  path: "/logs",
  validateSearch: (s: Record<string, unknown>): LogsSearch => ({
    q: str(s.q),
    project: str(s.project),
    since: str(s.since),
    live: s.live === true || s.live === "true" ? true : undefined,
  }),
  loader: () => void LogsPage.preload(),
  component: function Logs() {
    return <LogsPage {...logs.useSearch()} />;
  },
});
/** A project's own logs, inside its sidebar: the box's Logs page with the project fixed. */
const projectLogs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/logs",
  validateSearch: (s: Record<string, unknown>): Omit<LogsSearch, "project"> => ({
    q: str(s.q),
    since: str(s.since),
    live: s.live === true || s.live === "true" ? true : undefined,
  }),
  loader: () => void LogsPage.preload(),
  component: function ProjectLogs() {
    const { project: p } = projectLogs.useParams();
    return <LogsPage key={p} {...projectLogs.useSearch()} project={p} inProject />;
  },
});
const errors = createRoute({
  getParentRoute: () => app,
  path: "/errors",
  validateSearch: (s: Record<string, unknown>): { project?: string; status?: string } => ({ project: str(s.project), status: str(s.status) }),
  loader: () => void ErrorsPage.preload(),
  component: function Errors() {
    return <ErrorsPage {...errors.useSearch()} />;
  },
});
const issue = createRoute({
  getParentRoute: () => app,
  path: "/errors/$id",
  loader: () => void IssuePage.preload(),
  component: function Issue() {
    const { id } = issue.useParams();
    return <IssuePage key={id} id={id} />;
  },
});
const requests = createRoute({
  getParentRoute: () => app,
  path: "/requests",
  validateSearch: (s: Record<string, unknown>): { project?: string; since?: string; errors?: boolean } => ({
    project: str(s.project),
    since: str(s.since),
    errors: s.errors === true || s.errors === "true" ? true : undefined,
  }),
  loader: () => void TracesPage.preload(),
  component: function Requests() {
    return <TracesPage {...requests.useSearch()} />;
  },
});
const trace = createRoute({
  getParentRoute: () => app,
  path: "/requests/$project/$id",
  loader: () => void TracePage.preload(),
  component: function Trace() {
    const { project: p, id } = trace.useParams();
    return <TracePage key={id} project={p} id={id} />;
  },
});
const alerts = createRoute({ getParentRoute: () => app, path: "/alerts", loader: () => void AlertsPage.preload(),
  component: AlertsPage });
const backups = createRoute({ getParentRoute: () => app, path: "/backups", loader: () => void BackupsPage.preload(),
  component: BackupsPage });
/** The Jobs area: one route per tab, all reading the same search (dialogs, the selected run, filters). */
const jobsTab = (path: "/projects/$project/jobs" | "/projects/$project/jobs/schedules" | "/projects/$project/jobs/queues" | "/projects/$project/jobs/failed" | "/projects/$project/jobs/workers", Comp: typeof RunsTab) => {
  const r = createRoute({
    getParentRoute: () => app,
    path,
    validateSearch: jobsSearch,
    loader: () => void Comp.preload(),
    component: function JobsTabPage() {
      const { project: p } = r.useParams() as { project: string };
      return <Comp key={p} project={p} search={r.useSearch() as JobsSearch} />;
    },
  });
  return r;
};
const jobsRuns = jobsTab("/projects/$project/jobs", RunsTab);
const jobsSchedules = jobsTab("/projects/$project/jobs/schedules", SchedulesTab);
const jobsQueues = jobsTab("/projects/$project/jobs/queues", QueuesTab);
const jobsFailed = jobsTab("/projects/$project/jobs/failed", FailedTab);
const jobsWorkers = jobsTab("/projects/$project/jobs/workers", WorkersTab);
const jobDetail = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/jobs/$id",
  loader: () => void JobOrRunPage.preload(),
  component: function JobOrRun() {
    const { project: p, id } = jobDetail.useParams();
    return <JobOrRunPage key={id} project={p} id={id} />;
  },
});
// The Jobs area's earlier addresses (Queues, Jobs, Workflows) open their new places.
const oldQueues = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/projects/$project/jobs/queues", params });
  },
});
const oldJobs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs",
  beforeLoad: ({ params, search }) => {
    const s = search as Record<string, unknown>;
    if (s.state === "dead") throw redirect({ to: "/projects/$project/jobs/failed", params });
    throw redirect({ to: "/projects/$project/jobs", params, search: str(s.queue) ? { queue: str(s.queue) } : {} });
  },
});
const oldJob = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs/$id",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/projects/$project/jobs/$id", params });
  },
});
const oldWorkflows = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/projects/$project/jobs", params, search: { kind: "workflow" } });
  },
});
const oldRun = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows/$id",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/projects/$project/jobs/$id", params });
  },
});
const analytics = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/analytics",
  validateSearch: analyticsSearch,
  loader: () => void AnalyticsPage.preload(),
  component: function Analytics() {
    const { project: p } = analytics.useParams();
    return (
      <PartGate project={p} part="analytics">
        <AnalyticsPage key={p} project={p} search={analytics.useSearch()} />
      </PartGate>
    );
  },
});
const appsRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps",
  loader: () => void AppsPage.preload(),
  component: function Apps() {
    const { project: p } = appsRoute.useParams();
    return <AppsPage key={p} project={p} />;
  },
});
const appRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app",
  validateSearch: (s: Record<string, unknown>): { deploy?: boolean } => (s.deploy === true || s.deploy === "true" ? { deploy: true } : {}),
  loader: () => void AppPage.preload(),
  component: function AppView() {
    const { project: p, app: a } = appRoute.useParams();
    return <AppPage key={p + a} project={p} app={a} deploy={appRoute.useSearch().deploy} />;
  },
});
const deployRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app/deploys/$id",
  validateSearch: (s: Record<string, unknown>): { tab?: "runtime" } => (s.tab === "runtime" ? { tab: "runtime" } : {}),
  loader: () => void DeployPage.preload(),
  component: function DeployView() {
    const { project: p, app: a, id } = deployRoute.useParams();
    return <DeployPage key={id} project={p} app={a} id={id} />;
  },
});
const appLogs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app/logs",
  loader: () => void AppLogsPage.preload(),
  component: function AppLogs() {
    const { project: p, app: a } = appLogs.useParams();
    return <AppLogsPage key={p + a} project={p} app={a} />;
  },
});
const users = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/users",
  validateSearch: (s: Record<string, unknown>): { search?: string } => ({ search: str(s.search) }),
  loader: () => void UsersPage.preload(),
  component: function Users() {
    const { project: p } = users.useParams();
    const { search } = users.useSearch();
    return (
      <PartGate project={p} part="auth">
        <UsersPage key={p} project={p} search={search} />
      </PartGate>
    );
  },
});
const userRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/users/$id",
  loader: () => void UserPage.preload(),
  component: function UserView() {
    const { project: p, id } = userRoute.useParams();
    return (
      <PartGate project={p} part="auth">
        <UserPage key={id} project={p} id={id} />
      </PartGate>
    );
  },
});
const orgs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs",
  validateSearch: (s: Record<string, unknown>): { search?: string } => ({ search: str(s.search) }),
  loader: () => void OrgsPage.preload(),
  component: function Orgs() {
    const { project: p } = orgs.useParams();
    return (
      <PartGate project={p} part="auth">
        <OrgsPage key={p} project={p} search={orgs.useSearch().search} />
      </PartGate>
    );
  },
});
const orgRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs/$id",
  loader: () => void OrgPage.preload(),
  component: function OrgView() {
    const { project: p, id } = orgRoute.useParams();
    return (
      <PartGate project={p} part="auth">
        <OrgPage key={id} project={p} id={id} />
      </PartGate>
    );
  },
});
const protect = createRoute({ getParentRoute: () => app, path: "/protect", loader: () => void ProtectPage.preload(),
  component: ProtectPage });
const people = createRoute({ getParentRoute: () => app, path: "/settings/people", loader: () => void PeoplePage.preload(),
  component: PeoplePage });
const passkeys = createRoute({ getParentRoute: () => app, path: "/settings/passkeys", loader: () => void PasskeysPage.preload(),
  component: PasskeysPage });
const signIns = createRoute({ getParentRoute: () => app, path: "/settings/sign-ins", loader: () => void SignInsPage.preload(),
  component: SignInsPage });

/** No such page: the mascot has lifted its lid on an empty tin. */
function NotFound() {
  return (
    <Page>
      <div className="flex items-center gap-6">
        <span className="art-plate block size-28 shrink-0 max-sm:hidden">
          <img src={emptyTin} alt="" width={112} height={112} className="block size-full select-none" draggable={false} />
        </span>
        <div className="min-w-0">
          <h1 className="sentence text-ink">There’s no page here.</h1>
          <p className="mt-2 text-md text-ink-2">
            The link may be old, or the thing it pointed at was removed.{" "}
            <Link to="/" className="font-[550] text-brass-ink underline underline-offset-4">
              Back to your projects
            </Link>
            .
          </p>
        </div>
      </div>
    </Page>
  );
}

/** A page that threw while showing itself: the mascot's spill, what happened, and a way on. */
function RouteError({ error }: ErrorComponentProps) {
  const said = error instanceof Error ? error.message : "";
  return (
    <Page>
      <div className="flex items-center gap-6">
        <Mascot state="failed" size={112} className="max-sm:hidden" />
        <div className="min-w-0">
          <h1 className="sentence text-ink">This page hit a problem.</h1>
          <p className="mt-2 max-w-[38rem] text-md text-ink-2">{said ? `It said: ${said}` : "It stopped while showing itself."} Reload to try again.</p>
          <div className="mt-4 flex flex-wrap gap-2">
            <Button variant="primary" size="md" onClick={() => location.reload()}>
              Reload
            </Button>
            <Button asChild variant="ghost" size="md">
              <Link to="/">Back to your projects</Link>
            </Button>
          </div>
        </div>
      </div>
    </Page>
  );
}

const tree = root.addChildren([
  login,
  app.addChildren([
    box,
    boxRoute,
    boxUsage,
    activity,
    settings,
    gitSettings,
    dnsSettings,
    newProject,
    kit,
    change,
    status,
    tokens,
    keys,
    approvals,
    project,
    projectUsage,
    deploymentsRoute,
    envRoute,
    observabilityRoute,
    projectHistory,
    projectSettings,
    secrets,
    domainsRoute,
    storage,
    bucket,
    inbox,
    emailSettings,
    data,
    table,
    sqlRoute,
    branches,
    schemaRoute,
    restoreRoute,
    kv,
    kvConsole,
    metrics,
    logs,
    projectLogs,
    errors,
    issue,
    requests,
    trace,
    alerts,
    backups,
    jobsRuns,
    jobsSchedules,
    jobsQueues,
    jobsFailed,
    jobsWorkers,
    jobDetail,
    oldQueues,
    oldJobs,
    oldJob,
    oldWorkflows,
    oldRun,
    analytics,
    protect,
    appsRoute,
    appRoute,
    deployRoute,
    appLogs,
    users,
    userRoute,
    orgs,
    orgRoute,
    people,
    passkeys,
    signIns,
  ]),
]);

export function makeRouter(queryClient: QueryClient) {
  return createRouter({ routeTree: tree, context: { queryClient }, defaultPreload: "intent", defaultPreloadStaleTime: 0, scrollRestoration: true, defaultErrorComponent: RouteError });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof makeRouter>;
  }
}
