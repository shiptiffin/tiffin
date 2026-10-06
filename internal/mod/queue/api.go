package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// toProblem turns engine errors into API problems with hints.
func toProblem(err error) error {
	var qe *Error
	if errors.As(err, &qe) {
		p := api.NewProblem(qe.Status, qe.Code, qe.Msg)
		p.Hint = qe.Hint
		return p
	}
	return err
}

func (m *Module) ready() (*Engine, error) {
	e := m.engine()
	if e == nil {
		m.mu.RLock()
		st := m.status
		m.mu.RUnlock()
		if st == "" {
			st = "not started"
		}
		p := api.NewProblem(503, "precondition", "the queue is not running yet: "+st)
		p.Hint = "queues need the platform Postgres; check `tiffin status`"
		return nil, p
	}
	return e, nil
}

func actor(p *tokens.Principal) string {
	s := p.Name + " (" + p.Kind + ")"
	if p.Session != "" {
		s += " session " + p.Session
	}
	return s
}

func audit(ctx context.Context, plat *platform.Platform, p *tokens.Principal, action, target string, detail map[string]any) {
	if plat == nil || plat.DB == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["session"] = p.Session
	_ = plat.DB.Audit(ctx, p.TokenID, action, target, detail)
}

type out[T any] struct{ Body T }

func ok[T any](v T) *out[T] { return &out[T]{Body: v} }

// RegisterAPI adds the queue and workflow operations (CLI commands and MCP
// tools are generated from them) and the app-facing internal endpoints.
func (m *Module) RegisterAPI(a huma.API, plat *platform.Platform) {
	m.registerQueueAPI(a, plat)
	m.registerWorkflowAPI(a, plat)
	m.registerInternal(a)
}

type sendBody struct {
	Name         string          `json:"name" minLength:"1" maxLength:"64" doc:"Queue or topic name. A name with subscribers (see queue topics) fans out; any other name is a queue."`
	Payload      json.RawMessage `json:"payload,omitempty" doc:"Job payload: any JSON, up to 1 MB (on the CLI: --payload '{...}')."`
	DelaySeconds int             `json:"delaySeconds,omitempty" minimum:"0" maximum:"31622400" doc:"Run after this many seconds"`
	RunAt        *time.Time      `json:"runAt,omitempty" doc:"Run at this time (RFC 3339). Delay is added to it."`
	Key          string          `json:"key,omitempty" maxLength:"200" doc:"Limit key: the queue's keyConcurrency and rateLimit apply per key (e.g. a customer ID)"`
	GroupKey     string          `json:"groupKey,omitempty" maxLength:"200" doc:"FIFO group: jobs with the same group run one at a time, in send order"`
	Dedupe       string          `json:"dedupe,omitempty" maxLength:"200" doc:"Idempotency key: sending it again within 24 hours enqueues nothing and returns the first job"`
	Priority     string          `json:"priority,omitempty" enum:"high,normal,low" doc:"Default normal. Retries always run after new jobs."`
	App          string          `json:"app,omitempty" doc:"App whose route handles the job (queues only). Default: the queue's configured app."`
	Path         string          `json:"path,omitempty" doc:"Path on the app (default /queues/<name>)"`
	MaxAttempts  int             `json:"maxAttempts,omitempty" minimum:"0" maximum:"100" doc:"Override the queue's attempts before the dead-letter queue"`
}

func (b sendBody) request(by, fromApp string) SendRequest {
	r := SendRequest{Name: b.Name, Payload: b.Payload, Delay: time.Duration(b.DelaySeconds) * time.Second, Key: b.Key,
		GroupKey: b.GroupKey, Dedupe: b.Dedupe, Priority: b.Priority, App: b.App, Path: b.Path, MaxAttempts: b.MaxAttempts, By: by, FromApp: fromApp}
	if b.RunAt != nil {
		r.RunAt = *b.RunAt
	}
	return r
}

