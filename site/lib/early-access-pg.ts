// Invite requests in the project's Postgres: one table, invite_requests, which
// the owner reads and edits in the dashboard's Database browser (every column
// has a comment saying what it is). It is created on first use, so a fresh
// database needs nothing; a database that still has the older early_access
// table has its rows moved across and that table dropped.
import postgres from "postgres";
import { decide, type Answers, type Details, type InviteRequest, type Repo, type Status } from "./early-access";
import { AGENT_CHOICES, PROJECT_CHOICES, ROLE_CHOICES, SPEND_CHOICES, TOOL_CHOICES } from "./form";

type Sql = postgres.Sql;

const values = (list: readonly { value: string; label: string }[]) => list.map((c) => `${c.value} (${c.label})`).join(", ");
const q = (s: string) => `'${s.replace(/'/g, "''")}'`;

/** The table, its comments, and the move from early_access. Safe to run again. */
export const SCHEMA = `
create table if not exists invite_requests (
  id bigint generated always as identity primary key,
  email text not null unique check (email = lower(email)),
  name text,
  role text,
  status text not null default 'requested' check (status in ('requested', 'invited', 'joined', 'declined')),
  invited_at timestamptz,
  notes text,
  email_confirmed_at timestamptz,
  host_first text,
  projects text,
  current_tools text[] not null default '{}',
  monthly_spend text,
  ai_agents text[] not null default '{}',
  github text,
  x text,
  linkedin text,
  website text,
  note text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  submissions integer not null default 1,
  confirm_sent_at timestamptz,
  token_hash text not null unique,
  details_token_hash text unique,
  details_token_expires_at timestamptz
);
create index if not exists invite_requests_status on invite_requests (status, created_at);

comment on table invite_requests is ${q("The sign-up list from shiptiffin.com/start (people waiting for their sign-up link), one row per email address. When you send someone their link, set status to invited and invited_at to now(). Unconfirmed requests are deleted after 30 days, joined and declined ones 12 months after their last change. To remove someone completely, delete the row.")};
comment on column invite_requests.id is 'Row number, in the order requests arrived.';
comment on column invite_requests.email is 'Email address, lower-cased. Unique.';
comment on column invite_requests.name is 'Their name, if given.';
comment on column invite_requests.role is ${q(`What do you build? ${values(ROLE_CHOICES)}`)};
comment on column invite_requests.status is 'Yours to set: requested (waiting), invited (sign-up link sent), joined (signed up) or declined (not sending one).';
comment on column invite_requests.invited_at is 'When you sent their sign-up link. Set it with status = invited.';
comment on column invite_requests.notes is 'Your own notes about them. Never shown to them.';
comment on column invite_requests.email_confirmed_at is 'When they clicked the link in the confirmation email. Empty: not confirmed yet, so do not send a link.';
comment on column invite_requests.host_first is 'What would you host first?';
comment on column invite_requests.projects is ${q(`How many projects? ${values(PROJECT_CHOICES)}`)};
comment on column invite_requests.current_tools is ${q(`What do you use today? Any of: ${values(TOOL_CHOICES)}`)};
comment on column invite_requests.monthly_spend is ${q(`Monthly hosting spend today: ${values(SPEND_CHOICES)}`)};
comment on column invite_requests.ai_agents is ${q(`AI coding agents they use. Any of: ${values(AGENT_CHOICES)}`)};
comment on column invite_requests.github is 'GitHub profile, as a link.';
comment on column invite_requests.x is 'X (Twitter) profile, as a link.';
comment on column invite_requests.linkedin is 'LinkedIn profile, as a link.';
comment on column invite_requests.website is 'Their own website.';
comment on column invite_requests.note is 'Anything else they wanted to say.';
comment on column invite_requests.created_at is 'When they first asked.';
comment on column invite_requests.updated_at is 'When the row last changed.';
comment on column invite_requests.submissions is 'How many times the form was sent for this address.';
comment on column invite_requests.confirm_sent_at is 'When the newest confirmation email went out (at most one every 10 minutes).';
comment on column invite_requests.token_hash is 'sha256 of the token in their confirm and remove links. Do not edit.';
comment on column invite_requests.details_token_hash is 'sha256 of the token that lets the form add step 2 answers. Used once, for an hour. Do not edit.';
comment on column invite_requests.details_token_expires_at is 'When the step 2 token stops working.';

do $$
begin
  if to_regclass('public.early_access') is not null then
    insert into invite_requests (email, host_first, projects, current_tools, note, email_confirmed_at,
                                 created_at, updated_at, submissions, confirm_sent_at, token_hash)
    select email, hosting, projects, current_hosts, note, confirmed_at,
           created_at, updated_at, submissions, confirm_sent_at, token_hash
    from early_access
    on conflict do nothing;
    drop table early_access;
  end if;
end
$$;
`;

type Row = {
  id: string | number;
  email: string;
  name: string | null;
  role: string | null;
  status: Status;
  email_confirmed_at: Date | null;
  host_first: string | null;
  projects: string | null;
  current_tools: string[];
  monthly_spend: string | null;
  ai_agents: string[];
  github: string | null;
  x: string | null;
  linkedin: string | null;
  website: string | null;
  note: string | null;
  confirm_sent_at: Date | null;
  created_at: Date;
};

