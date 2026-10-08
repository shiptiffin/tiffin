package cloud

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var Schema string

// Lease is a worker's hold on a running job. Gen goes up at every claim, so
// a worker whose lease ran out (and whose job someone else may now run) can
// no longer write anything: every write about the job, and about its box,
// carries the lease and changes nothing once it is stale.
type Lease struct {
	JobID int64
	Gen   int64
}

// ErrLeaseLost: the job is no longer this worker's.
var ErrLeaseLost = errors.New("this worker no longer holds the job (its lease ran out)")

// Job is one piece of work.
type Job struct {
	Lease
	ID          int64
	BoxID       string
	Kind        string
	Args        json.RawMessage
	TokenSealed string
	TokenExpiry *time.Time
	Checkpoint  map[string]json.RawMessage
	Attempts    int
}

// Box is what the worker needs of a box row.
type Box struct {
	ID              string
	Name            string
	Status          string
	PlanStatus      string
	FirstPaid       bool
	ExtrasPausedAt  *time.Time
	ServerType      string
	Location        string
	IPv4, IPv6      string
	AddrMAC         string
	Generation      int64
	ServerID        int64
	DNSState        string
	Killed          bool
	TokenSealed     string
	LastHeartbeatAt *time.Time
	ReadyAt         *time.Time
}

// ServerInfo is what provisioning learned about the server.
type ServerInfo struct {
	ID         int64
	IPv4, IPv6 string
	Type       string
	Location   string
	VolumeGB   int
	MAC        string
}

// Resources are the IDs of what we made in the customer's project.
type Resources struct {
	Server, Volume, Firewall, SSHKey int64
}

// Store is the worker's database.
type Store interface {
	Claim(ctx context.Context, lease time.Duration) (*Job, error)
	Extend(ctx context.Context, l Lease, lease time.Duration) error
	Step(ctx context.Context, l Lease, text string) error
	Checkpoint(ctx context.Context, l Lease, key string, value any) error
	// Finish ends a job and clears its token, whatever the outcome.
	Finish(ctx context.Context, l Lease, jobErr error) error
	Box(ctx context.Context, id string) (*Box, error)
	NextGeneration(ctx context.Context, l Lease, boxID string) (int64, error)
	SetStatus(ctx context.Context, l Lease, boxID, status string) error
	SetServer(ctx context.Context, l Lease, boxID string, s ServerInfo) error
	SetResources(ctx context.Context, l Lease, boxID string, r Resources) error
	// SetDNS records the address's state; it never turns "killed" into anything else.
	SetDNS(ctx context.Context, l Lease, boxID, state string) error
	SetFingerprint(ctx context.Context, l Lease, boxID, fp string) error
	// KeepToken stores the sealed Hetzner token on the box (the customer
	// asked to keep it); "" forgets it.
	KeepToken(ctx context.Context, l Lease, boxID, sealed string) error
	SetSignin(ctx context.Context, l Lease, boxID, code string, expires time.Time) error
	Released(ctx context.Context, l Lease, boxID string) error
	// QueueEmail puts an email in the outbox (sent by the website), once per (box, kind, key).
	QueueEmail(ctx context.Context, l Lease, boxID, kind, key string, params map[string]any) error
	RecordCall(ctx context.Context, boxID string, jobID int64, purpose string, c Call) error
	// EnqueueCleanup queues a clean-up job with the job's sealed token (until it expires).
	EnqueueCleanup(ctx context.Context, l Lease, boxID string, a CleanupArgs, sealed string, exp *time.Time) error
	// CertPending lists boxes waiting for their dashboard's certificate.
	CertPending(ctx context.Context, olderThan time.Time, limit int) ([]Box, error)
	// MarkReady makes a cert_pending box active and queues its "ready" email.
	MarkReady(ctx context.Context, boxID string) (bool, error)
	CheckedHTTPS(ctx context.Context, boxID string) error
	// Sweep recovers after workers that stopped (failing or retrying their
	// jobs), wipes tokens past their time and expired sign-in links, and
	// queues the clean-up a failed or deleted box still needs. It says what it did.
	Sweep(ctx context.Context, now time.Time) (string, error)
}

// PG is the Postgres store.
type PG struct{ Pool *pgxpool.Pool }

