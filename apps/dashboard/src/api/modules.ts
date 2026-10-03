// The box modules' API: storage, email, Postgres, Valkey, backups, observe.
// Types come from the generated OpenAPI schema; hidden operations (browser
// upload, mail stream, attachments) are typed by hand.
import { queryOptions } from "@tanstack/react-query";
import { ApiError, request } from "./client";
import type { components } from "./schema";

type S = components["schemas"];
export type StorageInfo = S["StorageInfo"];
export type StorageBucket = S["StorageBucketInfo"];
export type StorageObject = S["StorageObject"];
export type StorageList = S["StorageObjectList"];
export type StorageContent = S["StorageObjectContent"];
export type Presigned = S["StoragePresigned"];
export type TrashEntry = S["StorageTrashEntry"];
export type Uploaded = S["StorageUploaded"];
export type EmailSummary = S["EmailSummary"];
export type EmailDetail = S["EmailDetail"];
export type EmailStatus = S["EmailStatus"];
export type Suppression = S["EmailSuppression"];
export type PgInfo = S["PostgresPGInfo"];
export type PgTable = S["PostgresPGTable"];
export type PgResult = S["PostgresPGSQLResult"];
export type PgStatement = S["PostgresPGStatementResult"];
export type PgBranch = S["PostgresPGBranch"];
export type PgBranchCreated = S["PostgresPGBranchCreated"];
export type PgSnapshot = S["PostgresPGSnapshot"];
export type KVStats = S["ValkeyKVStats"];
export type KVPage = S["ValkeyKVKeyPage"];
export type KVValue = S["ValkeyKVKeyValue"];
export type KVConnection = S["ValkeyKVConnection"];
export type BackupOverview = S["BackupOverview"];
export type Backup = S["Backup"];
export type BackupRestored = S["BackupRestored"];
export type Overview = S["ObserveOverview"];
export type Issue = S["ObserveIssue"];
export type IssueDetail = S["ObserveIssueDetail"];
export type AlertsView = S["ObserveAlertsView"];
export type AlertRule = S["ObserveRule"];
export type AppMetrics = S["ObserveAppMetrics"];
export type LogsResult = S["ObserveLogsResult"];
export type ObserveSettings = S["ObserveSettings"];
export type QueueStats = S["QueueStats"];
export type QueueJob = S["QueueJob"];
export type QueueAttempt = S["QueueAttempt"];
export type QueueTopic = S["QueueTopic"];
export type QueueCron = S["QueueCronInfo"];
export type QueueReplay = S["QueueReplayResult"];
export type QueuePurge = S["QueuePurgeResult"];
export type WorkflowRun = S["QueueRun"];
export type WorkflowStep = S["QueueStep"];
export type WorkflowApproval = S["QueueApproval"];
export type AnalyticsOverview = S["AnalyticsOverview"];
export type AnalyticsRealtime = S["AnalyticsRealtimeView"];
export type AnalyticsEvents = S["AnalyticsEventsView"];
export type AnalyticsSetup = S["AnalyticsSetup"];
export type AnalyticsCount = S["AnalyticsCount"];
export type AnalyticsEvent = S["AnalyticsEventSummary"];
export type Period = NonNullable<NonNullable<import("./schema").operations["analytics-overview"]["parameters"]["query"]>["period"]>;
export type ProtectStatus = S["ProtectStatus"];
export type ProtectDecision = S["ProtectDecision"];
export type ProtectAlert = S["ProtectAlert"];
export type ProtectPatch = S["ProtectSettingsPatch"];
export type Limit = S["ProtectLimit"];

const e = encodeURIComponent;
const P = (project: string) => `/v1/projects/${e(project)}`;
const qs = (o: Record<string, string | number | boolean | undefined>) => {
  const s = Object.entries(o)
    .filter(([, v]) => v !== undefined && v !== "" && v !== false)
    .map(([k, v]) => `${k}=${e(String(v))}`)
    .join("&");
  return s ? `?${s}` : "";
};
const arr = <T>(p: Promise<T[] | null>) => p.then((x) => x ?? []);

