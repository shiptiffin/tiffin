package cloud

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var Schema string

// Job is one piece of work.
type Job struct {
	ID          int64
	BoxID       string
	Kind        string
	Args        json.RawMessage
	TokenSealed string
}

// Box is what the worker needs of a box row.
type Box struct {
	ID          string
	Name        string
	Status      string
	ServerType  string
	Location    string
	IPv4, IPv6  string
	DNSState    string
	TokenSealed string
}

// ServerInfo is what provisioning learned about the server.
type ServerInfo struct {
	ID         int64
	IPv4, IPv6 string
	Type       string
	Location   string
	VolumeGB   int
}

// Store is the worker's database.
type Store interface {
	Claim(ctx context.Context, lease time.Duration) (*Job, error)
	Extend(ctx context.Context, jobID int64, lease time.Duration) error
	Step(ctx context.Context, jobID int64, text string) error
	// Finish ends a job and clears its token, whatever the outcome.
	Finish(ctx context.Context, jobID int64, jobErr error) error
	Box(ctx context.Context, id string) (*Box, error)
	SetStatus(ctx context.Context, boxID, status string) error
	SetServer(ctx context.Context, boxID string, s ServerInfo) error
	SetDNS(ctx context.Context, boxID, state string) error
	SetFingerprint(ctx context.Context, boxID, fp string) error
	// KeepToken stores the sealed Hetzner token on the box (the customer
	// asked to keep it); "" forgets it.
	KeepToken(ctx context.Context, boxID, sealed string) error
	SetOwnerToken(ctx context.Context, boxID, sealed string, expires time.Time) error
	RecordCall(ctx context.Context, boxID string, jobID int64, purpose string, c Call) error
	// Sweep fails jobs whose worker stopped, wipes job tokens older than
	// maxTokenAge and owner tokens past their time. It says what it did.
	Sweep(ctx context.Context, now time.Time, maxTokenAge time.Duration) (string, error)
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
// not trip over each other.
func (s *PG) Apply(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(7316253)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, Schema)
		return err
	})
}

func (s *PG) Claim(ctx context.Context, lease time.Duration) (*Job, error) {
	var j Job
	var tok *string
	err := s.Pool.QueryRow(ctx, `
		update cloud_jobs set status = 'running', started_at = now(), lease_until = now() + $1::interval
		where id = (
			select id from cloud_jobs where status = 'queued'
			and not exists (select 1 from cloud_jobs r where r.box_id = cloud_jobs.box_id and r.status = 'running')
			order by created_at, id limit 1 for update skip locked)
		returning id, box_id, kind, args, token_sealed`, fmt.Sprintf("%d seconds", int(lease.Seconds()))).
		Scan(&j.ID, &j.BoxID, &j.Kind, &j.Args, &tok)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tok != nil {
		j.TokenSealed = *tok
	}
	return &j, nil
}

func (s *PG) Extend(ctx context.Context, jobID int64, lease time.Duration) error {
	_, err := s.Pool.Exec(ctx, `update cloud_jobs set lease_until = now() + $2::interval where id = $1 and status = 'running'`,
		jobID, fmt.Sprintf("%d seconds", int(lease.Seconds())))
	return err
}

func (s *PG) Step(ctx context.Context, jobID int64, text string) error {
	step, _ := json.Marshal([]map[string]string{{"at": time.Now().UTC().Format(time.RFC3339), "text": text}})
	_, err := s.Pool.Exec(ctx, `update cloud_jobs set steps = steps || $2::jsonb where id = $1`, jobID, string(step))
	return err
}

func (s *PG) Finish(ctx context.Context, jobID int64, jobErr error) error {
	status, msg := "done", (*string)(nil)
	if jobErr != nil {
		status = "failed"
		m := jobErr.Error()
		if len(m) > 2000 {
			m = m[:2000]
		}
		msg = &m
	}
	_, err := s.Pool.Exec(ctx, `update cloud_jobs set status = $2, error = $3, token_sealed = null, finished_at = now(), lease_until = null where id = $1`,
		jobID, status, msg)
	return err
}