// OpenPG connects (simple protocol: the website's DATABASE_URL goes
// through PgBouncer) and applies the schema.
func OpenPG(ctx context.Context, url string) (*PG, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
	cfg.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &PG{Pool: pool}
	if err := s.Apply(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Apply runs the schema under a lock, so two workers starting together do
// not trip over each other, then checks the tables are this build's.
func (s *PG) Apply(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(7316253)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, Schema); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from information_schema.columns where table_name = 'cloud_jobs' and column_name = 'lease_gen'
			union all select count(*) from information_schema.columns where table_name = 'cloud_boxes' and column_name = 'addr_mac' order by 1 limit 1`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return errors.New("the cloud_* tables are from an earlier build: drop them (before launch nothing in them matters) and start the worker again")
		}
		return nil
	})
}

const fence = ` and exists (select 1 from cloud_jobs fj where fj.id = %d and fj.lease_gen = %d and fj.status = 'running')`

// fenced runs an update of cloud_boxes only while l is current.
func (s *PG) fenced(ctx context.Context, l Lease, q string, args ...any) error {
	tag, err := s.Pool.Exec(ctx, q+fmt.Sprintf(fence, l.JobID, l.Gen), args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if s.held(ctx, l) {
			return nil // the row did not match for another reason (killed, say)
		}
		return ErrLeaseLost
	}
	return nil
}

func (s *PG) held(ctx context.Context, l Lease) bool {
	var ok bool
	_ = s.Pool.QueryRow(ctx, `select exists (select 1 from cloud_jobs where id = $1 and lease_gen = $2 and status = 'running')`, l.JobID, l.Gen).Scan(&ok)
	return ok
}

func interval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

func (s *PG) Claim(ctx context.Context, lease time.Duration) (*Job, error) {
	var j Job
	var tok *string
	var cp []byte
	// The oldest waiting job of a box with nothing running; the unique index
	// on running jobs settles two workers claiming for one box at once.
	err := s.Pool.QueryRow(ctx, `
		update cloud_jobs set status = 'running', started_at = coalesce(started_at, now()), lease_until = now() + $1::interval,
			lease_gen = lease_gen + 1, attempts = attempts + 1
		where id = (
			select j.id from cloud_jobs j where j.status = 'queued'
			and not exists (select 1 from cloud_jobs r where r.box_id = j.box_id and r.status = 'running')
			and not exists (select 1 from cloud_jobs o where o.box_id = j.box_id and o.status = 'queued' and o.id < j.id)
			order by j.id limit 1 for update skip locked)
		returning id, lease_gen, box_id, kind, args, token_sealed, token_expires_at, checkpoint, attempts`, interval(lease)).
		Scan(&j.ID, &j.Gen, &j.BoxID, &j.Kind, &j.Args, &tok, &j.TokenExpiry, &cp, &j.Attempts)
	var pe *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &pe) && pe.Code == "23505" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.JobID = j.ID
	if tok != nil && (j.TokenExpiry == nil || j.TokenExpiry.After(time.Now())) {
		j.TokenSealed = *tok
	}
	_ = json.Unmarshal(cp, &j.Checkpoint)
	return &j, nil
}

func (s *PG) Extend(ctx context.Context, l Lease, lease time.Duration) error {
	tag, err := s.Pool.Exec(ctx, `update cloud_jobs set lease_until = now() + $3::interval where id = $1 and lease_gen = $2 and status = 'running'`,
		l.JobID, l.Gen, interval(lease))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PG) Step(ctx context.Context, l Lease, text string) error {
	step, _ := json.Marshal([]map[string]string{{"at": time.Now().UTC().Format(time.RFC3339), "text": text}})
	_, err := s.Pool.Exec(ctx, `update cloud_jobs set steps = steps || $3::jsonb where id = $1 and lease_gen = $2 and status = 'running'`, l.JobID, l.Gen, string(step))
	return err
}

func (s *PG) Checkpoint(ctx context.Context, l Lease, key string, value any) error {
	raw, _ := json.Marshal(map[string]any{key: value})
	tag, err := s.Pool.Exec(ctx, `update cloud_jobs set checkpoint = checkpoint || $3::jsonb where id = $1 and lease_gen = $2 and status = 'running'`, l.JobID, l.Gen, string(raw))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return err
}

func (s *PG) Finish(ctx context.Context, l Lease, jobErr error) error {
	status, msg := "done", (*string)(nil)
	if jobErr != nil {
		status = "failed"
		m := jobErr.Error()
		if len(m) > 2000 {
			m = m[:2000]
		}
		msg = &m
	}
	tag, err := s.Pool.Exec(ctx, `update cloud_jobs set status = $3, error = $4, token_sealed = null, finished_at = now(), lease_until = null
		where id = $1 and lease_gen = $2 and status = 'running'`, l.JobID, l.Gen, status, msg)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return err
}

func (s *PG) Box(ctx context.Context, id string) (*Box, error) {
	var b Box
	var name, st, loc, v4, v6, tok, mac *string
	var srv *int64
	err := s.Pool.QueryRow(ctx, `select id, name, status, plan_status, first_paid_at is not null, extras_paused_at, server_type, location, ipv4, ipv6,
		addr_mac, generation, hetzner_server_id, dns_state, killed_at is not null, token_sealed, last_heartbeat_at, ready_at from cloud_boxes where id = $1`, id).
		Scan(&b.ID, &name, &b.Status, &b.PlanStatus, &b.FirstPaid, &b.ExtrasPausedAt, &st, &loc, &v4, &v6, &mac, &b.Generation, &srv, &b.DNSState, &b.Killed, &tok,
			&b.LastHeartbeatAt, &b.ReadyAt)
	if err != nil {
		return nil, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	b.Name, b.ServerType, b.Location, b.IPv4, b.IPv6, b.TokenSealed, b.AddrMAC = deref(name), deref(st), deref(loc), deref(v4), deref(v6), deref(tok), deref(mac)
	if srv != nil {
		b.ServerID = *srv
	}
	return &b, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullID(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func (s *PG) NextGeneration(ctx context.Context, l Lease, boxID string) (int64, error) {
	var g int64
	err := s.Pool.QueryRow(ctx, `update cloud_boxes set generation = generation + 1, updated_at = now() where id = $1`+fmt.Sprintf(fence, l.JobID, l.Gen)+` returning generation`, boxID).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLeaseLost
	}
	return g, err
}

func (s *PG) SetStatus(ctx context.Context, l Lease, boxID, status string) error {
	return s.fenced(ctx, l, `update cloud_boxes set status = $2, updated_at = now() where id = $1`, boxID, status)
}

func (s *PG) SetServer(ctx context.Context, l Lease, boxID string, si ServerInfo) error {
	return s.fenced(ctx, l, `update cloud_boxes set hetzner_server_id = $2, ipv4 = $3, ipv6 = $4, server_type = $5, location = $6,
		volume_gb = coalesce(nullif($7, 0), volume_gb), addr_mac = coalesce(nullif($8, ''), addr_mac), updated_at = now() where id = $1`,
		boxID, nullID(si.ID), nullable(si.IPv4), nullable(si.IPv6), si.Type, si.Location, si.VolumeGB, si.MAC)
}

func (s *PG) SetResources(ctx context.Context, l Lease, boxID string, r Resources) error {
	return s.fenced(ctx, l, `update cloud_boxes set hetzner_server_id = coalesce($2, hetzner_server_id), hetzner_volume_id = coalesce($3, hetzner_volume_id),
		hetzner_firewall_id = coalesce($4, hetzner_firewall_id), hetzner_ssh_key_id = $5, updated_at = now() where id = $1`,
		boxID, nullID(r.Server), nullID(r.Volume), nullID(r.Firewall), nullID(r.SSHKey))
}

func (s *PG) SetDNS(ctx context.Context, l Lease, boxID, state string) error {
	return s.fenced(ctx, l, `update cloud_boxes set dns_state = case when killed_at is not null then 'killed' else $2 end,
		dns_changed_at = now(), updated_at = now() where id = $1`, boxID, state)
}

func (s *PG) SetFingerprint(ctx context.Context, l Lease, boxID, fp string) error {
	return s.fenced(ctx, l, `update cloud_boxes set token_fingerprint = $2, updated_at = now() where id = $1`, boxID, fp)
}

func (s *PG) KeepToken(ctx context.Context, l Lease, boxID, sealed string) error {
	return s.fenced(ctx, l, `update cloud_boxes set token_sealed = $2, token_kept_at = case when $2::text is null then null else now() end, updated_at = now() where id = $1`,
		boxID, nullable(sealed))
}

func (s *PG) SetSignin(ctx context.Context, l Lease, boxID, code string, expires time.Time) error {
	var exp *time.Time
	if code != "" {
		exp = &expires
	}
	return s.fenced(ctx, l, `update cloud_boxes set signin_code = $2, signin_expires_at = $3, updated_at = now() where id = $1`, boxID, nullable(code), exp)
}

func (s *PG) Released(ctx context.Context, l Lease, boxID string) error {
	return s.fenced(ctx, l, `update cloud_boxes set status = 'released', released_at = coalesce(released_at, now()), token_sealed = null, token_kept_at = null,
		signin_code = null, signin_expires_at = null, updated_at = now() where id = $1`, boxID)
}

func (s *PG) QueueEmail(ctx context.Context, l Lease, boxID, kind, key string, params map[string]any) error {
	if params == nil {
		params = map[string]any{}
	}
	raw, _ := json.Marshal(params)
	_, err := s.Pool.Exec(ctx, `insert into cloud_outbox (box_id, kind, key, params) select $1, $2, $3, $4::jsonb
		where exists (select 1 from cloud_jobs fj where fj.id = $5 and fj.lease_gen = $6 and fj.status = 'running')
		on conflict (box_id, kind, key) do nothing`, boxID, kind, key, string(raw), l.JobID, l.Gen)
	return err
}

func (s *PG) RecordCall(ctx context.Context, boxID string, jobID int64, purpose string, c Call) error {
	var status *int
	if c.Status != 0 {
		status = &c.Status
	}
	_, err := s.Pool.Exec(ctx, `insert into cloud_hetzner_calls (box_id, job_id, purpose, at, method, path, status, ms, error) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		boxID, nullID(jobID), purpose, c.At, c.Method, c.Path, status, c.Ms, nullable(c.Error))
	return err
}