const toRequest = (r: Row): InviteRequest => ({
  id: Number(r.id),
  email: r.email,
  name: r.name,
  role: r.role,
  status: r.status,
  emailConfirmed: r.email_confirmed_at != null,
  hostFirst: r.host_first,
  projects: r.projects,
  tools: r.current_tools ?? [],
  spend: r.monthly_spend,
  agents: r.ai_agents ?? [],
  github: r.github,
  x: r.x,
  linkedin: r.linkedin,
  site: r.website,
  note: r.note,
  confirmSentAt: r.confirm_sent_at,
  createdAt: r.created_at,
});

let client: Sql | null = null;
let ready: Promise<void> | null = null;

/** The shared client, or null when the project has no database (DATABASE_URL unset). */
export function sql(): Sql | null {
  const url = process.env.DATABASE_URL;
  if (!url) return null;
  // prepare: false, as everywhere on the box: DATABASE_URL goes through PgBouncer.
  client ??= postgres(url, {
    prepare: false,
    max: Number(process.env.DATABASE_POOL_MAX) || 5,
    idle_timeout: 30,
    connect_timeout: 10,
    onnotice: () => {},
  });
  return client;
}

async function ensure(db: Sql): Promise<void> {
  ready ??= db.unsafe(SCHEMA).simple().then(
    () => undefined,
    async (err) => {
      ready = null;
      // Two instances creating it at once: one wins, the other sees it there.
      const [{ ok }] = await db<{ ok: boolean }[]>`
        select to_regclass('public.invite_requests') is not null and to_regclass('public.early_access') is null as ok`;
      if (!ok) throw err;
    },
  );
  return ready;
}

/** Step 2's answers as an update: what's given replaces, what's left out stays. */
function detailsSet(tx: Sql | postgres.TransactionSql, d: Details) {
  return tx`
    host_first = coalesce(${d.hostFirst}, host_first),
    projects = coalesce(${d.projects}, projects),
    current_tools = case when cardinality(${d.tools}::text[]) > 0 then ${d.tools}::text[] else current_tools end,
    monthly_spend = coalesce(${d.spend}, monthly_spend),
    ai_agents = case when cardinality(${d.agents}::text[]) > 0 then ${d.agents}::text[] else ai_agents end,
    github = coalesce(${d.github}, github),
    x = coalesce(${d.x}, x),
    linkedin = coalesce(${d.linkedin}, linkedin),
    website = coalesce(${d.site}, website),
    note = coalesce(${d.note}, note)`;
}

export function pgRepo(db: Sql): Repo {
  return {
    async register(a: Answers, token, details, now) {
      await ensure(db);
      // What the privacy policy promises: unconfirmed requests go after 30 days,
      // joined and declined ones 12 months after their last change.
      await db`
        delete from invite_requests
        where (email_confirmed_at is null and status = 'requested' and created_at < ${now}::timestamptz - interval '30 days')
           or (status in ('joined', 'declined') and updated_at < ${now}::timestamptz - interval '12 months')`;
      try {
        return await db.begin(async (tx) => {
          const [row] = await tx<Row[]>`select * from invite_requests where email = ${a.email} for update`;
          const what = decide(row ? toRequest(row) : null, now);
          if (what === "new") {
            await tx`
              insert into invite_requests (
                email, name, role, host_first, projects, current_tools, monthly_spend, ai_agents,
                github, x, linkedin, website, note, token_hash, confirm_sent_at, details_token_hash, details_token_expires_at
              ) values (
                ${a.email}, ${a.name}, ${a.role}, ${a.hostFirst}, ${a.projects}, ${a.tools}, ${a.spend}, ${a.agents},
                ${a.github}, ${a.x}, ${a.linkedin}, ${a.site}, ${a.note}, ${token.hash}, ${now}, ${details.hash}, ${details.expiresAt}
              )`;
          } else if (row!.email_confirmed_at == null) {
            // Not confirmed yet: the newest answers win.
            await tx`
              update invite_requests set
                name = coalesce(${a.name}, name),
                role = coalesce(${a.role}, role),
                ${detailsSet(tx, a)},
                details_token_hash = ${details.hash},
                details_token_expires_at = ${details.expiresAt},
                submissions = submissions + 1,
                updated_at = ${now}
                ${what === "resend" ? tx`, token_hash = ${token.hash}, confirm_sent_at = ${now}` : tx``}
              where id = ${row!.id}`;
          } else {
            await tx`update invite_requests set submissions = submissions + 1, updated_at = ${now} where id = ${row!.id}`;
          }
          return what;
        });
      } catch (err) {
        // The same address sent twice at the same moment: the other request has it.
        if ((err as { code?: string }).code === "23505") return "quiet";
        throw err;
      }
    },

    async addDetails(hash, d, now) {
      await ensure(db);
      const rows = await db`
        update invite_requests set
          ${detailsSet(db, d)},
          details_token_hash = null,
          details_token_expires_at = null,
          updated_at = ${now}
        where details_token_hash = ${hash} and details_token_expires_at > ${now}
        returning id`;
      return rows.length > 0;
    },

    async confirm(hash, now) {
      await ensure(db);
      const [row] = await db<(Row & { was: Date | null })[]>`
        update invite_requests r set
          email_confirmed_at = coalesce(r.email_confirmed_at, ${now}),
          updated_at = ${now}
        from (select id, email_confirmed_at as was from invite_requests where token_hash = ${hash} for update) old
        where r.id = old.id
        returning r.*, old.was`;
      return row ? { request: toRequest(row), first: row.was == null } : null;
    },

    async remove(hash) {
      await ensure(db);
      const rows = await db`delete from invite_requests where token_hash = ${hash} returning id`;
      return rows.length > 0;
    },
  };
}