type configBody struct {
	App               string `json:"app,omitempty" doc:"App that receives this queue's jobs"`
	Path              string `json:"path,omitempty" doc:"Path on the app (default /queues/<name>)"`
	URL               string `json:"url,omitempty" doc:"Development only: a loopback URL (http://127.0.0.1:PORT/...) to push to instead of an app"`
	Concurrency       int    `json:"concurrency,omitempty" minimum:"0" maximum:"10000" doc:"Most jobs of this queue running at once (0 = no limit)"`
	KeyConcurrency    int    `json:"keyConcurrency,omitempty" minimum:"0" maximum:"10000" doc:"Most jobs running at once per send key (0 = no limit)"`
	RateLimit         int    `json:"rateLimit,omitempty" minimum:"0" maximum:"10000" doc:"Most jobs started per period, per send key (0 = no limit)"`
	RatePeriodSeconds int    `json:"ratePeriodSeconds,omitempty" minimum:"0" maximum:"86400" doc:"The rate limit window"`
	MaxAttempts       int    `json:"maxAttempts,omitempty" minimum:"0" maximum:"100" doc:"Attempts before the dead-letter queue (default 10)"`
	LeaseSeconds      int    `json:"leaseSeconds,omitempty" minimum:"0" maximum:"3600" doc:"How long an attempt may run without answering or heartbeating (default 60)"`
	Paused            bool   `json:"paused,omitempty" doc:"Accept jobs but deliver none"`
}

