package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// After an upload through the front (a PUT, a completed multipart upload,
// a copy, or the API's own upload) the module publishes an object.created
// event to the project's queue topic ObjectCreatedTopic. A project that
// declares the topic (with a subscriber queue) gets every upload as a
// normal queue job; one that does not gets nothing.

// ObjectCreatedTopic is the queue topic upload events are published to.
const ObjectCreatedTopic = "storage.object.created"

// ObjectCreated is the payload of an object.created event.
type ObjectCreated struct {
	Event       string    `json:"event"` // "object.created"
	Project     string    `json:"project"`
	Bucket      string    `json:"bucket" doc:"Bucket name in tiffin.config.ts"`
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	ETag        string    `json:"etag"`
	URL         string    `json:"url,omitempty" doc:"Public URL (public buckets only)"`
	At          time.Time `json:"at"`
}

// eventPublisher is implemented by the queue module.
type eventPublisher interface {
	PublishEvent(ctx context.Context, p *platform.Platform, project, topic string, payload json.RawMessage, dedupe string) (bool, error)
}

// createdEvent is one upload waiting to be published.
type createdEvent struct {
	meta      bucketMeta
	s3name    string
	key       string
	requestID string
	attempt   int
}

const eventQueueSize = 4096

func (m *Module) eventQueue() chan createdEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = make(chan createdEvent, eventQueueSize)
	}
	return m.events
}

// objectCreated queues an object.created event (never blocks).
func (m *Module) objectCreated(b *bucketMeta, s3name, key, requestID string) {
	select {
	case m.eventQueue() <- createdEvent{meta: *b, s3name: s3name, key: key, requestID: requestID}:
	default: // a full queue drops events rather than slow uploads down
	}
}

// runEvents publishes queued events until ctx ends. A publish that fails
// (the queue not running yet, say) is retried a few times with backoff.
func (m *Module) runEvents(ctx context.Context, p *platform.Platform) {
	ch := m.eventQueue()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			err := m.publishCreated(ctx, p, ev)
			if err == nil || ev.attempt >= 5 {
				if err != nil {
					p.Log.Warn("storage: object.created not published", "project", ev.meta.Project, "bucket", ev.meta.Name, "key", ev.key, "err", err)
				}
				continue
			}
			ev.attempt++
			time.AfterFunc(time.Duration(1<<ev.attempt)*time.Second, func() {
				select {
				case ch <- ev:
				default:
				}
			})
		}
	}
}

func (m *Module) publishCreated(ctx context.Context, p *platform.Platform, ev createdEvent) error {
	pub := m.publisher
	for _, mod := range platform.Modules() {
		if ep, ok := mod.(eventPublisher); ok && pub == nil {
			pub = ep
		}
	}
	if pub == nil {
		return nil // no queue module in this build
	}
	gw, err := m.gateway(p)
	if err != nil {
		return err
	}
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h, err := gw.headObject(hctx, ev.s3name, ev.key)
	if isCode(err, "NoSuchKey") || errorsIsNotFound(err) {
		return nil // deleted again already
	}
	if err != nil {
		return err
	}
	size, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	e := ObjectCreated{Event: "object.created", Project: ev.meta.Project, Bucket: ev.meta.Name, Key: ev.key, Size: size,
		ContentType: h.Get("Content-Type"), ETag: strings.Trim(h.Get("ETag"), `"`), At: time.Now().UTC()}
	if ev.meta.Public {
		e.URL = p.URL(p.Host("files")) + "/" + ev.meta.Project + "/" + ev.meta.Name + "/" + s3Escape(ev.key, true)
	}
	raw, _ := json.Marshal(e)
	dedupe := ""
	if ev.requestID != "" {
		dedupe = "storage:" + ev.requestID
	}
	_, err = pub.PublishEvent(ctx, p, ev.meta.Project, ObjectCreatedTopic, raw, dedupe)
	return err
}

func errorsIsNotFound(err error) bool {
	var se *s3Error
	return errors.As(err, &se) && se.Status == 404
}
