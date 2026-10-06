package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// Subscription is one subscriber of a topic: every message published to the
// topic becomes its own job for each subscriber, retried independently.
type Subscription struct {
	Name      string    `json:"name" doc:"Subscription name, unique within the topic"`
	App       string    `json:"app,omitempty" doc:"App that receives the messages"`
	Path      string    `json:"path,omitempty" doc:"Path on the app messages are POSTed to"`
	URL       string    `json:"url,omitempty" doc:"Address outside the box messages are POSTed to instead of an app"`
	CreatedAt time.Time `json:"createdAt"`
}

// Topic is a fan-out name with its subscribers.
type Topic struct {
	Name          string         `json:"name"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// Subscribe adds (or updates) a subscriber, creating the topic.
func (e *Engine) Subscribe(ctx context.Context, project, topic string, s Subscription) (*Topic, error) {
	if !nameRE.MatchString(topic) || !nameRE.MatchString(s.Name) {
		return nil, invalid("topic and subscription names are 1-64 lowercase letters, digits, dots, dashes or underscores", "")
	}
	if s.App == "" && s.URL == "" {
		return nil, invalid("a subscription needs an app, or a url outside the box", "")
	}
	if s.Path == "" && s.App != "" {
		s.Path = "/topics/" + topic
	}
	if err := e.validTarget(s.App, s.Path, s.URL); err != nil {
		return nil, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO tq_topics (project, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`, project, topic); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tq_subscriptions (project, topic, name, app, path, url) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project, topic, name) DO UPDATE SET app = $4, path = $5, url = $6`, project, topic, s.Name, s.App, s.Path, s.URL); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.topic(ctx, project, topic)
}

// Unsubscribe removes a subscriber. The topic stays (publishing to it with
// no subscribers enqueues nothing) so the name never silently turns back
// into a plain queue.
func (e *Engine) Unsubscribe(ctx context.Context, project, topic, name string) error {
	tag, err := e.pool.Exec(ctx, `DELETE FROM tq_subscriptions WHERE project = $1 AND topic = $2 AND name = $3`, project, topic, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound(fmt.Sprintf("topic %q has no subscription %q", topic, name))
	}
	return nil
}

func (e *Engine) topic(ctx context.Context, project, name string) (*Topic, error) {
	ts, err := e.Topics(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.Name == name {
			return &t, nil
		}
	}
	return nil, notFound("no topic " + strconv.Quote(name))
}

// Topics lists a project's topics and subscribers.
func (e *Engine) Topics(ctx context.Context, project string) ([]Topic, error) {
	rows, err := e.pool.Query(ctx, `SELECT t.name, s.name, s.app, s.path, s.url, s.created_at FROM tq_topics t
		LEFT JOIN tq_subscriptions s ON s.project = t.project AND s.topic = t.name WHERE t.project = $1 ORDER BY t.name, s.name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var t string
		var name, app, path, url *string
		var at *time.Time
		if err := rows.Scan(&t, &name, &app, &path, &url, &at); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != t {
			out = append(out, Topic{Name: t, Subscriptions: []Subscription{}})
		}
		if name != nil {
			cur := &out[len(out)-1]
			cur.Subscriptions = append(cur.Subscriptions, Subscription{Name: *name, App: *app, Path: *path, URL: *url, CreatedAt: *at})
		}
	}
	return out, rows.Err()
}

// PublishEvent sends a box event (such as storage.object.created) to the
// project's topic of that name. A project that has not declared the topic
// gets nothing: sent reports whether it was published.
func (m *Module) PublishEvent(ctx context.Context, _ *platform.Platform, project, topic string, payload json.RawMessage, dedupe string) (sent bool, err error) {
	e := m.engine()
	if e == nil {
		return false, errors.New("the queue is not running yet")
	}
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_topics WHERE project = $1 AND name = $2)`, project, topic).Scan(&sent); err != nil || !sent {
		return false, err
	}
	_, err = e.Send(ctx, project, SendRequest{Name: topic, Payload: payload, Dedupe: dedupe, By: "box"})
	return err == nil, err
}
