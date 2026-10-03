import { createRootRouteWithContext, createRoute, createRouter, Link, Outlet, redirect } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import type { Tier } from "@/api/client";
import { Shell } from "@/components/shell";
import type { ActivitySearch } from "@/routes/activity";
import { BoxPage } from "@/routes/box";
import { Page } from "@/components/page";
import { LoginPage } from "@/routes/login";
import { lazy, Suspense, type ComponentType, type ReactElement } from "react";
import { Skeleton } from "@/components/page";

// Every page but the Box and Login loads on demand, so the first paint
// ships only the shell and the landing page (the others bring their own code).
function lz<P extends object = Record<string, never>>(load: () => Promise<Record<string, unknown>>, name: string): (props: P) => ReactElement {
  const L = lazy(() => load().then((m) => ({ default: m[name] as ComponentType<P> })));
  return function Lazy(props: P) {
    return (
      <Suspense fallback={<Loading />}>
        <L {...props} />
      </Suspense>
    );
  };
}
function Loading() {
  return (
    <Page>
      <Skeleton className="h-10 w-72 max-w-full" />
      <Skeleton className="mt-4 h-5 w-96 max-w-full opacity-60" />
    </Page>
  );
}
const ActivityPage = lz<{ search: ActivitySearch }>(() => import("@/routes/activity"), "ActivityPage");
const SettingsPage = lz(() => import("@/routes/box-settings"), "SettingsPage");
const NewProjectPage = lz(() => import("@/routes/new"), "NewProjectPage");
const KitPage = lz(() => import("@/routes/kit"), "KitPage");
const ChangePage = lz<{ id: string }>(() => import("@/routes/change"), "ChangePage");
const StatusPage = lz(() => import("@/routes/status"), "StatusPage");
const TokensPage = lz<{ create?: boolean }>(() => import("@/routes/tokens"), "TokensPage");
const ApprovalsPage = lz(() => import("@/routes/approvals"), "ApprovalsPage");
const ApprovalPage = lz<{ id: string }>(() => import("@/routes/approvals"), "ApprovalPage");
const ProjectPage = lz<{ project: string }>(() => import("@/routes/project"), "ProjectPage");
const SecretsPage = lz<{ project: string }>(() => import("@/routes/project"), "SecretsPage");
const PeoplePage = lz(() => import("@/routes/settings"), "PeoplePage");
const PasskeysPage = lz(() => import("@/routes/settings"), "PasskeysPage");
const StoragePage = lz<{ project: string }>(() => import("@/routes/storage"), "StoragePage");
const BucketPage = lz<{ project: string; bucket: string; prefix?: string; file?: string }>(() => import("@/routes/storage"), "BucketPage");
const InboxPage = lz<{ project: string; q?: string; m?: string }>(() => import("@/routes/email"), "InboxPage");
const EmailSettingsPage = lz<{ project: string }>(() => import("@/routes/email"), "EmailSettingsPage");
const DataPage = lz<{ project: string }>(() => import("@/routes/data"), "DataPage");
const TablePage = lz<{ project: string; table: string; page?: number }>(() => import("@/routes/data"), "TablePage");
const SqlPage = lz<{ project: string }>(() => import("@/routes/data"), "SqlPage");
const BranchesPage = lz<{ project: string }>(() => import("@/routes/data"), "BranchesPage");
const KvPage = lz<{ project: string; match?: string; k?: string }>(() => import("@/routes/kv"), "KvPage");
const MetricsPage = lz(() => import("@/routes/observe"), "MetricsPage");
const LogsPage = lz<LogsSearch>(() => import("@/routes/observe"), "LogsPage");
const ErrorsPage = lz<{ project?: string; status?: string }>(() => import("@/routes/observe"), "ErrorsPage");
const IssuePage = lz<{ id: string }>(() => import("@/routes/observe"), "IssuePage");
const AlertsPage = lz(() => import("@/routes/observe"), "AlertsPage");
const BackupsPage = lz(() => import("@/routes/backups"), "BackupsPage");
const QueuesPage = lz<{ project: string }>(() => import("@/routes/queues"), "QueuesPage");
const JobsPage = lz<{ project: string; queue?: string; state?: string }>(() => import("@/routes/queues"), "JobsPage");
const JobPage = lz<{ project: string; id: string }>(() => import("@/routes/queues"), "JobPage");
const WorkflowsPage = lz<{ project: string; state?: string }>(() => import("@/routes/queues"), "WorkflowsPage");
const RunPage = lz<{ project: string; id: string }>(() => import("@/routes/queues"), "RunPage");
const AnalyticsPage = lz<{ project: string; period?: string }>(() => import("@/routes/analytics"), "AnalyticsPage");
const ProtectPage = lz(() => import("@/routes/protect"), "ProtectPage");
const AppsPage = lz<{ project: string }>(() => import("@/routes/apps"), "AppsPage");
const AppPage = lz<{ project: string; app: string }>(() => import("@/routes/apps"), "AppPage");
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
  validateSearch: (s: Record<string, unknown>): { reason?: string } => (typeof s.reason === "string" ? { reason: s.reason } : {}),
  component: function Login() {
    const { reason } = login.useSearch();
    return <LoginPage reason={reason} />;
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
  component: BoxPage,
});
const activity = createRoute({
  getParentRoute: () => app,
  path: "/ledger",
  validateSearch: (s: Record<string, unknown>): ActivitySearch => ({
    project: typeof s.project === "string" && s.project ? s.project : undefined,
    risk: tiers.includes(s.risk as Tier) ? (s.risk as Tier) : undefined,
  }),
  component: function Activity() {
    return <ActivityPage search={activity.useSearch()} />;
  },
});

