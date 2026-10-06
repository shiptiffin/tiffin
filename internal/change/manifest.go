package change

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// AppRoutes returns each app's routes in a project's stored resources, for
// manifest.ParseOnBox. Apps whose spec does not decode are left out.
func AppRoutes(res map[string]Resource) map[string][]string {
	out := map[string][]string{}
	for addr, r := range res {
		if Kind(addr) != KindApp {
			continue
		}
		var a manifest.App
		if json.Unmarshal(r.Spec, &a) == nil {
			out[Name(addr)] = a.Routes
		}
	}
	return out
}

// ManifestFromResources rebuilds a project's canonical manifest from its
// stored resources: the inverse of Resources. It works for every project,
// including ones created before the manifest itself was kept, because
// resources are the source of truth.
//
// For any valid manifest m: Resources(m) → ManifestFromResources → Canonical
// gives the same bytes as Canonical(m). Unknown resource kinds are an error
// rather than being dropped silently.
func ManifestFromResources(project string, res map[string]Resource) (*manifest.Manifest, error) {
	m := &manifest.Manifest{Version: manifest.Version, Project: project}
	addrs := make([]string, 0, len(res))
	for a := range res {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	var buckets map[string]manifest.Bucket
	for _, addr := range addrs {
		spec := res[addr].Spec
		kind, name := Kind(addr), Name(addr)
		dec := func(v any) error {
			d := json.NewDecoder(bytes.NewReader(spec))
			d.DisallowUnknownFields()
			if err := d.Decode(v); err != nil {
				return fmt.Errorf("resource %s: %w", addr, err)
			}
			return nil
		}
		switch kind {
		case KindProject:
			var ps ProjectSpec
			if err := dec(&ps); err != nil {
				return nil, err
			}
			m.Resources, m.SleepAfter = ps.Resources, ps.SleepAfter
		case KindApp:
			var a manifest.App
			if err := dec(&a); err != nil {
				return nil, err
			}
			if m.Apps == nil {
				m.Apps = map[string]manifest.App{}
			}
			m.Apps[name] = a
		case KindEnv:
			var v string
			if err := dec(&v); err != nil {
				return nil, err
			}
			if m.Env == nil {
				m.Env = map[string]string{}
			}
			m.Env[name] = v
		case KindBucket:
			var b manifest.Bucket
			if err := dec(&b); err != nil {
				return nil, err
			}
			if buckets == nil {
				buckets = map[string]manifest.Bucket{}
			}
			buckets[name] = b
		case KindQueue:
			var q manifest.Queue
			if err := dec(&q); err != nil {
				return nil, err
			}
			if m.Queues == nil {
				m.Queues = map[string]manifest.Queue{}
			}
			m.Queues[name] = q
		case KindTopic:
			var t manifest.Topic
			if err := dec(&t); err != nil {
				return nil, err
			}
			if m.Topics == nil {
				m.Topics = map[string]manifest.Topic{}
			}
			m.Topics[name] = t
		case KindCron:
			var c manifest.Cron
			if err := dec(&c); err != nil {
				return nil, err
			}
			if m.Crons == nil {
				m.Crons = map[string]manifest.Cron{}
			}
			m.Crons[name] = c
		case KindDomain:
			var d manifest.Domain
			if err := dec(&d); err != nil {
				return nil, err
			}
			if m.Domains == nil {
				m.Domains = map[string]manifest.Domain{}
			}
			m.Domains[name] = d
		case KindService:
			s := &m.Services
			var err error
			switch name {
			case "postgres":
				s.Postgres = &manifest.Postgres{}
				err = dec(s.Postgres)
			case "valkey":
				s.Valkey = &manifest.Valkey{}
				err = dec(s.Valkey)
			case "storage":
				s.Storage = &manifest.Storage{}
			case "auth":
				s.Auth = &manifest.Auth{}
				err = dec(s.Auth)
			case "email":
				s.Email = &manifest.Email{}
				err = dec(s.Email)
			case "analytics":
				s.Analytics = &manifest.Analytics{}
				err = dec(s.Analytics)
			default:
				return nil, fmt.Errorf("resource %s: unknown service", addr)
			}
			if err != nil {
				return nil, err
			}
		case KindSecret, KindReadOnly, KindStorageLimit, KindStopped:
			// Secrets, read-only holds, storage limits and stops are not part of the manifest.
		default:
			return nil, fmt.Errorf("resource %s: unknown kind %q", addr, strings.TrimSpace(kind))
		}
	}
	if len(buckets) > 0 {
		if m.Services.Storage == nil {
			m.Services.Storage = &manifest.Storage{}
		}
		m.Services.Storage.Buckets = buckets
	}
	manifest.Normalize(m)
	return m, nil
}
