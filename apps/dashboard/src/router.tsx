import { createRootRouteWithContext, createRoute, createRouter, Link, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import type { Tier } from "@/api/client";
import { Shell } from "@/components/shell";
import { ActivityPage, Page, type ActivitySearch } from "@/routes/activity";
import { ChangePage } from "@/routes/change";
import { LoginPage } from "@/routes/login";
import { StatusPage } from "@/routes/status";
import { TokensPage } from "@/routes/tokens";
import { ApprovalPage, ApprovalsPage } from "@/routes/approvals";
import { ProjectPage, SecretsPage } from "@/routes/project";
import { PasskeysPage, PeoplePage } from "@/routes/settings";

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
const activity = createRoute({
  getParentRoute: () => app,
  path: "/",
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
const people = createRoute({ getParentRoute: () => app, path: "/settings/people", component: PeoplePage });
const passkeys = createRoute({ getParentRoute: () => app, path: "/settings/passkeys", component: PasskeysPage });

function NotFound() {
  return (
    <Page>
      <h1 className="display text-3xl text-ink">Nothing in this tin.</h1>
      <p className="mt-2 text-md text-ink-2">
        That page doesn't exist.{" "}
        <Link to="/" search={{}} className="text-brass-ink underline underline-offset-4">
          Back to activity
        </Link>
        .
      </p>
    </Page>
  );
}

const tree = root.addChildren([login, app.addChildren([activity, change, status, tokens, approvals, approval, project, secrets, people, passkeys])]);

export function makeRouter(queryClient: QueryClient) {
  return createRouter({ routeTree: tree, context: { queryClient }, defaultPreload: "intent", defaultPreloadStaleTime: 0, scrollRestoration: true });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof makeRouter>;
  }
}