const change = createRoute({
  getParentRoute: () => app,
  path: "/changes/$id",
  component: function Change() {
    const { id } = change.useParams();
    return <ChangePage key={id} id={id} />;
  },
});

const status = createRoute({ getParentRoute: () => app, path: "/status", component: StatusPage });
const settings = createRoute({ getParentRoute: () => app, path: "/settings", component: SettingsPage });
const newProject = createRoute({ getParentRoute: () => app, path: "/new", component: NewProjectPage });
const kit = createRoute({ getParentRoute: () => app, path: "/_kit", component: KitPage });

const tokens = createRoute({
  getParentRoute: () => app,
  path: "/tokens",
  validateSearch: (s: Record<string, unknown>): { create?: boolean } => (s.create === true || s.create === "true" ? { create: true } : {}),
  component: function Tokens() {
    return <TokensPage create={tokens.useSearch().create} />;
  },
});

const approvals = createRoute({ getParentRoute: () => app, path: "/approvals", component: ApprovalsPage });
const approval = createRoute({
  getParentRoute: () => app,
  path: "/approvals/$id",
  component: function Approval() {
    const { id } = approval.useParams();
    return <ApprovalPage key={id} id={id} />;
  },
});
const project = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project",
  component: function Project() {
    const { project: p } = project.useParams();
    return <ProjectPage key={p} project={p} />;
  },
});
const secrets = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/secrets",
  component: function Secrets() {
    const { project: p } = secrets.useParams();
    return <SecretsPage key={p} project={p} />;
  },
});
const storage = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/storage",
  component: function Storage() {
    const { project: p } = storage.useParams();
    return <StoragePage key={p} project={p} />;
  },
});
const bucket = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/storage/$bucket",
  validateSearch: (s: Record<string, unknown>): { prefix?: string; file?: string } => ({ prefix: str(s.prefix), file: str(s.file) }),
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
  component: function Inbox() {
    const { project: p } = inbox.useParams();
    const { q, m } = inbox.useSearch();
    return <InboxPage key={p} project={p} q={q} m={m} />;
  },
});
const emailSettings = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/email/settings",
  component: function EmailSettings() {
    const { project: p } = emailSettings.useParams();
    return <EmailSettingsPage key={p} project={p} />;
  },
});
const data = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data",
  component: function Data() {
    const { project: p } = data.useParams();
    return <DataPage key={p} project={p} />;
  },
});
const table = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/tables/$table",
  validateSearch: (s: Record<string, unknown>): { page?: number } => (Number(s.page) > 1 ? { page: Number(s.page) } : {}),
  component: function Table() {
    const { project: p, table: t } = table.useParams();
    return <TablePage key={p + t} project={p} table={t} page={table.useSearch().page} />;
  },
});
const sqlRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/sql",
  component: function Sql() {
    const { project: p } = sqlRoute.useParams();
    return <SqlPage key={p} project={p} />;
  },
});
const branches = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/branches",
  component: function Branches() {
    const { project: p } = branches.useParams();
    return <BranchesPage key={p} project={p} />;
  },
});
const kv = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/data/kv",
  validateSearch: (s: Record<string, unknown>): { match?: string; key?: string } => ({ match: str(s.match), key: str(s.key) }),
  component: function Kv() {
    const { project: p } = kv.useParams();
    const { match, key } = kv.useSearch();
    return <KvPage key={p} project={p} match={match} k={key} />;
  },
});
const metrics = createRoute({ getParentRoute: () => app, path: "/metrics", component: MetricsPage });
const logs = createRoute({
  getParentRoute: () => app,
  path: "/logs",
  validateSearch: (s: Record<string, unknown>): LogsSearch => ({
    q: str(s.q),
    project: str(s.project),
    since: str(s.since),
    live: s.live === true || s.live === "true" ? true : undefined,
  }),
  component: function Logs() {
    return <LogsPage {...logs.useSearch()} />;
  },
});
const errors = createRoute({
  getParentRoute: () => app,
  path: "/errors",
  validateSearch: (s: Record<string, unknown>): { project?: string; status?: string } => ({ project: str(s.project), status: str(s.status) }),
  component: function Errors() {
    return <ErrorsPage {...errors.useSearch()} />;
  },
});
const issue = createRoute({
  getParentRoute: () => app,
  path: "/errors/$id",
  component: function Issue() {
    const { id } = issue.useParams();
    return <IssuePage key={id} id={id} />;
  },
});
const alerts = createRoute({ getParentRoute: () => app, path: "/alerts", component: AlertsPage });
const backups = createRoute({ getParentRoute: () => app, path: "/backups", component: BackupsPage });
const queues = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues",
  component: function Queues() {
    const { project: p } = queues.useParams();
    return <QueuesPage key={p} project={p} />;
  },
});
const jobs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs",
  validateSearch: (s: Record<string, unknown>): { queue?: string; state?: string } => ({ queue: str(s.queue), state: str(s.state) }),
  component: function Jobs() {
    const { project: p } = jobs.useParams();
    const { queue, state } = jobs.useSearch();
    return <JobsPage key={p} project={p} queue={queue} state={state} />;
  },
});
const job = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/queues/jobs/$id",
  component: function Job() {
    const { project: p, id } = job.useParams();
    return <JobPage key={id} project={p} id={id} />;
  },
});
const workflows = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows",
  validateSearch: (s: Record<string, unknown>): { state?: string } => ({ state: str(s.state) }),
  component: function Workflows() {
    const { project: p } = workflows.useParams();
    return <WorkflowsPage key={p} project={p} state={workflows.useSearch().state} />;
  },
});
const runRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/workflows/$id",
  component: function Run() {
    const { project: p, id } = runRoute.useParams();
    return <RunPage key={id} project={p} id={id} />;
  },
});
const analytics = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/analytics",
  validateSearch: (s: Record<string, unknown>): { period?: string } => ({ period: str(s.period) }),
  component: function Analytics() {
    const { project: p } = analytics.useParams();
    return <AnalyticsPage key={p} project={p} period={analytics.useSearch().period} />;
  },
});
const appsRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps",
  component: function Apps() {
    const { project: p } = appsRoute.useParams();
    return <AppsPage key={p} project={p} />;
  },
});
const appRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app",
  component: function AppView() {
    const { project: p, app: a } = appRoute.useParams();
    return <AppPage key={p + a} project={p} app={a} />;
  },
});
const deployRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app/deploys/$id",
  component: function DeployView() {
    const { project: p, app: a, id } = deployRoute.useParams();
    return <DeployPage key={id} project={p} app={a} id={id} />;
  },
});
const appLogs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/apps/$app/logs",
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
  component: function Users() {
    const { project: p } = users.useParams();
    const { search, page } = users.useSearch();
    return <UsersPage key={p} project={p} search={search} page={page} />;
  },
});
const userRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/users/$id",
  component: function UserView() {
    const { project: p, id } = userRoute.useParams();
    return <UserPage key={id} project={p} id={id} />;
  },
});
const orgs = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs",
  validateSearch: (s: Record<string, unknown>): { search?: string } => ({ search: str(s.search) }),
  component: function Orgs() {
    const { project: p } = orgs.useParams();
    return <OrgsPage key={p} project={p} search={orgs.useSearch().search} />;
  },
});
const orgRoute = createRoute({
  getParentRoute: () => app,
  path: "/projects/$project/orgs/$id",
  component: function OrgView() {
    const { project: p, id } = orgRoute.useParams();
    return <OrgPage key={id} project={p} id={id} />;
  },
});
const protect = createRoute({ getParentRoute: () => app, path: "/protect", component: ProtectPage });
const people = createRoute({ getParentRoute: () => app, path: "/settings/people", component: PeoplePage });
const passkeys = createRoute({ getParentRoute: () => app, path: "/settings/passkeys", component: PasskeysPage });

function NotFound() {
  return (
    <Page>
      <h1 className="sentence text-ink">There’s no page here.</h1>
      <p className="mt-2 text-md text-ink-2">
        The link may be old, or the thing it pointed at was removed.{" "}
        <Link to="/" className="font-[550] text-brass-ink underline underline-offset-4">
          Back to the Box
        </Link>
        .
      </p>
    </Page>
  );
}

const tree = root.addChildren([
  login,
  app.addChildren([
    box,
    activity,
    settings,
    newProject,
    kit,
    change,
    status,
    tokens,
    approvals,
    approval,
    project,
    secrets,
    storage,
    bucket,
    inbox,
    emailSettings,
    data,
    table,
    sqlRoute,
    branches,
    kv,
    metrics,
    logs,
    errors,
    issue,
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
  return createRouter({ routeTree: tree, context: { queryClient }, defaultPreload: "intent", defaultPreloadStaleTime: 0, scrollRestoration: true });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof makeRouter>;
  }
}