func (m *Module) registerQueueAPI(a huma.API, plat *platform.Platform) {
	tag := "queue"
	op := func(id, method, path, cli, risk, summary, desc string, errs ...int) huma.Operation {
		o := api.Op(id, method, path, cli, risk, summary, desc, tag)
		o.Errors = append(o.Errors, errs...)
		o.Errors = append(o.Errors, 503)
		return o
	}

	huma.Register(a, op("queue-send", http.MethodPost, "/v1/projects/{project}/queue/send", "queue send", api.RiskWrite,
		"Send a job to a queue or topic",
		"Enqueues a job. The box pushes it (HTTP POST, signed) to the app route that handles the queue, retries failures with "+
			"exponential backoff, and moves it to the dead-letter queue after maxAttempts. A topic name fans out one job per "+
			"subscriber. Use delaySeconds/runAt to schedule, key for per-key limits, groupKey for FIFO order and dedupe for idempotency.", 404, 409),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    sendBody
		}) (*out[*SendResult], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			res, err := e.Send(ctx, in.Project, in.Body.request(actor(p), ""))
			if err != nil {
				return nil, toProblem(err)
			}
			return ok(res), nil
		}))

	huma.Register(a, api.Untrusted(op("queue-jobs-list", http.MethodGet, "/v1/projects/{project}/queue/jobs", "queue jobs list", api.RiskRead,
		"List jobs",
		"Jobs of a project, newest first, without payloads. Filter by queue and state; state=dead is the dead-letter queue. "+
			"Page with before=<last id>. Queued jobs say what they are waitingFor (a limit, a FIFO group, a paused queue).")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Queue   string `query:"queue" doc:"Only this queue or topic"`
			State   string `query:"state" enum:"scheduled,queued,running,retrying,completed,dead,cancelled" doc:"Only jobs in this state"`
			Before  string `query:"before" doc:"Page: jobs older than this job ID"`
			Limit   int    `query:"limit" minimum:"1" maximum:"500" default:"50"`
		}) (*out[[]Job], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			f := ListFilter{Queue: in.Queue, State: in.State, Limit: in.Limit}
			if in.Before != "" {
				if f.Before, err = ParseJobID(in.Before); err != nil {
					return nil, toProblem(err)
				}
			}
			js, err := e.ListJobs(ctx, in.Project, f)
			return ok(js), toProblem(err)
		}))

	type jobPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" doc:"Job ID, e.g. job_42"`
	}
	withJob := func(ctx context.Context, in *jobPath, scope tokens.Scope) (*Engine, int64, error) {
		if err := api.PrincipalFrom(ctx).Require(scope, in.Project); err != nil {
			return nil, 0, err
		}
		e, err := m.ready()
		if err != nil {
			return nil, 0, err
		}
		id, err := ParseJobID(in.ID)
		return e, id, toProblem(err)
	}

	huma.Register(a, api.Untrusted(op("queue-job-get", http.MethodGet, "/v1/projects/{project}/queue/jobs/{id}", "queue jobs get", api.RiskRead,
		"Get a job",
		"One job with its payload, the app's response (output) and every attempt: when it ran, how long, the HTTP status and error.", 404)),
		api.Wrap(func(ctx context.Context, in *jobPath) (*out[*Job], error) {
			e, id, err := withJob(ctx, in, tokens.ScopeRead)
			if err != nil {
				return nil, err
			}
			j, err := e.GetJob(ctx, in.Project, id)
			return ok(j), toProblem(err)
		}))

	huma.Register(a, op("queue-job-retry", http.MethodPost, "/v1/projects/{project}/queue/jobs/{id}/retry", "queue jobs retry", api.RiskWrite,
		"Retry or replay a job now",
		"Runs a job again now. Dead (dead-letter), cancelled and completed jobs are replayed with a fresh attempt count; "+
			"scheduled and retrying jobs stop waiting. A replayed workflow turn resumes its failed run.", 404, 409),
		api.Wrap(func(ctx context.Context, in *jobPath) (*out[*Job], error) {
			e, id, err := withJob(ctx, in, tokens.ScopeApplyReversible)
			if err != nil {
				return nil, err
			}
			j, err := e.RetryJob(ctx, in.Project, id)
			if err == nil {
				audit(ctx, plat, api.PrincipalFrom(ctx), "queue.job.retry", in.Project+"/"+in.ID, nil)
			}
			return ok(j), toProblem(err)
		}))

	huma.Register(a, op("queue-job-cancel", http.MethodPost, "/v1/projects/{project}/queue/jobs/{id}/cancel", "queue jobs cancel", api.RiskWrite,
		"Cancel a job",
		"Stops a job: if it hasn't run it never will; if it is running, the request to the app is cut off. "+
			"Undo with queue jobs retry (replays it).", 404, 409),
		api.Wrap(func(ctx context.Context, in *jobPath) (*out[*Job], error) {
			e, id, err := withJob(ctx, in, tokens.ScopeApplyReversible)
			if err != nil {
				return nil, err
			}
			j, err := e.CancelJob(ctx, in.Project, id)
			if err == nil {
				audit(ctx, plat, api.PrincipalFrom(ctx), "queue.job.cancel", in.Project+"/"+in.ID, nil)
			}
			return ok(j), toProblem(err)
		}))

	huma.Register(a, op("queue-dlq-replay", http.MethodPost, "/v1/projects/{project}/queue/dlq/replay", "queue dlq replay", api.RiskWrite,
		"Replay dead-letter jobs",
		"Puts dead jobs back on their queues, oldest first. Dry run by default: it reports how many would be replayed; "+
			"send dryRun=false to replay. Fix the cause first (see queue jobs get for the error) or they will die again."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    struct {
				Queue  string `json:"queue,omitempty" doc:"Only this queue (default: all)"`
				Limit  int    `json:"limit,omitempty" minimum:"0" maximum:"1000" doc:"At most this many (default 100)"`
				DryRun *bool  `json:"dryRun,omitempty" doc:"Default true: only count"`
			}
		}) (*out[*ReplayResult], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			dry := in.Body.DryRun == nil || *in.Body.DryRun
			res, err := e.ReplayDead(ctx, in.Project, in.Body.Queue, in.Body.Limit, dry)
			if err == nil && !dry {
				audit(ctx, plat, p, "queue.dlq.replay", in.Project+"/"+in.Body.Queue, map[string]any{"count": res.Count})
			}
			return ok(res), toProblem(err)
		}))

	type queuePath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Queue   string `path:"queue" doc:"Queue or topic name"`
	}

	huma.Register(a, op("queue-purge", http.MethodPost, "/v1/projects/{project}/queue/queues/{queue}/purge", "queue purge", api.RiskDestructive,
		"Purge a queue's waiting jobs",
		"Deletes a queue's scheduled, queued and retrying jobs (and dead ones with dead=true) for good. Two steps: the first call "+
			"only counts them and returns a confirm token; call again with confirm=<token> to delete. Running jobs are not touched."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Queue   string `path:"queue" doc:"Queue or topic name"`
			Body    struct {
				Dead    bool   `json:"dead,omitempty" doc:"Also delete the queue's dead-letter jobs"`
				Confirm string `json:"confirm,omitempty" doc:"The token from the counting call"`
			}
		}) (*out[*PurgeResult], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			res, err := e.Purge(ctx, in.Project, in.Queue, in.Body.Dead, in.Body.Confirm)
			if err == nil && res.Purged {
				audit(ctx, plat, p, "queue.purge", in.Project+"/"+in.Queue, map[string]any{"count": res.Count, "dead": in.Body.Dead})
			}
			return ok(res), toProblem(err)
		}))

	huma.Register(a, op("queue-stats", http.MethodGet, "/v1/projects/{project}/queue/stats", "queue stats", api.RiskRead,
		"Show queues and their health",
		"Every queue and topic with its settings, depth (queued), scheduled, running, retrying and dead counts, the age of the oldest "+
			"due job, completions and failed attempts in the last hour, failure rate and p50/p95 attempt durations."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Queue   string `query:"queue" doc:"Only this queue"`
		}) (*out[[]QueueStats], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			s, err := e.Stats(ctx, in.Project, in.Queue)
			return ok(s), toProblem(err)
		}))

	huma.Register(a, op("queue-configure", http.MethodPut, "/v1/projects/{project}/queue/queues/{queue}", "queue configure", api.RiskWrite,
		"Configure a queue",
		"Sets a queue's target app and path, limits (concurrency, keyConcurrency, rateLimit per ratePeriodSeconds), maxAttempts and "+
			"leaseSeconds. Replaces the whole configuration: omitted fields go back to defaults. Jobs already waiting pick up new limits at once."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Queue   string `path:"queue" doc:"Queue or topic name"`
			Body    configBody
		}) (*out[QueueConfig], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			b := in.Body
			c, err := e.ConfigureQueue(ctx, in.Project, QueueConfig{Name: in.Queue, App: b.App, Path: b.Path, URL: b.URL, Concurrency: b.Concurrency,
				KeyConcurrency: b.KeyConcurrency, RateLimit: b.RateLimit, RatePeriodS: b.RatePeriodSeconds, MaxAttempts: b.MaxAttempts,
				LeaseS: b.LeaseSeconds, Paused: b.Paused})
			if err == nil {
				audit(ctx, plat, p, "queue.configure", in.Project+"/"+in.Queue, map[string]any{"config": c})
			}
			return ok(c), toProblem(err)
		}))

	for _, pause := range []bool{true, false} {
		id, cli, summary, desc := "queue-pause", "queue pause", "Pause a queue",
			"Stops delivering a queue's jobs. Sends still succeed; jobs wait (waitingFor says the queue is paused) until queue resume."
		if !pause {
			id, cli, summary, desc = "queue-resume", "queue resume", "Resume a paused queue", "Starts delivering a paused queue's jobs again, oldest first."
		}
		huma.Register(a, op(id, http.MethodPost, "/v1/projects/{project}/queue/queues/{queue}/"+cli[len("queue "):], cli, api.RiskWrite, summary, desc),
			api.Wrap(func(ctx context.Context, in *queuePath) (*out[QueueConfig], error) {
				p := api.PrincipalFrom(ctx)
				if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
					return nil, err
				}
				e, err := m.ready()
				if err != nil {
					return nil, err
				}
				c, err := e.SetPaused(ctx, in.Project, in.Queue, pause)
				if err == nil {
					audit(ctx, plat, p, "queue."+cli[len("queue "):], in.Project+"/"+in.Queue, nil)
				}
				return ok(c), toProblem(err)
			}))
	}

	huma.Register(a, op("queue-topics-list", http.MethodGet, "/v1/projects/{project}/queue/topics", "queue topics list", api.RiskRead,
		"List topics and subscribers",
		"Topics fan out: each message sent to a topic becomes one job per subscriber, delivered and retried independently."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		}) (*out[[]Topic], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			ts, err := e.Topics(ctx, in.Project)
			return ok(ts), toProblem(err)
		}))

	type subPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Topic   string `path:"topic" doc:"Topic name"`
		Name    string `path:"name" doc:"Subscription name"`
	}
	huma.Register(a, op("queue-subscribe", http.MethodPut, "/v1/projects/{project}/queue/topics/{topic}/subscriptions/{name}", "queue topics subscribe", api.RiskWrite,
		"Subscribe an app to a topic",
		"Creates the topic if needed and adds (or updates) a subscriber: an app route that receives every message sent to the topic from now on."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Topic   string `path:"topic" doc:"Topic name"`
			Name    string `path:"name" doc:"Subscription name, e.g. the subscribing app"`
			Body    struct {
				App  string `json:"app,omitempty" doc:"App that receives the messages"`
				Path string `json:"path,omitempty" doc:"Path on the app (default /topics/<topic>)"`
				URL  string `json:"url,omitempty" doc:"Development only: a loopback URL instead of an app"`
			}
		}) (*out[*Topic], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			t, err := e.Subscribe(ctx, in.Project, in.Topic, Subscription{Name: in.Name, App: in.Body.App, Path: in.Body.Path, URL: in.Body.URL})
			if err == nil {
				audit(ctx, plat, p, "queue.subscribe", in.Project+"/"+in.Topic+"/"+in.Name, nil)
			}
			return ok(t), toProblem(err)
		}))

	huma.Register(a, op("queue-unsubscribe", http.MethodDelete, "/v1/projects/{project}/queue/topics/{topic}/subscriptions/{name}", "queue topics unsubscribe", api.RiskWrite,
		"Remove a topic subscriber",
		"The subscriber gets no new messages; jobs already created for it still run.", 404),
		api.Wrap(func(ctx context.Context, in *subPath) (*struct{}, error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			if err := e.Unsubscribe(ctx, in.Project, in.Topic, in.Name); err != nil {
				return nil, toProblem(err)
			}
			audit(ctx, plat, p, "queue.unsubscribe", in.Project+"/"+in.Topic+"/"+in.Name, nil)
			return &struct{}{}, nil
		}))

	huma.Register(a, op("queue-crons-list", http.MethodGet, "/v1/projects/{project}/queue/crons", "queue crons list", api.RiskRead,
		"List crons",
		"The project's crons with their origin (tiffin.config.ts, or an app's vercel.json), time zone, next tick (UTC), the job and state of the latest tick, "+
			"and when a tick was last skipped because the previous run was still queued or running. Change crons in tiffin.config.ts; "+
			"vercel.json crons change with their app's next production deploy."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		}) (*out[[]CronInfo], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			cs, err := e.Crons(ctx, in.Project)
			return ok(cs), toProblem(err)
		}))

	huma.Register(a, op("queue-cron-trigger", http.MethodPost, "/v1/projects/{project}/queue/crons/{name}/trigger", "queue crons trigger", api.RiskWrite,
		"Run a cron now",
		"Calls the cron's app route now, outside its schedule (the schedule is unchanged). Returns the job to follow.", 404),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Name    string `path:"name" doc:"Cron name"`
		}) (*out[map[string]string], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			id, err := e.TriggerCron(ctx, in.Project, in.Name, actor(p))
			if err != nil {
				return nil, toProblem(err)
			}
			audit(ctx, plat, p, "queue.cron.trigger", in.Project+"/"+in.Name, nil)
			return ok(map[string]string{"job": id, "message": "cron " + in.Name + " queued as " + id}), nil
		}))
}