func (s *PG) Box(ctx context.Context, id string) (*Box, error) {
	var b Box
	var name, st, loc, v4, v6, tok *string
	err := s.Pool.QueryRow(ctx, `select id, name, status, server_type, location, ipv4, ipv6, dns_state, token_sealed from cloud_boxes where id = $1`, id).
		Scan(&b.ID, &name, &b.Status, &st, &loc, &v4, &v6, &b.DNSState, &tok)
	if err != nil {
		return nil, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	b.Name, b.ServerType, b.Location, b.IPv4, b.IPv6, b.TokenSealed = deref(name), deref(st), deref(loc), deref(v4), deref(v6), deref(tok)
	return &b, nil
}

func (s *PG) exec(ctx context.Context, q string, args ...any) error {
	_, err := s.Pool.Exec(ctx, q, args...)
	return err
}

func (s *PG) SetStatus(ctx context.Context, boxID, status string) error {
	return s.exec(ctx, `update cloud_boxes set status = $2, updated_at = now() where id = $1`, boxID, status)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *PG) SetServer(ctx context.Context, boxID string, si ServerInfo) error {
	return s.exec(ctx, `update cloud_boxes set hetzner_server_id = $2, ipv4 = $3, ipv6 = $4, server_type = $5, location = $6,
		volume_gb = coalesce(nullif($7, 0), volume_gb), updated_at = now() where id = $1`,
		boxID, si.ID, nullable(si.IPv4), nullable(si.IPv6), si.Type, si.Location, si.VolumeGB)
}

func (s *PG) SetDNS(ctx context.Context, boxID, state string) error {
	return s.exec(ctx, `update cloud_boxes set dns_state = $2, updated_at = now() where id = $1`, boxID, state)
}

func (s *PG) SetFingerprint(ctx context.Context, boxID, fp string) error {
	return s.exec(ctx, `update cloud_boxes set token_fingerprint = $2, updated_at = now() where id = $1`, boxID, fp)
}

func (s *PG) KeepToken(ctx context.Context, boxID, sealed string) error {
	return s.exec(ctx, `update cloud_boxes set token_sealed = $2, token_kept_at = case when $2::text is null then null else now() end, updated_at = now() where id = $1`,
		boxID, nullable(sealed))
}

func (s *PG) SetOwnerToken(ctx context.Context, boxID, sealed string, expires time.Time) error {
	var exp *time.Time
	if sealed != "" {
		exp = &expires
	}
	return s.exec(ctx, `update cloud_boxes set owner_token_sealed = $2, owner_token_expires_at = $3, updated_at = now() where id = $1`, boxID, nullable(sealed), exp)
}

func (s *PG) RecordCall(ctx context.Context, boxID string, jobID int64, purpose string, c Call) error {
	var job *int64
	if jobID != 0 {
		job = &jobID
	}
	var status *int
	if c.Status != 0 {
		status = &c.Status
	}
	return s.exec(ctx, `insert into cloud_hetzner_calls (box_id, job_id, purpose, at, method, path, status, ms, error) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		boxID, job, purpose, c.At, c.Method, c.Path, status, c.Ms, nullable(c.Error))
}

func (s *PG) Sweep(ctx context.Context, now time.Time, maxTokenAge time.Duration) (string, error) {
	var out []string
	// A job whose lease ran out: its worker stopped (a deploy, a crash).
	rows, err := s.Pool.Query(ctx, `update cloud_jobs set status = 'failed', token_sealed = null, finished_at = $1, lease_until = null,
		error = 'the worker stopped while this ran; try again' where status = 'running' and lease_until < $1 returning box_id, kind`, now)
	if err != nil {
		return "", err
	}
	type stale struct{ box, kind string }
	var stales []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.box, &st.kind); err != nil {
			rows.Close()
			return "", err
		}
		stales = append(stales, st)
	}
	rows.Close()
	for _, st := range stales {
		out = append(out, "failed a stale "+st.kind+" job of "+st.box)
		if st.kind == "provision" {
			_ = s.exec(ctx, `update cloud_boxes set status = 'failed', updated_at = now() where id = $1 and status = 'provisioning'`, st.box)
		}
	}
	tag, err := s.Pool.Exec(ctx, `update cloud_jobs set token_sealed = null where token_sealed is not null and created_at < $1`, now.Add(-maxTokenAge))
	if err != nil {
		return "", err
	}
	if n := tag.RowsAffected(); n > 0 {
		out = append(out, fmt.Sprintf("forgot %d unused job tokens", n))
	}
	tag, err = s.Pool.Exec(ctx, `update cloud_boxes set owner_token_sealed = null, owner_token_expires_at = null where owner_token_sealed is not null and owner_token_expires_at < $1`, now)
	if err != nil {
		return "", err
	}
	if n := tag.RowsAffected(); n > 0 {
		out = append(out, fmt.Sprintf("forgot %d sign-in keys", n))
	}
	if len(out) == 0 {
		return "", nil
	}
	return fmt.Sprint(out), nil
}
