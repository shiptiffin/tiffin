// The box modules' API: storage, email, Postgres, Valkey, backups, observe.
// Types come from the generated OpenAPI schema; hidden operations (browser
// upload, mail stream, attachments) are typed by hand.
import { queryOptions } from "@tanstack/react-query";
import { pagedQuery } from "@/lib/paged";
import { ApiError, request } from "./client";
import type { components, operations } from "./schema";

type S = components["schemas"];
type O = operations;
export type StorageInfo = S["StorageInfo"];
export type StorageBucket = S["StorageBucketInfo"];
export type StorageObject = S["StorageObject"];
export type StorageList = S["StorageObjectList"];
export type StorageContent = S["StorageObjectContent"];
export type Presigned = S["StoragePresigned"];
export type TrashEntry = S["StorageTrashEntry"];
export type Uploaded = S["StorageUploaded"];
export type FileLink = S["StorageFileLink"];
export type FilesResult = S["StorageFilesResult"];
export type UploadSession = S["StorageUploadSession"];
/** The Files console's changes to files, by path under the bucket, with their bodies. */
export type FilesWrites = {
  move: S["Storage-objects-moveRequest"];
  delete: S["Storage-objects-deleteRequest"];
};
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
export type KVTree = S["ValkeyKVTree"];
export type KVKeyInfo = S["ValkeyKVKeyInfo"];
export type KVWrite = S["ValkeyKVWriteResult"];
export type KVCommandResult = S["ValkeyKVCommandResult"];
/** The KV write operations, by path under /kv/, with their bodies. */
export type KVWrites = {
  set: S["ValkeySetBody"];
  "hash/set": S["ValkeyHashSetBody"];
  "hash/delete": S["ValkeyHashDelBody"];
  "list/push": S["ValkeyListPushBody"];
  "list/set": S["ValkeyListSetBody"];
  "list/remove": S["ValkeyListRemoveBody"];
  "set/add": S["ValkeySetAddBody"];
  "set/remove": S["ValkeySetRemoveBody"];
  "zset/add": S["ValkeyZsetAddBody"];
  "zset/remove": S["ValkeyZsetRemoveBody"];
  "zset/incr": S["ValkeyZsetIncrBody"];
  "stream/add": S["ValkeyStreamAddBody"];
  "stream/trim": S["ValkeyStreamTrimBody"];
  "stream/delete": S["ValkeyStreamDelBody"];
  expire: S["ValkeyExpireBody"];
  rename: S["ValkeyRenameBody"];
  delete: S["ValkeyDelBody"];
  "delete-prefix": S["ValkeyDeletePrefixBody"];
  undo: S["ValkeyUndoBody"];
};
export type BackupOverview = S["BackupOverview"];
export type Backup = S["Backup"];
export type BackupRestored = S["BackupRestored"];
export type BackupDrill = S["BackupDrill"];
export type BackupOffsite = S["BackupOffsite"];
export type BackupOffsiteTest = S["BackupOffsiteTest"];
export type OffsiteInput = S["BackupOffsiteInput"];
export type Overview = S["ObserveOverview"];
export type Issue = S["ObserveIssue"];
export type IssueDetail = S["ObserveIssueDetail"];
export type AlertsView = S["ObserveAlertsView"];
export type AlertRule = S["ObserveRule"];
export type AppMetrics = S["ObserveAppMetrics"];
export type LogsResult = S["ObserveLogsResult"];
export type ObserveSettings = S["ObserveSettings"];
export type OutsideCheck = S["Monitor"];
export type TraceSummary = S["ObserveTraceSummary"];
export type TraceDetail = S["ObserveTraceDetail"];
export type TraceSpan = S["ObserveSpan"];
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
export type AnalyticsVitals = S["AnalyticsVitalsView"];
export type Period = NonNullable<NonNullable<import("./schema").operations["analytics-overview"]["parameters"]["query"]>["period"]>;
/** What an analytics read can ask for: a period or days, one app, the step and filters. */
export type AnalyticsQuery = Omit<NonNullable<import("./schema").operations["analytics-overview"]["parameters"]["query"]>, "project" | "limit">;
export type AnalyticsFilters = S["AnalyticsFilters"];
export type UsageHistory = S["ObserveUsageHistory"];
export type ProtectStatus = S["ProtectStatus"];
export type ProtectDecision = S["ProtectDecision"];
export type ProtectAlert = S["ProtectAlert"];
export type ProtectPatch = S["ProtectSettingsPatch"];
export type Limit = S["ProtectLimit"];
export type AppRuntime = S["RuntimeAppRuntime"];
export type EnvStatus = S["RuntimeEnvStatus"];
export type Deploy = S["RuntimeDeploy"];
export type LogLine = S["RuntimeLogLine"];
export type AuthOverview = S["AuthOverview"];
export type AuthUser = S["AuthUser"];
export type AuthUserDetail = S["AuthUserDetail"];
export type AuthOrg = S["AuthOrg"];
export type AuthOrgDetail = S["AuthOrgDetail"];

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
  objects: (p: string, bucket: string, prefix: string, cursor?: string, o: { flat?: boolean; limit?: number } = {}) =>
    request<StorageList>("GET", `${P(p)}/storage/buckets/${e(bucket)}/objects${qs({ prefix, delimiter: o.flat ? undefined : "/", cursor, limit: o.limit ?? 200 })}`),
  /** A file's bytes on the dashboard's own origin (thumbnails, previews, downloads). */
  fileUrl: (p: string, bucket: string, key: string, o: { w?: number; q?: number; f?: string; download?: boolean } = {}) =>
    `${P(p)}/storage/buckets/${e(bucket)}/file${qs({ key, w: o.w, q: o.q, f: o.f, download: o.download })}`,
  fileLink: (p: string, bucket: string, body: S["Storage-linkRequest"]) => request<FileLink>("POST", `${P(p)}/storage/buckets/${e(bucket)}/link`, body),
  filesWrite: <K extends keyof FilesWrites>(p: string, bucket: string, op: K, body: FilesWrites[K]) =>
    request<FilesResult>("POST", `${P(p)}/storage/buckets/${e(bucket)}/${op}`, body),
  filesUndo: (p: string, id: string) => request<FilesResult>("POST", `${P(p)}/storage/undo`, { id }),
  uploadStart: (p: string, bucket: string, body: S["Storage-upload-startRequest"]) =>
    request<UploadSession>("POST", `${P(p)}/storage/buckets/${e(bucket)}/uploads`, body),
  uploadPartUrl: (p: string, bucket: string, uploadId: string, n: number, key: string) =>
    `${P(p)}/storage/buckets/${e(bucket)}/uploads/${e(uploadId)}/parts/${n}${qs({ key })}`,
  uploadComplete: (p: string, bucket: string, uploadId: string, key: string) =>
    request<Uploaded>("POST", `${P(p)}/storage/buckets/${e(bucket)}/uploads/${e(uploadId)}/complete`, { key }),
  uploadAbort: (p: string, bucket: string, uploadId: string, key: string) =>
    request<void>("DELETE", `${P(p)}/storage/buckets/${e(bucket)}/uploads/${e(uploadId)}${qs({ key })}`),
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
  /** One page of mail, newest first, searched and narrowed on the box. */
  messages: (p: string, o: { q?: string; all?: boolean; status?: EmailSummary["status"]; cursor?: string; limit?: number } = {}, signal?: AbortSignal) =>
    request<S["PageEmailSummary"]>("GET", `${P(p)}/email/messages${qs({ ...o, limit: o.limit ?? 50 })}`, undefined, signal),
  /** The latest mail (one page of up to 200), for summaries. */
  recentMail: async (p: string) => (await mod.messages(p, { all: true, limit: 200 })).items,
  message: (p: string, id: string) => request<EmailDetail>("GET", `${P(p)}/email/messages/${e(id)}`),
  deleteMessage: (p: string, id: string) => request<void>("DELETE", `${P(p)}/email/messages/${e(id)}`),
  clearInbox: (p: string) => request<{ deleted: number }>("DELETE", `${P(p)}/email/messages`),
  smtp: (p: string) => request<Record<string, string>>("GET", `${P(p)}/email/smtp`),
  suppressions: (p: string) => arr(request<Suppression[] | null>("GET", `${P(p)}/email/suppressions`)),
  suppress: (p: string, address: string, reason: Suppression["reason"]) =>
    request<Suppression>("POST", `${P(p)}/email/suppressions`, { address, reason }),
  unsuppress: (p: string, address: string) => request<void>("DELETE", `${P(p)}/email/suppressions/${e(address)}`),
  setRelay: (body: S["Email-relay-setRequest"]) => request<S["EmailRelay"]>("PUT", "/v1/email/relay", body),
  testRelay: (to: string) => request<S["EmailRelayTestResult"]>("POST", "/v1/email/relay/test", { to }),
  removeRelay: () => request<void>("DELETE", "/v1/email/relay"),
  attachmentUrl: (p: string, id: string, index: number) => `${P(p)}/email/messages/${e(id)}/attachments/${index}`,
  streamUrl: (p: string) => `${P(p)}/email/stream`,

  // postgres
  pg: (p: string) => request<PgInfo>("GET", `${P(p)}/postgres`),
  pgConnection: (p: string) => request<S["PostgresPGConnection"]>("GET", `${P(p)}/postgres/connection`),
  tables: (p: string, branch?: string) => arr(request<PgTable[] | null>("GET", `${P(p)}/tables${qs({ branch })}`)),
  /** Read-only: one statement in a READ ONLY transaction. */
  sql: (p: string, body: S["PostgresPGSQLRequest"]) => request<PgResult>("POST", `${P(p)}/sql`, body),
  /** Writes (DDL and DML), after a snapshot. Needs apply:irreversible. */
  sqlWrite: (p: string, body: S["PostgresPGSQLRequest"]) => request<PgResult>("POST", `${P(p)}/sql/write`, body),
  branches: (p: string) => arr(request<PgBranch[] | null>("GET", `${P(p)}/branches`)),
  createBranch: (p: string, name: string, from?: string) => request<PgBranchCreated>("POST", `${P(p)}/branches`, { name, ...(from ? { from } : {}) }),
  deleteBranch: (p: string, name: string) => request<void>("DELETE", `${P(p)}/branches/${e(name)}`),
  snapshots: (p: string) => arr(request<PgSnapshot[] | null>("GET", `${P(p)}/snapshots`)),
  restoreSnapshot: (p: string, id: string, confirm?: string) =>
    request<S["PostgresPGSnapshotRestored"]>("POST", `${P(p)}/snapshots/${e(id)}/restore`, confirm ? { confirm } : {}),

  // valkey
  kvStats: (p: string) => request<KVStats>("GET", `${P(p)}/kv/stats`),
  kvKeys: (p: string, match?: string, cursor?: string) => request<KVPage>("GET", `${P(p)}/kv/keys${qs({ match, cursor, count: 200 })}`),
  kvKey: (p: string, key: string, o: { cursor?: string; match?: string; count?: number } = {}) =>
    request<KVValue>("GET", `${P(p)}/kv/key${qs({ key, ...o })}`),
  kvTree: (p: string, o: { prefix?: string; delimiter?: string; match?: string; type?: string; expiry?: string; offset?: number; limit?: number }) =>
    request<KVTree>("GET", `${P(p)}/kv/tree${qs(o)}`),
  /** Secrets only with reveal, which needs full access to the project. */
  kvConnection: (p: string, reveal = false) => request<KVConnection>("GET", `${P(p)}/kv/connection${qs({ reveal })}`),
  /** Every write answers an undo id; one too big to undo answers 428 with a confirm value first. */
  kvWrite: <K extends keyof KVWrites>(p: string, op: K, body: KVWrites[K]) => request<KVWrite>("POST", `${P(p)}/kv/${op}`, body),
  kvCommand: (p: string, commands: string, write: boolean) =>
    request<S["ValkeyKVConsole"]>("POST", `${P(p)}/kv/command`, { commands, ...(write ? { write } : {}) }),

  // backups
  backups: () => request<BackupOverview>("GET", "/v1/backups"),
  backupNow: (kind: "full" | "incremental") => request<Backup>("POST", "/v1/backups", { kind }),
  /** Restores a set; with time (id "latest"), Postgres to that moment and the rest to the newest set before it. */
  restore: (id: string, targets: string[], confirm?: string, time?: string) =>
    request<BackupRestored>("POST", `/v1/backups/${e(id)}/restore`, { targets, ...(time ? { time } : {}), ...(confirm ? { confirm } : {}) }),
  setSchedule: (body: S["Backups-schedule-setRequest"]) => request<S["BackupSchedule"]>("PUT", "/v1/backups/schedule", body),
  /** Starts a restore drill of the newest good backup, or of one backup; poll drillGet until it isn't running. */
  drill: (backup?: string) => request<BackupDrill>("POST", backup ? `/v1/backups/${e(backup)}/drill` : "/v1/backups/drill", {}),
  drillGet: (id: string) => request<BackupDrill>("GET", `/v1/backups/drills/${e(id)}`),
  drillCancel: (id: string) => request<BackupDrill>("POST", `/v1/backups/drills/${e(id)}/cancel`, {}),
  /** Copies off the box: set (tested first; a new destination returns its passphrase once), test, copy now, off. */
  offsiteSet: (body: OffsiteInput) => request<BackupOffsite>("PUT", "/v1/backups/offsite", body),
  offsiteTest: () => request<BackupOffsiteTest>("POST", "/v1/backups/offsite/test", {}),
  offsiteCopy: () => request<S["BackupOffsiteCopy"]>("POST", "/v1/backups/offsite/copy", {}),
  offsiteOff: () => request<BackupOffsite>("DELETE", "/v1/backups/offsite"),

  // observe
  overview: () => request<Overview>("GET", "/v1/observe/overview"),
  apps: (project?: string, since = "1h") => arr(request<AppMetrics[] | null>("GET", `/v1/observe/apps${qs({ project, since })}`)),
  metrics: (query: string, since: string, step?: string) =>
    request<S["ObserveMetricsResult"]>("POST", "/v1/observe/metrics/query", { query, since, ...(step ? { step } : {}) }),
  logs: (body: S["ObserveLogsQueryBody"]) => request<LogsResult>("POST", "/v1/observe/logs/query", body),
  issues: (o: { project?: string; status?: Issue["status"] }) => arr(request<Issue[] | null>("GET", `/v1/observe/issues${qs({ ...o, limit: 100 })}`)),
  issue: (id: string) => request<IssueDetail>("GET", `/v1/observe/issues/${e(id)}`),
  traces: (project: string, o: { since?: string; errors?: boolean; sort?: "slowest" | "recent" }) =>
    arr(request<TraceSummary[] | null>("GET", `/v1/observe/traces${qs({ project, ...o, limit: 100 })}`)),
  trace: (project: string, id: string) => request<TraceDetail>("GET", `/v1/observe/traces/${e(id)}${qs({ project })}`),
  resolveIssue: (id: string, status: Issue["status"]) => request<Issue>("POST", `/v1/observe/issues/${e(id)}/resolve`, { status }),
  alerts: () => request<AlertsView>("GET", "/v1/observe/alerts?limit=100"),
  rules: () => arr(request<AlertRule[] | null>("GET", "/v1/observe/alert-rules")),
  putRule: (name: string, body: S["ObserveRuleBody"]) => request<AlertRule>("PUT", `/v1/observe/alert-rules/${e(name)}`, body),
  deleteRule: (name: string) => request<void>("DELETE", `/v1/observe/alert-rules/${e(name)}`),
  testAlert: () => request<unknown>("POST", "/v1/observe/alerts/test", {}),
  observeSettings: () => request<ObserveSettings>("GET", "/v1/observe/settings"),
  setObserveSettings: (body: S["ObserveSettingsBody"]) => request<ObserveSettings>("PUT", "/v1/observe/settings", body),
  // the outside check (heartbeat)
  monitor: () => request<OutsideCheck>("GET", "/v1/monitor"),
  monitorSet: (url: string) => request<OutsideCheck>("PUT", "/v1/monitor", { url }),
  monitorTest: () => request<OutsideCheck>("POST", "/v1/monitor/test", {}),
  monitorOff: () => request<OutsideCheck>("DELETE", "/v1/monitor"),
};