func (m *Module) registerWorkflowAPI(a huma.API, plat *platform.Platform) {
	tag := "workflows"
	op := func(id, method, path, cli, risk, summary, desc string, errs ...int) huma.Operation {
		o := api.Op(id, method, path, cli, risk, summary, desc, tag)
		o.Errors = append(o.Errors, errs...)
		o.Errors = append(o.Errors, 503)
		return o
	}
	type runPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" doc:"Run ID, e.g. run_01J..."`
	}

	huma.Register(a, op("workflow-start", http.MethodPost, "/v1/projects/{project}/workflows/runs", "workflows start", api.RiskWrite,
		"Start a workflow run",
		"Starts a durable workflow defined in an app with tiffin-sdk (workflow.define). The run is pinned to the app's current release. "+
			"Pass id to make it idempotent: the same id returns the existing run.", 409),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    struct {
				Workflow string          `json:"workflow" minLength:"1" maxLength:"64" doc:"Workflow name as defined in the app"`
				App      string          `json:"app,omitempty" doc:"App that defines the workflow"`
				Input    json.RawMessage `json:"input,omitempty" doc:"Input passed to the workflow (JSON)"`
				ID       string          `json:"id,omitempty" maxLength:"200" doc:"Idempotency key"`
				Path     string          `json:"path,omitempty" doc:"Where the app mounts the workflow handler (default /_tiffin/workflows)"`
				URL      string          `json:"url,omitempty" doc:"Development only: loopback URL of the workflow handler instead of an app"`
			}
		}) (*out[*Run], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			b := in.Body
			run, _, err := e.StartRun(ctx, in.Project, StartRequest{Workflow: b.Workflow, Input: b.Input, ID: b.ID, App: b.App, Path: b.Path, URL: b.URL, By: actor(p)})
			return ok(run), toProblem(err)
		}))

	huma.Register(a, api.Untrusted(op("workflow-runs-list", http.MethodGet, "/v1/projects/{project}/workflows/runs", "workflows runs list", api.RiskRead,
		"List workflow runs",
		"Runs newest first, with state (running, waiting, completed, failed, cancelled) and, for waiting runs, what they wait for.")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project  string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Workflow string `query:"workflow" doc:"Only this workflow"`
			State    string `query:"state" enum:"running,waiting,completed,failed,cancelled" doc:"Only runs in this state"`
			Limit    int    `query:"limit" minimum:"1" maximum:"500" default:"50"`
		}) (*out[[]Run], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			rs, err := e.ListRuns(ctx, in.Project, RunFilter{Workflow: in.Workflow, State: in.State, Limit: in.Limit})
			return ok(rs), toProblem(err)
		}))

	huma.Register(a, api.Untrusted(op("workflow-run-get", http.MethodGet, "/v1/projects/{project}/workflows/runs/{id}", "workflows runs get", api.RiskRead,
		"Get a workflow run and its timeline",
		"A run with its input, output or error, every step in call order (state, timing, output, attempts), the timeline of turns, waits, "+
			"events, approvals and operator actions, and the queue job behind each turn.", 404)),
		api.Wrap(func(ctx context.Context, in *runPath) (*out[*Run], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			r, err := e.GetRun(ctx, in.Project, in.ID, true)
			return ok(r), toProblem(err)
		}))

	huma.Register(a, op("workflow-run-cancel", http.MethodPost, "/v1/projects/{project}/workflows/runs/{id}/cancel", "workflows runs cancel", api.RiskWrite,
		"Cancel a workflow run",
		"Stops a run for good: its waits are cancelled, queued turns are dropped and a running turn is cut off. Completed steps are not undone.", 404, 409),
		api.Wrap(func(ctx context.Context, in *runPath) (*out[*Run], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			r, err := e.CancelRun(ctx, in.Project, in.ID, actor(p))
			if err == nil {
				audit(ctx, plat, p, "workflow.cancel", in.Project+"/"+in.ID, nil)
			}
			return ok(r), toProblem(err)
		}))

	huma.Register(a, op("workflow-run-retry", http.MethodPost, "/v1/projects/{project}/workflows/runs/{id}/retry", "workflows runs retry", api.RiskWrite,
		"Retry a failed workflow run",
		"Resumes a failed run from its last checkpoint: completed steps keep their results, failed steps run again with fresh attempts.", 404, 409),
		api.Wrap(func(ctx context.Context, in *runPath) (*out[*Run], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			r, err := e.RetryRun(ctx, in.Project, in.ID, actor(p))
			if err == nil {
				audit(ctx, plat, p, "workflow.retry", in.Project+"/"+in.ID, nil)
			}
			return ok(r), toProblem(err)
		}))

	huma.Register(a, op("workflow-event-send", http.MethodPost, "/v1/projects/{project}/workflows/events", "workflows events send", api.RiskWrite,
		"Send a workflow event",
		"Emits a named event (e.g. order-123-paid) that resumes every run waiting for it (ctx.waitForEvent). The first emit of a name "+
			"wins: later emits are ignored. Events are kept 30 days, so a run that starts waiting later still receives it."),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    struct {
				Name    string          `json:"name" minLength:"1" maxLength:"300" doc:"Event name"`
				Payload json.RawMessage `json:"payload,omitempty" doc:"Event payload (JSON) handed to the waiting runs"`
			}
		}) (*out[*EmitResult], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			res, err := e.Emit(ctx, in.Project, in.Body.Name, in.Body.Payload, actor(p))
			if err == nil {
				audit(ctx, plat, p, "workflow.event", in.Project+"/"+in.Body.Name, map[string]any{"woke": res.Woke})
			}
			return ok(res), toProblem(err)
		}))

	huma.Register(a, api.Untrusted(op("workflow-approvals-list", http.MethodGet, "/v1/projects/{project}/workflows/approvals", "workflows approvals list", api.RiskRead,
		"List workflow approvals",
		"Approval steps (ctx.approval) waiting for a decision, or all with state=all. humanOnly approvals can only be decided by a person.")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			State   string `query:"state" enum:"waiting,completed,timed_out,cancelled,all" default:"waiting"`
		}) (*out[[]Approval], error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			as, err := e.Approvals(ctx, in.Project, in.State)
			return ok(as), toProblem(err)
		}))

	huma.Register(a, op("workflow-approval-decide", http.MethodPost, "/v1/projects/{project}/workflows/approvals/{id}", "workflows approvals decide", api.RiskWrite,
		"Approve or reject a workflow approval",
		"Decides a waiting approval and resumes its run. Approvals marked humanOnly refuse agent tokens: a person must decide them "+
			"(dashboard or a human token). The decision, who made it and the comment go on the run's timeline.", 404, 409),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			ID      string `path:"id" doc:"Approval ID, e.g. apr_12"`
			Body    struct {
				Decision string `json:"decision" enum:"approve,reject" doc:"approve or reject"`
				Comment  string `json:"comment,omitempty" maxLength:"2000" doc:"Why, for the timeline"`
			}
		}) (*out[*Approval], error) {
			p := api.PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			e, err := m.ready()
			if err != nil {
				return nil, err
			}
			human := p.Kind == tokens.KindHuman || p.Kind == tokens.KindOwner
			res, err := e.Decide(ctx, in.Project, in.ID, in.Body.Decision == "approve", in.Body.Comment, Decider{Name: actor(p), Human: human})
			if err == nil {
				audit(ctx, plat, p, "workflow.approval."+in.Body.Decision, in.Project+"/"+in.ID, nil)
			}
			return ok(res), toProblem(err)
		}))
}