export const mod = {
  // storage
  storage: (p: string) => request<StorageInfo>("GET", `${P(p)}/storage`),
  objects: (p: string, bucket: string, prefix: string, cursor?: string) =>
    request<StorageList>("GET", `${P(p)}/storage/buckets/${e(bucket)}/objects${qs({ prefix, delimiter: "/", cursor, limit: 200 })}`),
  object: (p: string, bucket: string, key: string) => request<StorageContent>("GET", `${P(p)}/storage/buckets/${e(bucket)}/object${qs({ key })}`),
  presign: (p: string, bucket: string, key: string, expiresIn = 3600) =>
    request<Presigned>("POST", `${P(p)}/storage/buckets/${e(bucket)}/presign`, { key, method: "GET", expiresIn }),
  deleteObject: (p: string, bucket: string, key: string) => request<void>("DELETE", `${P(p)}/storage/buckets/${e(bucket)}/objects${qs({ key })}`),
  upload: async (p: string, bucket: string, key: string, file: File): Promise<Uploaded> => {
    const fd = new FormData();
    fd.set("key", key);
    fd.set("file", file);
    const res = await fetch(`${P(p)}/storage/buckets/${e(bucket)}/objects`, {
      method: "POST",
      body: fd,
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok)
      throw new ApiError({
        status: res.status,
        code: data.code ?? "internal",
        title: data.title ?? res.statusText,
        detail: data.detail,
        hint: data.hint,
        errors: data.errors,
      });
    return data as Uploaded;
  },
  credentials: (p: string) => request<Record<string, string>>("GET", `${P(p)}/storage/credentials`),
  trash: (p?: string) => arr(request<TrashEntry[] | null>("GET", `/v1/storage/trash${qs({ project: p })}`)),
  purge: (id: string) => request<void>("DELETE", `/v1/storage/trash/${e(id)}`),

  // email
  emailStatus: () => request<EmailStatus>("GET", "/v1/email"),
  messages: (p: string, q?: string, all?: boolean) =>
    arr(request<EmailSummary[] | null>("GET", `${P(p)}/email/messages${qs({ q, all, limit: 200 })}`)),
  message: (p: string, id: string) => request<EmailDetail>("GET", `${P(p)}/email/messages/${e(id)}`),
  deleteMessage: (p: string, id: string) => request<void>("DELETE", `${P(p)}/email/messages/${e(id)}`),
  clearInbox: (p: string) => request<{ deleted: number }>("DELETE", `${P(p)}/email/messages`),
  smtp: (p: string) => request<Record<string, string>>("GET", `${P(p)}/email/smtp`),
  suppressions: (p: string) => arr(request<Suppression[] | null>("GET", `${P(p)}/email/suppressions`)),
  suppress: (p: string, address: string, reason: Suppression["reason"]) =>
    request<Suppression>("POST", `${P(p)}/email/suppressions`, { address, reason }),
  unsuppress: (p: string, address: string) => request<void>("DELETE", `${P(p)}/email/suppressions/${e(address)}`),
  setRelay: (body: S["Email-relay-setRequest"]) => request<S["EmailRelay"]>("PUT", "/v1/email/relay", body),
  testRelay: (to: string) => request<S["Email-relay-testResponse"]>("POST", "/v1/email/relay/test", { to }),
  removeRelay: () => request<void>("DELETE", "/v1/email/relay"),
  attachmentUrl: (p: string, id: string, index: number) => `${P(p)}/email/messages/${e(id)}/attachments/${index}`,
  streamUrl: (p: string) => `${P(p)}/email/stream`,

  // postgres
  pg: (p: string) => request<PgInfo>("GET", `${P(p)}/postgres`),
  pgConnection: (p: string) => request<S["PostgresPGConnection"]>("GET", `${P(p)}/postgres/connection`),
  tables: (p: string, branch?: string) => arr(request<PgTable[] | null>("GET", `${P(p)}/tables${qs({ branch })}`)),
  sql: (p: string, body: S["PostgresPGSQLRequest"]) => request<PgResult>("POST", `${P(p)}/sql`, body),
  branches: (p: string) => arr(request<PgBranch[] | null>("GET", `${P(p)}/branches`)),
  createBranch: (p: string, name: string, from?: string) => request<PgBranchCreated>("POST", `${P(p)}/branches`, { name, ...(from ? { from } : {}) }),
  deleteBranch: (p: string, name: string) => request<void>("DELETE", `${P(p)}/branches/${e(name)}`),
  snapshots: (p: string) => arr(request<PgSnapshot[] | null>("GET", `${P(p)}/snapshots`)),
  restoreSnapshot: (p: string, id: string, confirm?: string) =>
    request<S["PostgresPGSnapshotRestored"]>("POST", `${P(p)}/snapshots/${e(id)}/restore`, confirm ? { confirm } : {}),

  // valkey
  kvStats: (p: string) => request<KVStats>("GET", `${P(p)}/kv/stats`),
  kvKeys: (p: string, match?: string, cursor?: string) => request<KVPage>("GET", `${P(p)}/kv/keys${qs({ match, cursor, count: 200 })}`),
  kvKey: (p: string, key: string) => request<KVValue>("GET", `${P(p)}/kv/key${qs({ key })}`),
  kvConnection: (p: string) => request<KVConnection>("GET", `${P(p)}/kv/connection`),

  // backups
  backups: () => request<BackupOverview>("GET", "/v1/backups"),
  backupNow: (kind: "full" | "incremental") => request<Backup>("POST", "/v1/backups", { kind }),
  restore: (id: string, targets: string[], confirm?: string) =>
    request<BackupRestored>("POST", `/v1/backups/${e(id)}/restore`, { targets, ...(confirm ? { confirm } : {}) }),
  setSchedule: (body: S["Backups-schedule-setRequest"]) => request<S["BackupSchedule"]>("PUT", "/v1/backups/schedule", body),

  // observe
  overview: () => request<Overview>("GET", "/v1/observe/overview"),
  apps: (project?: string, since = "1h") => arr(request<AppMetrics[] | null>("GET", `/v1/observe/apps${qs({ project, since })}`)),
  metrics: (query: string, since: string, step?: string) =>
    request<S["ObserveMetricsResult"]>("POST", "/v1/observe/metrics/query", { query, since, ...(step ? { step } : {}) }),
  logs: (body: S["ObserveLogsQueryBody"]) => request<LogsResult>("POST", "/v1/observe/logs/query", body),
  issues: (o: { project?: string; status?: Issue["status"] }) => arr(request<Issue[] | null>("GET", `/v1/observe/issues${qs({ ...o, limit: 100 })}`)),
  issue: (id: string) => request<IssueDetail>("GET", `/v1/observe/issues/${e(id)}`),
  resolveIssue: (id: string, status: Issue["status"]) => request<Issue>("POST", `/v1/observe/issues/${e(id)}/resolve`, { status }),
  alerts: () => request<AlertsView>("GET", "/v1/observe/alerts?limit=100"),
  rules: () => arr(request<AlertRule[] | null>("GET", "/v1/observe/alert-rules")),
  putRule: (name: string, body: S["ObserveRuleBody"]) => request<AlertRule>("PUT", `/v1/observe/alert-rules/${e(name)}`, body),
  deleteRule: (name: string) => request<void>("DELETE", `/v1/observe/alert-rules/${e(name)}`),
  testAlert: () => request<unknown>("POST", "/v1/observe/alerts/test", {}),
  observeSettings: () => request<ObserveSettings>("GET", "/v1/observe/settings"),
};

export const mod2 = {
  // queues
  queueStats: (p: string) => arr(request<QueueStats[] | null>("GET", `${P(p)}/queue/stats`)),
  jobs: (p: string, o: { queue?: string; state?: QueueJob["state"]; before?: string }) =>
    arr(request<QueueJob[] | null>("GET", `${P(p)}/queue/jobs${qs({ ...o, limit: 100 })}`)),
  job: (p: string, id: string) => request<QueueJob>("GET", `${P(p)}/queue/jobs/${e(id)}`),
  retryJob: (p: string, id: string) => request<QueueJob>("POST", `${P(p)}/queue/jobs/${e(id)}/retry`, {}),
  cancelJob: (p: string, id: string) => request<QueueJob>("POST", `${P(p)}/queue/jobs/${e(id)}/cancel`, {}),
  pause: (p: string, q: string) => request<unknown>("POST", `${P(p)}/queue/queues/${e(q)}/pause`, {}),
  resume: (p: string, q: string) => request<unknown>("POST", `${P(p)}/queue/queues/${e(q)}/resume`, {}),
  purge: (p: string, q: string, confirm?: string, dead?: boolean) =>
    request<QueuePurge>("POST", `${P(p)}/queue/queues/${e(q)}/purge`, { ...(confirm ? { confirm } : {}), ...(dead ? { dead } : {}) }),
  replay: (p: string, dryRun: boolean, queue?: string) =>
    request<QueueReplay>("POST", `${P(p)}/queue/dlq/replay`, { dryRun, ...(queue ? { queue } : {}) }),
  topics: (p: string) => arr(request<QueueTopic[] | null>("GET", `${P(p)}/queue/topics`)),
  crons: (p: string) => arr(request<QueueCron[] | null>("GET", `${P(p)}/queue/crons`)),
  triggerCron: (p: string, name: string) => request<unknown>("POST", `${P(p)}/queue/crons/${e(name)}/trigger`, {}),
  // workflows
  runs: (p: string, o: { workflow?: string; state?: WorkflowRun["state"] }) =>
    arr(request<WorkflowRun[] | null>("GET", `${P(p)}/workflows/runs${qs({ ...o, limit: 100 })}`)),
  run: (p: string, id: string) => request<WorkflowRun>("GET", `${P(p)}/workflows/runs/${e(id)}`),
  cancelRun: (p: string, id: string) => request<WorkflowRun>("POST", `${P(p)}/workflows/runs/${e(id)}/cancel`, {}),
  retryRun: (p: string, id: string) => request<WorkflowRun>("POST", `${P(p)}/workflows/runs/${e(id)}/retry`, {}),
  sendEvent: (p: string, name: string, payload?: unknown) => request<S["QueueEmitResult"]>("POST", `${P(p)}/workflows/events`, { name, payload }),
  wfApprovals: (p: string) => arr(request<WorkflowApproval[] | null>("GET", `${P(p)}/workflows/approvals`)),
  decide: (p: string, id: string, decision: "approve" | "reject", comment?: string) =>
    request<WorkflowApproval>("POST", `${P(p)}/workflows/approvals/${e(id)}`, { decision, ...(comment ? { comment } : {}) }),
  // analytics
  analytics: (p: string, period: Period) => request<AnalyticsOverview>("GET", `/v1/analytics/overview${qs({ project: p, period, limit: 8 })}`),
  realtime: (p: string) => request<AnalyticsRealtime>("GET", `/v1/analytics/realtime${qs({ project: p })}`),
  events: (p: string, period: Period) => request<AnalyticsEvents>("GET", `/v1/analytics/events${qs({ project: p, period })}`),
  analyticsSetup: (p: string) => request<AnalyticsSetup>("GET", `/v1/analytics/setup${qs({ project: p })}`),
  // protection
  protect: () => request<ProtectStatus>("GET", "/v1/protect"),
  setProtect: (patch: ProtectPatch) => request<ProtectStatus>("PUT", "/v1/protect", patch),
  underAttack: (on: boolean, minutes?: number) => request<ProtectStatus>("POST", "/v1/protect/under-attack", { on, ...(minutes ? { minutes } : {}) }),
  decisions: () => arr(request<ProtectDecision[] | null>("GET", "/v1/protect/decisions")),
  protectAlerts: () => arr(request<ProtectAlert[] | null>("GET", "/v1/protect/alerts?limit=50")),
  ban: (ip: string, duration: string, reason: string) => request<unknown>("POST", "/v1/protect/bans", { ip, duration, reason }),
  unban: (ip: string) => request<S["Protect-unbanResponse"]>("POST", "/v1/protect/unban", { ip }),
};

export const mq = {
  storage: (p: string) => queryOptions({ queryKey: ["storage", p], queryFn: () => mod.storage(p), refetchInterval: 30_000 }),
  objects: (p: string, b: string, prefix: string) => queryOptions({ queryKey: ["objects", p, b, prefix], queryFn: () => mod.objects(p, b, prefix) }),
  trash: (p?: string) => queryOptions({ queryKey: ["trash", p ?? ""], queryFn: () => mod.trash(p) }),
  emailStatus: queryOptions({ queryKey: ["email-status"], queryFn: mod.emailStatus }),
  messages: (p: string, q: string) => queryOptions({ queryKey: ["messages", p, q], queryFn: () => mod.messages(p, q || undefined, true) }),
  message: (p: string, id: string) => queryOptions({ queryKey: ["message", p, id], queryFn: () => mod.message(p, id), staleTime: Infinity }),
  suppressions: (p: string) => queryOptions({ queryKey: ["suppressions", p], queryFn: () => mod.suppressions(p) }),
  pg: (p: string) => queryOptions({ queryKey: ["pg", p], queryFn: () => mod.pg(p) }),
  tables: (p: string, branch?: string) => queryOptions({ queryKey: ["tables", p, branch ?? ""], queryFn: () => mod.tables(p, branch) }),
  branches: (p: string) => queryOptions({ queryKey: ["branches", p], queryFn: () => mod.branches(p) }),
  snapshots: (p: string) => queryOptions({ queryKey: ["snapshots", p], queryFn: () => mod.snapshots(p) }),
  kvStats: (p: string) => queryOptions({ queryKey: ["kv-stats", p], queryFn: () => mod.kvStats(p), refetchInterval: 15_000 }),
  backups: queryOptions({ queryKey: ["backups"], queryFn: mod.backups, refetchInterval: 10_000 }),
  overview: queryOptions({ queryKey: ["overview"], queryFn: mod.overview, refetchInterval: 15_000 }),
  issues: (project?: string, status?: Issue["status"]) =>
    queryOptions({ queryKey: ["issues", project ?? "", status ?? ""], queryFn: () => mod.issues({ project, status }), refetchInterval: 30_000 }),
  issue: (id: string) => queryOptions({ queryKey: ["issue", id], queryFn: () => mod.issue(id) }),
  alerts: queryOptions({ queryKey: ["alerts"], queryFn: mod.alerts, refetchInterval: 30_000 }),
  rules: queryOptions({ queryKey: ["rules"], queryFn: mod.rules }),
  // Polled by the shell for the alarm state; stops when the box has no protection module.
  protect: queryOptions({ queryKey: ["protect"], queryFn: mod2.protect, retry: false, refetchInterval: (q) => (q.state.error ? false : 20_000) }),
  wfApprovals: (p: string) => queryOptions({ queryKey: ["wf-approvals", p], queryFn: () => mod2.wfApprovals(p), refetchInterval: 15_000 }),
  runs: (p: string, state?: WorkflowRun["state"]) =>
    queryOptions({ queryKey: ["runs", p, state ?? ""], queryFn: () => mod2.runs(p, { state }), refetchInterval: 10_000 }),
  run: (p: string, id: string) => queryOptions({ queryKey: ["run", p, id], queryFn: () => mod2.run(p, id), refetchInterval: 5_000 }),
  crons: (p: string) => queryOptions({ queryKey: ["crons", p], queryFn: () => mod2.crons(p), refetchInterval: 30_000 }),
  topics: (p: string) => queryOptions({ queryKey: ["topics", p], queryFn: () => mod2.topics(p) }),
};