/** What a jobs list can narrow to on the box. */
export type JobFilter = { queue?: string; state?: QueueJob["state"][]; kind?: Array<"job" | "cron" | "workflow">; q?: string };
/** What a workflow runs list can narrow to on the box. */
export type RunFilter = { workflow?: string; state?: WorkflowRun["state"][]; q?: string };

export const mod2 = {
  // queues
  queueStats: (p: string) => arr(request<QueueStats[] | null>("GET", `${P(p)}/queue/stats`)),
  /** One page of jobs, newest first, narrowed on the box. */
  jobs: (p: string, f: JobFilter & { cursor?: string; limit?: number } = {}, signal?: AbortSignal) =>
    request<S["PageQueueJob"]>(
      "GET",
      `${P(p)}/queue/jobs${qs({ queue: f.queue, state: f.state?.join(","), kind: f.kind?.join(","), q: f.q, cursor: f.cursor, limit: f.limit ?? 50 })}`,
      undefined,
      signal,
    ),
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
  /** One page of workflow runs, newest first, narrowed on the box. */
  runs: (p: string, f: RunFilter & { cursor?: string; limit?: number } = {}, signal?: AbortSignal) =>
    request<S["PageQueueRun"]>("GET", `${P(p)}/workflows/runs${qs({ workflow: f.workflow, state: f.state?.join(","), q: f.q, cursor: f.cursor, limit: f.limit ?? 50 })}`, undefined, signal),
  run: (p: string, id: string) => request<WorkflowRun>("GET", `${P(p)}/workflows/runs/${e(id)}`),
  cancelRun: (p: string, id: string) => request<WorkflowRun>("POST", `${P(p)}/workflows/runs/${e(id)}/cancel`, {}),
  retryRun: (p: string, id: string) => request<WorkflowRun>("POST", `${P(p)}/workflows/runs/${e(id)}/retry`, {}),
  sendEvent: (p: string, name: string, payload?: unknown) => request<S["QueueEmitResult"]>("POST", `${P(p)}/workflows/events`, { name, payload }),
  wfApprovals: (p: string) => arr(request<WorkflowApproval[] | null>("GET", `${P(p)}/workflows/approvals`)),
  decide: (p: string, id: string, decision: "approve" | "reject", comment?: string) =>
    request<WorkflowApproval>("POST", `${P(p)}/workflows/approvals/${e(id)}`, { decision, ...(comment ? { comment } : {}) }),
  // analytics
  analytics: (p: string, period: Period) => request<AnalyticsOverview>("GET", `/v1/analytics/overview${qs({ project: p, period, limit: 8 })}`),
  /** The overview for a period or day range, one app and filters (see AnalyticsQuery). */
  analyticsView: (p: string, q: AnalyticsQuery, limit = 10) => request<AnalyticsOverview>("GET", `/v1/analytics/overview${qs({ project: p, ...q, limit })}`),
  realtime: (p: string, app?: string) => request<AnalyticsRealtime>("GET", `/v1/analytics/realtime${qs({ project: p, app })}`),
  events: (p: string, q: AnalyticsQuery) => request<AnalyticsEvents>("GET", `/v1/analytics/events${qs({ project: p, ...q, interval: undefined })}`),
  vitals: (p: string, q: AnalyticsQuery) => request<AnalyticsVitals>("GET", `/v1/analytics/vitals${qs({ project: p, period: q.period, from: q.from, to: q.to, app: q.app, page: q.page, limit: 8 })}`),
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

export const mod3 = {
  /** Memory, CPU, traffic and data over time, for the project or one app. */
  usageHistory: (p: string, range: "1h" | "24h" | "7d" | "30d", app?: string) => request<UsageHistory>("GET", `${P(p)}/usage/history${qs({ range, app })}`),
  // runtime
  runtime: (p: string, app: string) => request<AppRuntime>("GET", `${P(p)}/apps/${e(app)}/runtime`),
  deploys: (p: string, app: string, preview?: string) =>
    request<S["RuntimeDeployList"]>("GET", `${P(p)}/apps/${e(app)}/deploys${qs({ preview, limit: 50 })}`).then((r) => r.deploys ?? []),
  deploy: (p: string, app: string, id: string) => request<Deploy>("GET", `${P(p)}/apps/${e(app)}/deploys/${e(id)}`),
  buildLog: (p: string, app: string, id: string, offset = 0) =>
    request<S["RuntimeBuildLog"]>("GET", `${P(p)}/apps/${e(app)}/deploys/${e(id)}/build-log${qs({ offset })}`),
  buildLogStream: (p: string, app: string, id: string, offset: number) =>
    `${P(p)}/apps/${e(app)}/deploys/${e(id)}/build-log${qs({ offset, follow: true })}`,
  rollback: (p: string, app: string, id: string) => request<Deploy>("POST", `${P(p)}/apps/${e(app)}/deploys/${e(id)}/rollback`, {}),
  restart: (p: string, app: string, preview?: string) => request<unknown>("POST", `${P(p)}/apps/${e(app)}/restart${qs({ preview })}`, {}),
  previews: (p: string, app: string) => arr(request<EnvStatus[] | null>("GET", `${P(p)}/apps/${e(app)}/previews`)),
  deletePreview: (p: string, app: string, name: string) => request<void>("DELETE", `${P(p)}/apps/${e(app)}/previews/${e(name)}`),
  sleepPreview: (p: string, app: string, name: string) => request<unknown>("POST", `${P(p)}/apps/${e(app)}/previews/${e(name)}/sleep`, {}),
  /** Starts the project's sleeping apps (or one) and answers once they are up. */
  wake: (p: string, app?: string) => arr(request<S["RuntimeWakeResult"][] | null>("POST", `${P(p)}/wake${qs({ app })}`, {})),
  appLogs: (p: string, app: string, o: { since?: string; preview?: string; deploy?: string }) =>
    request<S["RuntimeLogPage"]>("GET", `${P(p)}/apps/${e(app)}/logs${qs({ ...o, limit: 500 })}`),
  appLogStream: (p: string, app: string, o: { preview?: string; since?: string }) => `${P(p)}/apps/${e(app)}/logs${qs({ ...o, follow: true })}`,
  git: (p: string) => request<S["RuntimeGitInfo"]>("GET", `${P(p)}/git`),
  // auth (the project's own end users)
  auth: (p: string) => request<AuthOverview>("GET", `${P(p)}/auth`),
  /** One page of the project's users, newest first, searched on the box. */
  users: (p: string, search: string, cursor?: string, signal?: AbortSignal) =>
    request<S["PageAuthUser"]>("GET", `${P(p)}/auth/users${qs({ search, cursor, limit: 50 })}`, undefined, signal),
  user: (p: string, id: string) => request<AuthUserDetail>("GET", `${P(p)}/auth/users/${e(id)}`),
  ban: (p: string, id: string, reason: string) => request<S["AuthBanResult"]>("POST", `${P(p)}/auth/users/${e(id)}/ban`, reason ? { reason } : {}),
  unban: (p: string, id: string) => request<S["AuthBanResult"]>("POST", `${P(p)}/auth/users/${e(id)}/unban`, {}),
  revokeSessions: (p: string, id: string) => request<S["AuthRevokeResult"]>("POST", `${P(p)}/auth/users/${e(id)}/sessions/revoke`, {}),
  /** One page of the project's organizations, newest first, searched on the box. */
  orgs: (p: string, search: string, cursor?: string, signal?: AbortSignal) =>
    request<S["PageAuthOrg"]>("GET", `${P(p)}/auth/orgs${qs({ search, cursor, limit: 50 })}`, undefined, signal),
  org: (p: string, id: string) => request<AuthOrgDetail>("GET", `${P(p)}/auth/orgs/${e(id)}`),
};

export const mq = {
  storage: (p: string) => queryOptions({ queryKey: ["storage", p], queryFn: () => mod.storage(p), refetchInterval: 30_000 }),
  objects: (p: string, b: string, prefix: string) => queryOptions({ queryKey: ["objects", p, b, prefix], queryFn: () => mod.objects(p, b, prefix) }),
  trash: (p?: string) => queryOptions({ queryKey: ["trash", p ?? ""], queryFn: () => mod.trash(p) }),
  emailStatus: queryOptions({ queryKey: ["email-status"], queryFn: mod.emailStatus }),
  /** Mail a page at a time (the inbox), searched and narrowed on the box. */
  messagePages: (p: string, q: string, status?: EmailSummary["status"]) =>
    pagedQuery(["messages", p, "pages", q, status ?? ""], (cursor, signal) => mod.messages(p, { q: q || undefined, all: true, status, cursor }, signal)),
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
  traces: (project: string, since: string, errors: boolean) =>
    queryOptions({ queryKey: ["traces", project, since, errors], queryFn: () => mod.traces(project, { since, errors: errors || undefined }), enabled: !!project, refetchInterval: 30_000 }),
  trace: (project: string, id: string) => queryOptions({ queryKey: ["trace", project, id], queryFn: () => mod.trace(project, id), staleTime: Infinity }),
  alerts: queryOptions({ queryKey: ["alerts"], queryFn: mod.alerts, refetchInterval: 30_000 }),
  rules: queryOptions({ queryKey: ["rules"], queryFn: mod.rules }),
  monitor: queryOptions({ queryKey: ["monitor"], queryFn: mod.monitor, refetchInterval: 30_000 }),
  // Polled by the shell for the alarm state; stops when the box has no protection module.
  protect: queryOptions({ queryKey: ["protect"], queryFn: mod2.protect, retry: false, refetchInterval: (q) => (q.state.error ? false : 20_000) }),
  wfApprovals: (p: string) => queryOptions({ queryKey: ["wf-approvals", p], queryFn: () => mod2.wfApprovals(p), refetchInterval: 15_000 }),
  run: (p: string, id: string) => queryOptions({ queryKey: ["run", p, id], queryFn: () => mod2.run(p, id), refetchInterval: 5_000 }),
  crons: (p: string) => queryOptions({ queryKey: ["crons", p], queryFn: () => mod2.crons(p), refetchInterval: 30_000 }),
  topics: (p: string) => queryOptions({ queryKey: ["topics", p], queryFn: () => mod2.topics(p) }),
};

// ------------------------------------------------------------------ deployments (project-wide)

export type ProjectDeployList = S["RuntimeProjectDeployList"];
/** What the project's Deployments list can narrow to. */
export type DeployFilter = NonNullable<O["project-deploys"]["parameters"]["query"]>;

export const deploysApi = {
  /** Every app's deploys in one list, newest first; `next` pages back. */
  projectDeploys: (p: string, f: DeployFilter = {}) =>
    request<ProjectDeployList>("GET", `${P(p)}/deploys${qs({ ...f, env: f.env === "all" ? undefined : f.env, limit: f.limit ?? 100 })}`).then((r) => ({ deploys: r.deploys ?? [], next: r.next })),
  /** One deploy's runtime log lines, followed as server-sent events. */
  deployLogStream: (p: string, app: string, deploy: string, o: { preview?: string; since?: string }) => `${P(p)}/apps/${e(app)}/logs${qs({ ...o, deploy, follow: true })}`,
};

// ------------------------------------------------------------------ DNS records (a domain's zone)

export type ZoneRecords = S["DomainsZoneRecords"];
export type ZoneRecord = S["DomainsZoneRecord"];
export type DnsLookup = S["DomainsDNSLookup"];
export type DnsLookupType = NonNullable<NonNullable<O["dns-lookup"]["parameters"]["query"]>["type"]>;
type DnsRecordInput = S["Record"];

export const dnsApi = {
  /** Every record in the zone that holds `name`, from the connected DNS provider. 412: no provider holds it (admins only). */
  records: (name: string) => request<ZoneRecords>("GET", `/v1/dns/records${qs({ name })}`),
  /** Each name and type ends up with exactly these values: send every value you want to keep. */
  setRecords: (records: DnsRecordInput[]) => request<S["Dns-records-setResponse"]>("PUT", "/v1/dns/records", { records } satisfies S["Dns-records-setRequest"]),
  /** Deletes exactly these (name, type, value); 409 for records the box relies on. */
  deleteRecords: (records: DnsRecordInput[]) =>
    request<S["Dns-records-deleteResponse"]>("POST", "/v1/dns/records/delete", { records } satisfies S["Dns-records-deleteRequest"]),
  /** What public DNS answers now (the box's resolvers). */
  lookup: (name: string, type: DnsLookupType) => request<DnsLookup>("GET", `/v1/dns/lookup${qs({ name, type })}`),
};

// ------------------------------------------------------------------ email relay providers and delivery events (box-wide)

export type EmailPreset = S["EmailPreset"];
export type EmailWebhook = S["EmailWebhook"];
export type EmailEvent = S["EmailEvent"];
export type EmailProvider = EmailPreset["id"];
export type EmailEventsProvider = EmailWebhook["provider"];

export const emailApi = {
  /** The mail services the box can fill in, with where to get keys and how events come back. */
  providers: () => arr(request<EmailPreset[] | null>("GET", "/v1/email/providers")),
  /** Point the box at a relay; with a provider, only the key (plus region or username for some) is needed. */
  setRelay: (body: S["Email-relay-setRequest"]) => request<EmailStatus>("PUT", "/v1/email/relay", body),
  /** Save the key the box checks a provider's event requests with. Postmark: the box makes it, and secretUrl comes back once. */
  setWebhook: (provider: EmailEventsProvider, key?: string) =>
    request<S["EmailWebhookOut"]>("PUT", `/v1/email/webhooks/${e(provider)}`, key ? { key } : {}),
  removeWebhook: (provider: EmailEventsProvider) => request<void>("DELETE", `/v1/email/webhooks/${e(provider)}`),
};

export const emailQ = {
  providers: queryOptions({ queryKey: ["email-providers"], queryFn: emailApi.providers, staleTime: Infinity }),
};

// ------------------------------------------------------------------ sign-in providers (box-wide keys for every project's apps)

export type BoxProviders = S["AuthBoxProviders"];
export type BoxProvider = S["AuthBoxProviderState"];
export type ProviderState = S["AuthProviderState"];
/** What the box owner sets for a provider; omit clientSecret/privateKey to keep the stored one. */
export type BoxProviderInput = S["Auth-provider-setRequest"];

export const signInApi = {
  /** Every provider with its box-wide setup (never a secret) and the one callback URL. */
  providers: () => request<BoxProviders>("GET", "/v1/auth/providers"),
  /** Box admins only. */
  set: (id: string, body: BoxProviderInput) => request<BoxProvider>("PUT", `/v1/auth/providers/${e(id)}`, body),
  /** Box admins only. Projects in usedBy lose the button until they get keys again. */
  remove: (id: string) => request<BoxProvider>("DELETE", `/v1/auth/providers/${e(id)}`),
  /** This app's own keys, saved as the project's secrets (one change, with Undo). Empty fields keep stored values. */
  setAppKeys: (project: string, id: string, body: S["AppKeysInBody"]) =>
    request<ProviderState>("PUT", `/v1/projects/${e(project)}/auth/providers/${e(id)}/keys`, body),
  /** Back to the box-wide keys: deletes the project's own secrets for the provider. */
  removeAppKeys: (project: string, id: string) => request<ProviderState>("DELETE", `/v1/projects/${e(project)}/auth/providers/${e(id)}/keys`),
  /** The app's current redirect URI is registered with the provider. */
  confirmCallback: (project: string, id: string) => request<ProviderState>("POST", `/v1/projects/${e(project)}/auth/providers/${e(id)}/callback-confirm`, {}),
};

export const signInQ = {
  providers: queryOptions({ queryKey: ["signin-providers"], queryFn: signInApi.providers, staleTime: 30_000 }),
};

// ------------------------------------------------------------------ the box's own mail, email sign-in, sending domains

export type BoxMailResult = S["BoxMailResult"];
export type BoxSender = S["EmailBoxSender"];
export type BoxMessage = EmailSummary;
export type SendingDomain = S["EmailSendingDomain"];
export type SendingRecord = S["EmailSendingRecord"];
export type SendingView = S["EmailSendingView"];

const SD = (p: string) => `${P(p)}/email/sending-domain`;

export const boxMail = {
  sender: () => request<BoxSender>("GET", "/v1/email/box"),
  setSender: (body: S["Email-box-setRequest"]) => request<BoxSender>("PUT", "/v1/email/box", body),
  /** One page of the box's own mail, newest first. */
  messages: (cursor?: string, signal?: AbortSignal) => request<S["PageEmailSummary"]>("GET", `/v1/email/box/messages${qs({ cursor, limit: 10 })}`, undefined, signal),
  message: (id: string) => request<EmailDetail>("GET", `/v1/email/box/messages/${e(id)}`),
  setEmail: (person: string, email: string) => request<S["Person"]>("PUT", `/v1/people/${e(person)}/email`, { email } satisfies S["Person-email-setRequest"]),
  /** A fresh sign-in link, emailed to them too. */
  emailLink: (person: string) => request<S["Invite"]>("POST", `/v1/people/${e(person)}/login-link?email=true`),
  signInAvailable: () => request<S["EmailSignInStatus"]>("GET", "/v1/session/email"),
  emailSignIn: (email: string) => request<S["EmailSignInAnswer"]>("POST", "/v1/session/email", { email } satisfies S["Session-emailRequest"]),
  sending: (project: string, domain?: string) => request<SendingView>("GET", `${SD(project)}${qs({ domain })}`),
  startSending: (project: string, domain: string, local: string) =>
    request<SendingView>("POST", SD(project), { domain, local } satisfies S["Email-sending-domain-setRequest"]),
  checkSending: (project: string) => request<SendingView>("POST", `${SD(project)}/check`),
  stopSending: (project: string) => request<SendingView>("DELETE", SD(project)),
};

export const boxMailQ = {
  sender: queryOptions({ queryKey: ["box-mail-sender"], queryFn: boxMail.sender, retry: false }),
  messages: { ...pagedQuery(["box-mail-messages"], boxMail.messages), retry: false },
  signIn: queryOptions({ queryKey: ["session-email"], queryFn: boxMail.signInAvailable, retry: false, staleTime: 60_000 }),
  sending: (project: string) =>
    queryOptions({
      queryKey: ["email-sending", project],
      queryFn: () => boxMail.sending(project),
      retry: false,
      // While the box waits for the provider, follow along.
      refetchInterval: (q) => (q.state.data?.setup?.state === "verifying" ? 20_000 : false),
    }),
};

// ------------------------------------------------------------------ project icons

/** One of a project's icons. `url` changes with the image, so the browser keeps it for good. */
export type IconImage = S["RuntimeIconImage"];
export type IconInfo = S["RuntimeIconInfo"];

const iconPath = (project: string) => `${P(project)}/icon`;

export const iconQuery = (project: string) =>
  queryOptions({
    queryKey: ["project-icon", project],
    queryFn: () => request<IconInfo>("GET", iconPath(project)),
    staleTime: 5 * 60_000,
    retry: false,
  });

const base64 = async (b: Blob) => {
  const bytes = new Uint8Array(await b.arrayBuffer());
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
};

/** Uploads an image (PNG, JPEG, GIF, WebP, ICO or SVG, ≤ 512 KB); with an SVG, a PNG of it for email. */
export async function uploadIcon(project: string, image: Blob, png?: Blob) {
  const body: S["Icon-uploadRequest"] = { image: await base64(image), ...(png ? { png: await base64(png) } : {}) };
  return request<IconInfo>("PUT", iconPath(project), body);
}

/** app: the app's own icon (the box looks again now); letter: the initials. Removes an upload. */
export const resetIcon = (project: string, use: S["Icon-resetRequest"]["use"]) =>
  request<IconInfo>("POST", `${iconPath(project)}/reset`, { use } satisfies S["Icon-resetRequest"]);

/** The most a file may weigh. */
export const ICON_MAX_BYTES = 512 * 1024;
