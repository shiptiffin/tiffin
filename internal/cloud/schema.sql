-- ShipTiffin control plane tables, in the website project's Postgres. The
-- cloud worker applies this at start (it is safe to run again); the website
-- reads and writes the same tables. The owner can read them in the
-- dashboard's Database browser: every table and most columns say what they are.

create table if not exists cloud_boxes (
  id text primary key,
  user_id text not null,
  email text not null,
  name text unique,
  status text not null default 'awaiting_payment'
    check (status in ('awaiting_payment', 'paid', 'provisioning', 'active', 'failed', 'released')),
  plan_status text not null default 'none',
  plan_status_at timestamptz,
  cancel_at_period_end boolean not null default false,
  current_period_end timestamptz,
  extras_paused_at timestamptz,
  stripe_customer_id text,
  stripe_subscription_id text unique,
  checkout_session_id text unique,
  founding boolean not null default false,
  server_type text,
  location text,
  volume_gb integer,
  ipv4 text,
  ipv6 text,
  hetzner_server_id bigint,
  token_fingerprint text,
  token_sealed text,
  token_kept_at timestamptz,
  owner_token_sealed text,
  owner_token_expires_at timestamptz,
  first_opened_at timestamptz,
  dns_state text not null default 'none' check (dns_state in ('none', 'live', 'removed', 'killed')),
  last_heartbeat_at timestamptz,
  last_version text,
  failing text[] not null default '{}',
  health_failures integer not null default 0,
  health_checked_at timestamptz,
  down_alerted_at timestamptz,
  heartbeat_alerted_at timestamptz,
  killed_at timestamptz,
  kill_reason text,
  released_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists cloud_boxes_user on cloud_boxes (user_id, created_at);

comment on table cloud_boxes is 'Managed boxes: one row per box a customer started at shiptiffin.com/start. The server itself is in the customer''s own Hetzner project; this is our side (billing, address, check-ins).';
comment on column cloud_boxes.id is 'Box id (box_…), also in the Stripe subscription''s metadata and the box''s licence.';
comment on column cloud_boxes.user_id is 'The account (tiffin_auth user id).';
comment on column cloud_boxes.name is 'The box''s name: its address is <name>.shiptiffin.app. Unique; chosen at setup.';
comment on column cloud_boxes.status is 'awaiting_payment, paid (ready to connect Hetzner), provisioning, active, failed (setup failed; can be retried) or released (no longer managed by us; the server is untouched).';
comment on column cloud_boxes.plan_status is 'The Stripe subscription''s status (active, past_due, unpaid, canceled, …). active, trialing and past_due keep the managed extras on.';
comment on column cloud_boxes.extras_paused_at is 'When the managed extras paused (subscription ended or unpaid). The shiptiffin.app address is removed 30 days later.';
comment on column cloud_boxes.token_fingerprint is 'First 12 hex characters of the sha256 of the customer''s Hetzner token: shown to them, useless to call with.';
comment on column cloud_boxes.token_sealed is 'The customer''s Hetzner token, sealed with CLOUD_KEK, only when they chose "keep my key". Empty otherwise.';
comment on column cloud_boxes.owner_token_sealed is 'The box''s owner token, sealed, kept only so "Open your dashboard" can make sign-in links; deleted a day after the first open, after 7 days at most, or when the customer says so.';
comment on column cloud_boxes.dns_state is 'none, live, removed (grace period over or released) or killed (abuse kill switch; never restored automatically).';
comment on column cloud_boxes.failing is 'Names of the box''s failing status checks at its last check-in.';

create table if not exists cloud_jobs (
  id bigint generated always as identity primary key,
  box_id text not null references cloud_boxes (id),
  kind text not null check (kind in ('provision', 'resize', 'delete_server', 'dns_set', 'dns_remove')),
  status text not null default 'queued' check (status in ('queued', 'running', 'done', 'failed')),
  args jsonb not null default '{}',
  token_sealed text,
  steps jsonb not null default '[]',
  error text,
  created_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz,
  lease_until timestamptz
);
create index if not exists cloud_jobs_queue on cloud_jobs (status, created_at);
create index if not exists cloud_jobs_box on cloud_jobs (box_id, created_at desc);
comment on table cloud_jobs is 'Work for the cloud worker: provision, resize, delete_server, dns_set, dns_remove. token_sealed holds a customer''s Hetzner token only while its job waits or runs; it is cleared when the job ends.';

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

create table if not exists cloud_emails_sent (
  box_id text not null,
  kind text not null,
  key text not null default '',
  sent_at timestamptz not null default now(),
  primary key (box_id, kind, key)
);
comment on table cloud_emails_sent is 'Emails sent about a box, so each goes once (kind + key, e.g. payment_failed + invoice id).';