func (s *PG) EnqueueCleanup(ctx context.Context, l Lease, boxID string, a CleanupArgs, sealed string, exp *time.Time) error {
	raw, _ := json.Marshal(a)
	_, err := s.Pool.Exec(ctx, `insert into cloud_jobs (box_id, kind, args, token_sealed, token_expires_at) select $1, 'cleanup', $2::jsonb, $3, $4
		where exists (select 1 from cloud_jobs fj where fj.id = $5 and fj.lease_gen = $6 and fj.status = 'running')`, boxID, string(raw), nullable(sealed), exp, l.JobID, l.Gen)
	return err
}

func (s *PG) CertPending(ctx context.Context, olderThan time.Time, limit int) ([]Box, error) {
	rows, err := s.Pool.Query(ctx, `select id from cloud_boxes where status = 'cert_pending' and coalesce(https_checked_at, 'epoch') < $1 order by https_checked_at nulls first limit $2`, olderThan, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var out []Box
	for _, id := range ids {
		if b, err := s.Box(ctx, id); err == nil {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (s *PG) CheckedHTTPS(ctx context.Context, boxID string) error {
	_, err := s.Pool.Exec(ctx, `update cloud_boxes set https_checked_at = now() where id = $1`, boxID)
	return err
}

func (s *PG) MarkReady(ctx context.Context, boxID string) (bool, error) {
	ok := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update cloud_boxes set status = 'active', ready_at = coalesce(ready_at, now()), https_checked_at = now(), updated_at = now()
			where id = $1 and status = 'cert_pending'`, boxID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ok = true
		_, err = tx.Exec(ctx, `insert into cloud_outbox (box_id, kind, key) values ($1, 'ready', '') on conflict do nothing`, boxID)
		return err
	})
	return ok, err
}

// MaxAttempts is how often a job is tried before it stays failed.
const MaxAttempts = 5

func (s *PG) Sweep(ctx context.Context, now time.Time) (string, error) {
	var out []string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Jobs whose lease ran out: their worker stopped (a deploy, a crash)
		// or lost the database. That worker cancels itself before its lease
		// ends, and its writes are fenced by lease_gen from now on.
		rows, err := tx.Query(ctx, `select id, box_id, kind, attempts, token_sealed, token_expires_at from cloud_jobs where status = 'running' and lease_until < $1 for update`, now)
		if err != nil {
			return err
		}
		type stale struct {
			id       int64
			box      string
			kind     string
			attempts int
			tok      *string
			exp      *time.Time
		}
		var stales []stale
		for rows.Next() {
			var st stale
			if err := rows.Scan(&st.id, &st.box, &st.kind, &st.attempts, &st.tok, &st.exp); err != nil {
				rows.Close()
				return err
			}
			stales = append(stales, st)
		}
		rows.Close()
		for _, st := range stales {
			if st.kind == "provision" {
				// A setup is not resumed half way: it fails, and a clean-up job
				// (with the same key, while it lasts) removes what it made.
				if _, err := tx.Exec(ctx, `update cloud_jobs set status = 'failed', token_sealed = null, finished_at = $2, lease_until = null,
					error = 'the worker stopped while this ran; we clean up what it made, then you can try again' where id = $1`, st.id, now); err != nil {
					return err
				}
				var gen int64
				if err := tx.QueryRow(ctx, `update cloud_boxes set status = 'failed', updated_at = now() where id = $1 and status = 'provisioning' returning generation`, st.box).Scan(&gen); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				args, _ := json.Marshal(CleanupArgs{Reason: "setup_failed", Gen: gen})
				if _, err := tx.Exec(ctx, `insert into cloud_jobs (box_id, kind, args, token_sealed, token_expires_at) values ($1, 'cleanup', $2::jsonb, $3, $4)`,
					st.box, string(args), st.tok, st.exp); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `insert into cloud_outbox (box_id, kind, key, params) values ($1, 'setup_failed', $2, '{}'::jsonb) on conflict do nothing`, st.box, fmt.Sprint(gen)); err != nil {
					return err
				}
				out = append(out, "failed a stale setup of "+st.box+" and queued its clean-up")
				continue
			}
			if st.attempts >= MaxAttempts {
				if _, err := tx.Exec(ctx, `update cloud_jobs set status = 'failed', token_sealed = null, finished_at = $2, lease_until = null,
					error = 'the worker stopped while this ran, too many times' where id = $1`, st.id, now); err != nil {
					return err
				}
				out = append(out, "gave up on a "+st.kind+" job of "+st.box)
				continue
			}
			// Everything else picks up where it stopped (its checkpoints say how far it got).
			if _, err := tx.Exec(ctx, `update cloud_jobs set status = 'queued', lease_until = null where id = $1`, st.id); err != nil {
				return err
			}
			out = append(out, "retrying a stale "+st.kind+" job of "+st.box)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	tag, err := s.Pool.Exec(ctx, `update cloud_jobs set token_sealed = null where token_sealed is not null and (token_expires_at is null or token_expires_at < $1)`, now)
	if err != nil {
		return "", err
	}
	if n := tag.RowsAffected(); n > 0 {
		out = append(out, fmt.Sprintf("forgot %d job tokens", n))
	}
	tag, err = s.Pool.Exec(ctx, `update cloud_boxes set signin_code = null, signin_expires_at = null where signin_code is not null and signin_expires_at < $1`, now)
	if err != nil {
		return "", err
	}
	if n := tag.RowsAffected(); n > 0 {
		out = append(out, fmt.Sprintf("forgot %d sign-in links", n))
	}
	// An address still live on a box that failed, is being deleted or was
	// released: remove it (again, if an earlier try failed), at most every
	// five minutes per box.
	tag, err = s.Pool.Exec(ctx, `insert into cloud_jobs (box_id, kind, args)
		select b.id, 'dns_remove', jsonb_build_object('reason', case b.status when 'failed' then 'setup_failed' else 'released' end, 'gen', b.generation)
		from cloud_boxes b where b.dns_state = 'live' and b.status in ('failed', 'deleting', 'released')
		and not exists (select 1 from cloud_jobs j where j.box_id = b.id and (j.status in ('queued', 'running') or j.created_at > $1::timestamptz - interval '5 minutes'))`, now)
	if err != nil {
		return "", err
	}
	if n := tag.RowsAffected(); n > 0 {
		out = append(out, fmt.Sprintf("queued the removal of %d stray addresses", n))
	}
	if len(out) == 0 {
		return "", nil
	}
	return fmt.Sprint(out), nil
}
