import { createRootRouteWithContext, createRoute, createRouter, Link, Outlet, redirect, type ErrorComponentProps } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import type { Tier } from "@/api/client";
import emptyTin from "@/assets/illustrations/empty-inbox.webp";
import { Mascot } from "@/components/mascot";
import { Shell } from "@/components/shell";
import { Button } from "@/components/ui/button";
import type { ActivitySearch } from "@/routes/activity";
import type { GitSearch } from "@/routes/git-settings";
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
const NewProjectPage = lz(() => import("@/routes/new"), "NewProjectPage");
const GitSettingsPage = lz<{ search: GitSearch }>(() => import("@/routes/git-settings"), "GitSettingsPage");
const KitPage = lz(() => import("@/routes/kit"), "KitPage");
const ChangePage = lz<{ id: string }>(() => import("@/routes/change"), "ChangePage");
const StatusPage = lz(() => import("@/routes/status"), "StatusPage");
const KeysPage = lz<{ create?: boolean }>(() => import("@/routes/keys"), "KeysPage");
const ProjectPage = lz<{ project: string }>(() => import("@/routes/project"), "ProjectPage");
const SecretsPage = lz<{ project: string }>(() => import("@/routes/project-settings"), "SecretsPage");
const DomainsPage = lz<{ project: string }>(() => import("@/routes/domains"), "DomainsPage");
const DnsSettingsPage = lz(() => import("@/routes/dns-settings"), "DnsSettingsPage");
const PeoplePage = lz(() => import("@/routes/settings"), "PeoplePage");
const PasskeysPage = lz(() => import("@/routes/settings"), "PasskeysPage");
const FilesPage = lz<{ project: string; isNew?: boolean; connect?: boolean }>(() => import("@/routes/files"), "FilesPage");
const BucketPage = lz<{ project: string; bucket: string; prefix?: string; file?: string }>(() => import("@/routes/files"), "BucketPage");
const InboxPage = lz<{ project: string; q?: string; m?: string }>(() => import("@/routes/email"), "InboxPage");
const EmailSettingsPage = lz<{ project: string }>(() => import("@/routes/email"), "EmailSettingsPage");
const DataPage = lz<{ project: string }>(() => import("@/routes/data"), "DataPage");
const TablePage = lz<{ project: string; table: string }>(() => import("@/routes/data"), "TablePage");
const SqlPage = lz<{ project: string }>(() => import("@/routes/data"), "SqlPage");
const BranchesPage = lz<{ project: string }>(() => import("@/routes/data"), "BranchesPage");
const SchemaPage = lz<{ project: string }>(() => import("@/routes/data"), "SchemaPage");
const RestorePage = lz<{ project: string }>(() => import("@/routes/data"), "RestorePage");
// The Database pages keep the copy (?branch) and the table view (?f filters, ?s sort, ?h hidden columns) in the URL.
type DataSearch = { branch?: string; f?: string; s?: string; h?: string; new?: string };
const dataSearch = (s: Record<string, unknown>): DataSearch => ({ branch: str(s.branch), f: str(s.f), s: str(s.s), h: str(s.h), new: str(s.new) });
const KvPage = lz<{ project: string; match?: string; k?: string; tab?: "keys" | "console"; isNew?: boolean }>(() => import("@/routes/kv"), "KvPage");
const MetricsPage = lz(() => import("@/routes/observe"), "MetricsPage");
const LogsPage = lz<LogsSearch>(() => import("@/routes/observe"), "LogsPage");
const ErrorsPage = lz<{ project?: string; status?: string }>(() => import("@/routes/observe"), "ErrorsPage");
const IssuePage = lz<{ id: string }>(() => import("@/routes/observe"), "IssuePage");
const AlertsPage = lz(() => import("@/routes/observe"), "AlertsPage");
const TracesPage = lz<{ project?: string; since?: string; errors?: boolean }>(() => import("@/routes/observe"), "TracesPage");
const TracePage = lz<{ project: string; id: string }>(() => import("@/routes/observe"), "TracePage");
const BackupsPage = lz(() => import("@/routes/backups"), "BackupsPage");
const QueuesPage = lz<{ project: string }>(() => import("@/routes/queues"), "QueuesPage");
const JobsPage = lz<{ project: string; queue?: string; state?: string }>(() => import("@/routes/queues"), "JobsPage");
const JobPage = lz<{ project: string; id: string }>(() => import("@/routes/queues"), "JobPage");
const WorkflowsPage = lz<{ project: string; state?: string }>(() => import("@/routes/queues"), "WorkflowsPage");
const RunPage = lz<{ project: string; id: string }>(() => import("@/routes/queues"), "RunPage");
const AnalyticsPage = lz<{ project: string; period?: string }>(() => import("@/routes/analytics"), "AnalyticsPage");
const ProtectPage = lz(() => import("@/routes/protect"), "ProtectPage");
const AppsPage = lz<{ project: string }>(() => import("@/routes/apps"), "AppsPage");
const AppPage = lz<{ project: string; app: string; deploy?: boolean }>(() => import("@/routes/apps"), "AppPage");
const DeployPage = lz<{ project: string; app: string; id: string }>(() => import("@/routes/apps"), "DeployPage");
const AppLogsPage = lz<{ project: string; app: string }>(() => import("@/routes/apps"), "AppLogsPage");
const UsersPage = lz<{ project: string; search?: string; page?: number }>(() => import("@/routes/users"), "UsersPage");
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
const newProject = createRoute({ getParentRoute: () => app, path: "/new", loader: () => void NewProjectPage.preload(),
  component: NewProjectPage });
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
/** Warms a project page: its code and the project's state and config. */
const warm =
  (page: { preload: () => Promise<void> }) =>
  ({ params, context }: { params: { project: string }; context: { queryClient: QueryClient } }) => {
    void page.preload();
    void context.queryClient.prefetchQuery(q.project(params.project));
    void context.queryClient.prefetchQuery(q.manifest(params.project));
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
const projectHistory = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/history",
  loader: ({ params, context }) => {
    void ProjectHistoryPage.preload();
    void context.queryClient.prefetchQuery(q.changes(params.project));
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
  loader: warm(DomainsPage),
  component: function Domains() {
    const { project: p } = domainsRoute.useParams();
    return <DomainsPage key={p} project={p} />;
  },
});
const storage = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/storage",
  validateSearch: (s: Record<string, unknown>): { new?: string; connect?: boolean } => ({
    new: str(s.new),
    ...(s.connect === true || s.connect === "1" || s.connect === "true" ? { connect: true } : {}),
  }),
  loader: () => void FilesPage.preload(),
  component: function Storage() {
    const { project: p } = storage.useParams();
    const { new: isNew, connect } = storage.useSearch();
    return <FilesPage key={p} project={p} isNew={isNew === "bucket"} connect={connect} />;
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
    return <BucketPage key={p + b} project={p} bucket={b} prefix={prefix} file={file} />;
  },
});
const inbox = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/email",
  validateSearch: (s: Record<string, unknown>): { q?: string; m?: string } => ({ q: str(s.q), m: str(s.m) }),
  loader: () => void InboxPage.preload(),
  component: function Inbox() {
    const { project: p } = inbox.useParams();
    const { q, m } = inbox.useSearch();
    return <InboxPage key={p} project={p} q={q} m={m} />;
  },
});
const emailSettings = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/email/settings",
  loader: () => void EmailSettingsPage.preload(),
  component: function EmailSettings() {
    const { project: p } = emailSettings.useParams();
    return <EmailSettingsPage key={p} project={p} />;
  },
});
const data = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data",
  validateSearch: dataSearch,
  loader: () => void DataPage.preload(),
  component: function Data() {
    const { project: p } = data.useParams();
    return <DataPage key={p} project={p} />;
  },
});
const table = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/tables/$table",
  validateSearch: dataSearch,
  loader: () => void TablePage.preload(),
  component: function Table() {
    const { project: p, table: t } = table.useParams();
    return <TablePage key={p} project={p} table={t} />;
  },
});
const sqlRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/sql",
  validateSearch: (s: Record<string, unknown>): { branch?: string; sql?: string } => ({ branch: str(s.branch), sql: str(s.sql) }),
  loader: () => void SqlPage.preload(),
  component: function Sql() {
    const { project: p } = sqlRoute.useParams();
    return <SqlPage key={p} project={p} />;
  },
});
const branches = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/branches",
  validateSearch: dataSearch,
  loader: () => void BranchesPage.preload(),
  component: function Branches() {
    const { project: p } = branches.useParams();
    return <BranchesPage key={p} project={p} />;
  },
});
const schemaRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/schema",
  validateSearch: dataSearch,
  loader: () => void SchemaPage.preload(),
  component: function Schema() {
    const { project: p } = schemaRoute.useParams();
    return <SchemaPage key={p} project={p} />;
  },
});
const restoreRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/restore",
  validateSearch: dataSearch,
  loader: () => void RestorePage.preload(),
  component: function Restore() {
    const { project: p } = restoreRoute.useParams();
    return <RestorePage key={p} project={p} />;
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
    return <KvPage key={p} project={p} match={match} k={key} isNew={isNew} />;
  },
});
const kvConsole = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/kv/console",
  loader: () => void KvPage.preload(),
  component: function KvConsole() {
    const { project: p } = kvConsole.useParams();
    return <KvPage key={p} project={p} tab="console" />;
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
const queues = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues",
  loader: () => void QueuesPage.preload(),
  component: function Queues() {
    const { project: p } = queues.useParams();
    return <QueuesPage key={p} project={p} />;
  },
});
const jobs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs",
  validateSearch: (s: Record<string, unknown>): { queue?: string; state?: string } => ({ queue: str(s.queue), state: str(s.state) }),
  loader: () => void JobsPage.preload(),
  component: function Jobs() {
    const { project: p } = jobs.useParams();
    const { queue, state } = jobs.useSearch();
    return <JobsPage key={p} project={p} queue={queue} state={state} />;
  },
});
const job = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs/$id",
  loader: () => void JobPage.preload(),
  component: function Job() {
    const { project: p, id } = job.useParams();
    return <JobPage key={id} project={p} id={id} />;
  },
});
const workflows = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows",
  validateSearch: (s: Record<string, unknown>): { state?: string } => ({ state: str(s.state) }),
  loader: () => void WorkflowsPage.preload(),
  component: function Workflows() {
    const { project: p } = workflows.useParams();
    return <WorkflowsPage key={p} project={p} state={workflows.useSearch().state} />;
  },
});
const runRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows/$id",
  loader: () => void RunPage.preload(),
  component: function Run() {
    const { project: p, id } = runRoute.useParams();
    return <RunPage key={id} project={p} id={id} />;
  },
});
const analytics = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/analytics",
  validateSearch: (s: Record<string, unknown>): { period?: string } => ({ period: str(s.period) }),
  loader: () => void AnalyticsPage.preload(),
  component: function Analytics() {
    const { project: p } = analytics.useParams();
    return <AnalyticsPage key={p} project={p} period={analytics.useSearch().period} />;
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
  validateSearch: (s: Record<string, unknown>): { search?: string; page?: number } => ({
    search: str(s.search),
    page: Number(s.page) > 1 ? Number(s.page) : undefined,
  }),
  loader: () => void UsersPage.preload(),
  component: function Users() {
    const { project: p } = users.useParams();
    const { search, page } = users.useSearch();
    return <UsersPage key={p} project={p} search={search} page={page} />;
  },
});
const userRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/users/$id",
  loader: () => void UserPage.preload(),
  component: function UserView() {
    const { project: p, id } = userRoute.useParams();
    return <UserPage key={id} project={p} id={id} />;
  },
});
const orgs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs",
  validateSearch: (s: Record<string, unknown>): { search?: string } => ({ search: str(s.search) }),
  loader: () => void OrgsPage.preload(),
  component: function Orgs() {
    const { project: p } = orgs.useParams();
    return <OrgsPage key={p} project={p} search={orgs.useSearch().search} />;
  },
});
const orgRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs/$id",
  loader: () => void OrgPage.preload(),
  component: function OrgView() {
    const { project: p, id } = orgRoute.useParams();
    return <OrgPage key={id} project={p} id={id} />;
  },
});
const protect = createRoute({ getParentRoute: () => app, path: "/protect", loader: () => void ProtectPage.preload(),
  component: ProtectPage });
const people = createRoute({ getParentRoute: () => app, path: "/settings/people", loader: () => void PeoplePage.preload(),
  component: PeoplePage });
const passkeys = createRoute({ getParentRoute: () => app, path: "/settings/passkeys", loader: () => void PasskeysPage.preload(),
  component: PasskeysPage });

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
    errors,
    issue,
    requests,
    trace,
    alerts,
    backups,
    queues,
    jobs,
    job,
    workflows,
    runRoute,
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
