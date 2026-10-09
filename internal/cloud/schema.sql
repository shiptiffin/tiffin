-- ShipTiffin control plane tables, in the website project's Postgres. The
-- provisioner (its own project, which reaches this database with the
-- website's DATABASE_URL as CONTROL_DATABASE_URL) applies this at start; it
-- is safe to run again. The website reads and writes the same tables. The
-- owner can read them in the website project's Database browser.
--
-- Before launch there are no migrations: when a column is missing (tables
-- from an earlier build) the worker refuses to start and says to drop the
-- old cloud_* tables.

create table if not exists cloud_boxes (
  id text primary key,
  user_id text not null,
  email text not null,
  name text unique,
  status text not null default 'awaiting_payment'
    check (status in ('awaiting_payment', 'paid', 'provisioning', 'cert_pending', 'active', 'failed', 'deleting', 'deleted', 'released')),
  plan_status text not null default 'none',
  cancel_at_period_end boolean not null default false,
  current_period_end timestamptz,
  first_paid_at timestamptz,
  extras_paused_at timestamptz,
  stripe_customer_id text,
  stripe_subscription_id text unique,
  checkout_session_id text unique,
  checkout_url text,
  checkout_expires_at timestamptz,
  checkout_founding boolean,
  refunded_at timestamptz,
  founding boolean not null default false,
  server_type text,
  location text,
  volume_gb integer,
  ipv4 text,
  ipv6 text,
  addr_mac text,
  generation bigint not null default 0,
  hetzner_server_id bigint,
  hetzner_volume_id bigint,
  hetzner_firewall_id bigint,
  hetzner_ssh_key_id bigint,
  token_fingerprint text,
  signin_code text,
  signin_expires_at timestamptz,
  dns_state text not null default 'none' check (dns_state in ('none', 'pending', 'live', 'removed', 'parked', 'killed')),
  dns_changed_at timestamptz,
  last_heartbeat_at timestamptz,
  last_heartbeat_ip text,
  heartbeat_refused_at timestamptz,
  heartbeat_refused_why text,
  last_version text,
  failing text[] not null default '{}',
  health_failures integer not null default 0,
  health_checked_at timestamptz,
  https_checked_at timestamptz,
  ready_at timestamptz,
  installed_at timestamptz,
  attention text,
  attention_at timestamptz,
  handoff_closed_at timestamptz,
  signin_requested_at timestamptz,
  checkout_attempt jsonb,
  down_alerted_at timestamptz,
  heartbeat_alerted_at timestamptz,
  killed_at timestamptz,
  kill_reason text,
  released_at timestamptz,
  deleted_at timestamptz,
  data_deleted boolean,
  last_charge_cents integer,
  last_charge_currency text,
  last_charge_at timestamptz,
  plan_ended_at timestamptz,
  backup_key text,
  offsite_sealed text,
  offsite_expires_at timestamptz,
  offsite_purge_after timestamptz,
  offsite_purged_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
-- Columns added after the first build (the tables may predate them).
alter table cloud_boxes add column if not exists installed_at timestamptz;
alter table cloud_boxes add column if not exists attention text;
alter table cloud_boxes add column if not exists attention_at timestamptz;
alter table cloud_boxes add column if not exists handoff_closed_at timestamptz;
alter table cloud_boxes add column if not exists signin_requested_at timestamptz;
alter table cloud_boxes add column if not exists checkout_attempt jsonb;
alter table cloud_boxes add column if not exists deleted_at timestamptz;
alter table cloud_boxes add column if not exists data_deleted boolean;
alter table cloud_boxes add column if not exists last_charge_cents integer;
alter table cloud_boxes add column if not exists last_charge_currency text;
alter table cloud_boxes add column if not exists last_charge_at timestamptz;
alter table cloud_boxes add column if not exists plan_ended_at timestamptz;
alter table cloud_boxes add column if not exists backup_key text;
alter table cloud_boxes add column if not exists offsite_sealed text;
alter table cloud_boxes add column if not exists offsite_expires_at timestamptz;
alter table cloud_boxes add column if not exists offsite_purge_after timestamptz;
alter table cloud_boxes add column if not exists offsite_purged_at timestamptz;
-- Columns dropped since: a customer's Hetzner token lives only on its job.
alter table cloud_boxes drop column if exists token_sealed;
alter table cloud_boxes drop column if exists token_kept_at;
alter table cloud_boxes drop constraint if exists cloud_boxes_dns_state_check;
alter table cloud_boxes add constraint cloud_boxes_dns_state_check check (dns_state in ('none', 'pending', 'live', 'removed', 'parked', 'killed'));
alter table cloud_boxes drop constraint if exists cloud_boxes_status_check;
alter table cloud_boxes add constraint cloud_boxes_status_check
  check (status in ('awaiting_payment', 'paid', 'provisioning', 'cert_pending', 'active', 'failed', 'deleting', 'deleted', 'released'));
-- Deleted is for good: no late job, webhook or check-in changes it back.
create or replace function cloud_boxes_keep_deleted() returns trigger language plpgsql as $$
begin
  if old.status = 'deleted' then
    new.status := 'deleted';
    new.deleted_at := old.deleted_at;
    new.data_deleted := old.data_deleted;
  end if;
  return new;
end
$$;
drop trigger if exists cloud_boxes_keep_deleted on cloud_boxes;
create trigger cloud_boxes_keep_deleted before update on cloud_boxes for each row execute function cloud_boxes_keep_deleted();
create index if not exists cloud_boxes_user on cloud_boxes (user_id, created_at);
-- One box waiting for its first payment per account: two tabs reuse it.
create unique index if not exists cloud_boxes_one_pending on cloud_boxes (user_id) where status = 'awaiting_payment';

comment on table cloud_boxes is 'Managed boxes: one row per box a customer started at shiptiffin.com/start. The server itself is in the customer''s own Hetzner project; this is our side (billing, address, check-ins).';
comment on column cloud_boxes.id is 'Box id (box_…): in the Stripe subscription''s metadata, the box''s licence, and the shiptiffin-box label on every Hetzner resource we make for it.';
comment on column cloud_boxes.status is 'awaiting_payment, paid (first payment succeeded; ready to connect Hetzner), provisioning, cert_pending (installed; waiting for its HTTPS certificate), active, failed (setup failed; can be retried), deleting, deleted (the customer had us delete the server: for good, see deleted_at) or released (the customer stopped the managed service; the server is theirs and runs on).';
comment on column cloud_boxes.deleted_at is 'When the delete_server job finished: the address, the server, its firewall and (data_deleted) its data volume are gone.';
comment on column cloud_boxes.last_charge_cents is 'The subscription''s latest paid invoice, from the Stripe events we receive (with last_charge_currency and last_charge_at): what the account page and emails say was last charged.';
comment on column cloud_boxes.plan_ended_at is 'When the subscription ended (Stripe''s ended_at, else canceled_at).';
comment on column cloud_boxes.backup_key is 'The public half of the box''s own X25519 key, from its check-ins that count: its off-site backup credentials are sealed to it.';
comment on column cloud_boxes.offsite_sealed is 'The box''s off-site backup credentials (R2 temporary credentials for its folder <box id>/ in the customer backup bucket), sealed to backup_key by the provisioner: the website hands them over at check-ins and cannot read them. Only while the subscription is active; offsite_expires_at is when they stop working.';
comment on column cloud_boxes.offsite_purge_after is 'When the box''s folder in the customer backup bucket is emptied (7 days after it was deleted or released); offsite_purged_at when it was.';
comment on column cloud_boxes.data_deleted is 'For a deleted box: whether its data volume was deleted too (false: it stays in the customer''s Hetzner project).';
comment on column cloud_boxes.plan_status is 'The Stripe subscription''s status as retrieved from Stripe (active, past_due, unpaid, canceled, …). active and trialing keep the managed extras on; past_due too once the first payment succeeded.';
comment on column cloud_boxes.first_paid_at is 'When the first invoice was paid. Setup needs it.';
comment on column cloud_boxes.extras_paused_at is 'When the managed extras paused (subscription ended or unpaid). The shiptiffin.app address is removed 30 days later.';
comment on column cloud_boxes.addr_mac is 'The worker''s MAC over (box, name, ipv4, ipv6, generation): DNS only ever points at addresses the worker recorded itself.';
comment on column cloud_boxes.generation is 'Installation generation: each setup attempt gets the next one; licences carry it and only the current one counts.';
comment on column cloud_boxes.token_fingerprint is 'What stays of the Hetzner token the last setup used: 12 hex characters of its sha256. A token itself is kept only on its job (cloud_jobs.token_sealed) and forgotten when the job ends.';
comment on column cloud_boxes.signin_code is 'The one-time owner sign-in link the box made (at setup, or at a check-in once the dashboard is ready or the customer asked for a new one); it expires on the box after 24 hours. Kept until the box reports the owner signed in, the customer forgets it, or it expires.';
comment on column cloud_boxes.installed_at is 'When Tiffin finished installing. From then on nothing we do deletes the server or its volume: a failure leaves them and sets attention.';
comment on column cloud_boxes.attention is 'Set when something after the install went wrong and needs a person (shown to the customer and in /admin). The server and data are kept.';
comment on column cloud_boxes.handoff_closed_at is 'When the hand-off ended: the box reported its owner signed in, or the customer forgot the link. No sign-in link is asked for after that.';
comment on column cloud_boxes.signin_requested_at is 'The customer asked for a new one-time sign-in link; the box makes it at its next check-in.';
comment on column cloud_boxes.checkout_attempt is 'The Checkout request being made (its idempotency key and exact parameters), saved before Stripe is called so a retry sends the very same request.';
comment on column cloud_boxes.dns_state is 'none, pending (records are being published, or a publish failed half way: treated as live for removal), live, removed (grace period over, setup failed, released or deleted), parked (no check-in for 72 hours; the next one brings it back) or killed (abuse kill switch; only an admin restores it).';
comment on column cloud_boxes.last_heartbeat_at is 'The last check-in that counted: a licence of the current generation, sent from the box''s own address.';

create table if not exists cloud_jobs (
  id bigint generated always as identity primary key,
  box_id text not null references cloud_boxes (id),
  kind text not null check (kind in ('provision', 'resize', 'delete_server', 'cleanup', 'dns_set', 'dns_remove')),
  status text not null default 'queued' check (status in ('queued', 'running', 'done', 'failed')),
  args jsonb not null default '{}',
  token_sealed text,
  token_expires_at timestamptz,
  steps jsonb not null default '[]',
  checkpoint jsonb not null default '{}',
  attempts integer not null default 0,
  not_before timestamptz,
  lease_gen bigint not null default 0,
  error text,
  created_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz,
  lease_until timestamptz
);
create index if not exists cloud_jobs_queue on cloud_jobs (status, id);
create index if not exists cloud_jobs_box on cloud_jobs (box_id, created_at desc);
-- One running job per box: infrastructure work on a box never overlaps.
create unique index if not exists cloud_jobs_one_running on cloud_jobs (box_id) where status = 'running';
comment on table cloud_jobs is 'Work for the provisioner. token_sealed holds a customer''s Hetzner token only while its job waits or runs (and never past token_expires_at); it is cleared when the job ends. lease_gen fences a worker that lost its lease; checkpoint records the phases a retry skips; not_before delays a retry.';

create table if not exists cloud_hetzner_calls (
  id bigint generated always as identity primary key,
  box_id text not null,
  job_id bigint,
  purpose text not null,
  at timestamptz not null,
  method text not null,
  path text not null,
  status integer,
  ms integer,
  error text
);
create index if not exists cloud_hetzner_calls_box on cloud_hetzner_calls (box_id, at desc);
comment on table cloud_hetzner_calls is 'Every request we made with a customer''s Hetzner token (never the token itself). Shown to them in their account.';

create table if not exists cloud_stripe_events (
  id text primary key,
  type text not null,
  created timestamptz,
  received_at timestamptz not null default now()
);
comment on table cloud_stripe_events is 'Stripe webhook events already handled (so a retried delivery changes nothing).';

create table if not exists cloud_founding_claims (
  box_id text primary key,
  claimed_at timestamptz not null default now()
);
comment on table cloud_founding_claims is 'Boxes that got the founding price. The offer stops at 100 rows (and at the coupon''s own limit in Stripe).';

create table if not exists cloud_customers (
  user_id text primary key,
  email text not null,
  stripe_customer_id text unique,
  created_at timestamptz not null default now()
);

create table if not exists cloud_abuse_reports (
  id bigint generated always as identity primary key,
  target text not null,
  box_id text,
  reporter_email text,
  details text,
  status text not null default 'new' check (status in ('new', 'acted', 'dismissed')),
  created_at timestamptz not null default now()
);
comment on table cloud_abuse_reports is 'Reports from shiptiffin.com/abuse about a <name>.shiptiffin.app address.';

create table if not exists cloud_outbox (
  id bigint generated always as identity primary key,
  box_id text not null,
  kind text not null,
  key text not null default '',
  params jsonb not null default '{}',
  status text not null default 'queued' check (status in ('queued', 'done', 'failed', 'dropped')),
  attempts integer not null default 0,
  next_attempt_at timestamptz not null default now(),
  last_error text,
  created_at timestamptz not null default now(),
  done_at timestamptz,
  unique (box_id, kind, key)
);
create index if not exists cloud_outbox_due on cloud_outbox (next_attempt_at) where status = 'queued';
alter table cloud_outbox drop constraint if exists cloud_outbox_status_check;
alter table cloud_outbox add constraint cloud_outbox_status_check check (status in ('queued', 'done', 'failed', 'dropped'));
comment on table cloud_outbox is 'Emails (kind = the template) and Stripe actions (kind stripe_…) to carry out after the change that asked for them committed, each once (box, kind, key), retried with backoff until done or failed. Dropped: not sent on purpose (an email about a running box, for a box that was deleted).';

create table if not exists cloud_billing_log (
  id bigint generated always as identity primary key,
  box_id text not null,
  what text not null,
  subscription_id text,
  detail jsonb not null default '{}',
  at timestamptz not null default now()
);
comment on table cloud_billing_log is 'Billing actions we took on our own: a duplicate subscription cancelled and refunded, a money-back refund, a refund seen from Stripe.';

-- Boxes deleted before "deleted" existed were marked released: a released
-- box whose latest delete_server job finished was deleted by it.
update cloud_boxes b set status = 'deleted', deleted_at = j.finished_at, data_deleted = coalesce((j.args->>'deleteData')::boolean, false)
from (select distinct on (box_id) box_id, status, finished_at, args from cloud_jobs where kind = 'delete_server' order by box_id, id desc) j
where j.box_id = b.id and j.status = 'done' and b.status = 'released';
